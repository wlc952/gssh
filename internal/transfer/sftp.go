package transfer

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gssh/internal/protocol"
	"gssh/internal/session"

	"github.com/pkg/sftp"
)

// Service handles file transfer operations via SFTP.
type Service struct {
	sessions *session.Manager
}

// NewService creates a new transfer service.
func NewService(sessions *session.Manager) *Service {
	return &Service{sessions: sessions}
}

// Upload transfers files from local to remote.
func (s *Service) Upload(sessionName, localPath, remotePath string) (*protocol.TransferResult, error) {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	start := time.Now()
	bytes, err := sess.Upload(localPath, remotePath)
	duration := time.Since(start).Milliseconds()

	if err != nil {
		return &protocol.TransferResult{
			Success:  false,
			Message:  err.Error(),
			Bytes:    bytes,
			Duration: duration,
		}, nil
	}

	return &protocol.TransferResult{
		Success:  true,
		Message:  fmt.Sprintf("Transferred %d bytes in %dms", bytes, duration),
		Bytes:    bytes,
		Duration: duration,
	}, nil
}

// Download transfers files from remote to local.
func (s *Service) Download(sessionName, remotePath, localPath string) (*protocol.TransferResult, error) {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	start := time.Now()
	bytes, err := sess.Download(remotePath, localPath)
	duration := time.Since(start).Milliseconds()

	if err != nil {
		return &protocol.TransferResult{
			Success:  false,
			Message:  err.Error(),
			Bytes:    bytes,
			Duration: duration,
		}, nil
	}

	return &protocol.TransferResult{
		Success:  true,
		Message:  fmt.Sprintf("Transferred %d bytes in %dms", bytes, duration),
		Bytes:    bytes,
		Duration: duration,
	}, nil
}

// ListDir lists files in a remote directory.
func (s *Service) ListDir(sessionName, path string) ([]string, error) {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return nil, err
	}
	defer sess.Close()

	return sess.List(path)
}

// Mkdir creates a remote directory.
func (s *Service) Mkdir(sessionName, path string) error {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return err
	}
	defer sess.Close()

	return sess.Mkdir(path)
}

// Remove removes a remote file.
func (s *Service) Remove(sessionName, path string) error {
	sess, err := s.getSFTPSession(sessionName)
	if err != nil {
		return err
	}
	defer sess.Close()

	return sess.Remove(path)
}

// sessionReadyTimeout bounds how long a transfer waits for an in-flight
// connect/reconnect dial before giving up.
const sessionReadyTimeout = 20 * time.Second

// --- SFTP Client wrapper ---

type sftpSession struct {
	client *sftp.Client
}

func (s *Service) getSFTPSession(sessionName string) (*sftpSession, error) {
	sess, err := s.sessions.Get(sessionName)
	if err != nil {
		return nil, err
	}

	if sess.IsLocal() {
		return nil, fmt.Errorf("SFTP not available for local sessions")
	}

	sshSess := sess.(*session.SSHSession)
	sshClient, err := sshSess.AwaitClient(sessionReadyTimeout)
	if err != nil {
		return nil, err
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		return nil, fmt.Errorf("failed to create SFTP client: %w", err)
	}

	return &sftpSession{client: sftpClient}, nil
}

func (s *sftpSession) Close() error {
	return s.client.Close()
}

func (s *sftpSession) Upload(localPath, remotePath string) (int64, error) {
	localInfo, err := os.Stat(localPath)
	if err != nil {
		return 0, fmt.Errorf("failed to stat local path: %w", err)
	}

	if !localInfo.IsDir() {
		target, err := s.resolveUploadFileTarget(localPath, remotePath)
		if err != nil {
			return 0, err
		}
		return s.uploadFile(localPath, target)
	}

	var totalWritten int64
	err = filepath.Walk(localPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(localPath, path)
		if err != nil {
			return err
		}

		targetPath := filepath.ToSlash(filepath.Join(remotePath, relPath))

		if info.IsDir() {
			if err := s.client.MkdirAll(targetPath); err != nil {
				return fmt.Errorf("failed to create remote directory %s: %w", targetPath, err)
			}
			return nil
		}

		// Skip if same size+mtime
		remoteInfo, err := s.client.Stat(targetPath)
		if err == nil && remoteInfo.Size() == info.Size() && remoteInfo.ModTime().Unix() == info.ModTime().Unix() {
			return nil
		}

		written, err := s.uploadFile(path, targetPath)
		if err != nil {
			return err
		}
		totalWritten += written
		if err := s.client.Chtimes(targetPath, info.ModTime(), info.ModTime()); err != nil {
			return fmt.Errorf("failed to preserve remote timestamps for %s: %w", targetPath, err)
		}
		return nil
	})

	return totalWritten, err
}

