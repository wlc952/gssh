package ssh

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HostConfig contains the subset of ~/.ssh/config understood by gssh.
type HostConfig struct {
	HostName     string
	Host         string
	User         string
	Port         int
	IdentityFile string
	ProxyJump    string
}

// ResolveHostConfig reads ~/.ssh/config and resolves alias. Missing config is
// treated as an empty configuration, matching OpenSSH's behavior.
func ResolveHostConfig(alias string) (HostConfig, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return HostConfig{}, nil
	}
	return resolveHostConfigFile(filepath.Join(home, ".ssh", "config"), alias)
}

func resolveHostConfigFile(path, alias string) (HostConfig, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return HostConfig{}, nil
	}
	if err != nil {
		return HostConfig{}, err
	}
	defer f.Close()
	var out HostConfig
	fieldScore := map[string]int{}
	active := false
	activeScore := 0
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(strings.SplitN(s.Text(), "#", 2)[0])
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.ToLower(fields[0]) {
		case "host":
			active = hostPatternsMatch(fields[1:], alias)
			activeScore = hostPatternScore(fields[1:], alias)
		case "user":
			if active && activeScore > fieldScore["user"] {
				out.User = fields[1]
				fieldScore["user"] = activeScore
			}
		case "port":
			if active && activeScore > fieldScore["port"] {
				if p, e := strconv.Atoi(fields[1]); e == nil && p > 0 && p <= 65535 {
					out.Port = p
					fieldScore["port"] = activeScore
				}
			}
		case "identityfile":
			if active && activeScore > fieldScore["identityfile"] {
				out.IdentityFile = expandConfigPath(fields[1])
				fieldScore["identityfile"] = activeScore
			}
		case "hostname":
			if active && activeScore > fieldScore["hostname"] {
				out.HostName = fields[1]
				fieldScore["hostname"] = activeScore
			}
		case "proxyjump":
			if active && activeScore > fieldScore["proxyjump"] {
				out.ProxyJump = fields[1]
				fieldScore["proxyjump"] = activeScore
			}
		}
	}
	return out, s.Err()
}

func expandConfigPath(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}

func hostPatternMatch(pattern, alias string) bool {
	if strings.HasPrefix(pattern, "!") {
		pattern = pattern[1:]
	}
	matched, _ := filepath.Match(pattern, alias)
	if pattern == alias {
		matched = true
	}
	return matched
}

// hostPatternsMatch follows OpenSSH's Host pattern-list semantics: a
// negated pattern always excludes the stanza, even when a positive pattern
// also matched. A list containing only negated patterns applies everywhere
// except the excluded aliases.
func hostPatternsMatch(patterns []string, alias string) bool {
	positive, positiveMatch, negatedMatch := false, false, false
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "!") {
			if hostPatternMatch(pattern, alias) {
				negatedMatch = true
			}
			continue
		}
		positive = true
		if hostPatternMatch(pattern, alias) {
			positiveMatch = true
		}
	}
	if negatedMatch {
		return false
	}
	return positiveMatch || !positive
}

// hostPatternScore prefers an exact alias stanza over wildcard stanzas. This
// lets the common `Host *` defaults be refined by a later `Host prod` block.
func hostPatternScore(patterns []string, alias string) int {
	score := 0
	for _, p := range patterns {
		if strings.HasPrefix(p, "!") {
			continue
		}
		if !hostPatternMatch(p, alias) {
			continue
		}
		if p == alias {
			return 2
		}
		score = 1
	}
	return score
}
