package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"home-streaming-assistant/internal/model"
)

// GDrive lists and stats Google Drive through the packaged rclone binary
// and the dedicated read-only remote "gdrive-ro". The remote name is
// fixed; requests never specify another. Shortcuts are always skipped to
// avoid resolving targets outside the allowed root.
type GDrive struct {
	Binary string // rclone binary path
	Config string // dedicated writable config file path
	Root   string // configured non-root directory, absolute

	mu    sync.Mutex
	cache map[string]*gdriveCacheEntry
	bytes int64

	enabled bool
}

const (
	gdrivePageSize        = 100
	gdriveMaxEntries      = 100000
	gdriveMaxListBytes    = int64(64 << 20)
	gdriveCacheTTL        = 60 * time.Second
	gdriveCacheTotalBytes = int64(128 << 20)
)

type gdriveCacheEntry struct {
	entries  []model.Entry
	gen      int64
	cachedAt time.Time
	bytes    int64
}

// NewGDrive validates that the rclone config contains a gdrive-ro remote
// of type drive with scope drive.readonly. Anything else disables the
// source: requests fail with NOT_CONFIGURED rather than silently widening
// access.
func NewGDrive(binary, config, root string) (*GDrive, error) {
	if binary == "" || config == "" || root == "" {
		return nil, fmt.Errorf("gdrive source not fully configured")
	}
	if err := checkRcloneRemote(config); err != nil {
		return nil, err
	}
	return &GDrive{
		Binary: binary, Config: config, Root: strings.TrimPrefix(root, "/"),
		cache: map[string]*gdriveCacheEntry{}, enabled: true,
	}, nil
}

// checkRcloneRemote parses the INI-shaped rclone config and verifies the
// gdrive-ro section. This is a deployment sanity check; it does not
// replace verifying the granted OAuth scope on a live token.
func checkRcloneRemote(config string) error {
	b, err := os.ReadFile(config)
	if err != nil {
		return model.E(model.CodeNotConfigured, 503, "gdrive config unreadable")
	}
	var inSection bool
	sectionFound := false
	typ, scope := "", ""
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = line == "[gdrive-ro]"
			if inSection {
				sectionFound = true
			}
			continue
		}
		if !inSection {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "type":
			typ = strings.TrimSpace(v)
		case "scope":
			scope = strings.TrimSpace(v)
		}
	}
	if !sectionFound {
		return model.E(model.CodeNotConfigured, 503, "gdrive-ro remote missing")
	}
	if typ != "drive" {
		return model.E(model.CodeNotConfigured, 503, "gdrive-ro is not a drive remote")
	}
	if scope != "drive.readonly" {
		return model.E(model.CodeNotConfigured, 503, "gdrive-ro scope must be drive.readonly")
	}
	return nil
}

func (g *GDrive) remotePath(path string) string {
	return "gdrive-ro:" + UpstreamRootPath(g.Root, path)
}

// env is a controlled environment: rclone must not inherit host remotes
// or arbitrary overrides.
func gdriveEnv() []string {
	return []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/tmp",
		"LANG=C.UTF-8",
	}
}

// rcloneJSON runs rclone and decodes its stdout with strict size bounds.
func (g *GDrive) rcloneJSON(ctx context.Context, maxBytes int64, args ...string) ([]byte, error) {
	full := append([]string{"--config", g.Config, "--use-json-log"}, args...)
	cmd := exec.CommandContext(ctx, g.Binary, full...)
	cmd.Env = gdriveEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	stderr.Grow(64 << 10)
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, model.E(model.CodeUpstreamUnavailable, 503, "gdrive request timed out")
		}
		return nil, model.E(model.CodeUpstreamUnavailable, 503, "rclone command failed")
	}
	if int64(stdout.Len()) > maxBytes {
		return nil, model.E(model.CodeDirectoryTooLarge, 413, "gdrive listing too large")
	}
	return stdout.Bytes(), nil
}

type limitedWriter struct {
	w io.Writer
	n int64
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if int64(len(p)) > l.n {
		l.n = 0
		return l.w.Write(p[:1])
	}
	l.n -= int64(len(p))
	return l.w.Write(p)
}

// lsjsonItem is one rclone lsjson record.
type lsjsonItem struct {
	Path     string            `json:"Path"`
	Name     string            `json:"Name"`
	Size     *int64            `json:"Size"`
	MimeType string            `json:"MimeType"`
	ModTime  string            `json:"ModTime"`
	IsDir    bool              `json:"IsDir"`
	ID       string            `json:"ID"`
	Hashes   map[string]string `json:"Hashes"`
}

func gdriveToEntry(parentPath string, it lsjsonItem) (model.Entry, error) {
	name := it.Name
	if name == "" {
		name = it.Path
	}
	if err := model.ValidateUpstreamName(name); err != nil {
		return model.Entry{}, model.E(model.CodeUpstreamError, 502, "rclone returned an invalid entry name")
	}
	child, err := model.JoinClientPath(parentPath, name)
	if err != nil {
		return model.Entry{}, model.E(model.CodeUpstreamError, 502, "rclone returned an invalid entry path")
	}
	e := model.Entry{Source: model.SourceGDrive, Path: child, Name: name, IsDir: it.IsDir}
	if it.ModTime != "" {
		if t, err := time.Parse(time.RFC3339Nano, it.ModTime); err == nil {
			e.Modified = &t
		}
	}
	if !it.IsDir && it.Size != nil {
		e.Size = it.Size
		if it.MimeType != "" {
			mt := it.MimeType
			e.MimeType = &mt
		}
		var hashes []model.HashPayload
		for k, v := range it.Hashes {
			if v != "" {
				hashes = append(hashes, model.HashPayload{Type: strings.ToLower(k), Value: v})
			}
		}
		var id *string
		if it.ID != "" {
			id = &it.ID
		}
		best := model.BestHash(hashes)
		e.Hashes = hashes
		var hashStr *string
		if best != nil {
			h := best.Type + ":" + best.Value
			hashStr = &h
		}
		v, verr := model.ComputeVersion(model.SourceGDrive, child, e.Size, e.Modified, id, hashStr)
		if verr != nil {
			return model.Entry{}, model.E(model.CodeVersionUnavailable, 400, "cannot compute gdrive version")
		}
		e.Version = v
	}
	return e, nil
}

