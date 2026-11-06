package downloads

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"home-streaming-assistant/internal/config"
	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/storage"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

func (m *Manager) workerLoop() {
	defer m.wg.Done()
	for {
		if m.rootCtx.Err() != nil {
			return
		}
		job, err := m.store.ClaimNextQueued(m.rootCtx)
		if err != nil {
			if m.rootCtx.Err() != nil {
				return
			}
			time.Sleep(time.Second)
			continue
		}
		if job == nil {
			select {
			case <-m.wake:
			case <-m.rootCtx.Done():
				return
			}
			continue
		}
		m.runJob(job)
		m.signalWake()
	}
}

// runJob executes one claimed download end to end.
func (m *Manager) runJob(row *storage.JobRow) {
	ctx, cancel := context.WithCancel(m.rootCtx)
	defer cancel()

	a := &active{cancelCh: make(chan struct{}), done: make(chan struct{})}
	m.mu.Lock()
	m.active[row.ID] = a
	m.mu.Unlock()
	defer func() {
		close(a.done)
		m.mu.Lock()
		delete(m.active, row.ID)
		m.mu.Unlock()
	}()

	fail := func(code string, detail string) {
		_ = m.store.UpdateJobFields(m.rootCtx, row.ID, map[string]any{
			"state": model.JobFailed, "error_code": code,
		})
		if detail != "" {
			log.Printf("job %s attempt %d failed: %s: %s", row.ID, row.Attempt, code, detail)
		}
	}

	// Re-check space at worker start; never auto-delete media.
	avail, err := m.Space()
	if err == nil {
		reserved, rerr := m.store.ReservedBytes(m.rootCtx)
		if rerr == nil && avail-reserved < m.cfg.MinFreeBytes && row.BytesTotal > 0 {
			fail(model.CodeNoSpace, "space below reserve before start")
			return
		}
	}

	tempDir := ".incoming/" + row.ID + "/" + strconv.FormatInt(row.Attempt, 10)
	root, err := openFilesRoot(m.cfg.FilesDir)
	if err != nil {
		fail(model.CodeInternal, err.Error())
		return
	}
	defer root.Close()
	if err := root.MkdirAll(tempDir, 0o700); err != nil {
		fail(model.CodeInternal, "create temp dir: "+err.Error())
		return
	}
	tempPath := tempDir + "/part.bin"
	code, detail := m.runRclone(ctx, row, tempPath, a.cancelCh)
	if code != "" {
		fail(code, detail)
		_ = root.RemoveAll(".incoming/" + row.ID)
		return
	}
	if cancelled(a.cancelCh) {
		_ = root.RemoveAll(".incoming/" + row.ID)
		_ = m.store.UpdateJobFields(m.rootCtx, row.ID, map[string]any{
			"state": model.JobCancelled, "error_code": nil,
		})
		return
	}

	if err := m.store.UpdateJobFields(m.rootCtx, row.ID, map[string]any{
		"state": model.JobVerifying,
	}); err != nil {
		fail(model.CodeInternal, err.Error())
		return
	}

	sum, err := hashFile(root, tempPath)
	if err != nil {
		fail(model.CodeInternal, "hash temp file: "+err.Error())
		return
	}
	if err := m.verifyRemote(ctx, row, root, tempPath, sum, a.cancelCh); err != nil {
		code := model.ErrCode(err)
		if code != model.CodeRemoteChanged && code != "CHECKSUM_MISMATCH" {
			code = model.CodeUpstreamError
		}
		fail(code, "verify: "+err.Error())
		_ = root.RemoveAll(".incoming/" + row.ID)
		return
	}
	if cancelled(a.cancelCh) {
		_ = root.RemoveAll(".incoming/" + row.ID)
		_ = m.store.UpdateJobFields(m.rootCtx, row.ID, map[string]any{
			"state": model.JobCancelled, "error_code": nil,
		})
		return
	}

	// Publish: persist intent first, then fsync + atomic rename.
	var entry model.Entry
	_ = json.Unmarshal([]byte(row.RemoteJSON), &entry)
	rel := finalRel(row.Source, row.Origin, row.ID, row.Attempt, safeBasename(entry.Name))
	if err := m.store.UpdateJobFields(m.rootCtx, row.ID, map[string]any{
		"state": model.JobPublishing, "final_rel": rel, "local_sha256": sum,
	}); err != nil {
		fail(model.CodeInternal, err.Error())
		return
	}
	if err := m.publish(root, tempDir, tempPath, rel); err != nil {
		fail(model.CodeInternal, "publish: "+err.Error())
		return
	}
	lib := &storage.LibraryRow{
		ID: storage.RandomID(16), JobID: row.ID, Attempt: row.Attempt,
		Source: row.Source, Path: row.Path, Version: row.Version,
		RelativePath: rel, Size: row.BytesTotal, LocalSHA256: sum,
		State: model.LibPendingIndex,
	}
	if err := m.store.CompletePublish(m.rootCtx, row.ID, lib); err != nil {
		fail(model.CodeInternal, "complete publish: "+err.Error())
		return
	}
	if m.IndexerWake != nil {
		go m.IndexerWake()
	}
}

