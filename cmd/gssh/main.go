package main

import (
	"fmt"
	"os"
	"strings"

	"gssh/internal/socketpath"
)

var (
	socketPath string
	version    = "dev"

	// allowAutoStart controls whether CLI commands transparently spawn the
	// daemon when its socket is missing or stale. Lifecycle commands
	// (start/stop/ping/server) disable it.
	allowAutoStart = true
)

func main() {
	// Parse global -S flag first
	args := os.Args[1:]
	socketPath = socketpath.Default()
	for i := 0; i < len(args); i++ {
		if args[i] == "-S" && i+1 < len(args) {
			socketPath = args[i+1]
			args = append(args[:i], args[i+2:]...)
			i--
		}
	}

	if len(args) > 0 && (args[0] == "-v" || args[0] == "--version") {
		fmt.Printf("gssh version %s\n", version)
		os.Exit(0)
	}

	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		for _, arg := range args[1:] {
			if arg == "--json" {
				printJSONUsage()
				return
			}
		}
		printUsage()
		os.Exit(0)
	}

	if len(args) < 1 {
		printUsage()
		os.Exit(1)
	}

	cmd := args[0]
	subArgs := args[1:]

	// Lifecycle commands must not implicitly start the daemon.
	switch cmd {
	case "start", "stop", "ping", "server":
		allowAutoStart = false
	}

	var err error
	switch cmd {
	case "start":
		err = handleStart()
	case "server":
		err = runDaemon(subArgs)
	case "connect":
		err = handleConnect(subArgs)
	case "local":
		err = handleLocal(subArgs)
	case "kill":
		err = handleKill(subArgs)
	case "cancel":
		err = handleCancel(subArgs)
	case "tasks":
		err = handleTasks(subArgs)
	case "batch":
		err = handleBatch(subArgs)
	case "exec":
		err = handleExec(subArgs)
	case "run":
		err = handleRun(subArgs)
	case "list", "ls":
		err = handleList(subArgs)
	case "status":
		err = handleList(subArgs)
	case "use":
		err = handleUse(subArgs)
	case "forward":
		err = handleForward(subArgs)
	case "forwards":
		err = handleForwards(subArgs)
	case "forward-close":
		err = handleForwardClose(subArgs)
	case "scp", "sync":
		err = handleSCP(subArgs)
	case "sftp":
		err = handleSFTP(subArgs)
	case "ping":
		err = handlePing()
	case "stop":
		err = handleStop()
	case "help":
		for _, arg := range subArgs {
			if arg == "--json" {
				printJSONUsage()
				return
			}
		}
		printUsage()
	case "completion":
		// Completion only emits a script and must never auto-start the daemon.
		allowAutoStart = false
		err = handleCompletion(subArgs)
	default:
		// ssh-style shorthand: `gssh user@host [flags] [command...]`
		if strings.Contains(cmd, "@") {
			err = handleDestination(cmd, subArgs)
			break
		}
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Printf(`gssh - SSH session multiplexer for agents v%s

Usage (ssh-style):
  gssh user@host [-p port] [-i key] [--pswd pw|--password-stdin] [-n name] Connect (creates session)
  gssh user@host [-p port] [-e KEY=VALUE] [flags] "command"   Connect if needed, then exec
  gssh connect user@host [-p port] [-i key] [--pswd pw|--password-stdin] Same, explicit subcommand

  Auth: with no flags, tries ssh-agent then ~/.ssh default keys
  (id_ed25519, id_ecdsa, id_rsa) — just like ssh. Use --pswd for password.

Session commands:
  gssh local [-n name]                           Create local session
  gssh exec [-n name] [-t timeout] [-e KEY=VALUE] [--stream] [--raw] [--sudo ...] "command"
  gssh run [-t timeout] [-e KEY=VALUE] [--stream] [--raw] [--sudo ...] "command"  One-off local exec
  gssh list | ls | status [--json]               List session status
  gssh use <name> [--pswd pw|--password-stdin] [-i key] Switch default / reconnect session
  gssh kill [-n name]                            Kill session
  gssh cancel <task_id>                          Cancel a running streamed command
  gssh tasks                                     List running streamed commands
  gssh batch -f hosts --parallel 16 -- "uname -a"  Execute on hosts concurrently (JSON lines)

Transfers & forwards (scp/ssh-style):
  gssh scp <src> <dst>                           One side is session:path (upload or download)
  gssh sync <src> <dst>                          Alias of scp (transfers skip unchanged files)
  gssh sftp [-n name] ls|mkdir|rm <path>
  gssh forward [-n name] -L localPort:remotePort
  gssh forward [-n name] -R remotePort:localPort [--bind addr|--public]
  gssh forwards [--json]                         List forwards
  gssh forward-close <id>                        Close forward (ID or unique prefix)

Daemon:
  gssh start | stop | ping                       Daemon starts automatically when needed
  gssh server                                    Run daemon in foreground (internal)
  gssh completion <bash|zsh|fish>                Generate shell completion script
  gssh -v, --version
  gssh -S <socket_path> ...                      Override socket (default: %s)

Exec options:
  -t timeout        Command timeout in seconds
  --stream          Stream stdout/stderr in real-time (for long-running commands)
  --task-id         Optional task ID, printed for streamed cancellation
  --raw             Raw stdout/stderr instead of the default JSON result
  --sudo            Run with sudo (password via stdin, no shell injection)
  --pswd            Sudo password (on connect: SSH password; doubles as sudo
                    password in the user@host shorthand when --sudo is set)
  --password-stdin  Read SSH password from stdin (safer for scripts; connect/use)
  --sudo-user       Run as specified user
  --sudo-login      Login shell (-i)

Non-stream exec prints {"stdout","stderr","exit_code"} JSON by default and
exits with the command's exit code, so shell &&-chains still work.

The daemon starts automatically when any command needs it; "gssh start" is
only needed to control startup explicitly. Daemon logs go to ~/.gssh/server.log.

Examples:
  gssh admin@10.0.1.1                            Connect with default key / agent
  gssh admin@10.0.1.1 -p 7080 --pswd secret      Connect with port + password
  gssh admin@10.0.1.1 "df -h"                    One-off exec (reuses session, JSON out)
  gssh admin@10.0.1.1 --raw "tail -20 app.log"   Raw output for humans/pipes
  gssh exec -n production -t 30 "make build"
  gssh scp ./app.zip production:/opt/
  gssh scp production:/var/log/app.log ./logs/
  gssh forward -n production -L 8080:80
`, version, socketpath.Default())
}
