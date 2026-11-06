package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/schedule"
	"home-streaming-assistant/internal/storage"
)

// handleSources lists upstream health; ?refresh=true re-probes roots.
func (s *Server) handleSources(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "true" {
		for _, src := range model.Sources {
			s.cat.Probe(r.Context(), src)
		}
	}
	writeJSON(w, r, 200, map[string]any{"sources": s.cat.Statuses()})
}

func parseSourceParam(r *http.Request) (model.Source, error) {
	src := model.Source(r.URL.Query().Get("source"))
	if !src.Valid() {
		return "", model.E(model.CodeInvalidRequest, 400, "unknown source")
	}
	return src, nil
}

// enrichLocalState attaches visible local copy state to entries.
func (s *Server) enrichLocalState(ctx context.Context, entries []model.Entry) {
	states, err := s.store.LibraryLocalStates(ctx)
	if err != nil {
		return
	}
	for i := range entries {
		entries[i].LocalState = model.LocalStateFor(states, entries[i].Path)
	}
}

// handleEntries lists one directory page.
func (s *Server) handleEntries(w http.ResponseWriter, r *http.Request) {
	src, err := parseSourceParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	path := r.URL.Query().Get("path")
	if err := model.ValidateClientPath(path); err != nil {
		writeError(w, r, model.E(model.CodeInvalidPath, 400, err.Error()))
		return
	}
	cursor := r.URL.Query().Get("cursor")
	refresh := r.URL.Query().Get("refresh") == "true"
	page, err := s.cat.List(r.Context(), src, path, cursor, refresh)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.enrichLocalState(r.Context(), page.Entries)
	writeJSON(w, r, 200, page)
}

// handleEntry resolves one file's details.
func (s *Server) handleEntry(w http.ResponseWriter, r *http.Request) {
	src, err := parseSourceParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		writeError(w, r, model.E(model.CodeInvalidRequest, 400, "path required"))
		return
	}
	if err := model.ValidateClientPath(path); err != nil {
		writeError(w, r, model.E(model.CodeInvalidPath, 400, err.Error()))
		return
	}
	entry, err := s.cat.Stat(r.Context(), src, path)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.enrichLocalState(r.Context(), []model.Entry{entry})
	writeJSON(w, r, 200, entry)
}

// handleCreateDownload enqueues a manual download.
func (s *Server) handleCreateDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"`
		Path   string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	job, created, err := s.dl.Enqueue(r.Context(), model.Source(req.Source), req.Path, model.OriginManual)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.attachLibraryID(r, &job)
	status := 202
	if !created {
		status = 200
	}
	writeJSON(w, r, status, job)
}

func (s *Server) attachLibraryID(r *http.Request, job *model.Job) {
	if job.State != model.JobCompleted {
		return
	}
	lib, err := s.store.LibraryByJobID(r.Context(), job.ID)
	if err == nil && lib != nil {
		job.LibraryID = &lib.ID
	}
}

// handleListDownloads lists jobs with optional state filter.
func (s *Server) handleListDownloads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var state *string
	if v := q.Get("state"); v != "" {
		switch v {
		case model.JobQueued, model.JobDownloading, model.JobVerifying,
			model.JobPublishing, model.JobCompleted, model.JobCancelled, model.JobFailed:
			state = &v
		default:
			writeError(w, r, model.E(model.CodeInvalidRequest, 400, "unknown state filter"))
			return
		}
	}
	limit := 100
	rows, next, err := s.store.ListJobs(r.Context(), state, q.Get("cursor"), limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	jobs := make([]model.Job, 0, len(rows))
	for _, row := range rows {
		j := row.Public()
		s.attachLibraryID(r, &j)
		jobs = append(jobs, j)
	}
	var nextPtr *string
	if next != "" {
		nextPtr = &next
	}
	writeJSON(w, r, 200, map[string]any{"jobs": jobs, "nextCursor": nextPtr})
}

