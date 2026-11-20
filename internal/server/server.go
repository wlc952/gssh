package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"gssh/internal/audit"
	"gssh/internal/exec"
	"gssh/internal/persist"
	"gssh/internal/portforward"
	"gssh/internal/protocol"
	"gssh/internal/reconnect"
	"gssh/internal/session"
	"gssh/internal/socketpath"
	"gssh/internal/transfer"

	"gssh/pkg/imsg"
)

// requestContext extracts optional correlation fields from any request
// payload. Older clients do not send task_id; those requests receive a
// generated id in error responses so failures remain diagnosable.
func requestContext(payload []byte) (sessionName, taskID string) {
	var v struct {
		Name   string `json:"name"`
		TaskID string `json:"task_id"`
	}
	_ = json.Unmarshal(payload, &v)
	if v.TaskID == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err == nil {
			v.TaskID = hex.EncodeToString(b[:])
		}
	}
	return v.Name, v.TaskID
}

func marshalRPCError(err error, sessionName, taskID string) []byte {
	payload, _ := json.Marshal(protocol.NewErrorPayload(err, sessionName, taskID))
	return payload
}

// Server is the core daemon that listens on a Unix socket and dispatches commands.
type Server struct {
	socketPath string
	listener   net.Listener
	sessions   *session.Manager
	executor   *exec.Executor
	forwards   *portforward.Service
	transfer   *transfer.Service
	reconnect  *reconnect.Monitor
	persist    *persist.Store
	audit      *audit.Logger
	wg         sync.WaitGroup
	shutdown   chan struct{}
	stopOnce   sync.Once
	connMu     sync.Mutex
	activeConn map[net.Conn]struct{}
	taskMu     sync.Mutex
	activeTask map[string]taskHandle
	acceptMu   sync.Mutex
	stopping   bool
}

type taskHandle struct {
	conn   net.Conn
	cancel chan struct{}
}

// NewServer creates a new server instance.
func NewServer(socketPath string) *Server {
	sessions := session.NewManager()

	s := &Server{
		socketPath: socketPath,
		sessions:   sessions,
		executor:   exec.NewExecutor(sessions),
		forwards:   portforward.NewService(sessions),
		transfer:   transfer.NewService(sessions),
		persist:    persist.NewStore(),
		audit:      audit.NewLogger(),
		shutdown:   make(chan struct{}),
		activeConn: make(map[net.Conn]struct{}),
		activeTask: make(map[string]taskHandle),
	}

	// Reconnect monitor uses the actual forwards service.
	s.reconnect = reconnect.NewMonitor(sessions, s.forwards)

	return s
}

// Start begins listening on the Unix socket.
func (s *Server) Start() error {
	if err := socketpath.EnsureParentDir(s.socketPath); err != nil {
		return fmt.Errorf("failed to create socket parent directory: %w", err)
	}
	if err := socketpath.RemoveIfOwnedSocket(s.socketPath); err != nil {
		return err
	}

	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.socketPath, err)
	}

	// Set socket permissions to 0600 (owner only)
	if err := os.Chmod(s.socketPath, 0600); err != nil {
		listener.Close()
		return fmt.Errorf("failed to set socket permissions: %w", err)
	}

	s.listener = listener
	log.Printf("[server] Listening on %s", s.socketPath)

	// Restore previous state if available
	s.restoreState()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-s.shutdown:
				return nil // graceful shutdown
			default:
				log.Printf("[server] Accept error: %v", err)
				continue
			}
		}
		select {
		case <-s.shutdown:
			conn.Close()
			return nil
		default:
		}
		s.acceptMu.Lock()
		if s.stopping {
			s.acceptMu.Unlock()
			conn.Close()
			return nil
		}
		s.addConn(conn)
		s.wg.Add(1)
		s.acceptMu.Unlock()
		go s.handleConn(conn)
	}
}

