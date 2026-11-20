package exec

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"gssh/internal/protocol"
	"gssh/internal/session"
	"gssh/internal/shellquote"

	"golang.org/x/crypto/ssh"
)

// execResult holds the output of a command execution.
type execResult struct {
	stdout []byte
	stderr []byte
	err    error
}

// Executor handles command execution for both SSH and local sessions.
type Executor struct {
	sessions *session.Manager
}

// NewExecutor creates a new command executor.
func NewExecutor(sessions *session.Manager) *Executor {
	return &Executor{sessions: sessions}
}

// sessionReadyTimeout bounds how long exec waits for an in-flight
// connect/reconnect dial before giving up.
const sessionReadyTimeout = 20 * time.Second

// Exec executes a command in the specified session context.
func (e *Executor) Exec(sessionName, command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	return e.ExecWithCwd(sessionName, command, "", timeout, sudoOpts)
}

// ExecWithCwd executes a command with an optional working directory.
func (e *Executor) ExecWithCwd(sessionName, command, cwd string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	return e.ExecWithCwdInput(sessionName, command, cwd, timeout, sudoOpts, nil)
}

// ExecWithCwdInput executes a command while feeding stdin bytes to it.
func (e *Executor) ExecWithCwdInput(sessionName, command, cwd string, timeout int, sudoOpts *protocol.SudoOptions, input []byte) (*protocol.ExecResult, error) {
	sess, err := e.sessions.Get(sessionName)
	if err != nil {
		return nil, err
	}

	sess.SetLastCmd(command)

	command = withWorkingDir(command, cwd)
	if sess.IsLocal() {
		return runLocalInput(command, timeout, sudoOpts, input)
	}

	sshSess := sess.(*session.SSHSession)
	sshClient, err := sshSess.AwaitClient(sessionReadyTimeout)
	if err != nil {
		return nil, err
	}

	return runRemoteInput(sshClient, command, timeout, sudoOpts, input)
}

// ExecLocal executes a one-off local command (no session needed).
func (e *Executor) ExecLocal(command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	return e.ExecLocalWithCwd(command, "", timeout, sudoOpts)
}

func (e *Executor) ExecLocalWithCwd(command, cwd string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	return e.ExecLocalWithCwdInput(command, cwd, timeout, sudoOpts, nil)
}

func (e *Executor) ExecLocalWithCwdInput(command, cwd string, timeout int, sudoOpts *protocol.SudoOptions, input []byte) (*protocol.ExecResult, error) {
	return runLocalInput(withWorkingDir(command, cwd), timeout, sudoOpts, input)
}

// streamExecResult holds the result of a streaming command execution.
type streamExecResult struct {
	exitCode int
	err      error
}

// ExecStream executes a command in the specified session, streaming stdout/stderr
// to the provided writers as output arrives.
// Returns the exit code and any fatal (non-exit-status) error.
func (e *Executor) ExecStream(sessionName, command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	return e.ExecStreamWithCwd(sessionName, command, "", timeout, sudoOpts, stdoutW, stderrW)
}

func (e *Executor) ExecStreamWithCwd(sessionName, command, cwd string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	return e.ExecStreamWithCwdCancel(sessionName, command, cwd, timeout, sudoOpts, stdoutW, stderrW, nil)
}

// ExecStreamWithCwdCancel is ExecStreamWithCwd with an optional cancellation signal.
func (e *Executor) ExecStreamWithCwdCancel(sessionName, command, cwd string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer, cancel <-chan struct{}) (int, error) {
	sess, err := e.sessions.Get(sessionName)
	if err != nil {
		return 1, err
	}

	sess.SetLastCmd(command)

	command = withWorkingDir(command, cwd)
	if sess.IsLocal() {
		return runLocalStreamCancel(command, timeout, sudoOpts, stdoutW, stderrW, cancel)
	}

	sshSess := sess.(*session.SSHSession)
	sshClient, err := sshSess.AwaitClient(sessionReadyTimeout)
	if err != nil {
		return 1, err
	}

	return runRemoteStreamCancel(sshClient, command, timeout, sudoOpts, stdoutW, stderrW, cancel)
}

