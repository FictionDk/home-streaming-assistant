package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"home-streaming-assistant/internal/model"
)

func readRandom(b []byte) (int, error) { return rand.Read(b) }

// JobRow is the persisted job record.
type JobRow struct {
	ID              string
	Source          model.Source
	Path            string
	Version         string
	Origin          string
	State           string
	Attempt         int64
	RemoteJSON      string
	BytesDone       int64
	BytesTotal      int64
	CancelRequested bool
	FinalRel        sql.NullString
	LocalSHA256     sql.NullString
	ErrorCode       sql.NullString
	CreatedAt       string
	UpdatedAt       string
}

// Public converts a row into the API model.
func (r *JobRow) Public() model.Job {
	j := model.Job{
		ID: r.ID, Source: r.Source, Path: r.Path, Version: r.Version,
		Origin: r.Origin, State: r.State, Attempt: r.Attempt,
		BytesDone: r.BytesDone, BytesTotal: r.BytesTotal,
		CancelRequested: r.CancelRequested,
	}
	if r.ErrorCode.Valid && r.ErrorCode.String != "" {
		c := r.ErrorCode.String
		j.ErrorCode = &c
	}
	parse := func(s string) time.Time {
		t, _ := time.Parse(time.RFC3339Nano, s)
		return t
	}
	j.CreatedAt = parse(r.CreatedAt)
	j.UpdatedAt = parse(r.UpdatedAt)
	return j
}

const jobColumns = `id,source,path,version,origin,state,attempt,remote_json,bytes_done,bytes_total,cancel_requested,final_rel,local_sha256,error_code,created_at,updated_at`