// resolveUploadFileTarget maps scp-style directory destinations to the full
// remote file path: "." (session home), a trailing slash, or an existing
// remote directory all mean "inside that directory, same basename".
// SFTP paths always use forward slashes.
func (s *sftpSession) resolveUploadFileTarget(localPath, remotePath string) (string, error) {
	base := filepath.ToSlash(filepath.Base(localPath))
	if remotePath == "" || remotePath == "." {
		return base, nil
	}
	if strings.HasSuffix(remotePath, "/") {
		remotePath = strings.TrimSuffix(remotePath, "/")
		info, err := s.client.Stat(remotePath)
		if err != nil || !info.IsDir() {
			return "", fmt.Errorf("remote destination %s is not a directory", remotePath)
		}
		return remotePath + "/" + base, nil
	}
	if info, err := s.client.Stat(remotePath); err == nil && info.IsDir() {
		return remotePath + "/" + base, nil
	}
	return remotePath, nil
}

// resolveDownloadFileTarget maps scp-style directory destinations to the
// full local file path: a trailing slash or an existing local directory
// means "inside that directory, same basename".
func resolveDownloadFileTarget(localPath, remotePath string) string {
	base := filepath.Base(strings.TrimSuffix(remotePath, "/"))
	if strings.HasSuffix(localPath, "/") || strings.HasSuffix(localPath, string(os.PathSeparator)) {
		return filepath.Join(localPath, base)
	}
	if info, err := os.Stat(localPath); err == nil && info.IsDir() {
		return filepath.Join(localPath, base)
	}
	return localPath
}

func (s *sftpSession) uploadFile(localPath, remotePath string) (int64, error) {
	localFile, err := os.Open(localPath)
	if err != nil {
		return 0, fmt.Errorf("failed to open local file: %w", err)
	}
	defer localFile.Close()

	localInfo, err := localFile.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to stat local file: %w", err)
	}

	if err := s.client.MkdirAll(filepath.ToSlash(filepath.Dir(remotePath))); err != nil {
		return 0, fmt.Errorf("failed to create remote parent directory: %w", err)
	}

	// Resume an interrupted upload when the remote file is a strict prefix of
	// the local file. A larger or equal remote file is restarted to avoid
	// silently producing corrupted content.
	var offset int64
	if info, statErr := s.client.Stat(remotePath); statErr == nil && info.Size() > 0 && info.Size() < localInfo.Size() {
		// A size-only check is insufficient: an unrelated shorter remote file
		// would otherwise be appended to and silently corrupt the result. Verify
		// that the remote bytes are an exact prefix of the local file first.
		remoteRead, openErr := s.client.Open(remotePath)
		if openErr != nil {
			return 0, fmt.Errorf("failed to open remote file for resume verification: %w", openErr)
		}
		equal, verifyErr := verifyRemotePrefix(localFile, remoteRead, info.Size())
		closeErr := remoteRead.Close()
		if verifyErr != nil {
			return 0, fmt.Errorf("failed to verify remote prefix: %w", verifyErr)
		}
		if closeErr != nil {
			return 0, fmt.Errorf("failed to close remote file after resume verification: %w", closeErr)
		}
		if equal {
			offset = info.Size()
		}
	}
	flags := os.O_WRONLY | os.O_CREATE
	if offset == 0 {
		flags |= os.O_TRUNC
	}
	remoteFile, err := s.client.OpenFile(remotePath, flags)
	if err != nil {
		return 0, fmt.Errorf("failed to create remote file: %w", err)
	}
	defer remoteFile.Close()
	// Prefix verification may have advanced localFile even when resume is
	// rejected; always position it at the actual copy offset.
	if _, err := localFile.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	if offset > 0 {
		if _, err := remoteFile.Seek(offset, io.SeekStart); err != nil {
			return 0, err
		}
	}

	written, err := io.Copy(remoteFile, localFile)
	if err != nil {
		return 0, fmt.Errorf("failed to upload file: %w", err)
	}

	if err := s.client.Chmod(remotePath, localInfo.Mode()); err != nil {
		return 0, fmt.Errorf("failed to preserve remote mode: %w", err)
	}
	return written, nil
}

// verifyRemotePrefix reports whether the first n bytes of remote match the
// first n bytes of local. Both files are rewound before reading and local is
// rewound again by the caller's subsequent seek when a resume is selected.
func verifyRemotePrefix(local, remote io.ReadSeeker, n int64) (bool, error) {
	if n <= 0 {
		return true, nil
	}
	if _, err := local.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	if _, err := remote.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	const chunkSize = 32 * 1024
	lbuf := make([]byte, chunkSize)
	rbuf := make([]byte, chunkSize)
	remaining := n
	for remaining > 0 {
		want := int64(chunkSize)
		if remaining < want {
			want = remaining
		}
		ln, lerr := io.ReadFull(local, lbuf[:want])
		rn, rerr := io.ReadFull(remote, rbuf[:want])
		if ln != rn || !bytes.Equal(lbuf[:ln], rbuf[:rn]) {
			return false, nil
		}
		if lerr != nil || rerr != nil {
			return false, fmt.Errorf("short read while verifying prefix (local: %v, remote: %v)", lerr, rerr)
		}
		remaining -= want
	}
	return true, nil
}