// ExecLocalStream executes a one-off local command with streaming output (no session needed).
func (e *Executor) ExecLocalStream(command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	return e.ExecLocalStreamWithCwd(command, "", timeout, sudoOpts, stdoutW, stderrW)
}

func (e *Executor) ExecLocalStreamWithCwd(command, cwd string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	return runLocalStreamCancel(withWorkingDir(command, cwd), timeout, sudoOpts, stdoutW, stderrW, nil)
}

func (e *Executor) ExecLocalStreamWithCwdCancel(command, cwd string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer, cancel <-chan struct{}) (int, error) {
	return runLocalStreamCancel(withWorkingDir(command, cwd), timeout, sudoOpts, stdoutW, stderrW, cancel)
}

func withWorkingDir(command, cwd string) string {
	if cwd == "" {
		return command
	}
	// Keep the closing parenthesis on a new line so a trailing shell comment
	// in command cannot consume it.
	return "cd " + shellquote.Quote(cwd) + " && (\n" + command + "\n)"
}

// --- Remote streaming execution ---

func runRemoteStream(client *ssh.Client, command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	return runRemoteStreamCancel(client, command, timeout, sudoOpts, stdoutW, stderrW, nil)
}
func runRemoteStreamCancel(client *ssh.Client, command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer, cancel <-chan struct{}) (int, error) {
	sshSession, err := client.NewSession()
	if err != nil {
		return 1, fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer sshSession.Close()

	fullCmd := "/bin/bash -c " + shellquote.Quote(command)

	if sudoOpts != nil && sudoOpts.Enabled {
		return runRemoteSudoStreamCancel(sshSession, fullCmd, sudoOpts, timeout, stdoutW, stderrW, cancel)
	}

	return runRemoteNormalStreamCancel(sshSession, fullCmd, timeout, stdoutW, stderrW, cancel)
}

func runRemoteNormalStream(sshSession *ssh.Session, fullCmd string, timeout int, stdoutW, stderrW io.Writer) (int, error) {
	return runRemoteNormalStreamCancel(sshSession, fullCmd, timeout, stdoutW, stderrW, nil)
}
func runRemoteNormalStreamCancel(sshSession *ssh.Session, fullCmd string, timeout int, stdoutW, stderrW io.Writer, cancel <-chan struct{}) (int, error) {
	stdoutPipe, err := sshSession.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderrPipe, err := sshSession.StderrPipe()
	if err != nil {
		return 1, err
	}

	if err := sshSession.Start(fullCmd); err != nil {
		return 1, err
	}

	done := make(chan streamExecResult, 1)
	go func() {
		var wg sync.WaitGroup
		var closeOnce sync.Once
		abort := func() { closeOnce.Do(func() { _ = sshSession.Close() }) }
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := io.Copy(stdoutW, stdoutPipe); err != nil {
				abort()
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := io.Copy(stderrW, stderrPipe); err != nil {
				abort()
			}
		}()
		wg.Wait()

		code, exitErr := sshExitCode(sshSession.Wait())
		done <- streamExecResult{exitCode: code, err: exitErr}
	}()

	return waitStreamTimeoutCancel(done, sshSession, nil, timeout, cancel)
}

func runRemoteSudoStream(sshSession *ssh.Session, fullCmd string, sudoOpts *protocol.SudoOptions, timeout int, stdoutW, stderrW io.Writer) (int, error) {
	return runRemoteSudoStreamCancel(sshSession, fullCmd, sudoOpts, timeout, stdoutW, stderrW, nil)
}
func runRemoteSudoStreamCancel(sshSession *ssh.Session, fullCmd string, sudoOpts *protocol.SudoOptions, timeout int, stdoutW, stderrW io.Writer, cancel <-chan struct{}) (int, error) {
	wrappedCmd := buildSudoCommand(fullCmd, sudoOpts)

	stdoutPipe, err := sshSession.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderrPipe, err := sshSession.StderrPipe()
	if err != nil {
		return 1, err
	}
	stdinPipe, err := sshSession.StdinPipe()
	if err != nil {
		return 1, err
	}

	if err := sshSession.Start(wrappedCmd); err != nil {
		return 1, err
	}

	if err := writePassword(stdinPipe, sudoOpts.Password); err != nil {
		return 1, err
	}

	done := make(chan streamExecResult, 1)
	go func() {
		var wg sync.WaitGroup
		var closeOnce sync.Once
		abort := func() { closeOnce.Do(func() { _ = sshSession.Close() }) }
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := io.Copy(stdoutW, stdoutPipe); err != nil {
				abort()
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := io.Copy(stderrW, stderrPipe); err != nil {
				abort()
			}
		}()
		wg.Wait()

		code, exitErr := sshExitCode(sshSession.Wait())
		done <- streamExecResult{exitCode: code, err: exitErr}
	}()

	return waitStreamTimeoutCancel(done, sshSession, nil, timeout, cancel)
}

// sshExitCode normalises the error returned by ssh.Session.Wait:
// a non-zero exit is not a fatal error — it is returned as an exit code.
func sshExitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*ssh.ExitError); ok {
		return exitErr.ExitStatus(), nil
	}
	return 1, err
}

