package downloads

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"home-streaming-assistant/internal/catalog"
	"home-streaming-assistant/internal/config"
	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/storage"
)

// fakeCatalog serves a single file entry.
type fakeCatalog struct {
	entry model.Entry
}

func (f *fakeCatalog) List(ctx context.Context, path, cursor string, refresh bool, cs *catalog.CursorStore) (model.EntryPage, error) {
	return model.EntryPage{}, nil
}

func (f *fakeCatalog) Stat(ctx context.Context, path string) (model.Entry, error) {
	if path != f.entry.Path {
		return model.Entry{}, model.E(model.CodeNotFound, 404, "no entry")
	}
	return f.entry, nil
}

func (f *fakeCatalog) Probe(ctx context.Context) error { return nil }

// fakeRclone acts as rclone copyto: copies the "remote" fixture file to
// the destination and emits progress-like JSON stats.
func fakeRclone(t *testing.T, dir string) string {
	t.Helper()
	bin := filepath.Join(dir, "fake-rclone")
	script := "#!/bin/sh\n" +
		"set -e\n" +
		// args: copyto --config X --use-json-log --stats 1s --stats-log-level NOTICE src dst
		"src=\"\"\n dst=\"\"\n" +
		"while [ $# -gt 0 ]; do\n" +
		"  case \"$1\" in copyto|--config|--use-json-log|--stats|--stats-log-level|1s|NOTICE) shift; continue;; esac\n" +
		"  if [ -z \"$src\" ]; then src=\"$1\"; else dst=\"$1\"; fi; shift; done\n" +
		"echo '{\"message\":\"Transferred:        40 B / 40 B, 100%, 0 B/s, ETA 0s\"}' 1>&2\n" +
		"cp \"$src\" \"$dst\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func testSetup(t *testing.T) (*Manager, *storage.Store, *config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		Listen: "127.0.0.1:0", Origins: []string{"http://127.0.0.1:1"},
		AdminUser: "admin", AdminPasswordFile: filepath.Join(dir, "pw"),
		StateDir: filepath.Join(dir, "state"), FilesDir: filepath.Join(dir, "files"),
		RcloneBinary: fakeRclone(t, dir),
		RcloneConfig: filepath.Join(dir, "rclone.conf"),
		MinFreeBytes: 1,
	}
	if err := os.WriteFile(cfg.AdminPasswordFile, []byte("pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg.FilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return nil, store, cfg, dir
}

func newManager(store *storage.Store, cfg *config.Config, entry model.Entry, space func() (int64, error)) *Manager {
	cat := catalog.NewManager(&fakeCatalog{entry: entry}, nil)
	m := New(store, cfg, cat)
	if space != nil {
		m.Space = space
	}
	m.Clock = func() time.Time { return time.Now().UTC() }
	return m
}

func fileEntry(path string, size int64, content []byte) model.Entry {
	mod := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	v, _ := model.ComputeVersion(model.SourceAliyun, path, &size, &mod, nil, nil)
	return model.Entry{
		Source: model.SourceAliyun, Path: path, Name: filepath.Base(path),
		Size: &size, Modified: &mod, Version: v,
	}
}

// waitForState polls until the job reaches one of the states.
func waitForState(t *testing.T, m *Manager, id string, states ...string) model.Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := m.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range states {
			if job.State == s {
				return job
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %v", id, states)
	return model.Job{}
}

func TestEnqueueDedupAndDownload(t *testing.T) {
	_, store, cfg, dir := testSetup(t)
	content := []byte("hello world hello world hello world")
	src := filepath.Join(dir, "remote-source.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	// The fake rclone copies from its first non-flag arg; point the
	// catalog entry path at the fixture via a symlink-like stand-in:
	// fakeRclone copies src->dst; make the remote path resolve by
	// creating the file at the expected absolute temp location is not
	// possible, so instead monkey-patch via a wrapper that always uses
	// the fixture.
	bin := filepath.Join(dir, "fake-rclone2")
	script := fmt.Sprintf("#!/bin/sh\nwhile [ $# -gt 0 ]; do\n case \"$1\" in copyto|--config|--use-json-log|--stats|--stats-log-level|1s|NOTICE) shift;; *) if [ -z \"$src\" ]; then src=%q; else dst=\"$1\"; fi; shift;; esac; done\ncp \"$src\" \"$dst\"\n", src)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.RcloneBinary = bin

	size := int64(len(content))
	m := newManager(store, cfg, fileEntry("docs/small.txt", size, content), nil)
	m.Start()
	t.Cleanup(m.Stop)

	job, created, err := m.Enqueue(context.Background(), model.SourceAliyun, "docs/small.txt", model.OriginManual)
	if err != nil || !created {
		t.Fatalf("enqueue: created=%v err=%v", created, err)
	}
	// Duplicate enqueue returns the same job.
	job2, created2, err := m.Enqueue(context.Background(), model.SourceAliyun, "docs/small.txt", model.OriginManual)
	if err != nil || created2 || job2.ID != job.ID {
		t.Fatalf("dedup: created=%v err=%v id=%s/%s", created2, err, job.ID, job2.ID)
	}

	done := waitForState(t, m, job.ID, model.JobCompleted)
	if done.BytesTotal != size {
		t.Fatalf("bytesTotal=%d want %d", done.BytesTotal, size)
	}
	// Published file exists under media/aliyun/manual/<id>/1/small.txt.
	pub := filepath.Join(cfg.FilesDir, "media", "aliyun", "manual", job.ID, "1", "small.txt")
	got, err := os.ReadFile(pub)
	if err != nil {
		t.Fatalf("published file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatal("published content mismatch")
	}
	// No temp residue.
	entries, _ := os.ReadDir(filepath.Join(cfg.FilesDir, ".incoming"))
	if len(entries) != 0 {
		t.Fatalf(".incoming not cleaned: %v", entries)
	}
}

func TestEnqueueRejectsDirAndBadPath(t *testing.T) {
	_, store, cfg, _ := testSetup(t)
	e := fileEntry("x", 1, nil)
	e.IsDir = true
	m := newManager(store, cfg, e, nil)
	if _, _, err := m.Enqueue(context.Background(), model.SourceAliyun, "x", model.OriginManual); err == nil ||
		model.ErrCode(err) != model.CodeInvalidRequest {
		t.Fatalf("dir enqueue: %v", err)
	}
	if _, _, err := m.Enqueue(context.Background(), model.SourceAliyun, "../escape", model.OriginManual); err == nil {
		t.Fatal("traversal accepted")
	}
}

func TestNoSpace(t *testing.T) {
	_, store, cfg, _ := testSetup(t)
	entry := fileEntry("a.bin", 1000, nil)
	m := newManager(store, cfg, entry, func() (int64, error) { return 10, nil })
	if _, _, err := m.Enqueue(context.Background(), model.SourceAliyun, "a.bin", model.OriginManual); err == nil ||
		model.ErrCode(err) != model.CodeNoSpace {
		t.Fatalf("no-space: %v", err)
	}
}

func TestCancelActiveDownload(t *testing.T) {
	_, store, cfg, dir := testSetup(t)
	// Slow rclone: sleeps long enough to be cancelled.
	bin := filepath.Join(dir, "slow-rclone")
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in /*) dst=\"$a\";; esac; done\n" +
		"sleep 30\necho x > \"$dst\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.RcloneBinary = bin
	entry := fileEntry("big.bin", 5, nil)
	m := newManager(store, cfg, entry, nil)
	m.Start()
	t.Cleanup(m.Stop)

	job, _, err := m.Enqueue(context.Background(), model.SourceAliyun, "big.bin", model.OriginManual)
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, m, job.ID, model.JobDownloading)
	if _, pending, err := m.Cancel(context.Background(), job.ID); err != nil {
		t.Fatalf("cancel: %v", err)
	} else if !pending {
		t.Fatal("cancel of active job should be pending")
	}
	waitForState(t, m, job.ID, model.JobCancelled)
	// rclone must be gone.
	if _, err := os.Stat(filepath.Join(cfg.FilesDir, ".incoming", job.ID)); !os.IsNotExist(err) {
		t.Fatal("temp dir left after cancel")
	}
}

func TestRetryFailedJob(t *testing.T) {
	_, store, cfg, dir := testSetup(t)
	// rclone that fails the first time, succeeds afterwards.
	bin := filepath.Join(dir, "flaky-rclone")
	flagFile := filepath.Join(dir, "failed-once")
	script := "#!/bin/sh\nif [ ! -f " + flagFile + " ]; then touch " + flagFile + "; exit 1; fi\n" +
		"for a in \"$@\"; do case \"$a\" in /*) dst=\"$a\";; esac; done\necho data > \"$dst\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.RcloneBinary = bin
	entry := fileEntry("f.bin", 5, nil)
	m := newManager(store, cfg, entry, nil)
	m.Start()
	t.Cleanup(m.Stop)

	job, _, err := m.Enqueue(context.Background(), model.SourceAliyun, "f.bin", model.OriginManual)
	if err != nil {
		t.Fatal(err)
	}
	waitForState(t, m, job.ID, model.JobFailed)
	retried, err := m.Retry(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if retried.ID != job.ID || retried.Attempt != job.Attempt+1 {
		t.Fatalf("retry reuse: id=%s attempt=%d", retried.ID, retried.Attempt)
	}
	waitForState(t, m, job.ID, model.JobCompleted)
}

func TestRemoteChangedFailsJob(t *testing.T) {
	_, store, cfg, dir := testSetup(t)
	content := []byte("v1-content")
	src := filepath.Join(dir, "remote.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fake-rclone3")
	script := fmt.Sprintf("#!/bin/sh\nwhile [ $# -gt 0 ]; do\n case \"$1\" in copyto|--config|--use-json-log|--stats|--stats-log-level|1s|NOTICE) shift;; *) if [ -z \"$src\" ]; then src=%q; else dst=\"$1\"; fi; shift;; esac; done\ncp \"$src\" \"$dst\"\n", src)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg.RcloneBinary = bin
	size := int64(len(content))
	entry := fileEntry("doc.txt", size, content)

	// Catalog whose version flips after the transfer (verify re-Stat).
	fc := &flippingCatalog{fakeCatalog: fakeCatalog{entry: entry}}
	cat := catalog.NewManager(fc, nil)
	m := New(store, cfg, cat)
	m.Start()
	t.Cleanup(m.Stop)

	job, _, err := m.Enqueue(context.Background(), model.SourceAliyun, "doc.txt", model.OriginManual)
	if err != nil {
		t.Fatal(err)
	}
	done := waitForState(t, m, job.ID, model.JobFailed)
	if done.ErrorCode == nil || *done.ErrorCode != model.CodeRemoteChanged {
		t.Fatalf("want REMOTE_CHANGED, got %v", done.ErrorCode)
	}
	// Nothing published.
	if _, err := os.Stat(filepath.Join(cfg.FilesDir, "media")); !os.IsNotExist(err) {
		t.Fatal("nothing should be published")
	}
}

// flippingCatalog changes its version once Stat is called again.
type flippingCatalog struct {
	fakeCatalog
	flipped bool
}

func (f *flippingCatalog) Stat(ctx context.Context, path string) (model.Entry, error) {
	e, err := f.fakeCatalog.Stat(ctx, path)
	if err != nil {
		return e, err
	}
	if f.flipped {
		size := int64(999)
		mod := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		v, _ := model.ComputeVersion(model.SourceAliyun, path, &size, &mod, nil, nil)
		e2 := e
		e2.Size, e2.Version = &size, v
		return e2, nil
	}
	f.flipped = true
	return e, nil
}
