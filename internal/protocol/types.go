package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"net"
)

// SessionInfo is the serialized representation of a session returned to the CLI.
type SessionInfo struct {
	Name      string `json:"name"`
	Type      string `json:"type"` // "ssh" or "local"
	Host      string `json:"host"`
	User      string `json:"user"`
	Port      int    `json:"port,omitempty"`
	Status    string `json:"status"`
	KeyPath   string `json:"key_path,omitempty"`
	LastCmd   string `json:"last_cmd,omitempty"`
	CreatedAt int64  `json:"created_at"` // Unix timestamp
}

// ExecResult represents command execution output.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// ForwardInfo represents a port forward entry.
type ForwardInfo struct {
	ID         string `json:"id"`
	Session    string `json:"session"`
	Type       string `json:"type"` // "local" or "remote"
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	BindAddr   string `json:"bind_addr,omitempty"`
}

// TransferResult represents a file transfer result.
type TransferResult struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Bytes    int64  `json:"bytes"`
	Duration int64  `json:"duration_ms"`
}

// SudoOptions for sudo command execution.
type SudoOptions struct {
	Enabled  bool   `json:"enabled"`
	Password string `json:"password,omitempty"`
	User     string `json:"user,omitempty"`
	Login    bool   `json:"login,omitempty"`
}

// --- Request Params (decoded from imsg payload JSON) ---

type ConnectParams struct {
	TaskID    string `json:"task_id,omitempty"`
	Name      string `json:"name,omitempty"`
	User      string `json:"user"`
	Host      string `json:"host"`
	Port      int    `json:"port,omitempty"`
	Password  string `json:"password,omitempty"`
	KeyPath   string `json:"key_path,omitempty"`
	ProxyJump string `json:"proxy_jump,omitempty"`
	// Timeout is the SSH connection/authentication timeout in seconds. Zero uses the daemon default.
	Timeout int `json:"timeout,omitempty"`
}

type LocalParams struct {
	TaskID string `json:"task_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

type KillParams struct {
	TaskID string `json:"task_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

type UseParams struct {
	TaskID   string `json:"task_id,omitempty"`
	Name     string `json:"name"`
	Password string `json:"password,omitempty"`
	KeyPath  string `json:"key_path,omitempty"`
}

type ExecParams struct {
	TaskID  string      `json:"task_id,omitempty"`
	Name    string      `json:"name,omitempty"`
	Command string      `json:"command"`
	Cwd     string      `json:"cwd,omitempty"`
	Timeout int         `json:"timeout,omitempty"`
	Sudo    SudoOptions `json:"sudo,omitempty"`
	Stdin   []byte      `json:"stdin,omitempty"`
}

type LocalExecParams struct {
	TaskID  string      `json:"task_id,omitempty"`
	Command string      `json:"command"`
	Cwd     string      `json:"cwd,omitempty"`
	Timeout int         `json:"timeout,omitempty"`
	Sudo    SudoOptions `json:"sudo,omitempty"`
	Stdin   []byte      `json:"stdin,omitempty"`
}

// CancelParams identifies a running streaming task.
type CancelParams struct {
	TaskID string `json:"task_id"`
}

type TaskInfo struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

type ForwardParams struct {
	TaskID     string `json:"task_id,omitempty"`
	Name       string `json:"name,omitempty"`
	Type       string `json:"type"` // "local" or "remote"
	LocalPort  int    `json:"local_port"`
	RemotePort int    `json:"remote_port"`
	BindAddr   string `json:"bind_addr,omitempty"`
}

type ForwardCloseParams struct {
	TaskID string `json:"task_id,omitempty"`
	ID     string `json:"id"`
}

type SCPParams struct {
	TaskID   string `json:"task_id,omitempty"`
	Name     string `json:"name,omitempty"`
	Source   string `json:"source"`
	Dest     string `json:"dest"`
	IsUpload bool   `json:"is_upload"`
}

type SFTPParams struct {
	TaskID  string `json:"task_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Command string `json:"command"` // "ls", "mkdir", "rm"
	Path    string `json:"path"`
}

// StreamChunk carries an incremental chunk of command output during streaming exec.
type StreamChunk struct {
	Stream string `json:"stream"` // "stdout" or "stderr"
	Data   []byte `json:"data"`   // raw output bytes (JSON-encoded as base64)
}

// StreamEnd signals the end of a streaming exec, carrying the final exit code.
type StreamEnd struct {
	ExitCode int    `json:"exit_code"`
	Error    string `json:"error,omitempty"` // set when a fatal (non-exit) error occurred
}

// --- Helper functions ---

// EncodePayload marshals a value to JSON bytes for imsg payload.
func EncodePayload(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

// DecodePayload unmarshals imsg payload JSON bytes into a value.
func DecodePayload(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// ErrorPayload is the stable, machine-readable RPC error envelope. Code and
// Message retain the original gssh error semantics; the additional fields let
// clients decide whether a request can be retried and correlate failures with
// a session or task.
type ErrorPayload struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Session   string `json:"session,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
}

// RetryableError can be implemented by operations that know a failure is
// safe to retry. It is intentionally small so packages need not depend on a
// concrete RPC error type.
type RetryableError interface{ Retryable() bool }

// NewErrorPayload converts an arbitrary operation error into the wire format.
// The default code remains -32000 for backwards compatibility.
func NewErrorPayload(err error, session, taskID string) ErrorPayload {
	p := ErrorPayload{Code: -32000, Message: "unknown error", Session: session, TaskID: taskID}
	if err == nil {
		return p
	}
	p.Message = err.Error()
	var re RetryableError
	if errors.As(err, &re) {
		p.Retryable = re.Retryable()
	} else {
		var ne net.Error
		p.Retryable = errors.As(err, &ne) && (ne.Timeout() || ne.Temporary())
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			p.Retryable = true
		}
	}
	return p
}