// --- Local streaming execution ---

func runLocalStream(command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer) (int, error) {
	return runLocalStreamCancel(command, timeout, sudoOpts, stdoutW, stderrW, nil)
}
func runLocalStreamCancel(command string, timeout int, sudoOpts *protocol.SudoOptions, stdoutW, stderrW io.Writer, cancel <-chan struct{}) (int, error) {
	fullCmd := "/bin/bash -c " + shellquote.Quote(command)

	if sudoOpts != nil && sudoOpts.Enabled {
		return runLocalSudoStreamCancel(fullCmd, sudoOpts, timeout, stdoutW, stderrW, cancel)
	}

	return runLocalNormalStreamCancel(fullCmd, timeout, stdoutW, stderrW, cancel)
}

func runLocalNormalStream(fullCmd string, timeout int, stdoutW, stderrW io.Writer) (int, error) {
	return runLocalNormalStreamCancel(fullCmd, timeout, stdoutW, stderrW, nil)
}
func runLocalNormalStreamCancel(fullCmd string, timeout int, stdoutW, stderrW io.Writer, cancel <-chan struct{}) (int, error) {
	cmd := exec.Command("/bin/sh", "-c", fullCmd)
	configureProcessGroup(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return 1, err
	}

	if err := cmd.Start(); err != nil {
		return 1, err
	}

	done := make(chan streamExecResult, 1)
	go func() {
		var wg sync.WaitGroup
		var killOnce sync.Once
		kill := func() { killProcessGroup(cmd) }
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := io.Copy(stdoutW, stdoutPipe); err != nil {
				killOnce.Do(kill)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := io.Copy(stderrW, stderrPipe); err != nil {
				killOnce.Do(kill)
			}
		}()
		wg.Wait()

		code, exitErr := localExitCode(cmd.Wait())
		done <- streamExecResult{exitCode: code, err: exitErr}
	}()

	return waitStreamTimeoutCancel(done, nil, cmd, timeout, cancel)
}

func runLocalSudoStream(fullCmd string, sudoOpts *protocol.SudoOptions, timeout int, stdoutW, stderrW io.Writer) (int, error) {
	return runLocalSudoStreamCancel(fullCmd, sudoOpts, timeout, stdoutW, stderrW, nil)
}
func runLocalSudoStreamCancel(fullCmd string, sudoOpts *protocol.SudoOptions, timeout int, stdoutW, stderrW io.Writer, cancel <-chan struct{}) (int, error) {
	sudoCmd := buildSudoCommand(fullCmd, sudoOpts)
	cmd := exec.Command("/bin/sh", "-c", sudoCmd)
	configureProcessGroup(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return 1, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return 1, err
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return 1, err
	}

	if err := cmd.Start(); err != nil {
		return 1, err
	}

	if err := writePassword(stdinPipe, sudoOpts.Password); err != nil {
		return 1, err
	}

	done := make(chan streamExecResult, 1)
	go func() {
		var wg sync.WaitGroup
		var killOnce sync.Once
		kill := func() { killProcessGroup(cmd) }
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := io.Copy(stdoutW, stdoutPipe); err != nil {
				killOnce.Do(kill)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := io.Copy(stderrW, stderrPipe); err != nil {
				killOnce.Do(kill)
			}
		}()
		wg.Wait()

		code, exitErr := localExitCode(cmd.Wait())
		done <- streamExecResult{exitCode: code, err: exitErr}
	}()

	return waitStreamTimeoutCancel(done, nil, cmd, timeout, cancel)
}

