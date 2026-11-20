package main

import (
	"encoding/json"
	"fmt"
	"gssh/internal/socketpath"
)

// helpDocument is intentionally made up of plain data.  Keeping the schema
// stable makes `gssh help --json` useful to shell scripts and AI agents while
// allowing the human help text to evolve independently.
type helpDocument struct {
	Name     string        `json:"name"`
	Version  string        `json:"version"`
	Synopsis string        `json:"synopsis"`
	Globals  []helpFlag    `json:"global_flags"`
	Commands []helpCommand `json:"commands"`
	// Capabilities lets integrations discover intentional protocol boundaries.
	// In particular, interactive TTY is false because gssh commands are
	// structured/non-interactive; callers should use OpenSSH/tmux for a human
	// terminal instead of trying to infer support from an exec failure.
	Capabilities map[string]bool `json:"capabilities"`
}

type helpCommand struct {
	Name        string     `json:"name"`
	Aliases     []string   `json:"aliases,omitempty"`
	Usage       string     `json:"usage"`
	Description string     `json:"description"`
	Flags       []helpFlag `json:"flags,omitempty"`
}

type helpFlag struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
}

func usageJSON() ([]byte, error) {
	sock := socketPath
	if sock == "" {
		sock = socketpath.Default()
	}
	doc := helpDocument{
		Name: "gssh", Version: version,
		Synopsis: "SSH session multiplexer for agents",
		Capabilities: map[string]bool{
			"streaming_exec":  true,
			"batch_exec":      true,
			"sftp_resume":     true,
			"proxy_jump":      true,
			"interactive_tty": false,
		},
		Globals: []helpFlag{
			{Name: "-S, --socket", Type: "string", Default: sock, Description: "Unix socket path for the daemon"},
			{Name: "-v, --version", Type: "bool", Description: "Print version"},
			{Name: "-h, --help", Type: "bool", Description: "Print human-readable help"},
		},
		Commands: []helpCommand{
			{Name: "connect", Usage: "gssh connect [flags] user@host", Description: "Create or reuse an SSH session", Flags: connectHelpFlags()},
			{Name: "local", Usage: "gssh local [-n name]", Description: "Create a local session", Flags: []helpFlag{{Name: "-n, --name", Type: "string", Description: "Session name"}}},
			{Name: "use", Usage: "gssh use <name> [flags]", Description: "Switch to or reconnect a session", Flags: []helpFlag{{Name: "-n, --name", Type: "string", Description: "Session name"}, {Name: "--pswd", Type: "string", Description: "SSH password"}, {Name: "--password-stdin", Type: "bool", Description: "Read SSH password from stdin"}, {Name: "-i, --identity", Type: "string", Description: "SSH private key path"}}},
			{Name: "exec", Usage: "gssh exec [flags] command", Description: "Execute a command in the current session", Flags: execHelpFlags()},
			{Name: "run", Usage: "gssh run [flags] command", Description: "Execute a one-off local command", Flags: execHelpFlags()},
			{Name: "batch", Usage: "gssh batch -f hosts [flags] command", Description: "Execute a command on hosts concurrently", Flags: []helpFlag{{Name: "-f, --hosts-file", Type: "string", Description: "Host inventory file"}, {Name: "--parallel", Type: "int", Default: "4", Description: "Maximum concurrent hosts"}, {Name: "-t, --timeout", Type: "int", Description: "Command timeout in seconds"}, {Name: "--json", Type: "bool", Description: "Emit JSON Lines results"}, {Name: "--raw", Type: "bool", Description: "Print raw output"}}},
			{Name: "list", Aliases: []string{"ls", "status"}, Usage: "gssh list [--json]", Description: "List sessions and their status", Flags: []helpFlag{{Name: "--json", Type: "bool", Description: "Emit machine-readable JSON"}}},
			{Name: "kill", Usage: "gssh kill [-n name]", Description: "Close a session", Flags: []helpFlag{{Name: "-n, --name", Type: "string", Description: "Session name"}}},
			{Name: "tasks", Usage: "gssh tasks [--json]", Description: "List running streamed commands", Flags: []helpFlag{{Name: "--json", Type: "bool", Description: "Emit machine-readable JSON"}}},
			{Name: "cancel", Usage: "gssh cancel <task_id>", Description: "Cancel a running streamed command"},
			{Name: "scp", Aliases: []string{"sync"}, Usage: "gssh scp <src> <dst>", Description: "Copy files using an SSH session"},
			{Name: "sftp", Usage: "gssh sftp [flags] operation path", Description: "Run an SFTP operation", Flags: []helpFlag{{Name: "-n, --name", Type: "string", Description: "Session name"}}},
			{Name: "forward", Usage: "gssh forward [flags]", Description: "Create a local or remote port forward", Flags: []helpFlag{{Name: "-n, --name", Type: "string", Description: "Session name"}, {Name: "-L", Type: "string", Description: "Local forward localPort:remotePort"}, {Name: "-R", Type: "string", Description: "Remote forward remotePort:localPort"}, {Name: "--bind", Type: "string", Default: "127.0.0.1", Description: "Bind address"}, {Name: "--public", Type: "bool", Description: "Allow public bind address"}}},
			{Name: "forwards", Usage: "gssh forwards [--json]", Description: "List active port forwards", Flags: []helpFlag{{Name: "--json", Type: "bool", Description: "Emit machine-readable JSON"}}},
			{Name: "forward-close", Usage: "gssh forward-close <id>", Description: "Close a port forward"},
			{Name: "start", Usage: "gssh start", Description: "Start the daemon"},
			{Name: "stop", Usage: "gssh stop", Description: "Stop the daemon"},
			{Name: "ping", Usage: "gssh ping", Description: "Check daemon availability"},
			{Name: "server", Usage: "gssh server", Description: "Run the daemon in the foreground"},
			{Name: "completion", Usage: "gssh completion <bash|zsh|fish>", Description: "Generate shell completion script"},
			{Name: "help", Usage: "gssh help [--json]", Description: "Print command help"},
			{Name: "ssh-style", Usage: "gssh user@host [flags] [command]", Description: "Connect using SSH-style shorthand"},
		},
	}
	return json.MarshalIndent(doc, "", "  ")
}