// Stop performs graceful shutdown.
func (s *Server) Stop() error {
	s.stopOnce.Do(func() {
		log.Println("[server] Shutting down...")

		close(s.shutdown)
		s.acceptMu.Lock()
		s.stopping = true
		s.acceptMu.Unlock()

		// 1. Close listener
		if s.listener != nil {
			s.listener.Close()
		}

		// 2. Persist state
		states := persist.CollectState(s.sessions.List())
		if err := s.persist.Save(states); err != nil {
			log.Printf("[server] Failed to persist state: %v", err)
		}

		// 3. Kill all sessions
		s.sessions.KillAll()

		// 4. Drain active handlers. Connections can appear while shutdown is in progress,
		// so keep closing tracked conns until all handlers exit.
		waitDone := make(chan struct{})
		go func() {
			s.wg.Wait()
			close(waitDone)
		}()
	drainLoop:
		for {
			s.closeActiveConns()
			select {
			case <-waitDone:
				break drainLoop
			case <-time.After(50 * time.Millisecond):
			}
		}

		// 5. Cleanup
		if err := socketpath.RemoveIfOwnedSocket(s.socketPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[server] Failed to remove socket: %v", err)
		}
		s.audit.Close()

		log.Println("[server] Shutdown complete")
	})

	return nil
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.removeConn(conn)
		conn.Close()
	}()

	for {
		msg, err := imsg.ReadMessage(conn)
		if err != nil {
			return // connection closed or error
		}

		// Streaming exec: the handler writes multiple frames directly to conn
		// before returning, so it bypasses the single-response dispatch loop.
		sessionName, taskID := requestContext(msg.Payload)
		if msg.Type == protocol.MsgExecStream {
			var params protocol.ExecParams
			if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
				errPayload := marshalRPCError(err, sessionName, taskID)
				imsg.WriteMessage(conn, imsg.NewImsg(protocol.MsgError, errPayload))
			} else {
				cancel, regErr := s.registerTask(params.TaskID, conn)
				if regErr != nil {
					errPayload := marshalRPCError(regErr, sessionName, taskID)
					_ = imsg.WriteMessage(conn, imsg.NewImsg(protocol.MsgError, errPayload))
					continue
				}
				done := make(chan struct{})
				monitorDone := make(chan struct{})
				go monitorConnDisconnect(conn, cancel, done, monitorDone)
				s.handleExecStreamCancelable(conn, params, cancel)
				close(done)
				<-monitorDone
				s.unregisterTask(params.TaskID, conn)
			}
			continue
		}

		if msg.Type == protocol.MsgLocalExecStream {
			var params protocol.LocalExecParams
			if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
				errPayload := marshalRPCError(err, sessionName, taskID)
				imsg.WriteMessage(conn, imsg.NewImsg(protocol.MsgError, errPayload))
			} else {
				cancel, regErr := s.registerTask(params.TaskID, conn)
				if regErr != nil {
					errPayload := marshalRPCError(regErr, sessionName, taskID)
					_ = imsg.WriteMessage(conn, imsg.NewImsg(protocol.MsgError, errPayload))
					continue
				}
				done := make(chan struct{})
				monitorDone := make(chan struct{})
				go monitorConnDisconnect(conn, cancel, done, monitorDone)
				s.handleLocalExecStreamCancelable(conn, params, cancel)
				close(done)
				<-monitorDone
				s.unregisterTask(params.TaskID, conn)
			}
			continue
		}

		response, err := s.dispatch(msg)
		if err != nil {
			// Send error response
			errPayload := marshalRPCError(err, sessionName, taskID)
			response = imsg.NewImsg(protocol.MsgError, errPayload)
		}

		// Guard against oversized payloads (e.g. huge buffered exec output):
		// fail with a clear error instead of dying mid-write or dropping the conn.
		if len(response.Payload) > imsg.MaxPayloadSize {
			errPayload := marshalRPCError(fmt.Errorf("response exceeds %d bytes; re-run with --stream for large output", imsg.MaxPayloadSize), sessionName, taskID)
			response = imsg.NewImsg(protocol.MsgError, errPayload)
		}

		if err := imsg.WriteMessage(conn, response); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(msg *imsg.Imsg) (*imsg.Imsg, error) {
	switch msg.Type {
	case protocol.MsgCancel:
		var params protocol.CancelParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if params.TaskID == "" {
			return nil, fmt.Errorf("task_id is required")
		}
		if s.cancelTask(params.TaskID) {
			return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"cancelling"}`)), nil
		}
		return nil, fmt.Errorf("task %q not found or already finished", params.TaskID)
	case protocol.MsgTaskList:
		s.taskMu.Lock()
		tasks := make([]protocol.TaskInfo, 0, len(s.activeTask))
		for id := range s.activeTask {
			tasks = append(tasks, protocol.TaskInfo{TaskID: id, Status: "running"})
		}
		s.taskMu.Unlock()
		payload, _ := protocol.EncodePayload(tasks)
		return imsg.NewImsg(protocol.MsgResult, payload), nil
	case protocol.MsgPing:
		return imsg.NewImsg(protocol.MsgPong, []byte(`{"status":"ok"}`)), nil

	case protocol.MsgConnect:
		var params protocol.ConnectParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if params.Timeout < 0 {
			return nil, fmt.Errorf("invalid connection timeout %d: must be zero or positive seconds", params.Timeout)
		}
		sess, err := s.sessions.ConnectSSHWithTimeout(params.Name, params.User, params.Host, params.Port, params.Password, params.KeyPath, params.Timeout, params.ProxyJump)
		if err != nil {
			s.audit.Log(audit.Entry{Action: "connect", Result: "error", Detail: err.Error()})
			return nil, err
		}
		// Start reconnect monitor for SSH sessions
		s.reconnect.Watch(sess)
		s.audit.Log(audit.Entry{Session: sess.GetName(), Action: "connect", Result: "success"})
		info := sessionToInfo(sess)
		payload, _ := protocol.EncodePayload(info)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgLocal:
		var params protocol.LocalParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		sess, err := s.sessions.ConnectLocal(params.Name)
		if err != nil {
			return nil, err
		}
		info := sessionToInfo(sess)
		payload, _ := protocol.EncodePayload(info)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgKill:
		var params protocol.KillParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		s.reconnect.StopWatch(params.Name)
		err := s.sessions.Kill(params.Name)
		if err != nil {
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "kill", Result: "success"})
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"killed"}`)), nil

	case protocol.MsgExec:
		var params protocol.ExecParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		result, err := s.executor.ExecWithCwdInput(params.Name, params.Command, params.Cwd, params.Timeout, &params.Sudo, params.Stdin)
		if err != nil {
			s.logExecAudit(params.Name, params.Command, err, nil)
			return nil, err
		}
		s.logExecAudit(params.Name, params.Command, nil, result)
		payload, _ := protocol.EncodePayload(result)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgLocalExec:
		var params protocol.LocalExecParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		result, err := s.executor.ExecLocalWithCwdInput(params.Command, params.Cwd, params.Timeout, &params.Sudo, params.Stdin)
		if err != nil {
			s.logExecAudit("", params.Command, err, nil)
			return nil, err
		}
		s.logExecAudit("", params.Command, nil, result)
		payload, _ := protocol.EncodePayload(result)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgList:
		sessions := s.sessions.List()
		infos := make([]protocol.SessionInfo, len(sessions))
		for i, sess := range sessions {
			infos[i] = sessionToInfo(sess)
		}
		payload, _ := protocol.EncodePayload(infos)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgUse:
		var params protocol.UseParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		// Check status before Use to decide if reconnect monitor is needed
		sess, _ := s.sessions.Get(params.Name)
		wasOffline := false
		if sess != nil && !sess.IsLocal() {
			status := sess.(*session.SSHSession).GetStatus()
			wasOffline = status == session.StatusOffline || status == session.StatusDisconnected
		}

		err := s.sessions.Use(params.Name, params.Password, params.KeyPath)
		if err != nil {
			return nil, err
		}
		// Only register reconnect monitor if session was offline/disconnected before Use
		if wasOffline {
			sess, _ = s.sessions.Get(params.Name)
			if sess != nil && !sess.IsLocal() {
				s.reconnect.Watch(sess.(*session.SSHSession))
			}
		}
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"ok"}`)), nil

	case protocol.MsgForward:
		var params protocol.ForwardParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		info, err := s.forwards.Add(params.Name, params.Type, params.LocalPort, params.RemotePort, params.BindAddr)
		if err != nil {
			return nil, err
		}
		payload, _ := protocol.EncodePayload(info)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgForwards:
		forwards := s.forwards.List()
		payload, _ := protocol.EncodePayload(forwards)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgForwardClose:
		var params protocol.ForwardCloseParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		err := s.forwards.Close(params.ID)
		if err != nil {
			return nil, err
		}
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"closed"}`)), nil

	case protocol.MsgSCP:
		var params protocol.SCPParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		var result *protocol.TransferResult
		var err error
		if params.IsUpload {
			result, err = s.transfer.Upload(params.Name, params.Source, params.Dest)
		} else {
			result, err = s.transfer.Download(params.Name, params.Source, params.Dest)
		}
		if err != nil {
			s.logTransferAudit(params.Name, "scp", params.Source+" -> "+params.Dest, err, nil)
			return nil, err
		}
		s.logTransferAudit(params.Name, "scp", params.Source+" -> "+params.Dest, nil, result)
		payload, _ := protocol.EncodePayload(result)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgSFTPLs:
		var params protocol.SFTPParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		files, err := s.transfer.ListDir(params.Name, params.Path)
		if err != nil {
			s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_ls", Command: params.Path, Result: "error", Detail: err.Error()})
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_ls", Command: params.Path, Result: "success"})
		payload, _ := protocol.EncodePayload(files)
		return imsg.NewImsg(protocol.MsgResult, payload), nil

	case protocol.MsgSFTPMkdir:
		var params protocol.SFTPParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		err := s.transfer.Mkdir(params.Name, params.Path)
		if err != nil {
			s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_mkdir", Command: params.Path, Result: "error", Detail: err.Error()})
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_mkdir", Command: params.Path, Result: "success"})
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"created"}`)), nil

	case protocol.MsgSFTPRm:
		var params protocol.SFTPParams
		if err := protocol.DecodePayload(msg.Payload, &params); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		err := s.transfer.Remove(params.Name, params.Path)
		if err != nil {
			s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_rm", Command: params.Path, Result: "error", Detail: err.Error()})
			return nil, err
		}
		s.audit.Log(audit.Entry{Session: params.Name, Action: "sftp_rm", Command: params.Path, Result: "success"})
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"removed"}`)), nil

	case protocol.MsgStop:
		// Trigger graceful shutdown from CLI
		go s.Stop()
		return imsg.NewImsg(protocol.MsgResult, []byte(`{"status":"stopping"}`)), nil

	default:
		return nil, fmt.Errorf("unknown message type: %d", msg.Type)
	}
}

