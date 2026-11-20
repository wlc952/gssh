package transfer

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyRemotePrefix(t *testing.T) {
	tests := []struct {
		name   string
		local  string
		remote string
		n      int64
		want   bool
	}{
		{name: "matching prefix", local: "abcdef", remote: "abc", n: 3, want: true},
		{name: "different bytes", local: "abcdef", remote: "abx", n: 3, want: false},
		{name: "short remote", local: "abcdef", remote: "ab", n: 3, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := verifyRemotePrefix(bytes.NewReader([]byte(tt.local)), bytes.NewReader([]byte(tt.remote)), tt.n)
			if err != nil {
				t.Fatalf("verifyRemotePrefix returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("verifyRemotePrefix = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestVerifyRemotePrefixWithExistingDownload(t *testing.T) {
	dir := t.TempDir()
	localPath := filepath.Join(dir, "local")
	remotePath := filepath.Join(dir, "remote")
	if err := os.WriteFile(localPath, []byte("stale-data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(remotePath, []byte("fresh-data-and-more"), 0600); err != nil {
		t.Fatal(err)
	}
	local, err := os.OpenFile(localPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	remote, err := os.Open(remotePath)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()
	equal, err := verifyRemotePrefix(local, remote, int64(len("stale-data")))
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("stale local file must not be treated as resumable prefix")
	}
}

func TestEnsureLocalParentDirCreatesMissingDirectories(t *testing.T) {
	target := filepath.Join(t.TempDir(), "nested", "dir", "file.txt")

	if err := ensureLocalParentDir(target); err != nil {
		t.Fatalf("ensureLocalParentDir failed: %v", err)
	}

	if _, err := os.Stat(filepath.Dir(target)); err != nil {
		t.Fatalf("expected parent directory to exist: %v", err)
	}
}

func TestEnsureLocalParentDirAllowsCurrentDirectory(t *testing.T) {
	if err := ensureLocalParentDir("file.txt"); err != nil {
		t.Fatalf("ensureLocalParentDir failed: %v", err)
	}
}

func TestCheckPathTraversalRejectsEscapingPath(t *testing.T) {
	if err := checkPathTraversal("../etc/passwd"); err == nil {
		t.Fatal("expected path traversal error")
	}
}

func TestCheckPathTraversalAllowsLocalPath(t *testing.T) {
	if err := checkPathTraversal("nested/file.txt"); err != nil {
		t.Fatalf("unexpected path traversal error: %v", err)
	}
}

func TestSafeLocalDownloadPathRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	if err := os.MkdirAll(filepath.Join(root, "nested"), 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "nested", "escape")); err != nil {
		t.Fatalf("Symlink failed: %v", err)
	}

	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks failed: %v", err)
	}

	target := filepath.Join(root, "nested", "escape", "file.txt")
	if _, err := safeLocalDownloadPath(root, rootResolved, target); err == nil {
		t.Fatal("expected symlink escape error")
	}
}

func TestSafeLocalDownloadPathAllowsRegularPath(t *testing.T) {
	root := t.TempDir()
	rootResolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks failed: %v", err)
	}

	target := filepath.Join(root, "nested", "file.txt")
	safe, err := safeLocalDownloadPath(root, rootResolved, target)
	if err != nil {
		t.Fatalf("safeLocalDownloadPath failed: %v", err)
	}
	if safe != target {
		t.Fatalf("safeLocalDownloadPath = %s, want %s", safe, target)
	}
}

func TestSafeRootOpenFileCreatesRegularFile(t *testing.T) {
	dir := t.TempDir()
	root, err := openSafeRoot(dir)
	if err != nil {
		t.Fatalf("openSafeRoot failed: %v", err)
	}
	defer root.Close()

	f, err := root.openFile("safe.txt", 0600)
	if err != nil {
		t.Fatalf("openFile failed: %v", err)
	}
	defer f.Close()
}

func TestResolveDownloadFileTarget(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name       string
		localPath  string
		remotePath string
		want       string
	}{
		{name: "plain file target unchanged", localPath: filepath.Join(dir, "out.log"), remotePath: "/var/log/app.log", want: filepath.Join(dir, "out.log")},
		{name: "trailing slash joins basename", localPath: dir + "/", remotePath: "/var/log/app.log", want: filepath.Join(dir, "app.log")},
		{name: "existing directory joins basename", localPath: dir, remotePath: "/var/log/app.log", want: filepath.Join(dir, "app.log")},
	}

	for _, tt := range tests {
		if got := resolveDownloadFileTarget(tt.localPath, tt.remotePath); got != tt.want {
			t.Errorf("%s: resolveDownloadFileTarget(%q, %q) = %q, want %q", tt.name, tt.localPath, tt.remotePath, got, tt.want)
		}
	}
}