// localExitCode normalises the error returned by cmd.Wait for local processes.
func localExitCode(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	return 1, err
}

// waitStreamTimeout waits for a streaming goroutine to finish, applying an
// optional timeout.  sshSession / cmd are used to forcefully stop the process
// on timeout; pass nil for the one that does not apply.
func waitStreamTimeout(done <-chan streamExecResult, sshSession *ssh.Session, cmd *exec.Cmd, timeout int) (int, error) {
	return waitStreamTimeoutCancel(done, sshSession, cmd, timeout, nil)
}
func waitStreamTimeoutCancel(done <-chan streamExecResult, sshSession *ssh.Session, cmd *exec.Cmd, timeout int, cancel <-chan struct{}) (int, error) {
	if timeout <= 0 {
		if cancel == nil {
			res := <-done
			return res.exitCode, res.err
		}
		select {
		case res := <-done:
			return res.exitCode, res.err
		case <-cancel:
			if sshSession != nil {
				_ = sshSession.Close()
			}
			if cmd != nil {
				killProcessGroup(cmd)
			}
			<-done
			return -1, fmt.Errorf("command cancelled")
		}
	}

	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()

	select {
	case res := <-done:
		return res.exitCode, res.err
	case <-timer.C:
		if sshSession != nil {
			sshSession.Signal(ssh.SIGKILL)
			sshSession.Close()
		}
		if cmd != nil && cmd.Process != nil {
			killProcessGroup(cmd)
		}
		<-done
		return -1, fmt.Errorf("command timed out")
	case <-cancel:
		if sshSession != nil {
			_ = sshSession.Close()
		}
		if cmd != nil {
			killProcessGroup(cmd)
		}
		<-done
		return -1, fmt.Errorf("command cancelled")
	}
}

func runRemote(client *ssh.Client, command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	return runRemoteInput(client, command, timeout, sudoOpts, nil)
}
func runRemoteInput(client *ssh.Client, command string, timeout int, sudoOpts *protocol.SudoOptions, input []byte) (*protocol.ExecResult, error) {
	sshSession, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("failed to create SSH session: %w", err)
	}
	defer sshSession.Close()

	fullCmd := "/bin/bash -c " + shellquote.Quote(command)

	if sudoOpts != nil && sudoOpts.Enabled {
		return runRemoteSudoInput(sshSession, fullCmd, sudoOpts, timeout, input)
	}
	return runRemoteNormalInput(sshSession, fullCmd, timeout, input)
}

func runRemoteNormal(sshSession *ssh.Session, fullCmd string, timeout int) (*protocol.ExecResult, error) {
	return runRemoteNormalInput(sshSession, fullCmd, timeout, nil)
}
func runRemoteNormalInput(sshSession *ssh.Session, fullCmd string, timeout int, input []byte) (*protocol.ExecResult, error) {
	done := make(chan execResult, 1)

	go func() {
		stdoutPipe, err := sshSession.StdoutPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}
		stderrPipe, err := sshSession.StderrPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}
		stdinPipe, err := sshSession.StdinPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := sshSession.Start(fullCmd); err != nil {
			done <- execResult{nil, nil, err}
			return
		}
		if len(input) > 0 {
			if _, err := stdinPipe.Write(input); err != nil {
				done <- execResult{nil, nil, err}
				return
			}
		}
		_ = stdinPipe.Close()

		stdoutBuf, stderrBuf, err := readPipes(stdoutPipe, stderrPipe)
		if err != nil {
			done <- execResult{stdoutBuf, stderrBuf, err}
			return
		}

		err = sshSession.Wait()
		done <- execResult{stdoutBuf, stderrBuf, err}
	}()

	res, timedOut := waitTimeout(done, timeout)
	if timedOut {
		sshSession.Signal(ssh.SIGKILL)
		sshSession.Close()
		<-done
		return &protocol.ExecResult{Stdout: "", Stderr: "", ExitCode: -1}, fmt.Errorf("command timed out")
	}

	return sshExecResult(res), nil
}