func cancelled(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// runRclone runs one copyto transfer, parsing progress from JSON logs and
// persisting at most one progress row per second. It returns "" on
// success/cancel or a public error code plus safe detail.
func (m *Manager) runRclone(ctx context.Context, row *storage.JobRow, tempPath string, cancelCh chan struct{}) (string, string) {
	remote := remoteFor(m.cfg, row)
	dst := m.cfg.FilesDir + "/" + tempPath
	args := []string{
		"copyto",
		"--config", m.cfg.RcloneConfig,
		"--use-json-log",
		"--stats", "1s",
		"--stats-log-level", "NOTICE",
		remote, dst,
	}
	cmd := exec.Command(m.cfg.RcloneBinary, args...)
	// Fixed environment: no inherited remote overrides.
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/tmp",
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return model.CodeInternal, "stderr pipe: " + err.Error()
	}
	if err := cmd.Start(); err != nil {
		return model.CodeUpstreamUnavailable, "start rclone: " + err.Error()
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.parseProgress(stderrPipe, row)
	}()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	spaceFail := false
	for {
		select {
		case err := <-done:
			wg.Wait()
			if err == nil {
				return "", ""
			}
			if ctx.Err() != nil {
				return model.InterruptedCode, "interrupted by shutdown"
			}
			if cancelled(cancelCh) {
				return "", "" // caller finalises cancellation
			}
			return model.CodeUpstreamError, "rclone exited non-zero"
		case <-cancelCh:
			terminateGroup(cmd, 5*time.Second)
			<-done
			wg.Wait()
			return "", ""
		case <-ctx.Done():
			terminateGroup(cmd, 5*time.Second)
			<-done
			wg.Wait()
			return model.InterruptedCode, "interrupted by shutdown"
		case <-ticker.C:
			if avail, err := m.Space(); err == nil && avail < m.cfg.MinFreeBytes {
				spaceFail = true
			}
			if spaceFail {
				terminateGroup(cmd, 5*time.Second)
				<-done
				wg.Wait()
				return model.CodeNoSpace, "space fell below reserve during transfer"
			}
		}
	}
}

func terminateGroup(cmd *exec.Cmd, grace time.Duration) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if cmd.Process.Signal(syscall.Signal(0)) != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// remoteFor builds the fixed remote path for a job.
func remoteFor(cfg *config.Config, row *storage.JobRow) string {
	if row.Source == model.SourceGDrive {
		return "gdrive-ro:" + strings.TrimPrefix(cfg.GDrive.Root, "/") + "/" + row.Path
	}
	root := strings.TrimSuffix(cfg.Aliyun.Root, "/")
	if root == "/" || root == "" {
		return "aliyun:/" + row.Path
	}
	return "aliyun:" + root + "/" + row.Path
}

var statsRe = regexp.MustCompile(
	`Transferred:\s+([\d.,]+)\s*(B|KiB|MiB|GiB|TiB)?\s*/\s*([\d.,]+)\s*(B|KiB|MiB|GiB|TiB)`)

