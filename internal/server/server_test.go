package server

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gssh/internal/audit"
	"gssh/internal/protocol"
)

func TestStopIsIdempotent(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	srv := NewServer(filepath.Join(t.TempDir(), "gssh.sock"))

	if _, err := srv.sessions.ConnectLocal("local"); err != nil {
		t.Fatalf("ConnectLocal failed: %v", err)
	}

	if err := srv.Stop(); err != nil {
		t.Fatalf("first Stop failed: %v", err)
	}

	if err := srv.Stop(); err != nil {
		t.Fatalf("second Stop failed: %v", err)
	}
}

func TestMonitorConnDisconnectCancelsQuietStream(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	cancel := make(chan struct{})
	done := make(chan struct{})
	monitorDone := make(chan struct{})
	go monitorConnDisconnect(serverConn, cancel, done, monitorDone)
	_ = clientConn.Close()
	select {
	case <-cancel:
	case <-time.After(2 * time.Second):
		t.Fatal("connection disconnect did not cancel stream")
	}
	close(done)
	<-monitorDone
	_ = serverConn.Close()
}

func TestRegisterTaskRejectsDuplicateID(t *testing.T) {
	s := NewServer(filepath.Join(t.TempDir(), "gssh.sock"))
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	first, err := s.registerTask("same", serverConn)
	if err != nil || first == nil {
		t.Fatalf("first registration failed: %v", err)
	}
	if second, err := s.registerTask("same", serverConn); err == nil || second != nil {
		t.Fatalf("duplicate registration should fail, got cancel=%v err=%v", second, err)
	}
	s.unregisterTask("same", serverConn)
}

func TestLogExecAuditRecordsExitCode(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	srv := NewServer(filepath.Join(t.TempDir(), "gssh.sock"))
	defer srv.audit.Close()

	srv.logExecAudit("local", "exit 7", nil, &protocol.ExecResult{ExitCode: 7})

	entry := readLastAuditEntry(t, homeDir)
	if entry.Action != "exec" || entry.Result != "exit_7" {
		t.Fatalf("unexpected audit entry: %+v", entry)
	}
}

func TestLogTransferAuditRecordsBusinessFailure(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)

	srv := NewServer(filepath.Join(t.TempDir(), "gssh.sock"))
	defer srv.audit.Close()

	srv.logTransferAudit("prod", "scp", "a -> b", nil, &protocol.TransferResult{
		Success: false,
		Message: "partial failure",
	})

	entry := readLastAuditEntry(t, homeDir)
	if entry.Action != "scp" || entry.Result != "error" || entry.Detail != "partial failure" {
		t.Fatalf("unexpected audit entry: %+v", entry)
	}
}

func readLastAuditEntry(t *testing.T, homeDir string) audit.Entry {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(homeDir, ".gssh", "audit.log"))
	if err != nil {
		t.Fatalf("read audit log failed: %v", err)
	}

	var entry audit.Entry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("unmarshal audit entry failed: %v", err)
	}

	return entry
}
