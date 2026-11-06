// Package schedule runs the single Google Drive daily download plan.
// Each daily slot runs at most once, missed days collapse into one run,
// and disabling or editing the plan bumps a revision that cancels any
// in-flight scan.
package schedule

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"home-streaming-assistant/internal/downloads"
	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/storage"
)

// Runner checks the plan every 15 seconds.
type Runner struct {
	store    *storage.Store
	dl       *downloads.Manager
	Interval time.Duration
	// Now is injectable for tests.
	Now func() time.Time

	mu       sync.Mutex
	revision int64
	running  bool
	cancel   context.CancelFunc
	stop     chan struct{}
	wg       sync.WaitGroup
}

// New builds the runner.
func New(store *storage.Store, dl *downloads.Manager) *Runner {
	return &Runner{store: store, dl: dl, Interval: 15 * time.Second,
		Now: func() time.Time { return time.Now() }, stop: make(chan struct{})}
}

// Start launches the check loop.
func (r *Runner) Start() {
	r.refreshRevision()
	r.wg.Add(1)
	go r.loop()
}

// Stop cancels any running scan and stops the loop.
func (r *Runner) Stop() {
	close(r.stop)
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

func (r *Runner) refreshRevision() {
	row, err := r.store.GetSchedule(context.Background())
	if err == nil && row != nil {
		r.revision = row.Revision
	}
}

func (r *Runner) loop() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			r.check()
		case <-r.stop:
			return
		}
	}
}

// validTime checks HH:MM.
func validTime(t string) bool {
	if len(t) != 5 {
		return false
	}
	h, m := t[0:2], t[3:5]
	if t[2] != ':' {
		return false
	}
	hh, err1 := atoi2(h)
	mm, err2 := atoi2(m)
	return err1 == nil && err2 == nil && hh >= 0 && hh <= 23 && mm >= 0 && mm <= 59
}

func atoi2(s string) (int, error) {
	if len(s) != 2 || s[0] < '0' || s[0] > '9' || s[1] < '0' || s[1] > '9' {
		return 0, &timeParseError{}
	}
	return int(s[0]-'0')*10 + int(s[1]-'0'), nil
}

type timeParseError struct{}

func (*timeParseError) Error() string { return "invalid time" }

// validateTimezone requires a loadable IANA name.
func validateTimezone(tz string) error {
	_, err := time.LoadLocation(tz)
	return err
}

// Validate checks plan fields for the settings API.
func Validate(path string, enabled bool, tm, tz string) error {
	if enabled {
		if path == "" {
			return model.E(model.CodeInvalidRequest, 400, "schedule path is required when enabled")
		}
		if err := model.ValidateClientPath(path); err != nil {
			return model.E(model.CodeInvalidPath, 400, err.Error())
		}
	}
	if !validTime(tm) {
		return model.E(model.CodeInvalidRequest, 400, "time must be HH:MM")
	}
	if err := validateTimezone(tz); err != nil {
		return model.E(model.CodeInvalidRequest, 400, "unknown IANA timezone")
	}
	return nil
}

// check evaluates whether a slot is due and runs it once.
func (r *Runner) check() {
	row, err := r.store.GetSchedule(context.Background())
	if err != nil || row == nil {
		return
	}
	r.mu.Lock()
	currentRev := row.Revision
	if currentRev != r.revision {
		r.revision = currentRev
	}
	if r.running || !row.Enabled || row.Path == "" {
		r.mu.Unlock()
		return
	}
	loc, err := time.LoadLocation(row.Timezone)
	if err != nil {
		r.mu.Unlock()
		return
	}
	now := r.Now().In(loc)
	slot, due := r.dueSlot(now, row)
	if !due {
		r.mu.Unlock()
		return
	}
	if row.LastSlot.Valid && row.LastSlot.String == slot &&
		row.LastStatus.Valid && row.LastStatus.String != "interrupted" {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.running = true
	r.cancel = cancel
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		r.running = false
		r.cancel = nil
		r.mu.Unlock()
		cancel()
	}()

	_ = r.store.UpdateScheduleRun(ctx, map[string]any{
		"last_slot": slot, "last_started_at": nowString(r),
	})
	enqueued, errCode := r.scan(ctx, currentRev, row.Path)
	finished := nowString(r)
	status := "succeeded"
	if r.revisionChanged(currentRev) {
		return // superseded: the update path owns bookkeeping now
	}
	if errCode != "" {
		status = "failed"
	}
	fields := map[string]any{
		"last_finished_at": finished, "last_status": status,
		"last_enqueued": enqueued,
	}
	if errCode != "" {
		fields["last_error_code"] = errCode
	}
	_ = r.store.UpdateScheduleRun(ctx, fields)
}