func scanJob(row interface{ Scan(...any) error }) (*JobRow, error) {
	var r JobRow
	err := row.Scan(&r.ID, &r.Source, &r.Path, &r.Version, &r.Origin, &r.State,
		&r.Attempt, &r.RemoteJSON, &r.BytesDone, &r.BytesTotal, &r.CancelRequested,
		&r.FinalRel, &r.LocalSHA256, &r.ErrorCode, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// InsertJob inserts a new queued job.
func (s *Store) InsertJob(ctx context.Context, j *JobRow) error {
	now := s.nowString()
	j.CreatedAt = now
	j.UpdatedAt = now
	j.State = model.JobQueued
	if j.Attempt == 0 {
		j.Attempt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO download_jobs (`+jobColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		j.ID, string(j.Source), j.Path, j.Version, j.Origin, j.State, j.Attempt,
		j.RemoteJSON, j.BytesDone, j.BytesTotal, boolInt(j.CancelRequested),
		j.FinalRel, j.LocalSHA256, j.ErrorCode, j.CreatedAt, j.UpdatedAt)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetJob loads one job by id.
func (s *Store) GetJob(ctx context.Context, id string) (*JobRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM download_jobs WHERE id = ?`, id)
	r, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// GetJobByVersion finds the unique job for (source,path,version).
func (s *Store) GetJobByVersion(ctx context.Context, source model.Source, path, version string) (*JobRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+jobColumns+` FROM download_jobs WHERE source=? AND path=? AND version=?`,
		string(source), path, version)
	r, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// ListJobs returns jobs newest-first with keyset pagination over
// (created_at,id) and an optional state filter. cursor is the opaque
// "created_at|id" pair bound to the caller's filter.
func (s *Store) ListJobs(ctx context.Context, state *string, cursor string, limit int) ([]*JobRow, string, error) {
	q := `SELECT ` + jobColumns + ` FROM download_jobs`
	var args []any
	if state != nil {
		q += ` WHERE state = ?`
		args = append(args, *state)
	}
	if cursor != "" {
		parts := strings.SplitN(cursor, "|", 2)
		if len(parts) != 2 {
			return nil, "", model.E(model.CodeInvalidRequest, 400, "bad cursor")
		}
		if state != nil {
			q += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		} else {
			q += ` WHERE (created_at < ? OR (created_at = ? AND id < ?))`
		}
		args = append(args, parts[0], parts[0], parts[1])
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []*JobRow
	for rows.Next() {
		r, err := scanJob(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		last := out[limit-1]
		next = last.CreatedAt + "|" + last.ID
		out = out[:limit]
	}
	return out, next, nil
}

// ActiveJobs returns all jobs in the given states (for recovery).
func (s *Store) JobsInStates(ctx context.Context, states ...string) ([]*JobRow, error) {
	ph := make([]string, len(states))
	args := make([]any, len(states))
	for i, st := range states {
		ph[i] = "?"
		args[i] = st
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+jobColumns+` FROM download_jobs WHERE state IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JobRow
	for rows.Next() {
		r, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ReservedBytes sums outstanding transfer volume for queued and active
// jobs, used for disk-space accounting.
func (s *Store) ReservedBytes(ctx context.Context) (int64, error) {
	var sum sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT SUM(bytes_total - bytes_done) FROM download_jobs
		 WHERE state IN ('queued','downloading','verifying')`).Scan(&sum)
	if err != nil {
		return 0, err
	}
	if !sum.Valid {
		return 0, nil
	}
	return sum.Int64, nil
}

// UpdateJobFields applies a partial update to one job.
func (s *Store) UpdateJobFields(ctx context.Context, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	sets := make([]string, 0, len(fields)+1)
	args := make([]any, 0, len(fields)+1)
	for k, v := range fields {
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, s.nowString())
	args = append(args, id)
	_, err := s.db.ExecContext(ctx,
		`UPDATE download_jobs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

// ClaimNextQueued transactionally moves the oldest queued job to
// downloading. Returns nil when the queue is empty.
func (s *Store) ClaimNextQueued(ctx context.Context) (*JobRow, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx,
		`SELECT `+jobColumns+` FROM download_jobs WHERE state='queued'
		 ORDER BY created_at ASC, id ASC LIMIT 1`)
	r, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	now := s.nowString()
	if _, err := tx.ExecContext(ctx,
		`UPDATE download_jobs SET state='downloading', updated_at=? WHERE id=? AND state='queued'`,
		now, r.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	r.State = model.JobDownloading
	r.UpdatedAt = now
	return r, nil
}

// LibraryRow is a persisted library file record.
type LibraryRow struct {
	ID             string
	JobID          string
	Attempt        int64
	Source         model.Source
	Path           string
	Version        string
	RelativePath   string
	Size           int64
	LocalSHA256    string
	State          string
	ErrorCode      sql.NullString
	JellyfinItemID sql.NullString
	JellyfinSrvID  sql.NullString
	CreatedAt      string
	UpdatedAt      string
}

const libColumns = `id,job_id,attempt,source,path,version,relative_path,size,local_sha256,state,error_code,jellyfin_item_id,jellyfin_server_id,created_at,updated_at`

func scanLib(row interface{ Scan(...any) error }) (*LibraryRow, error) {
	var r LibraryRow
	err := row.Scan(&r.ID, &r.JobID, &r.Attempt, &r.Source, &r.Path, &r.Version,
		&r.RelativePath, &r.Size, &r.LocalSHA256, &r.State, &r.ErrorCode,
		&r.JellyfinItemID, &r.JellyfinSrvID, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// UpsertLibrary inserts or refreshes the pending_index record for a
// published file, keyed by relative_path.
func (s *Store) UpsertLibrary(ctx context.Context, r *LibraryRow) error {
	now := s.nowString()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO library_files (`+libColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (relative_path) DO UPDATE SET
		   job_id=excluded.job_id, attempt=excluded.attempt, source=excluded.source,
		   path=excluded.path, version=excluded.version, size=excluded.size,
		   local_sha256=excluded.local_sha256, state=excluded.state,
		   error_code=NULL, jellyfin_item_id=NULL, jellyfin_server_id=NULL,
		   updated_at=excluded.updated_at`,
		r.ID, r.JobID, r.Attempt, string(r.Source), r.Path, r.Version,
		r.RelativePath, r.Size, r.LocalSHA256, r.State, r.ErrorCode,
		r.JellyfinItemID, r.JellyfinSrvID, now, now)
	return err
}

// GetLibrary loads one library file by id.
func (s *Store) GetLibrary(ctx context.Context, id string) (*LibraryRow, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+libColumns+` FROM library_files WHERE id=?`, id)
	r, err := scanLib(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// LibraryByRelativePath finds the record occupying a physical path.
func (s *Store) LibraryByRelativePath(ctx context.Context, rel string) (*LibraryRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+libColumns+` FROM library_files WHERE relative_path=?`, rel)
	r, err := scanLib(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// LibraryByJobID finds the library record created by a job.
func (s *Store) LibraryByJobID(ctx context.Context, jobID string) (*LibraryRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+libColumns+` FROM library_files WHERE job_id=? ORDER BY attempt DESC LIMIT 1`, jobID)
	r, err := scanLib(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// LibraryByPathVersion finds the current record for a source path+version.
func (s *Store) LibraryByPathVersion(ctx context.Context, source model.Source, path, version string) (*LibraryRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+libColumns+` FROM library_files
		 WHERE source=? AND path=? AND version=? AND state IN ('pending_index','playable')
		 ORDER BY created_at DESC LIMIT 1`, string(source), path, version)
	r, err := scanLib(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

// LibraryLocalStates returns the visible local state (pending_index or
// playable) for every source path, for Entry enrichment.
func (s *Store) LibraryLocalStates(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, state FROM library_files WHERE state IN ('pending_index','playable')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var p, st string
		if err := rows.Scan(&p, &st); err != nil {
			return nil, err
		}
		if _, exists := m[p]; !exists || st == model.LibPlayable {
			m[p] = st
		}
	}
	return m, rows.Err()
}

// ListLibrary returns visible (non deleted/deleting) files, optional
// source filter, keyset paginated by created_at|id.
func (s *Store) ListLibrary(ctx context.Context, source *model.Source, cursor string, limit int) ([]*LibraryRow, string, error) {
	q := `SELECT ` + libColumns + ` FROM library_files WHERE state NOT IN ('deleted','deleting')`
	var args []any
	if source != nil {
		q += ` AND source = ?`
		args = append(args, string(*source))
	}
	if cursor != "" {
		parts := strings.SplitN(cursor, "|", 2)
		if len(parts) != 2 {
			return nil, "", model.E(model.CodeInvalidRequest, 400, "bad cursor")
		}
		q += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		args = append(args, parts[0], parts[0], parts[1])
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []*LibraryRow
	for rows.Next() {
		r, err := scanLib(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(out) > limit {
		last := out[limit-1]
		next = last.CreatedAt + "|" + last.ID
		out = out[:limit]
	}
	return out, next, nil
}

// LibraryInStates returns records in the given states (indexer/recovery).
func (s *Store) LibraryInStates(ctx context.Context, states ...string) ([]*LibraryRow, error) {
	ph := make([]string, len(states))
	args := make([]any, len(states))
	for i, st := range states {
		ph[i] = "?"
		args[i] = st
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+libColumns+` FROM library_files WHERE state IN (`+strings.Join(ph, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*LibraryRow
	for rows.Next() {
		r, err := scanLib(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateLibraryFields applies a partial update.
func (s *Store) UpdateLibraryFields(ctx context.Context, id string, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	sets := make([]string, 0, len(fields)+1)
	args := make([]any, 0, len(fields)+1)
	for k, v := range fields {
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, s.nowString())
	args = append(args, id)
	_, err := s.db.ExecContext(ctx,
		`UPDATE library_files SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

// CompletePublish atomically upserts the pending_index library record and
// marks the job completed. Callers must have persisted publishing state
// and the final_rel BEFORE touching the filesystem.
func (s *Store) CompletePublish(ctx context.Context, jobID string, lib *LibraryRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := s.nowString()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO library_files (`+libColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		 ON CONFLICT (relative_path) DO UPDATE SET
		   job_id=excluded.job_id, attempt=excluded.attempt, source=excluded.source,
		   path=excluded.path, version=excluded.version, size=excluded.size,
		   local_sha256=excluded.local_sha256, state=excluded.state,
		   error_code=NULL, jellyfin_item_id=NULL, jellyfin_server_id=NULL,
		   updated_at=excluded.updated_at`,
		lib.ID, lib.JobID, lib.Attempt, string(lib.Source), lib.Path, lib.Version,
		lib.RelativePath, lib.Size, lib.LocalSHA256, model.LibPendingIndex,
		sql.NullString{}, sql.NullString{}, sql.NullString{}, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE download_jobs SET state='completed', error_code=NULL, updated_at=? WHERE id=?`,
		now, jobID); err != nil {
		return err
	}
	return tx.Commit()
}

// ScheduleRow is the single gdrive_schedule row.
type ScheduleRow struct {
	Path           string
	Enabled        bool
	Time           string
	Timezone       string
	Revision       int64
	LastSlot       sql.NullString
	LastStartedAt  sql.NullString
	LastFinishedAt sql.NullString
	LastStatus     sql.NullString
	LastErrorCode  sql.NullString
	LastEnqueued   sql.NullInt64
}

// GetSchedule loads the single schedule row.
func (s *Store) GetSchedule(ctx context.Context) (*ScheduleRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT path,enabled,time,timezone,revision,last_slot,last_started_at,last_finished_at,last_status,last_error_code,last_enqueued FROM gdrive_schedule WHERE id=1`)
	var r ScheduleRow
	err := row.Scan(&r.Path, &r.Enabled, &r.Time, &r.Timezone, &r.Revision,
		&r.LastSlot, &r.LastStartedAt, &r.LastFinishedAt, &r.LastStatus,
		&r.LastErrorCode, &r.LastEnqueued)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// UpdateSchedule sets path/enabled/time/timezone and bumps revision,
// which cancels any in-flight scan of the previous revision.
func (s *Store) UpdateSchedule(ctx context.Context, path string, enabled bool, tm, tz string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE gdrive_schedule SET path=?, enabled=?, time=?, timezone=?, revision=revision+1 WHERE id=1`,
		path, boolInt(enabled), tm, tz)
	return err
}

// UpdateScheduleRun persists one run bookkeeping update.
func (s *Store) UpdateScheduleRun(ctx context.Context, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	sets := make([]string, 0, len(fields))
	args := make([]any, 0, len(fields))
	for k, v := range fields {
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	args = append(args, 1)
	_, err := s.db.ExecContext(ctx,
		`UPDATE gdrive_schedule SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...)
	return err
}

// MarkInterruptedRuns flags schedules left mid-run by a crash/restart.
func (s *Store) MarkInterruptedRuns(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE gdrive_schedule SET last_status='interrupted'
		 WHERE last_started_at IS NOT NULL AND last_finished_at IS NULL`)
	return err
}

// SessionRow is a persisted browser session.
type SessionRow struct {
	TokenHash string
	CSRFToken string
	ExpiresAt time.Time
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, tokenHash, csrf string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, csrf_token, expires_at) VALUES (?,?,?)`,
		tokenHash, csrf, expires.UTC().Format(time.RFC3339Nano))
	return err
}

// GetSession loads a live session; expired sessions are deleted and
// reported as absent.
func (s *Store) GetSession(ctx context.Context, tokenHash string) (*SessionRow, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT token_hash, csrf_token, expires_at FROM sessions WHERE token_hash=?`, tokenHash)
	var r SessionRow
	var exp string
	err := row.Scan(&r.TokenHash, &r.CSRFToken, &exp)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.ExpiresAt, err = time.Parse(time.RFC3339Nano, exp)
	if err != nil {
		return nil, fmt.Errorf("parse session expiry: %w", err)
	}
	if s.Now().UTC().After(r.ExpiresAt) {
		_ = s.DeleteSession(ctx, tokenHash)
		return nil, nil
	}
	return &r, nil
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash=?`, tokenHash)
	return err
}

// CleanupExpiredSessions prunes dead sessions at startup.
func (s *Store) CleanupExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at < ?`, s.nowString())
	return err
}