// loadDir lists one directory (non-recursive) through the 60s cache.
func (g *GDrive) loadDir(ctx context.Context, path string, refresh bool) ([]model.Entry, error) {
	g.mu.Lock()
	ce, ok := g.cache[path]
	if ok && !refresh && time.Since(ce.cachedAt) < gdriveCacheTTL {
		entries := ce.entries
		g.mu.Unlock()
		return entries, nil
	}
	g.mu.Unlock()

	stdout, err := g.rcloneJSON(ctx, gdriveMaxListBytes,
		"lsjson", "--drive-skip-shortcuts", "--hash", g.remotePath(path))
	if err != nil {
		return nil, err
	}
	entries, err := decodeLSJSON(stdout, gdriveMaxEntries, path)
	if err != nil {
		return nil, err
	}
	model.SortEntries(entries)

	// Old cursors and cached pages for this path expire on refresh.
	if refresh {
		globalCursors.Invalidate(model.SourceGDrive, path)
	}
	g.mu.Lock()
	var gen int64
	if ce != nil {
		gen = ce.gen
	}
	if refresh {
		gen++
	}
	ne := &gdriveCacheEntry{entries: entries, gen: gen, cachedAt: time.Now().UTC(), bytes: int64(len(stdout))}
	if g.bytes+ne.bytes > gdriveCacheTotalBytes {
		g.evictLocked(ne.bytes)
	}
	g.cache[path] = ne
	g.bytes += ne.bytes
	g.mu.Unlock()
	return entries, nil
}

// evictLocked drops oldest cached directories to stay under the budget.
func (g *GDrive) evictLocked(need int64) {
	for g.bytes+need > gdriveCacheTotalBytes && len(g.cache) > 0 {
		var oldestKey string
		var oldest time.Time
		for k, v := range g.cache {
			if oldestKey == "" || v.cachedAt.Before(oldest) {
				oldestKey, oldest = k, v.cachedAt
			}
		}
		if oldestKey == "" {
			return
		}
		g.bytes -= g.cache[oldestKey].bytes
		delete(g.cache, oldestKey)
	}
}

// decodeLSJSON streams a top-level array so oversized directories fail
// with DIRECTORY_TOO_LARGE instead of pretending to be complete.
func decodeLSJSON(data []byte, maxEntries int, path string) ([]model.Entry, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, model.E(model.CodeUpstreamError, 502, "rclone output malformed")
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil, model.E(model.CodeUpstreamError, 502, "rclone output unexpected")
	}
	var entries []model.Entry
	for dec.More() {
		if len(entries) >= maxEntries {
			return nil, model.E(model.CodeDirectoryTooLarge, 413, "directory exceeds the entry limit")
		}
		var it lsjsonItem
		if err := dec.Decode(&it); err != nil {
			return nil, model.E(model.CodeUpstreamError, 502, "rclone output malformed")
		}
		e, err := gdriveToEntry(path, it)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// List serves one page from the cache.
func (g *GDrive) List(ctx context.Context, path, cursor string, refresh bool, cs *CursorStore) (model.EntryPage, error) {
	if !g.enabled {
		return model.EntryPage{}, model.E(model.CodeNotConfigured, 503, "gdrive not configured")
	}
	page := 0
	var err error
	if cursor != "" {
		page, err = cs.Get(model.SourceGDrive, path, cursor)
		if err != nil {
			return model.EntryPage{}, err
		}
	}
	entries, err := g.loadDir(ctx, path, refresh && page == 0)
	if err != nil {
		return model.EntryPage{}, err
	}
	start := page * gdrivePageSize
	if start > len(entries) {
		start = len(entries)
	}
	end := start + gdrivePageSize
	if end > len(entries) {
		end = len(entries)
	}
	out := model.EntryPage{Entries: entries[start:end]}
	if end < len(entries) {
		next := cs.New(model.SourceGDrive, path, page+1)
		out.NextCursor = &next
	}
	return out, nil
}

// Stat resolves a single object via lsjson --stat --hash.
func (g *GDrive) Stat(ctx context.Context, path string) (model.Entry, error) {
	if !g.enabled {
		return model.Entry{}, model.E(model.CodeNotConfigured, 503, "gdrive not configured")
	}
	stdout, err := g.rcloneJSON(ctx, 1<<20,
		"lsjson", "--drive-skip-shortcuts", "--stat", "--hash", g.remotePath(path))
	if err != nil {
		return model.Entry{}, err
	}
	var it lsjsonItem
	if err := json.Unmarshal(stdout, &it); err != nil {
		return model.Entry{}, model.E(model.CodeNotFound, 404, "entry not found")
	}
	parent := parentOf(path)
	if path == "" {
		parent = ""
	}
	return gdriveToEntry(parent, it)
}

// Probe checks root metadata through the configured remote.
func (g *GDrive) Probe(ctx context.Context) error {
	if !g.enabled {
		return model.E(model.CodeNotConfigured, 503, "gdrive not configured")
	}
	_, err := g.rcloneJSON(ctx, 1<<20,
		"lsjson", "--drive-skip-shortcuts", "--stat", "--hash", g.remotePath(""))
	return err
}