func (s *Server) logExecAudit(sessionName, command string, err error, result *protocol.ExecResult) {
	if err != nil {
		s.audit.Log(audit.Entry{Session: sessionName, Action: "exec", Command: command, Result: "error", Detail: err.Error()})
		return
	}

	resultStr := "success"
	if result != nil && result.ExitCode != 0 {
		resultStr = fmt.Sprintf("exit_%d", result.ExitCode)
	}
	s.audit.Log(audit.Entry{Session: sessionName, Action: "exec", Command: command, Result: resultStr})
}

func (s *Server) logTransferAudit(sessionName, action, command string, err error, result *protocol.TransferResult) {
	if err != nil {
		s.audit.Log(audit.Entry{Session: sessionName, Action: action, Command: command, Result: "error", Detail: err.Error()})
		return
	}

	resultStr := "success"
	detail := ""
	if result != nil && !result.Success {
		resultStr = "error"
		detail = result.Message
	}
	s.audit.Log(audit.Entry{Session: sessionName, Action: action, Command: command, Result: resultStr, Detail: detail})
}

func sessionToInfo(sess session.Session) protocol.SessionInfo {
	port := 0
	if !sess.IsLocal() {
		sshSess := sess.(*session.SSHSession)
		port = sshSess.Port
	}

	return protocol.SessionInfo{
		Name:      sess.GetName(),
		Type:      sess.GetType(),
		Host:      sess.GetHost(),
		User:      sess.GetUser(),
		Port:      port,
		Status:    string(sess.GetStatus()),
		CreatedAt: sess.GetCreatedAt().Unix(),
		LastCmd:   sess.GetLastCmd(),
	}
}

