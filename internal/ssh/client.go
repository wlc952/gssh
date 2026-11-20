package ssh

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// Client wraps an SSH client connection.
type Client struct {
	GoClient *ssh.Client
	// upstream is held open when this client is connected through ProxyJump.
	upstream *Client
	mu       sync.Mutex
	probing  bool
}

// AuthConfig holds authentication parameters.
type AuthConfig struct {
	Password  string
	KeyPath   string
	ProxyJump string
}

// Connect establishes an SSH connection using the given auth config.
//
// Auth method order mirrors user intent:
//  1. explicit password (AuthConfig.Password) — password + keyboard-interactive
//  2. explicit key (AuthConfig.KeyPath)
//  3. default key material when no explicit key: ssh-agent (SSH_AUTH_SOCK),
//     then the standard ~/.ssh default key files
//
// Password goes first when explicitly provided: offering a dozen agent keys
// before the password can trip the server's MaxAuthTries limit.
func Connect(user, host string, port int, auth AuthConfig) (*Client, error) {
	return ConnectWithTimeout(user, host, port, auth, 10*time.Second)
}

// ConnectWithTimeout establishes an SSH connection with an explicit handshake
// timeout. A zero or negative timeout uses the default (10 seconds).
func ConnectWithTimeout(user, host string, port int, auth AuthConfig, timeout time.Duration) (*Client, error) {
	if auth.ProxyJump != "" {
		jumpUser, jumpHost := user, auth.ProxyJump
		if i := strings.LastIndex(jumpHost, "@"); i >= 0 {
			jumpUser, jumpHost = jumpHost[:i], jumpHost[i+1:]
		}
		jumpPort := 22
		jumpPortExplicit := false
		// ProxyJump destinations may include an optional port (for example
		// user@bastion:2222). Strip it before resolving SSH config aliases and
		// dialing; otherwise the port becomes part of the hostname.
		if h, p, ok := splitDestinationPort(jumpHost); ok {
			jumpHost, jumpPort = h, p
			jumpPortExplicit = true
		} else if strings.Count(jumpHost, ":") == 1 {
			// A single colon denotes the host:port form. Reject malformed or
			// out-of-range ports instead of passing them to net.JoinHostPort,
			// which would produce a confusing hostname such as "host:http".
			return nil, fmt.Errorf("invalid ProxyJump port in %q", jumpHost)
		}
		jumpAuth := auth
		jumpAuth.ProxyJump = ""
		if jc, _ := ResolveHostConfig(jumpHost); jc.HostName != "" || jc.User != "" || jc.Port != 0 || jc.IdentityFile != "" {
			if jc.User != "" {
				jumpUser = jc.User
			}
			if jc.HostName != "" {
				jumpHost = jc.HostName
			}
			if jc.Port != 0 && !jumpPortExplicit {
				jumpPort = jc.Port
			}
			if jc.IdentityFile != "" {
				jumpAuth.KeyPath = jc.IdentityFile
			}
			return ConnectWithProxyJump(user, host, port, auth, timeout, jumpUser, jumpHost, jumpPort, jumpAuth)
		}
		base := auth
		base.ProxyJump = ""
		return ConnectWithProxyJump(user, host, port, base, timeout, jumpUser, jumpHost, jumpPort, jumpAuth)
	}
	return connectWithTimeout(user, host, port, auth, timeout, nil)
}

// splitDestinationPort extracts an explicit numeric port from a host name.
// It accepts bracketed IPv6 ([::1]:2222) and the common host:port form while
// leaving unbracketed IPv6 literals and non-numeric suffixes untouched.
func splitDestinationPort(dest string) (string, int, bool) {
	if h, p, err := net.SplitHostPort(dest); err == nil {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
			return h, n, true
		}
	}
	if strings.Count(dest, ":") == 1 {
		parts := strings.SplitN(dest, ":", 2)
		if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 && n <= 65535 {
			return parts[0], n, true
		}
	}
	return dest, 0, false
}