func (s *sftpSession) Download(remotePath, localPath string) (int64, error) {
	remoteInfo, err := s.client.Stat(remotePath)
	if err != nil {
		return 0, fmt.Errorf("failed to stat remote path: %w", err)
	}

	if !remoteInfo.IsDir() {
		localPath = resolveDownloadFileTarget(localPath, remotePath)
		targetPath, rootAbs, rootResolved, err := prepareSingleFileDownloadTarget(localPath)
		if err != nil {
			return 0, err
		}
		safeTarget, err := safeLocalDownloadPath(rootAbs, rootResolved, targetPath)
		if err != nil {
			return 0, err
		}
		root, err := openSafeRoot(rootAbs)
		if err != nil {
			return 0, err
		}
		defer root.Close()

		return s.downloadFile(remotePath, root, filepath.Base(safeTarget), remoteInfo.ModTime())
	}

	rootAbs, rootResolved, err := prepareDownloadRoot(localPath)
	if err != nil {
		return 0, err
	}
	root, err := openSafeRoot(rootAbs)
	if err != nil {
		return 0, err
	}
	defer root.Close()

	var totalWritten int64
	walker := s.client.Walk(remotePath)
	for walker.Step() {
		if walker.Err() != nil {
			return totalWritten, walker.Err()
		}

		path := walker.Path()
		info := walker.Stat()

		relPath, err := filepath.Rel(remotePath, path)
		if err != nil {
			return totalWritten, err
		}

		if err := checkPathTraversal(relPath); err != nil {
			return totalWritten, err
		}
		if relPath == "." {
			continue
		}

		targetPath := filepath.Join(rootAbs, relPath)
		safeTarget, err := safeLocalDownloadPath(rootAbs, rootResolved, targetPath)
		if err != nil {
			return totalWritten, err
		}

		if info.IsDir() {
			if err := root.ensureDir(relPath, info.Mode()); err != nil {
				return totalWritten, fmt.Errorf("failed to create local directory %s: %w", safeTarget, err)
			}
			if err := ensureSafeLocalDir(safeTarget, info.Mode()); err != nil {
				return totalWritten, fmt.Errorf("failed to create local directory %s: %w", safeTarget, err)
			}
			continue
		}

		// Skip if same size+mtime
		localFileInfo, err := os.Stat(safeTarget)
		if err == nil && localFileInfo.Size() == info.Size() && localFileInfo.ModTime().Unix() == info.ModTime().Unix() {
			continue
		}

		written, err := s.downloadFile(path, root, relPath, info.ModTime())
		if err != nil {
			return totalWritten, err
		}
		totalWritten += written
	}

	return totalWritten, nil
}

func (s *sftpSession) downloadFile(remotePath string, root *safeRoot, relPath string, modTime time.Time) (int64, error) {
	remoteFile, err := s.client.Open(remotePath)
	if err != nil {
		return 0, fmt.Errorf("failed to open remote file: %w", err)
	}
	defer remoteFile.Close()

	remoteInfo, _ := s.client.Stat(remotePath)
	var offset int64
	// Continue a partial local file when it is smaller than the remote file.
	// A larger/equal local file is restarted to guarantee correct contents.
	if remoteInfo != nil {
		if existing, statErr := root.openFileResume(relPath, 0600); statErr == nil {
			if st, e := existing.Stat(); e == nil && st.Size() > 0 && st.Size() < remoteInfo.Size() {
				// A size-only check is unsafe: the local file may be stale or
				// unrelated. Resume only when its bytes exactly match the remote
				// file's prefix; otherwise restart from the beginning.
				equal, verifyErr := verifyRemotePrefix(existing, remoteFile, st.Size())
				if verifyErr != nil {
					existing.Close()
					return 0, fmt.Errorf("failed to verify local prefix: %w", verifyErr)
				}
				if equal {
					offset = st.Size()
				}
			}
			existing.Close()
		}
	}
	if offset > 0 {
		localFile, err := root.openFileResume(relPath, 0600)
		if err != nil {
			return 0, fmt.Errorf("failed to open local file: %w", err)
		}
		if _, err = localFile.Seek(offset, io.SeekStart); err != nil {
			localFile.Close()
			return 0, err
		}
		if _, err = remoteFile.Seek(offset, io.SeekStart); err != nil {
			localFile.Close()
			return 0, err
		}
		defer localFile.Close()
		written, err := io.Copy(localFile, remoteFile)
		if err != nil {
			return 0, fmt.Errorf("failed to download file: %w", err)
		}
		if remoteInfo != nil {
			_ = localFile.Chmod(remoteInfo.Mode())
		}
		if err := setFileTimes(localFile, modTime); err != nil {
			return 0, err
		}
		return written, nil
	}
	localFile, err := root.openFile(relPath, 0600)
	if err != nil {
		return 0, fmt.Errorf("failed to create local file: %w", err)
	}
	defer localFile.Close()

	written, err := io.Copy(localFile, remoteFile)
	if err != nil {
		return 0, fmt.Errorf("failed to download file: %w", err)
	}

	remoteInfo2, err := s.client.Stat(remotePath)
	if err == nil {
		if chmodErr := localFile.Chmod(remoteInfo2.Mode()); chmodErr != nil {
			return 0, fmt.Errorf("failed to preserve local mode: %w", chmodErr)
		}
	}
	if err := setFileTimes(localFile, modTime); err != nil {
		return 0, fmt.Errorf("failed to preserve local timestamps: %w", err)
	}

	return written, nil
}

