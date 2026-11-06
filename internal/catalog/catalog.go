// Package catalog implements the two upstream read-only sources and the
// fixed browsing contract:
//
//	List(ctx, source, path, cursor, refresh) (model.EntryPage, error)
//	Stat(ctx, source, path) (model.Entry, error)
package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"home-streaming-assistant/internal/model"
)

// Backend is one upstream source adapter.
type Backend interface {
	List(ctx context.Context, path, cursor string, refresh bool, cs *CursorStore) (model.EntryPage, error)
	Stat(ctx context.Context, path string) (model.Entry, error)
	Probe(ctx context.Context) error // cheap root metadata check
}

// Manager routes requests to the configured backend for each source and
// tracks public source health.
type Manager struct {
	mu       sync.Mutex
	backends map[model.Source]Backend
	statuses map[model.Source]*status
}

type status struct {
	state   string
	checked time.Time
	errCode string
}

// NewManager builds the manager. Missing sources are allowed: requests to
// them fail with NOT_CONFIGURED instead of pretending empty results.
func NewManager(aliyun, gdrive Backend) *Manager {
	m := &Manager{backends: map[model.Source]Backend{}, statuses: map[model.Source]*status{}}
	if aliyun != nil {
		m.backends[model.SourceAliyun] = aliyun
		m.statuses[model.SourceAliyun] = &status{state: model.SrcNotConfigured}
	}
	if gdrive != nil {
		m.backends[model.SourceGDrive] = gdrive
		m.statuses[model.SourceGDrive] = &status{state: model.SrcNotConfigured}
	}
	return m
}

// List lists one directory page. HTTP callers get a 30s budget.
func (m *Manager) List(ctx context.Context, source model.Source, path, cursor string, refresh bool) (model.EntryPage, error) {
	b, err := m.backend(source)
	if err != nil {
		return model.EntryPage{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	page, err := b.List(cctx, path, cursor, refresh, globalCursors)
	m.record(source, err)
	return page, err
}

// Stat resolves one entry. Root listing and health probes use path "".
func (m *Manager) Stat(ctx context.Context, source model.Source, path string) (model.Entry, error) {
	b, err := m.backend(source)
	if err != nil {
		return model.Entry{}, err
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	e, err := b.Stat(cctx, path)
	m.record(source, err)
	return e, err
}

// Probe refreshes the health state of one source by checking root
// metadata. It never scans the whole drive.
func (m *Manager) Probe(ctx context.Context, source model.Source) {
	b, ok := m.backends[source]
	if !ok {
		return
	}
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := b.Probe(pctx)
	m.record(source, err)
}

func (m *Manager) backend(source model.Source) (Backend, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.backends[source]
	if !ok {
		return nil, model.E(model.CodeNotConfigured, 503, "source not configured")
	}
	return b, nil
}

func (m *Manager) record(source model.Source, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.statuses[source]
	if !ok {
		return
	}
	st.checked = time.Now().UTC()
	if err == nil {
		st.state = model.SrcReady
		st.errCode = ""
		return
	}
	switch model.ErrCode(err) {
	case model.CodeNotConfigured:
		st.state = model.SrcNotConfigured
	case model.CodeUpstreamForbidden:
		st.state = model.SrcForbidden
	default:
		st.state = model.SrcUnavailable
	}
	st.errCode = model.ErrCode(err)
}

// Statuses returns a snapshot of public source health.
func (m *Manager) Statuses() []model.SourceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.SourceStatus, 0, len(model.Sources))
	for _, src := range model.Sources {
		st, ok := m.statuses[src]
		if !ok {
			out = append(out, model.SourceStatus{ID: src, Status: model.SrcNotConfigured})
			continue
		}
		s := model.SourceStatus{ID: src, Status: st.state}
		if !st.checked.IsZero() {
			t := st.checked
			s.LastCheckedAt = &t
		}
		if st.errCode != "" {
			c := st.errCode
			s.ErrorCode = &c
		}
		out = append(out, s)
	}
	return out
}

// SourceReady reports whether a source currently passes health.
func (m *Manager) SourceReady(source model.Source) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.statuses[source]
	return ok && st.state == model.SrcReady
}

// CursorStore issues opaque server-side pagination tokens bound to
// source, path, page and a per-path generation, with a 5 minute TTL.
// Wrong directory, expired tokens and stale generations yield
// CURSOR_EXPIRED (409).
type CursorStore struct {
	mu   sync.Mutex
	toks map[string]cursorTok
	gens map[string]int64
	Now  func() time.Time
}

type cursorTok struct {
	Source  model.Source
	Path    string
	Page    int
	Gen     int64
	Expires time.Time
}

const cursorTTL = 5 * time.Minute

var globalCursors = &CursorStore{toks: map[string]cursorTok{}, gens: map[string]int64{},
	Now: func() time.Time { return time.Now().UTC() }}

func (c *CursorStore) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}

// New allocates a cursor token for the next page.
func (c *CursorStore) New(source model.Source, path string, page int) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	tok := hex.EncodeToString(b)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.toks[tok] = cursorTok{
		Source: source, Path: path, Page: page,
		Gen: c.gens[c.genKey(source, path)], Expires: c.now().Add(cursorTTL),
	}
	return tok
}

// Get validates a cursor token for source/path and returns its page.
func (c *CursorStore) Get(source model.Source, path, token string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t, ok := c.toks[token]
	if !ok || t.Source != source || t.Path != path {
		return 0, model.E(model.CodeCursorExpired, 409, "cursor does not match directory")
	}
	if c.now().After(t.Expires) {
		delete(c.toks, token)
		return 0, model.E(model.CodeCursorExpired, 409, "cursor expired")
	}
	if t.Gen != c.gens[c.genKey(source, path)] {
		return 0, model.E(model.CodeCursorExpired, 409, "directory was refreshed")
	}
	return t.Page, nil
}

// Invalidate bumps the generation for a path so all outstanding cursors
// and cached pages for it expire.
func (c *CursorStore) Invalidate(source model.Source, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := c.genKey(source, path)
	c.gens[key]++
}

func (c *CursorStore) genKey(source model.Source, path string) string {
	return fmt.Sprintf("%s\x00%s", source, path)
}

// UpstreamRootPath joins the configured root with a validated client
// path, producing the upstream logical path ("" client path -> root).
func UpstreamRootPath(root, path string) string {
	if path == "" {
		return root
	}
	return root + "/" + path
}