// ConnectWithProxyJump connects to host through a single SSH jump host.
// An optional final AuthConfig configures authentication for the jump itself;
// when omitted, auth is reused. jumpUser may be empty (in which case user is
// used), and jumpPort may be zero (22 is used). The returned client owns the
// jump connection and closes it with Close.
func ConnectWithProxyJump(user, host string, port int, auth AuthConfig, timeout time.Duration, jumpUser, jumpHost string, jumpPort int, jumpAuths ...AuthConfig) (*Client, error) {
	if jumpUser == "" {
		jumpUser = user
	}
	if jumpPort == 0 {
		jumpPort = 22
	}
	jumpAuth := auth
	if len(jumpAuths) > 0 {
		jumpAuth = jumpAuths[0]
	}
	jump, err := ConnectWithTimeout(jumpUser, jumpHost, jumpPort, jumpAuth, timeout)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to proxy jump %s: %w", jumpHost, err)
	}
	client, err := connectWithTimeout(user, host, port, auth, timeout, jump)
	if err != nil {
		_ = jump.Close()
		return nil, err
	}
	return client, nil
}

func connectWithTimeout(user, host string, port int, auth AuthConfig, timeout time.Duration, jump *Client) (*Client, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	var authMethods []ssh.AuthMethod

	if auth.Password != "" {
		authMethods = append(authMethods, ssh.Password(auth.Password))
		handler := &keyboardInteractiveHandler{Password: auth.Password}
		authMethods = append(authMethods, ssh.KeyboardInteractive(handler.Challenge))
	}

	var agentConn net.Conn
	if auth.KeyPath != "" {
		methods, err := authFromKeyPath(auth.KeyPath)
		if err != nil {
			return nil, err
		}
		authMethods = append(authMethods, methods...)
	} else {
		if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
			if conn, err := net.Dial("unix", sock); err == nil {
				agentConn = conn
				authMethods = append(authMethods, ssh.PublicKeysCallback(agent.NewClient(conn).Signers))
			}
		}
		authMethods = append(authMethods, defaultKeyAuthMethods()...)
	}
	if agentConn != nil {
		// The agent connection is only needed during the auth handshake.
		defer agentConn.Close()
	}

	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no authentication method available: pass --pswd or -i, load a key into ssh-agent, or add a default key (~/.ssh/id_ed25519)")
	}

	hostKeyCallback, err := getHostKeyCallback()
	if err != nil {
		return nil, fmt.Errorf("failed to get host key callback: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         timeout,
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	var goClient *ssh.Client
	if jump == nil {
		goClient, err = ssh.Dial("tcp", addr, config)
	} else {
		dialCh := make(chan struct {
			conn    net.Conn
			dialErr error
		}, 1)
		go func() {
			conn, dialErr := jump.GoClient.Dial("tcp", addr)
			dialCh <- struct {
				conn    net.Conn
				dialErr error
			}{conn, dialErr}
		}()
		var conn net.Conn
		var dialErr error
		select {
		case result := <-dialCh:
			conn, dialErr = result.conn, result.dialErr
		case <-time.After(timeout):
			_ = jump.Close()
			// The SSH client's Dial call runs in a goroutine. Drain its result
			// so a connection that races the timeout is closed instead of leaked.
			go func() {
				result := <-dialCh
				if result.conn != nil {
					_ = result.conn.Close()
				}
			}()
			dialErr = fmt.Errorf("proxy target dial timeout after %s", timeout)
		}
		if dialErr == nil {
			// ProxyJump's Dial only establishes the TCP stream; unlike ssh.Dial,
			// it does not apply ClientConfig.Timeout to the subsequent SSH
			// handshake. Bound the handshake explicitly and clear the deadline
			// once authentication completes so the connection remains usable.
			_ = conn.SetDeadline(time.Now().Add(timeout))
			cc, chans, reqs, handshakeErr := ssh.NewClientConn(conn, addr, config)
			if handshakeErr == nil {
				_ = conn.SetDeadline(time.Time{})
			}
			if handshakeErr != nil {
				_ = conn.Close()
				dialErr = handshakeErr
			} else {
				goClient = ssh.NewClient(cc, chans, reqs)
			}
		}
		err = dialErr
	}
	if err != nil {
		return nil, fmt.Errorf("failed to connect to %s: %w", addr, err)
	}

	return &Client{GoClient: goClient, upstream: jump}, nil
}

// Close closes the SSH connection.
func (c *Client) Close() error {
	c.mu.Lock()
	client := c.GoClient
	c.GoClient = nil
	c.probing = false
	c.mu.Unlock()

	if client != nil {
		err := client.Close()
		if c.upstream != nil {
			if upstreamErr := c.upstream.Close(); err == nil {
				err = upstreamErr
			}
		}
		return err
	}
	return nil
}

// IsAlive checks if the SSH connection is still alive.
func (c *Client) IsAlive() bool {
	c.mu.Lock()
	if c.GoClient == nil {
		c.mu.Unlock()
		return false
	}
	if c.probing {
		c.mu.Unlock()
		return false
	}
	client := c.GoClient
	c.probing = true
	c.mu.Unlock()

	aliveCh := make(chan bool, 1)
	go func() {
		_, _, err := client.SendRequest("keepalive@gssh", true, nil)
		c.mu.Lock()
		c.probing = false
		c.mu.Unlock()
		aliveCh <- (err == nil)
	}()
	select {
	case alive := <-aliveCh:
		return alive
	case <-time.After(5 * time.Second):
		_ = c.Close()
		return false
	}
}

// --- Auth helpers ---

type keyboardInteractiveHandler struct {
	Password string
}

func (k *keyboardInteractiveHandler) Challenge(name, instruction string, questions []string, echoes []bool) ([]string, error) {
	answers := make([]string, len(questions))
	for i := range questions {
		answers[i] = k.Password
	}
	return answers, nil
}

func authFromKeyPath(keyPath string) ([]ssh.AuthMethod, error) {
	keyPath = expandPath(keyPath)
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("unable to read private key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		var passErr *ssh.PassphraseMissingError
		if errors.As(err, &passErr) {
			return nil, fmt.Errorf("key %s is passphrase-protected: load it into ssh-agent (ssh-add) first; passphrase prompt is not supported", keyPath)
		}
		return nil, fmt.Errorf("unable to parse private key: %w", err)
	}
	return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
}

// defaultKeyAuthMethods loads the standard ~/.ssh default keys that exist
// and are usable without a passphrase (same set ssh tries by default).
// Passphrase-protected keys are skipped — they belong in ssh-agent.
func defaultKeyAuthMethods() []ssh.AuthMethod {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	var methods []ssh.AuthMethod
	for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa", "id_dsa"} {
		key, err := os.ReadFile(filepath.Join(homeDir, ".ssh", name))
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			continue
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	return methods
}

// --- TOFU host key policy ---

func getHostKeyCallback() (ssh.HostKeyCallback, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("could not get user home dir: %w", err)
	}

	knownHostsPath := filepath.Join(homeDir, ".ssh", "known_hosts")

	// Ensure .ssh directory exists
	if err := os.MkdirAll(filepath.Dir(knownHostsPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	// Create known_hosts if it doesn't exist
	if _, err := os.Stat(knownHostsPath); os.IsNotExist(err) {
		f, err := os.OpenFile(knownHostsPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("failed to create known_hosts file: %w", err)
		}
		f.Close()
	}

	cb, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create knownhosts callback: %w", err)
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		if err == nil {
			return nil
		}

		keyErr, ok := err.(*knownhosts.KeyError)
		if !ok {
			return err
		}

		// Unknown key (len(Want) == 0): Trust-On-First-Use — auto-accept
		if len(keyErr.Want) == 0 {
			autoTrust := strings.EqualFold(os.Getenv("GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS"), "1") ||
				strings.EqualFold(os.Getenv("GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS"), "true")
			if !autoTrust {
				return fmt.Errorf(
					"unknown host key for %s (%s), refusing auto-trust; add it to %s or set GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS=1",
					hostname,
					ssh.FingerprintSHA256(key),
					knownHostsPath,
				)
			}

			f, fErr := os.OpenFile(knownHostsPath, os.O_APPEND|os.O_WRONLY, 0600)
			if fErr != nil {
				return fmt.Errorf("failed to open known_hosts for appending: %w", fErr)
			}
			defer f.Close()

			addresses := []string{knownhosts.Normalize(hostname)}
			if remoteString := remote.String(); remoteString != hostname {
				addresses = append(addresses, knownhosts.Normalize(remoteString))
			}

			line := knownhosts.Line(addresses, key)
			if _, wErr := f.WriteString(line + "\n"); wErr != nil {
				return fmt.Errorf("failed to append key to known_hosts: %w", wErr)
			}
			return nil
		}

		// Key mismatch: reject (possible MITM)
		return err
	}, nil
}

func expandPath(path string) string {
	if len(path) > 0 && path[0] == '~' {
		homeDir, _ := os.UserHomeDir()
		return filepath.Join(homeDir, path[1:])
	}
	return path
}