func (s *Server) restoreState() {
	states, err := s.persist.Load()
	if err != nil {
		log.Printf("[server] Failed to load state: %v", err)
		return
	}

	if len(states) == 0 {
		return
	}

	log.Printf("[server] Restoring %d sessions from state", len(states))
	for _, state := range states {
		if state.Type == "local" {
			s.sessions.ConnectLocal(state.Name)
			continue
		}

		// SSH sessions: register as offline, then auto-reconnect in the
		// background when non-interactive auth is available (explicit key,
		// or default key material via agent/default key files). Password-only
		// sessions stay offline (passwords are never persisted) until the
		// agent runs `gssh use <name> --pswd password`.
		s.sessions.RegisterOfflineSession(
			state.Name, state.User, state.Host, state.Port,
			state.KeyPath, time.Unix(state.CreatedAt, 0), state.ProxyJump,
		)
		// state.KeyPath check keeps pre-auto_reconnect state files working.
		if state.AutoReconnect || state.KeyPath != "" {
			go func(name string) {
				sess, err := s.sessions.Reconnect(name)
				if err != nil {
					log.Printf("[server] Auto-reconnect of restored session %s failed: %v", name, err)
					return
				}
				s.reconnect.Watch(sess)
				log.Printf("[server] Restored session %s via key auth", name)
			}(state.Name)
		}
	}
}