func (s *sftpSession) List(path string) ([]string, error) {
	entries, err := s.client.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

func (s *sftpSession) Mkdir(path string) error {
	return s.client.Mkdir(path)
}

func (s *sftpSession) Remove(path string) error {
	return s.client.Remove(path)
}

func checkPathTraversal(relPath string) error {
	if !filepath.IsLocal(relPath) {
		return fmt.Errorf("path traversal detected: %s escapes destination", relPath)
	}
	return nil
}

func prepareDownloadRoot(localPath string) (string, string, error) {
	rootAbs, err := filepath.Abs(localPath)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve destination path: %w", err)
	}
	if err := ensureSafeLocalDir(rootAbs, 0755); err != nil {
		return "", "", err
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", fmt.Errorf("failed to resolve destination path: %w", err)
	}
	return rootAbs, rootResolved, nil
}

func prepareSingleFileDownloadTarget(localPath string) (string, string, string, error) {
	targetAbs, err := filepath.Abs(localPath)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to resolve destination path: %w", err)
	}
	rootAbs := filepath.Dir(targetAbs)
	if err := ensureSafeLocalDir(rootAbs, 0755); err != nil {
		return "", "", "", err
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to resolve destination path: %w", err)
	}
	return targetAbs, rootAbs, rootResolved, nil
}

func safeLocalDownloadPath(rootAbs, rootResolved, targetPath string) (string, error) {
	targetAbs, err := filepath.Abs(targetPath)
	if err != nil {
		return "", fmt.Errorf("failed to resolve destination path: %w", err)
	}

	if pathEscapesRoot(rootAbs, targetAbs) {
		return "", fmt.Errorf("path traversal detected: %s escapes destination", targetAbs)
	}

	resolvedParent, err := resolveExistingParent(filepath.Dir(targetAbs))
	if err != nil {
		return "", fmt.Errorf("failed to resolve destination parent: %w", err)
	}
	if pathEscapesRoot(rootResolved, resolvedParent) {
		return "", fmt.Errorf("symlink traversal detected: %s escapes destination", targetAbs)
	}

	if info, err := os.Lstat(targetAbs); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("symlink traversal detected: %s is a symlink", targetAbs)
	} else if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to inspect destination path: %w", err)
	}

	return targetAbs, nil
}

func resolveExistingParent(path string) (string, error) {
	current := path
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		next := filepath.Dir(current)
		if next == current {
			return "", err
		}
		current = next
	}
}

func ensureSafeLocalDir(path string, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination path is a symlink: %s", path)
		}
		if !info.IsDir() {
			return fmt.Errorf("destination path is not a directory: %s", path)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect destination path %s: %w", path, err)
	}
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("failed to create destination directory %s: %w", path, err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		return fmt.Errorf("failed to inspect destination path %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("destination path is a symlink: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("destination path is not a directory: %s", path)
	}
	return nil
}

func pathEscapesRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func ensureLocalParentDir(localPath string) error {
	parent := filepath.Dir(localPath)
	if parent == "." {
		return nil
	}
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("failed to create local parent directory %s: %w", parent, err)
	}
	return nil
}

// cleanRelPath cleans a root-relative path and rejects traversal above the
// root. Returns "" for the root itself ("." or empty input). Shared by the
// unix and non-unix safeRoot implementations.
func cleanRelPath(relPath string) (string, error) {
	clean := filepath.Clean(relPath)
	if clean == "." || clean == "" {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid destination relative path: %s", relPath)
	}
	return clean, nil
}