func connectHelpFlags() []helpFlag {
	return []helpFlag{{Name: "-p, --port", Type: "int", Default: "22", Description: "SSH port"}, {Name: "-i, --identity", Type: "string", Description: "SSH private key path"}, {Name: "--pswd", Type: "string", Description: "SSH password"}, {Name: "--password-stdin", Type: "bool", Description: "Read SSH password from stdin"}, {Name: "-n, --name", Type: "string", Description: "Session name"}, {Name: "--connect-timeout", Type: "int", Default: "10", Description: "SSH connection timeout in seconds"}}
}

func execHelpFlags() []helpFlag {
	return []helpFlag{{Name: "-n, --name", Type: "string", Description: "Session name"}, {Name: "-t, --timeout", Type: "int", Description: "Command timeout in seconds"}, {Name: "-e, --env", Type: "KEY=VALUE", Description: "Environment variable (repeatable)"}, {Name: "--cwd", Type: "string", Description: "Working directory"}, {Name: "--stream", Type: "bool", Description: "Stream stdout/stderr"}, {Name: "--task-id", Type: "string", Description: "Task ID for streamed cancellation"}, {Name: "--raw", Type: "bool", Description: "Print raw output"}, {Name: "--sudo", Type: "bool", Description: "Run with sudo"}, {Name: "--sudo-user", Type: "string", Description: "Sudo user"}, {Name: "--sudo-login", Type: "bool", Description: "Use sudo login shell"}, {Name: "--stdin", Type: "bool", Description: "Read command input from stdin"}, {Name: "--stdin-file", Type: "string", Description: "Read command input from file"}}
}

func printJSONUsage() {
	b, err := usageJSON()
	if err != nil {
		fmt.Printf(`{"error":%q}\n`, err.Error())
		return
	}
	fmt.Println(string(b))
}
