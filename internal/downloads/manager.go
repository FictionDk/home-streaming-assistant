// Package downloads owns the persistent single-worker download queue,
// rclone transfer execution, verification and atomic publication.
package downloads

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"home-streaming-assistant/internal/catalog"
	"home-streaming-assistant/internal/config"
	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/storage"
)

// Manager is the single download queue.
type Manager struct {
	store   *storage.Store
	cfg     *config.Config
	catalog *catalog.Manager

	mu      sync.Mutex
	active  map[string]*active
	wake    chan struct{}
	rootCtx context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// IndexerWake is called (non-blocking) after each successful publish.
	IndexerWake func()

	// Space is injectable for tests; default is statfs on filesDir.
	Space func() (int64, error)
	// Clock is injectable for tests.
	Clock func() time.Time
}

type active struct {
	cancelCh chan struct{}
	done     chan struct{}
}

// Catalog exposes the upstream catalog for the scheduler.
func (m *Manager) Catalog() *catalog.Manager { return m.catalog }

// New creates the manager. Call Recover then Start.
func New(store *storage.Store, cfg *config.Config, cat *catalog.Manager) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		store: store, cfg: cfg, catalog: cat,
		active:  map[string]*active{},
		wake:    make(chan struct{}, 1),
		rootCtx: ctx, cancel: cancel,
		Space: defaultSpace(cfg.FilesDir),
		Clock: func() time.Time { return time.Now().UTC() },
	}
}

func defaultSpace(filesDir string) func() (int64, error) {
	return func() (int64, error) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(filesDir, &st); err != nil {
			return 0, err
		}
		return int64(st.Bavail) * int64(st.Bsize), nil
	}
}

// Start launches the single worker goroutine.
func (m *Manager) Start() {
	m.wg.Add(1)
	go m.workerLoop()
}

// Stop terminates active work, waits for rclone children to exit and
// stops the worker. Queued jobs remain queued.
func (m *Manager) Stop() {
	m.cancel()
	m.wg.Wait()
}