func nowString(r *Runner) string {
	return r.Now().UTC().Format(time.RFC3339Nano)
}

func (r *Runner) revisionChanged(rev int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.revision != rev
}

// dueSlot returns the slot date that should have run by now. Slots run at
// most once per local day; a missed previous day collapses into one
// immediate run, never a multi-day backfill. DST folds are not repeated
// because the slot key is the calendar date, not the wall clock hour.
func (r *Runner) dueSlot(now time.Time, row *storage.ScheduleRow) (string, bool) {
	hh, _ := atoi2(row.Time[0:2])
	mm, _ := atoi2(row.Time[3:5])
	today := now.Format("2006-01-02")
	schedToday := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, now.Location())
	if !now.Before(schedToday) {
		return today, true
	}
	yesterday := schedToday.AddDate(0, 0, -1).Format("2006-01-02")
	return yesterday, true
}

// scan walks the configured directory level by level and enqueues every
// file. It reads metadata only: no bodies, no recursive copy, no
// out-of-root traversal.
func (r *Runner) scan(ctx context.Context, rev int64, path string) (int, string) {
	enqueued := 0
	var walk func(ctx context.Context, p string) error
	walk = func(ctx context.Context, p string) error {
		if r.revisionChanged(rev) || ctx.Err() != nil {
			return errSuperseded
		}
		page, err := r.dl.Catalog().List(ctx, model.SourceGDrive, p, "", false)
		if err != nil {
			return err
		}
		for {
			for _, e := range page.Entries {
				if e.IsDir {
					if err := walk(ctx, e.Path); err != nil {
						return err
					}
					continue
				}
				if _, _, err := r.dl.Enqueue(ctx, model.SourceGDrive, e.Path, model.OriginSchedule); err != nil {
					// VERSION_UNAVAILABLE and NO_SPACE skip the file but do
					// not abort the whole scan.
					switch model.ErrCode(err) {
					case model.CodeVersionUnavailable, model.CodeNoSpace:
						log.Printf("schedule: skip %s: %s", e.Path, model.ErrCode(err))
						continue
					}
					return err
				}
				enqueued++
			}
			if page.NextCursor == nil {
				return nil
			}
			page, err = r.dl.Catalog().List(ctx, model.SourceGDrive, p, *page.NextCursor, false)
			if err != nil {
				return err
			}
		}
	}
	err := walk(ctx, path)
	if err == errSuperseded || ctx.Err() != nil {
		return enqueued, ""
	}
	if err != nil {
		return enqueued, model.ErrCode(err)
	}
	return enqueued, ""
}

var errSuperseded = &supersededError{}

type supersededError struct{}

func (*supersededError) Error() string { return "schedule revision superseded" }

// Update applies plan changes from the settings API, bumping the
// revision and cancelling an in-flight scan.
func (r *Runner) Update(ctx context.Context, path string, enabled bool, tm, tz string) error {
	if err := Validate(path, enabled, tm, tz); err != nil {
		return err
	}
	if err := r.store.UpdateSchedule(ctx, strings.Trim(path, "/"), enabled, tm, tz); err != nil {
		return err
	}
	r.refreshRevision()
	r.mu.Lock()
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	return nil
}

// DefaultRow is the initial disabled plan.
var DefaultRow = storage.ScheduleRow{Time: "03:00", Timezone: "Asia/Shanghai"}
