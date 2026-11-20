package exec

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"gssh/internal/protocol"
)

func TestLocalExecSimple(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("echo hello", 0, nil)
	if err != nil {
		t.Fatalf("ExecLocal failed: %v", err)
	}
	if result.Stdout != "hello\n" {
		t.Errorf("Stdout = %q, want \"hello\\n\"", result.Stdout)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestLocalExecStdin(t *testing.T) {
	e := NewExecutor(nil)
	result, err := e.ExecLocalWithCwdInput("cat", "", 0, nil, []byte("hello stdin\n"))
	if err != nil {
		t.Fatalf("ExecLocalWithCwdInput failed: %v", err)
	}
	if result.Stdout != "hello stdin\n" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
}

func TestLocalExecWithCwd(t *testing.T) {
	e := NewExecutor(nil)
	dir := t.TempDir()
	result, err := e.ExecLocalWithCwd("pwd", dir, 0, nil)
	if err != nil {
		t.Fatalf("ExecLocalWithCwd failed: %v", err)
	}
	if got := strings.TrimSpace(result.Stdout); got != dir {
		t.Fatalf("cwd = %q, want %q", got, dir)
	}
}

func TestLocalStreamCancellationStopsQuietCommand(t *testing.T) {
	e := NewExecutor(nil)
	cancel := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := e.ExecLocalStreamWithCwdCancel("sleep 30", "", 0, nil, io.Discard, io.Discard, cancel)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	close(cancel)
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancel") {
			t.Fatalf("error = %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("quiet command did not stop after cancellation")
	}
}

func TestWithWorkingDirPreservesTrailingComment(t *testing.T) {
	dir := t.TempDir()
	result, err := runLocalInput(withWorkingDir("printf ok # trailing comment", dir), 0, nil, nil)
	if err != nil {
		t.Fatalf("runLocalInput failed: %v", err)
	}
	if result.Stdout != "ok" {
		t.Fatalf("stdout = %q, want %q", result.Stdout, "ok")
	}
}

type captureWriteCloser struct {
	bytes.Buffer
	closed bool
}

func (w *captureWriteCloser) Close() error { w.closed = true; return nil }

func TestWritePasswordAndInputWithoutPassword(t *testing.T) {
	w := &captureWriteCloser{}
	if err := writePasswordAndInput(w, "", []byte("payload")); err != nil {
		t.Fatalf("writePasswordAndInput failed: %v", err)
	}
	if got := w.String(); got != "payload" {
		t.Fatalf("captured input = %q, want %q", got, "payload")
	}
	if !w.closed {
		t.Fatal("stdin writer was not closed")
	}
}

func TestLocalExecStderr(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("echo err >&2", 0, nil)
	if err != nil {
		t.Fatalf("ExecLocal failed: %v", err)
	}
	if result.Stderr != "err\n" {
		t.Errorf("Stderr = %q, want \"err\\n\"", result.Stderr)
	}
}

func TestLocalExecExitCode(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("exit 42", 0, nil)
	if err != nil {
		t.Fatalf("ExecLocal failed: %v", err)
	}
	if result.ExitCode != 42 {
		t.Errorf("ExitCode = %d, want 42", result.ExitCode)
	}
}

func TestLocalExecTimeout(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("sleep 10", 2, nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result.ExitCode != -1 {
		t.Errorf("ExitCode = %d, want -1 (timeout)", result.ExitCode)
	}
}

func TestLocalExecPipe(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("echo hello | tr h H", 0, nil)
	if err != nil {
		t.Fatalf("ExecLocal pipe failed: %v", err)
	}
	if result.Stdout != "Hello\n" {
		t.Errorf("Stdout = %q, want \"Hello\\n\"", result.Stdout)
	}
}

func TestBuildSudoPrefix(t *testing.T) {
	tests := []struct {
		opts *protocol.SudoOptions
		want string
	}{
		{&protocol.SudoOptions{Login: true}, "sudo -i"},
		{&protocol.SudoOptions{User: "www-data"}, "sudo -u www-data"},
		{&protocol.SudoOptions{}, "sudo"},
	}

	for _, tt := range tests {
		got := buildSudoPrefix(tt.opts)
		if got != tt.want {
			t.Errorf("buildSudoPrefix() = %s, want %s", got, tt.want)
		}
	}
}

func TestBuildSudoCommand(t *testing.T) {
	tests := []struct {
		name string
		opts *protocol.SudoOptions
		want string
	}{
		{name: "default", opts: &protocol.SudoOptions{}, want: "sudo -S -p '' /bin/sh -c \"echo hi\""},
		{name: "login", opts: &protocol.SudoOptions{Login: true}, want: "sudo -i -S -p '' /bin/sh -c \"echo hi\""},
		{name: "user", opts: &protocol.SudoOptions{User: "www-data"}, want: "sudo -u www-data -S -p '' /bin/sh -c \"echo hi\""},
	}

	for _, tt := range tests {
		if got := buildSudoCommand("/bin/sh -c \"echo hi\"", tt.opts); got != tt.want {
			t.Fatalf("%s: buildSudoCommand() = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestWritePassword(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}

	done := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- data
	}()

	password := "pa'ss%word"
	if err := writePassword(writer, password); err != nil {
		t.Fatalf("writePassword failed: %v", err)
	}

	got := <-done
	want := password + "\n"
	if string(got) != want {
		t.Fatalf("writePassword() = %q, want %q", string(got), want)
	}
}

func TestWritePasswordEmptyPassword(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}

	done := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(reader)
		done <- data
	}()

	if err := writePassword(writer, ""); err != nil {
		t.Fatalf("writePassword failed: %v", err)
	}

	if got := <-done; len(got) != 0 {
		t.Fatalf("writePassword() wrote %q, want empty", string(got))
	}
}

func TestLocalExecLargeStderrDoesNotHang(t *testing.T) {
	e := NewExecutor(nil)

	result, err := e.ExecLocal("yes x | head -c 131072 >&2; echo ok", 5, nil)
	if err != nil {
		t.Fatalf("ExecLocal failed: %v", err)
	}
	if result.Stdout != "ok\n" {
		t.Fatalf("Stdout = %q, want %q", result.Stdout, "ok\n")
	}
	if len(result.Stderr) < 131072 {
		t.Fatalf("len(Stderr) = %d, want at least 131072", len(result.Stderr))
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0", result.ExitCode)
	}
}

func TestExecLocalStreamSimple(t *testing.T) {
	e := NewExecutor(nil)

	var stdout, stderr bytes.Buffer
	exitCode, err := e.ExecLocalStream("echo hello", 0, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("ExecLocalStream failed: %v", err)
	}
	if stdout.String() != "hello\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "hello\n")
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
}

func TestExecLocalStreamStderr(t *testing.T) {
	e := NewExecutor(nil)

	var stdout, stderr bytes.Buffer
	_, err := e.ExecLocalStream("echo err >&2", 0, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("ExecLocalStream failed: %v", err)
	}
	if stderr.String() != "err\n" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "err\n")
	}
}

func TestExecLocalStreamExitCode(t *testing.T) {
	e := NewExecutor(nil)

	var stdout, stderr bytes.Buffer
	exitCode, err := e.ExecLocalStream("exit 7", 0, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("ExecLocalStream failed: %v", err)
	}
	if exitCode != 7 {
		t.Errorf("exitCode = %d, want 7", exitCode)
	}
}

func TestExecLocalStreamTimeout(t *testing.T) {
	e := NewExecutor(nil)

	var stdout, stderr bytes.Buffer
	exitCode, err := e.ExecLocalStream("sleep 10", 1, nil, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if exitCode != -1 {
		t.Errorf("exitCode = %d, want -1 (timeout)", exitCode)
	}
}

func TestExecLocalStreamPipe(t *testing.T) {
	e := NewExecutor(nil)

	var stdout, stderr bytes.Buffer
	exitCode, err := e.ExecLocalStream("echo hello | tr h H", 0, nil, &stdout, &stderr)
	if err != nil {
		t.Fatalf("ExecLocalStream pipe failed: %v", err)
	}
	if stdout.String() != "Hello\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "Hello\n")
	}
	if exitCode != 0 {
		t.Errorf("exitCode = %d, want 0", exitCode)
	}
}

func TestRunLocalStreamKillsProcessWhenWriterDisconnects(t *testing.T) {
	w := &failingWriter{}
	start := time.Now()
	code, err := runLocalNormalStream("printf boom; sleep 10", 0, w, io.Discard)
	if time.Since(start) > 3*time.Second {
		t.Fatal("stream execution did not stop after writer failure")
	}
	if err != nil && code == 0 {
		t.Fatalf("unexpected successful exit after writer failure: code=%d err=%v", code, err)
	}
}

type failingWriter struct{}

func (*failingWriter) Write([]byte) (int, error) { return 0, errors.New("client disconnected") }