func runRemoteSudo(sshSession *ssh.Session, fullCmd string, sudoOpts *protocol.SudoOptions, timeout int) (*protocol.ExecResult, error) {
	return runRemoteSudoInput(sshSession, fullCmd, sudoOpts, timeout, nil)
}
func runRemoteSudoInput(sshSession *ssh.Session, fullCmd string, sudoOpts *protocol.SudoOptions, timeout int, input []byte) (*protocol.ExecResult, error) {
	wrappedCmd := buildSudoCommand(fullCmd, sudoOpts)

	done := make(chan execResult, 1)

	go func() {
		stdoutPipe, err := sshSession.StdoutPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}
		stderrPipe, err := sshSession.StderrPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}
		stdinPipe, err := sshSession.StdinPipe()
		if err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := sshSession.Start(wrappedCmd); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		if err := writePasswordAndInput(stdinPipe, sudoOpts.Password, input); err != nil {
			done <- execResult{nil, nil, err}
			return
		}

		stdoutBuf, stderrBuf, err := readPipes(stdoutPipe, stderrPipe)
		if err != nil {
			done <- execResult{stdoutBuf, stderrBuf, err}
			return
		}

		err = sshSession.Wait()
		done <- execResult{stdoutBuf, stderrBuf, err}
	}()

	res, timedOut := waitTimeout(done, timeout)
	if timedOut {
		sshSession.Signal(ssh.SIGKILL)
		sshSession.Close()
		<-done
		return &protocol.ExecResult{Stdout: "", Stderr: "", ExitCode: -1}, fmt.Errorf("command timed out")
	}

	return sshExecResult(res), nil
}

// --- Local execution ---

func runLocal(command string, timeout int, sudoOpts *protocol.SudoOptions) (*protocol.ExecResult, error) {
	return runLocalInput(command, timeout, sudoOpts, nil)
}
func runLocalInput(command string, timeout int, sudoOpts *protocol.SudoOptions, input []byte) (*protocol.ExecResult, error) {
	fullCmd := "/bin/bash -c " + shellquote.Quote(command)

	if sudoOpts != nil && sudoOpts.Enabled {
		return runLocalSudoInput(fullCmd, sudoOpts, timeout, input)
	}
	return runLocalNormalInput(fullCmd, timeout, input)
}

func runLocalNormal(fullCmd string, timeout int) (*protocol.ExecResult, error) {
	return runLocalNormalInput(fullCmd, timeout, nil)
}
func runLocalNormalInput(fullCmd string, timeout int, input []byte) (*protocol.ExecResult, error) {
	cmd := exec.Command("/bin/sh", "-c", fullCmd)
	if len(input) > 0 {
		cmd.Stdin = bytes.NewReader(input)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	done := make(chan execResult, 1)
	go func() {

		stdoutBuf, stderrBuf, err := readPipes(stdoutPipe, stderrPipe)
		if err != nil {
			done <- execResult{stdoutBuf, stderrBuf, err}
			return
		}

		err = cmd.Wait()
		done <- execResult{stdoutBuf, stderrBuf, err}
	}()

	res, timedOut := waitTimeout(done, timeout)
	if timedOut {
		cmd.Process.Kill()
		<-done
		return &protocol.ExecResult{Stdout: "", Stderr: "", ExitCode: -1}, fmt.Errorf("command timed out")
	}

	return localExecResult(res), nil
}

func runLocalSudo(fullCmd string, sudoOpts *protocol.SudoOptions, timeout int) (*protocol.ExecResult, error) {
	return runLocalSudoInput(fullCmd, sudoOpts, timeout, nil)
}
func runLocalSudoInput(fullCmd string, sudoOpts *protocol.SudoOptions, timeout int, input []byte) (*protocol.ExecResult, error) {
	sudoCmd := buildSudoCommand(fullCmd, sudoOpts)

	cmd := exec.Command("/bin/sh", "-c", sudoCmd)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := writePasswordAndInput(stdinPipe, sudoOpts.Password, input); err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}

	done := make(chan execResult, 1)
	go func() {

		stdoutBuf, stderrBuf, err := readPipes(stdoutPipe, stderrPipe)
		if err != nil {
			done <- execResult{stdoutBuf, stderrBuf, err}
			return
		}

		err = cmd.Wait()
		done <- execResult{stdoutBuf, stderrBuf, err}
	}()

	res, timedOut := waitTimeout(done, timeout)
	if timedOut {
		cmd.Process.Kill()
		<-done
		return &protocol.ExecResult{Stdout: "", Stderr: "", ExitCode: -1}, fmt.Errorf("command timed out")
	}

	return localExecResult(res), nil
}

