// Package model defines the shared data contracts used by every layer.
// Field names and JSON shapes here are the single source of truth; other
// packages MUST NOT rename or re-derive them.
package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Source is the fixed set of upstream sources. Never extend without
// updating catalog, downloads and frontend together.
type Source string

const (
	SourceAliyun Source = "aliyun"
	SourceGDrive Source = "gdrive"
)

var Sources = []Source{SourceAliyun, SourceGDrive}

func (s Source) Valid() bool {
	return s == SourceAliyun || s == SourceGDrive
}

// Entry is one upstream catalog item. Null semantics: unknown time/type/
// version are null, never zero guesses.
type Entry struct {
	Source     Source     `json:"source"`
	Path       string     `json:"path"`
	Name       string     `json:"name"`
	IsDir      bool       `json:"isDir"`
	Size       *int64     `json:"size"`
	Modified   *time.Time `json:"modified"`
	MimeType   *string    `json:"mimeType"`
	Version    *string    `json:"version"`
	LocalState *string    `json:"localState"`
	// Hashes carries upstream checksums for verification only; it is
	// never serialized to clients.
	Hashes []HashPayload `json:"-"`
}

// EntryPage is one page of a directory listing.
type EntryPage struct {
	Entries    []Entry `json:"entries"`
	NextCursor *string `json:"nextCursor"`
}

// Job states. Transitions:
// queued -> downloading -> verifying -> publishing -> completed
// queued/downloading/verifying -> cancelled|failed
// publishing is NOT cancellable.
const (
	JobQueued      = "queued"
	JobDownloading = "downloading"
	JobVerifying   = "verifying"
	JobPublishing  = "publishing"
	JobCompleted   = "completed"
	JobCancelled   = "cancelled"
	JobFailed      = "failed"
)

// Origins.
const (
	OriginManual   = "manual"
	OriginSchedule = "schedule"
)

// Job is the public JSON shape of a download job.
type Job struct {
	ID              string    `json:"id"`
	Source          Source    `json:"source"`
	Path            string    `json:"path"`
	Version         string    `json:"version"`
	Origin          string    `json:"origin"`
	State           string    `json:"state"`
	Attempt         int64     `json:"attempt"`
	BytesDone       int64     `json:"bytesDone"`
	BytesTotal      int64     `json:"bytesTotal"`
	CancelRequested bool      `json:"cancelRequested"`
	ErrorCode       *string   `json:"errorCode"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
	LibraryID       *string   `json:"libraryId,omitempty"`
}

// Library file states.
const (
	LibPendingIndex = "pending_index"
	LibPlayable     = "playable"
	LibDeleting     = "deleting"
	LibDeleted      = "deleted"
	LibMissing      = "missing"
)

// File is the public JSON shape of a library file.
type File struct {
	ID          string    `json:"id"`
	Source      Source    `json:"source"`
	Path        string    `json:"path"`
	Version     string    `json:"version"`
	Name        string    `json:"name"`
	Size        int64     `json:"size"`
	State       string    `json:"state"`
	PlaybackURL *string   `json:"playbackURL"`
	ErrorCode   *string   `json:"errorCode"`
	CreatedAt   time.Time `json:"createdAt"`
}

// SourceStatus is the public health shape of one source.
type SourceStatus struct {
	ID            Source     `json:"id"`
	Status        string     `json:"status"` // ready|not_configured|unavailable|forbidden
	LastCheckedAt *time.Time `json:"lastCheckedAt"`
	ErrorCode     *string    `json:"errorCode"`
}

const (
	SrcReady         = "ready"
	SrcNotConfigured = "not_configured"
	SrcUnavailable   = "unavailable"
	SrcForbidden     = "forbidden"
)

// versionPayload is the canonical version fingerprint input.
type versionPayload struct {
	Source   string  `json:"source"`
	Path     string  `json:"path"`
	Size     *int64  `json:"size"`
	Modified *string `json:"modified,omitempty"`
	ID       *string `json:"id,omitempty"`
	Hash     *string `json:"hash,omitempty"`
}

// ComputeVersion derives the canonical content-version fingerprint:
// SHA-256 of canonical JSON over source, canonical path, size, modified,
// upstream ID and best available hash. It returns nil when there is no
// reliable modified time or content hash (size only) — callers must
// surface VERSION_UNAVAILABLE instead of assuming the ID is a content
// version.
func ComputeVersion(src Source, path string, size *int64, modified *time.Time, id, hash *string) (*string, error) {
	if size == nil {
		return nil, errors.New("version requires size")
	}
	if modified == nil && hash == nil {
		return nil, errors.New("version requires modified or hash")
	}
	p := versionPayload{Source: string(src), Path: path, Size: size, ID: id, Hash: hash}
	if modified != nil {
		u := modified.UTC().Format(time.RFC3339Nano)
		p.Modified = &u
	}
	b, err := json.Marshal(&p)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	h := hex.EncodeToString(sum[:])
	return &h, nil
}

// HashPayload is a remote-provided checksum used for verification.
type HashPayload struct {
	Type  string // "md5"|"sha1"|"sha256"
	Value string
}

// BestHash picks the strongest of the provided hashes.
func BestHash(hashes []HashPayload) *HashPayload {
	if len(hashes) == 0 {
		return nil
	}
	rank := map[string]int{"sha256": 3, "sha1": 2, "md5": 1}
	best := hashes[0]
	for _, h := range hashes[1:] {
		if rank[h.Type] > rank[best.Type] {
			best = h
		}
	}
	if rank[best.Type] == 0 {
		return nil
	}
	return &best
}

// LocalStateFor returns the visible local state of a path from the caller's
// map (nil-safe).
func LocalStateFor(m map[string]string, path string) *string {
	if m == nil {
		return nil
	}
	if s, ok := m[path]; ok {
		return &s
	}
	return nil
}

// ValidateClientPath validates a client-supplied path relative to the
// allowed root. Empty means the root itself. It rejects absolute paths,
// NUL, backslashes, "."/".." segments and duplicate separators instead of
// silently cleaning them away.
func ValidateClientPath(path string) error {
	if path == "" {
		return nil
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("absolute path not allowed")
	}
	if strings.HasSuffix(path, "/") {
		return fmt.Errorf("trailing slash not allowed")
	}
	if strings.Contains(path, "\\") {
		return fmt.Errorf("backslash not allowed")
	}
	if strings.Contains(path, "\x00") {
		return fmt.Errorf("NUL not allowed")
	}
	segs := strings.Split(path, "/")
	for _, s := range segs {
		switch {
		case s == "":
			return fmt.Errorf("empty path segment (duplicate separator)")
		case s == "." || s == "..":
			return fmt.Errorf("dot segment not allowed")
		}
	}
	return nil
}

// JoinClientPath returns path joined with child, validating child as a
// single segment.
func JoinClientPath(path, child string) (string, error) {
	if child == "" || child == "." || child == ".." || strings.ContainsAny(child, "/\\\x00") {
		return "", fmt.Errorf("invalid name")
	}
	if path == "" {
		return child, nil
	}
	return path + "/" + child, nil
}

// ValidateUpstreamName rejects names that cannot be safely used as a
// single path segment.
func ValidateUpstreamName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("invalid upstream name")
	}
	return nil
}

// SortEntries orders entries directories-first, then by UTF-8 byte order
// of the name.
func SortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})
}