func (s *Server) addConn(conn net.Conn) {
	s.connMu.Lock()
	s.activeConn[conn] = struct{}{}
	s.connMu.Unlock()
}

func (s *Server) removeConn(conn net.Conn) {
	s.connMu.Lock()
	delete(s.activeConn, conn)
	s.connMu.Unlock()
}

func (s *Server) closeActiveConns() {
	s.connMu.Lock()
	conns := make([]net.Conn, 0, len(s.activeConn))
	for conn := range s.activeConn {
		conns = append(conns, conn)
	}
	s.connMu.Unlock()

	for _, conn := range conns {
		conn.Close()
	}
}

// chunkWriter is an io.Writer that sends each Write call as a MsgStreamChunk
// frame over the connection.  stdout and stderr share the same mutex so that
// their frames never interleave on the wire.
type chunkWriter struct {
	conn   net.Conn
	stream string
	mu     *sync.Mutex
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	chunk := protocol.StreamChunk{
		Stream: w.stream,
		Data:   p,
	}
	payload, err := protocol.EncodePayload(chunk)
	if err != nil {
		return 0, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := imsg.WriteMessage(w.conn, imsg.NewImsg(protocol.MsgStreamChunk, payload)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Server) registerTask(id string, conn net.Conn) (chan struct{}, error) {
	cancel := make(chan struct{})
	if id == "" {
		return cancel, nil
	}
	s.taskMu.Lock()
	if _, exists := s.activeTask[id]; exists {
		s.taskMu.Unlock()
		return nil, fmt.Errorf("task ID %q is already active", id)
	}
	s.activeTask[id] = taskHandle{conn: conn, cancel: cancel}
	s.taskMu.Unlock()
	return cancel, nil
}

func (s *Server) unregisterTask(id string, conn net.Conn) {
	if id == "" {
		return
	}
	s.taskMu.Lock()
	if current, ok := s.activeTask[id]; ok && current.conn == conn {
		delete(s.activeTask, id)
	}
	s.taskMu.Unlock()
}

func (s *Server) cancelTask(id string) bool {
	s.taskMu.Lock()
	handle, ok := s.activeTask[id]
	if ok {
		delete(s.activeTask, id)
	}
	s.taskMu.Unlock()
	if !ok {
		return false
	}
	// Closing the stream connection makes its writers return an error. The
	// executor then terminates the local process group or SSH session.
	closeCancel(handle.cancel)
	_ = handle.conn.Close()
	return true
}

// monitorConnDisconnect watches an otherwise idle streaming connection for
// client closure. Streaming handlers are busy writing output and therefore do
// not read from the connection; without this watcher a quiet command (for
// example, "sleep 60") would keep running after the client disappeared.
// Short read deadlines let us poll for completion without permanently
// affecting the connection's normal read loop.
func monitorConnDisconnect(conn net.Conn, cancel chan struct{}, done <-chan struct{}, monitorDone chan<- struct{}) {
	defer func() {
		_ = conn.SetReadDeadline(time.Time{})
		close(monitorDone)
	}()
	buf := make([]byte, 1)
	for {
		select {
		case <-done:
			return
		default:
		}
		_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		_, err := conn.Read(buf)
		if err == nil {
			// No request is expected while a stream is active. Ignore any
			// incidental byte and continue watching for closure.
			continue
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			continue
		}
		closeCancel(cancel)
		return
	}
}

// closeCancel closes a task cancellation channel exactly once. Cancellation
// may race between an explicit gssh cancel request and connection teardown.
func closeCancel(ch chan struct{}) {
	defer func() { _ = recover() }()
	close(ch)
}

// sendStreamEnd writes a MsgStreamEnd frame to the connection.
func sendStreamEnd(conn net.Conn, exitCode int, execErr error) {
	end := protocol.StreamEnd{ExitCode: exitCode}
	if execErr != nil {
		end.Error = execErr.Error()
	}
	payload, _ := protocol.EncodePayload(end)
	imsg.WriteMessage(conn, imsg.NewImsg(protocol.MsgStreamEnd, payload))
}

func (s *Server) handleExecStream(conn net.Conn, params protocol.ExecParams) {
	s.handleExecStreamCancelable(conn, params, nil)
}

func (s *Server) handleExecStreamCancelable(conn net.Conn, params protocol.ExecParams, cancel <-chan struct{}) {
	mu := &sync.Mutex{}
	stdoutW := &chunkWriter{conn: conn, stream: "stdout", mu: mu}
	stderrW := &chunkWriter{conn: conn, stream: "stderr", mu: mu}

	exitCode, execErr := s.executor.ExecStreamWithCwdCancel(params.Name, params.Command, params.Cwd, params.Timeout, &params.Sudo, stdoutW, stderrW, cancel)
	s.logExecAudit(params.Name, params.Command, execErr, &protocol.ExecResult{ExitCode: exitCode})
	sendStreamEnd(conn, exitCode, execErr)
}

func (s *Server) handleLocalExecStream(conn net.Conn, params protocol.LocalExecParams) {
	s.handleLocalExecStreamCancelable(conn, params, nil)
}

func (s *Server) handleLocalExecStreamCancelable(conn net.Conn, params protocol.LocalExecParams, cancel <-chan struct{}) {
	mu := &sync.Mutex{}
	stdoutW := &chunkWriter{conn: conn, stream: "stdout", mu: mu}
	stderrW := &chunkWriter{conn: conn, stream: "stderr", mu: mu}

	exitCode, execErr := s.executor.ExecLocalStreamWithCwdCancel(params.Command, params.Cwd, params.Timeout, &params.Sudo, stdoutW, stderrW, cancel)
	s.logExecAudit("", params.Command, execErr, &protocol.ExecResult{ExitCode: exitCode})
	sendStreamEnd(conn, exitCode, execErr)
}