// --- Sudo helpers (safe, no shell injection) ---

func writePassword(stdin io.WriteCloser, password string) error {
	return writePasswordAndInput(stdin, password, nil)
}
func writePasswordAndInput(stdin io.WriteCloser, password string, input []byte) error {
	defer stdin.Close()

	if password != "" {
		if _, err := io.WriteString(stdin, password+"\n"); err != nil {
			return fmt.Errorf("failed to write sudo password: %w", err)
		}
	}
	if len(input) > 0 {
		if _, err := stdin.Write(input); err != nil {
			return err
		}
	}

	return nil
}

func buildSudoPrefix(sudoOpts *protocol.SudoOptions) string {
	if sudoOpts.Login {
		return "sudo -i"
	}
	if sudoOpts.User != "" {
		return fmt.Sprintf("sudo -u %s", shellquote.Quote(sudoOpts.User))
	}
	return "sudo"
}

func buildSudoCommand(fullCmd string, sudoOpts *protocol.SudoOptions) string {
	return fmt.Sprintf("%s -S -p '' %s", buildSudoPrefix(sudoOpts), fullCmd)
}

func readPipes(stdoutPipe, stderrPipe io.Reader) ([]byte, []byte, error) {
	type readResult struct {
		stream string
		data   []byte
		err    error
	}

	var wg sync.WaitGroup
	results := make(chan readResult, 2)

	read := func(stream string, r io.Reader) {
		defer wg.Done()
		data, err := io.ReadAll(r)
		results <- readResult{stream: stream, data: data, err: err}
	}

	wg.Add(2)
	go read("stdout", stdoutPipe)
	go read("stderr", stderrPipe)
	wg.Wait()
	close(results)

	var stdoutBuf []byte
	var stderrBuf []byte
	var readErr error

	for result := range results {
		if result.stream == "stdout" {
			stdoutBuf = result.data
		} else {
			stderrBuf = result.data
		}
		if readErr == nil && result.err != nil {
			readErr = result.err
		}
	}

	return stdoutBuf, stderrBuf, readErr
}

// --- Result processing helpers ---

func sshExecResult(res execResult) *protocol.ExecResult {
	if res.err != nil {
		if exitErr, ok := res.err.(*ssh.ExitError); ok {
			return &protocol.ExecResult{
				Stdout:   string(res.stdout),
				Stderr:   string(res.stderr),
				ExitCode: exitErr.ExitStatus(),
			}
		}
	}
	return &protocol.ExecResult{
		Stdout:   string(res.stdout),
		Stderr:   string(res.stderr),
		ExitCode: exitCodeFromErr(res.err),
	}
}

func localExecResult(res execResult) *protocol.ExecResult {
	if res.err != nil {
		if exitErr, ok := res.err.(*exec.ExitError); ok {
			stderr := res.stderr
			if len(exitErr.Stderr) > 0 {
				stderr = append(stderr, exitErr.Stderr...)
			}
			return &protocol.ExecResult{
				Stdout:   string(res.stdout),
				Stderr:   string(bytes.Clone(stderr)),
				ExitCode: exitErr.ExitCode(),
			}
		}
	}
	return &protocol.ExecResult{
		Stdout:   string(res.stdout),
		Stderr:   string(res.stderr),
		ExitCode: exitCodeFromErr(res.err),
	}
}

func exitCodeFromErr(err error) int {
	if err == nil {
		return 0
	}
	return 1 // generic error, no exit status available
}

// --- Timeout helper ---

func waitTimeout(done chan execResult, timeout int) (execResult, bool) {
	if timeout <= 0 {
		return <-done, false
	}

	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()

	select {
	case res := <-done:
		return res, false
	case <-timer.C:
		return execResult{}, true
	}
}