func parseUnit(u string) int64 {
	switch u {
	case "KiB":
		return 1 << 10
	case "MiB":
		return 1 << 20
	case "GiB":
		return 1 << 30
	case "TiB":
		return 1 << 40
	}
	return 1
}

func parseNum(s string) int64 {
	s = strings.ReplaceAll(strings.ReplaceAll(s, ",", ""), ".", "")
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// parseProgress consumes rclone JSON logs and persists byte progress at
// most once per second, bounding line size.
func (m *Manager) parseProgress(r io.Reader, row *storage.JobRow) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	var last time.Time
	for sc.Scan() {
		line := sc.Bytes()
		if !bytesContains(line, "Transferred:") {
			continue
		}
		var lg struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(line, &lg); err != nil {
			continue
		}
		mm := statsRe.FindStringSubmatch(lg.Message)
		if mm == nil {
			continue
		}
		done := parseNum(mm[1]) * parseUnit(mm[2])
		now := time.Now()
		if now.Sub(last) >= time.Second {
			last = now
			_ = m.store.UpdateJobFields(context.Background(), row.ID,
				map[string]any{"bytes_done": done})
		}
	}
}

func bytesContains(b []byte, s string) bool {
	return strings.Contains(string(b), s)
}

// hashFile streams SHA-256 over a file inside the rooted files tree.
func hashFile(root *os.Root, rel string) (string, error) {
	f, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifyRemote checks local sanity and refreshes the upstream version.
// A size-only fingerprint is never trusted as a content version.
func (m *Manager) verifyRemote(ctx context.Context, row *storage.JobRow, root *os.Root, tempPath, localSum string, cancelCh chan struct{}) error {
	fi, err := root.Stat(tempPath)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("temp file is not a regular file")
	}
	if row.BytesTotal > 0 && fi.Size() != row.BytesTotal {
		return model.E("CHECKSUM_MISMATCH", 409, "size differs from upstream")
	}
	var entry model.Entry
	if err := json.Unmarshal([]byte(row.RemoteJSON), &entry); err != nil {
		return model.E(model.CodeRemoteChanged, 409, "cached upstream metadata unreadable")
	}
	remote, err := m.catalog.Stat(ctx, row.Source, row.Path)
	if err != nil {
		return err
	}
	if entry.Version != nil {
		if remote.Version == nil || *remote.Version != *entry.Version {
			return model.E(model.CodeRemoteChanged, 409, "remote version changed")
		}
	}
	// Compare an available remote checksum against the local copy.
	best := model.BestHash(remote.Hashes)
	if best != nil && !remoteHashMatches(best, localSum) {
		return model.E("CHECKSUM_MISMATCH", 409, "remote checksum does not match local copy")
	}
	return nil
}

func remoteHashMatches(h *model.HashPayload, localSum string) bool {
	// Local value is SHA-256; it can only match a remote sha256.
	if h.Type != "sha256" {
		return true // no comparable checksum: size/version consistency only
	}
	return strings.EqualFold(h.Value, localSum)
}

// publish fsyncs the temp file, renames it atomically into media/ and
// fsyncs the parent directory. EXDEV aborts with no fallback copy.
func (m *Manager) publish(root *os.Root, tempDir, tempPath, rel string) error {
	dir := rel[:strings.LastIndex(rel, "/")]
	if err := root.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	f, err := root.Open(tempPath)
	if err != nil {
		return fmt.Errorf("open temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync temp: %w", err)
	}
	f.Close()
	if err := root.Rename(tempPath, rel); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	d, err := root.Open(dir)
	if err != nil {
		return fmt.Errorf("open parent: %w", err)
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return fmt.Errorf("fsync parent: %w", err)
	}
	d.Close()
	_ = root.RemoveAll(path.Dir(tempDir))
	return nil
}

// openFilesRoot opens the jailed root over filesDir.
func openFilesRoot(filesDir string) (*os.Root, error) {
	if err := os.MkdirAll(filesDir, 0o755); err != nil {
		return nil, fmt.Errorf("create files dir: %w", err)
	}
	return os.OpenRoot(filesDir)
}