func (m *Manager) signalWake() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// Enqueue validates, deduplicates and appends a download request.
// It returns the job and whether it was newly created.
func (m *Manager) Enqueue(ctx context.Context, source model.Source, path, origin string) (model.Job, bool, error) {
	if !source.Valid() {
		return model.Job{}, false, model.E(model.CodeInvalidRequest, 400, "unknown source")
	}
	if origin != model.OriginManual && origin != model.OriginSchedule {
		return model.Job{}, false, model.E(model.CodeInvalidRequest, 400, "unknown origin")
	}
	if err := model.ValidateClientPath(path); err != nil {
		return model.Job{}, false, model.E(model.CodeInvalidPath, 400, err.Error())
	}
	entry, err := m.catalog.Stat(ctx, source, path)
	if err != nil {
		return model.Job{}, false, err
	}
	if entry.IsDir {
		return model.Job{}, false, model.E(model.CodeInvalidRequest, 400, "directories cannot be downloaded")
	}
	if entry.Version == nil || entry.Size == nil {
		return model.Job{}, false, model.E(model.CodeVersionUnavailable, 400, "no reliable version for this file")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Existing library copy for this exact version is reused.
	lib, err := m.store.LibraryByPathVersion(ctx, source, path, *entry.Version)
	if err != nil {
		return model.Job{}, false, err
	}
	if lib != nil && (lib.State == model.LibPlayable || lib.State == model.LibPendingIndex) {
		job, err := m.store.GetJob(ctx, lib.JobID)
		if err != nil {
			return model.Job{}, false, err
		}
		if job != nil {
			return job.Public(), false, nil
		}
	}

	existing, err := m.store.GetJobByVersion(ctx, source, path, *entry.Version)
	if err != nil {
		return model.Job{}, false, err
	}
	if existing != nil {
		switch existing.State {
		case model.JobQueued, model.JobDownloading, model.JobVerifying, model.JobPublishing, model.JobCompleted:
			return existing.Public(), false, nil
		case model.JobFailed, model.JobCancelled:
			// Same-version re-request reuses the job with a fresh attempt;
			// this counts as an existing job, not a new one.
			job, rerr := m.requeueAttempt(ctx, existing)
			return job, false, rerr
		}
	}

	// Space accounting: available minus reserved outstanding work, then
	// the configured reserve. Never auto-delete media to make room.
	avail, err := m.Space()
	if err != nil {
		return model.Job{}, false, model.E(model.CodeInternal, 500, "cannot read disk space")
	}
	reserved, err := m.store.ReservedBytes(ctx)
	if err != nil {
		return model.Job{}, false, err
	}
	if avail-reserved-*entry.Size < m.cfg.MinFreeBytes {
		return model.Job{}, false, model.E(model.CodeNoSpace, 409, "insufficient disk space for this download")
	}

	row := &storage.JobRow{
		ID:         storage.RandomID(16),
		Source:     source,
		Path:       path,
		Version:    *entry.Version,
		Origin:     origin,
		RemoteJSON: marshalRemoteMeta(&entry),
		BytesTotal: *entry.Size,
	}
	if err := m.store.InsertJob(ctx, row); err != nil {
		// Unique-race: another enqueue for the same version won.
		if dup, gerr := m.store.GetJobByVersion(ctx, source, path, *entry.Version); gerr == nil && dup != nil {
			return dup.Public(), false, nil
		}
		return model.Job{}, false, err
	}
	m.signalWake()
	return row.Public(), true, nil
}

// requeueAttempt resets a terminal job to queued with attempt+1.
func (m *Manager) requeueAttempt(ctx context.Context, job *storage.JobRow) (model.Job, error) {
	err := m.store.UpdateJobFields(ctx, job.ID, map[string]any{
		"state":            model.JobQueued,
		"attempt":          job.Attempt + 1,
		"error_code":       nil,
		"cancel_requested": 0,
		"bytes_done":       0,
	})
	if err != nil {
		return model.Job{}, err
	}
	m.signalWake()
	job.State = model.JobQueued
	job.Attempt++
	return job.Public(), nil
}

// Cancel cancels a job. It returns the job and whether cancellation is
// still in progress (202) or done (200).
func (m *Manager) Cancel(ctx context.Context, id string) (model.Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, err := m.store.GetJob(ctx, id)
	if err != nil {
		return model.Job{}, false, err
	}
	if job == nil {
		return model.Job{}, false, model.E(model.CodeNotFound, 404, "job not found")
	}
	switch job.State {
	case model.JobQueued:
		if err := m.store.UpdateJobFields(ctx, id, map[string]any{
			"state": model.JobCancelled, "error_code": nil,
		}); err != nil {
			return model.Job{}, false, err
		}
		job.State = model.JobCancelled
		return job.Public(), false, nil
	case model.JobDownloading, model.JobVerifying:
		if _, ok := m.active[id]; !ok {
			// Worker has not registered yet; mark intent and let the
			// worker observe it.
			if err := m.store.UpdateJobFields(ctx, id, map[string]any{"cancel_requested": 1}); err != nil {
				return model.Job{}, false, err
			}
			return job.Public(), true, nil
		}
		if !job.CancelRequested {
			if err := m.store.UpdateJobFields(ctx, id, map[string]any{"cancel_requested": 1}); err != nil {
				return model.Job{}, false, err
			}
		}
		a := m.active[id]
		close(a.cancelCh)
		job.CancelRequested = true
		return job.Public(), true, nil
	case model.JobPublishing:
		return model.Job{}, false, model.E(model.CodeConflict, 409, "job is publishing and cannot be cancelled")
	default:
		return model.Job{}, false, model.E(model.CodeConflict, 409, "job already finished")
	}
}

// Retry re-runs a failed or cancelled job. If the upstream version
// changed, a new job for the new version is created and the old record is
// kept.
func (m *Manager) Retry(ctx context.Context, id string) (model.Job, error) {
	job, err := m.store.GetJob(ctx, id)
	if err != nil {
		return model.Job{}, err
	}
	if job == nil {
		return model.Job{}, model.E(model.CodeNotFound, 404, "job not found")
	}
	if job.State != model.JobFailed && job.State != model.JobCancelled {
		return model.Job{}, model.E(model.CodeConflict, 409, "only failed or cancelled jobs can be retried")
	}
	entry, err := m.catalog.Stat(ctx, job.Source, job.Path)
	if err != nil {
		return model.Job{}, err
	}
	if entry.Version == nil || entry.Size == nil {
		return model.Job{}, model.E(model.CodeVersionUnavailable, 400, "no reliable version for this file")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if *entry.Version == job.Version {
		return m.requeueAttempt(ctx, job)
	}
	// Version changed: return/create the job for the new version.
	existing, err := m.store.GetJobByVersion(ctx, job.Source, job.Path, *entry.Version)
	if err != nil {
		return model.Job{}, err
	}
	if existing != nil {
		switch existing.State {
		case model.JobQueued, model.JobDownloading, model.JobVerifying, model.JobPublishing, model.JobCompleted:
			return existing.Public(), nil
		}
		return m.requeueAttempt(ctx, existing)
	}
	avail, err := m.Space()
	if err != nil {
		return model.Job{}, model.E(model.CodeInternal, 500, "cannot read disk space")
	}
	reserved, err := m.store.ReservedBytes(ctx)
	if err != nil {
		return model.Job{}, err
	}
	if avail-reserved-*entry.Size < m.cfg.MinFreeBytes {
		return model.Job{}, model.E(model.CodeNoSpace, 409, "insufficient disk space for this download")
	}
	row := &storage.JobRow{
		ID: storage.RandomID(16), Source: job.Source, Path: job.Path,
		Version: *entry.Version, Origin: job.Origin,
		RemoteJSON: marshalRemoteMeta(&entry), BytesTotal: *entry.Size,
	}
	if err := m.store.InsertJob(ctx, row); err != nil {
		return model.Job{}, err
	}
	m.signalWake()
	return row.Public(), nil
}

// GetJob returns one job's public form.
func (m *Manager) GetJob(ctx context.Context, id string) (model.Job, error) {
	row, err := m.store.GetJob(ctx, id)
	if err != nil {
		return model.Job{}, err
	}
	if row == nil {
		return model.Job{}, model.E(model.CodeNotFound, 404, "job not found")
	}
	return row.Public(), nil
}

func marshalRemoteMeta(e *model.Entry) string {
	b, err := jsonMarshal(e)
	if err != nil {
		log.Printf("marshal remote meta: %v", err)
		return ""
	}
	return string(b)
}

func safeBasename(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	if name == "" || name == "." || name == ".." {
		name = "file"
	}
	return name
}

// finalRel computes the published relative path under filesDir:
// media/<source>/<origin>/<job-id>/<attempt>/<basename>.
// Updates never overwrite older versions (attempt increments).
func finalRel(source model.Source, origin, jobID string, attempt int64, basename string) string {
	return filepath.Join("media", string(source), origin, jobID,
		fmt.Sprintf("%d", attempt), basename)
}

// FinalRel is the exported form of finalRel, shared with crash recovery.
func FinalRel(source model.Source, origin, jobID string, attempt int64, basename string) string {
	return finalRel(source, origin, jobID, attempt, basename)
}