// handleGetDownload returns one job.
func (s *Server) handleGetDownload(w http.ResponseWriter, r *http.Request) {
	job, err := s.dl.GetJob(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.attachLibraryID(r, &job)
	writeJSON(w, r, 200, job)
}

// handleCancelDownload cancels a job: 200 done, 202 in progress.
func (s *Server) handleCancelDownload(w http.ResponseWriter, r *http.Request) {
	job, pending, err := s.dl.Cancel(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := 200
	if pending {
		status = 202
	}
	writeJSON(w, r, status, job)
}

// handleRetryDownload re-runs a failed/cancelled job.
func (s *Server) handleRetryDownload(w http.ResponseWriter, r *http.Request) {
	job, err := s.dl.Retry(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, r, 202, job)
}

// handleListLibrary lists visible local files.
func (s *Server) handleListLibrary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var src *model.Source
	if v := model.Source(q.Get("source")); v != "" {
		if !v.Valid() {
			writeError(w, r, model.E(model.CodeInvalidRequest, 400, "unknown source"))
			return
		}
		src = &v
	}
	rows, next, err := s.store.ListLibrary(r.Context(), src, q.Get("cursor"), 100)
	if err != nil {
		writeError(w, r, err)
		return
	}
	appOrigin := s.originForRequest(r)
	files := make([]model.File, 0, len(rows))
	for _, row := range rows {
		f := model.File{
			ID: row.ID, Source: row.Source, Path: row.Path, Version: row.Version,
			Name: filepath.Base(row.Path), Size: row.Size, State: row.State,
		}
		if row.ErrorCode.Valid && row.ErrorCode.String != "" {
			c := row.ErrorCode.String
			f.ErrorCode = &c
		}
		if t, err := time.Parse(time.RFC3339Nano, row.CreatedAt); err == nil {
			f.CreatedAt = t
		}
		if row.State == model.LibPlayable && s.jelly != nil &&
			row.JellyfinItemID.Valid && row.JellyfinSrvID.Valid {
			f.PlaybackURL = s.jelly.PlaybackURL(appOrigin, row.JellyfinItemID.String, row.JellyfinSrvID.String)
		}
		files = append(files, f)
	}
	var nextPtr *string
	if next != "" {
		nextPtr = &next
	}
	writeJSON(w, r, 200, map[string]any{"files": files, "nextCursor": nextPtr})
}

// originForRequest maps the request Host back to its configured origin,
// defaulting to the first origin.
func (s *Server) originForRequest(r *http.Request) string {
	for _, o := range s.cfg.Origins {
		if i := strings.Index(o, "://"); i >= 0 && o[i+3:] == r.Host {
			return o
		}
	}
	return s.cfg.Origins[0]
}

func (s *Server) fileLock(id string) *sync.Mutex {
	v, _ := s.fileLocks.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// handleDeleteLibrary deletes the local copy only, after a conservative
// cross-user playback check. The whole flow is synchronous in-request;
// `deleting` exists only as a crash-recovery marker.
func (s *Server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	lock := s.fileLock(id)
	lock.Lock()
	defer lock.Unlock()

	lib, err := s.store.GetLibrary(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if lib == nil || lib.State == model.LibDeleted {
		writeError(w, r, model.E(model.CodeNotFound, 404, "library file not found"))
		return
	}
	abs := "/media/" + filepath.ToSlash(lib.RelativePath)
	if s.jelly != nil {
		conflict, cerr := s.jelly.PlaybackConflict(r.Context(), abs)
		if cerr != nil {
			writeError(w, r, model.E(model.CodeJellyfinUnavailable, 503, "cannot check playback sessions"))
			return
		}
		if conflict {
			writeError(w, r, model.E(model.CodeFileInUse, 409, "file is playing or paused"))
			return
		}
	}
	if err := s.store.UpdateLibraryFields(r.Context(), id, map[string]any{
		"state": model.LibDeleting,
	}); err != nil {
		writeError(w, r, err)
		return
	}
	root, err := os.OpenRoot(s.cfg.FilesDir)
	if err != nil {
		writeError(w, r, model.E(model.CodeInternal, 500, "cannot open files root"))
		return
	}
	defer root.Close()
	if err := root.Remove(lib.RelativePath); err != nil && !os.IsNotExist(err) {
		// Keep `deleting` so startup recovery can retry.
		writeError(w, r, model.E(model.CodeInternal, 500, "cannot remove local file"))
		return
	}
	if err := s.store.UpdateLibraryFields(r.Context(), id, map[string]any{
		"state": model.LibDeleted,
	}); err != nil {
		writeError(w, r, err)
		return
	}
	if s.indexer != nil {
		go s.indexer.Wake()
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetSchedule returns the single gdrive plan.
func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	row, err := s.store.GetSchedule(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	if row == nil {
		row = &schedule.DefaultRow
	}
	writeJSON(w, r, 200, schedulePublic(row))
}

func schedulePublic(row *storage.ScheduleRow) map[string]any {
	var lastRun any
	if row.LastStartedAt.Valid || row.LastStatus.Valid {
		lr := map[string]any{"status": row.LastStatus.String}
		if row.LastStartedAt.Valid {
			lr["startedAt"] = row.LastStartedAt.String
		}
		if row.LastFinishedAt.Valid {
			lr["finishedAt"] = row.LastFinishedAt.String
		}
		if row.LastEnqueued.Valid {
			lr["enqueued"] = row.LastEnqueued.Int64
		}
		if row.LastErrorCode.Valid && row.LastErrorCode.String != "" {
			lr["errorCode"] = row.LastErrorCode.String
		}
		lastRun = lr
	}
	return map[string]any{
		"path": row.Path, "enabled": row.Enabled,
		"time": row.Time, "timezone": row.Timezone,
		"revision": row.Revision, "lastRun": lastRun,
	}
}

// handlePutSchedule updates the plan.
func (s *Server) handlePutSchedule(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path     string `json:"path"`
		Enabled  bool   `json:"enabled"`
		Time     string `json:"time"`
		Timezone string `json:"timezone"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Time == "" || req.Timezone == "" {
		// Reject silently-substituted defaults.
		writeError(w, r, model.E(model.CodeInvalidRequest, 400, "time and timezone are required"))
		return
	}
	if err := s.sched.Update(r.Context(), req.Path, req.Enabled, req.Time, req.Timezone); err != nil {
		writeError(w, r, err)
		return
	}
	row, err := s.store.GetSchedule(r.Context())
	if err != nil || row == nil {
		writeError(w, r, model.E(model.CodeInternal, 500, "cannot read back schedule"))
		return
	}
	writeJSON(w, r, 200, schedulePublic(row))
}
