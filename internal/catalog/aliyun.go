package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"home-streaming-assistant/internal/model"
)

// Aliyun talks to Alist v3 with a dedicated read-only account. The remote
// name is fixed to "aliyun"; requests never carry it.
type Aliyun struct {
	BaseURL  string // e.g. http://alist:5244
	Username string
	Password string
	Root     string // configured logical root (e.g. /aliyun-media or /)

	client *http.Client

	loginMu sync.Mutex
	token   string
}

// NewAliyun validates basic configuration and returns the client.
func NewAliyun(baseURL, username, password, root string) (*Aliyun, error) {
	if baseURL == "" || username == "" || password == "" {
		return nil, fmt.Errorf("alist source not fully configured")
	}
	if root == "" {
		root = "/"
	}
	return &Aliyun{
		BaseURL:  strings.TrimRight(baseURL, "/"),
		Username: username,
		Password: password,
		Root:     root,
		client:   &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// alistEnvelope is the common v3 response wrapper.
type alistEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (a *Aliyun) call(ctx context.Context, path string, body any) (*alistEnvelope, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, model.E(model.CodeInternal, 500, "encode request")
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.BaseURL+path, &buf)
	if err != nil {
		return nil, model.E(model.CodeInternal, 500, "build request")
	}
	req.Header.Set("Content-Type", "application/json")
	if a.token != "" {
		// Alist v3 takes the raw token, not a Bearer prefix.
		req.Header.Set("Authorization", a.token)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, model.E(model.CodeUpstreamUnavailable, 503, "alist request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, model.E(model.CodeUpstreamUnavailable, 503, "alist response read failed")
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, model.E(model.CodeUpstreamForbidden, 403, "alist rejected credentials")
	}
	var env alistEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, model.E(model.CodeUpstreamError, 502, "alist returned malformed response")
	}
	if env.Code == http.StatusUnauthorized {
		return &env, errAlistAuth
	}
	if env.Code != http.StatusOK {
		return nil, model.E(model.CodeUpstreamError, 502, fmt.Sprintf("alist error %d", env.Code))
	}
	return &env, nil
}

var errAlistAuth = fmt.Errorf("alist auth required")

// login obtains a fresh token. Concurrent 401 handling re-logins exactly
// once under the mutex; the original request is retried once afterwards.
func (a *Aliyun) login(ctx context.Context) error {
	a.loginMu.Lock()
	defer a.loginMu.Unlock()
	env, err := a.callUnauthed(ctx, "/api/auth/login", map[string]string{
		"username": a.Username, "password": a.Password,
	})
	if err != nil {
		return err
	}
	var data struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || data.Token == "" {
		return model.E(model.CodeUpstreamError, 502, "alist login response missing token")
	}
	a.token = data.Token
	return nil
}

func (a *Aliyun) callUnauthed(ctx context.Context, path string, body any) (*alistEnvelope, error) {
	saved := a.token
	a.token = ""
	defer func() { a.token = saved }()
	return a.call(ctx, path, body)
}

// callWithAuth runs call and relogins once on auth failure.
func (a *Aliyun) callWithAuth(ctx context.Context, path string, body any) (*alistEnvelope, error) {
	if a.token == "" {
		if err := a.login(ctx); err != nil {
			return nil, err
		}
	}
	env, err := a.call(ctx, path, body)
	if err == errAlistAuth {
		// Mutual-exclusion re-login then a single retry of the original
		// request; any other error is not retried.
		if lerr := a.login(ctx); lerr != nil {
			return nil, model.E(model.CodeUpstreamForbidden, 403, "alist authentication failed")
		}
		env, err = a.call(ctx, path, body)
		if err == errAlistAuth {
			return nil, model.E(model.CodeUpstreamForbidden, 403, "alist authentication failed")
		}
	}
	return env, err
}

// alistObj is one fs/list or fs/get content item.
type alistObj struct {
	Name     string          `json:"name"`
	Size     int64           `json:"size"`
	IsDir    bool            `json:"is_dir"`
	Modified string          `json:"modified"`
	HashInfo json.RawMessage `json:"hash_info"`
}

type alistHash struct {
	Type string `json:"type"`
	Hash string `json:"hash"`
}

// parseHashInfo accepts the map or array shapes hash_info has used.
func parseHashInfo(raw json.RawMessage) []model.HashPayload {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err == nil {
		var out []model.HashPayload
		for k, v := range m {
			if v != "" {
				out = append(out, model.HashPayload{Type: strings.ToLower(k), Value: v})
			}
		}
		return out
	}
	var arr []alistHash
	if err := json.Unmarshal(raw, &arr); err == nil {
		var out []model.HashPayload
		for _, h := range arr {
			if h.Hash != "" {
				out = append(out, model.HashPayload{Type: strings.ToLower(h.Type), Value: h.Hash})
			}
		}
		return out
	}
	return nil
}

var modifiedLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05 -0700 MST",
	"2006-01-02 15:04:05",
}

func parseModified(s string) *time.Time {
	if s == "" {
		return nil
	}
	for _, l := range modifiedLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return &t
		}
	}
	return nil
}

func (a *Aliyun) upstreamPath(path string) string {
	return UpstreamRootPath(a.Root, path)
}

func (a *Aliyun) toEntry(path, name string, o alistObj, withVersion bool) (model.Entry, error) {
	if err := model.ValidateUpstreamName(name); err != nil {
		return model.Entry{}, model.E(model.CodeUpstreamError, 502, "alist returned an invalid entry name")
	}
	child, err := model.JoinClientPath(path, name)
	if err != nil {
		return model.Entry{}, model.E(model.CodeUpstreamError, 502, "alist returned an invalid entry path")
	}
	e := model.Entry{
		Source:   model.SourceAliyun,
		Path:     child,
		Name:     name,
		IsDir:    o.IsDir,
		Modified: parseModified(o.Modified),
	}
	if !o.IsDir {
		sz := o.Size
		e.Size = &sz
		mt := "application/octet-stream"
		e.MimeType = &mt
	}
	if !o.IsDir && e.Size != nil {
		e.Hashes = parseHashInfo(o.HashInfo)
	}
	if withVersion && !o.IsDir && e.Size != nil {
		best := model.BestHash(e.Hashes)
		var hashStr *string
		if best != nil {
			h := best.Type + ":" + best.Value
			hashStr = &h
		}
		v, verr := model.ComputeVersion(model.SourceAliyun, child, e.Size, e.Modified, nil, hashStr)
		if verr != nil {
			return model.Entry{}, model.E(model.CodeVersionUnavailable, 400, "cannot compute aliyun version")
		}
		e.Version = v
	}
	return e, nil
}

// List fetches one directory page from /api/fs/list.
func (a *Aliyun) List(ctx context.Context, path, cursor string, refresh bool, cs *CursorStore) (model.EntryPage, error) {
	page := 0
	var err error
	if cursor != "" {
		page, err = cs.Get(model.SourceAliyun, path, cursor)
		if err != nil {
			return model.EntryPage{}, err
		}
	}
	if refresh && page == 0 {
		cs.Invalidate(model.SourceAliyun, path)
	}
	env, err := a.callWithAuth(ctx, "/api/fs/list", map[string]any{
		"path": a.upstreamPath(path), "password": "",
		"page": page + 1, "per_page": perPage, "refresh": refresh,
	})
	if err != nil {
		return model.EntryPage{}, err
	}
	var data struct {
		Content  *[]alistObj `json:"content"`
		Total    *int        `json:"total"`
		Provider *string     `json:"provider"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		return model.EntryPage{}, model.E(model.CodeUpstreamError, 502, "alist list data malformed")
	}
	// The version's promised fields must exist; a missing total must not
	// be read as "zero entries".
	if data.Total == nil {
		return model.EntryPage{}, model.E(model.CodeUpstreamError, 502, "alist list missing total")
	}
	total := *data.Total
	var entries []model.Entry
	if data.Content != nil {
		for _, o := range *data.Content {
			e, err := a.toEntry(path, o.Name, o, false)
			if err != nil {
				return model.EntryPage{}, err
			}
			entries = append(entries, e)
		}
	} else if total > (page+1)*perPage && page > 0 {
		return model.EntryPage{}, model.E(model.CodeUpstreamError, 502, "alist list missing content page")
	}
	out := model.EntryPage{Entries: entries}
	if (page+1)*perPage < total {
		next := cs.New(model.SourceAliyun, path, page+1)
		out.NextCursor = &next
	}
	return out, nil
}

// Stat resolves one file with /api/fs/get, refresh=true, and computes the
// content version. raw_url/sign/thumb are never exposed.
func (a *Aliyun) Stat(ctx context.Context, path string) (model.Entry, error) {
	env, err := a.callWithAuth(ctx, "/api/fs/get", map[string]any{
		"path": a.upstreamPath(path), "password": "", "refresh": true,
	})
	if err != nil {
		return model.Entry{}, err
	}
	var o alistObj
	if err := json.Unmarshal(env.Data, &o); err != nil || o.Name == "" {
		return model.Entry{}, model.E(model.CodeNotFound, 404, "entry not found")
	}
	return a.toEntry(parentOf(path), o.Name, o, true)
}

// Probe checks the configured root exists and is readable.
func (a *Aliyun) Probe(ctx context.Context) error {
	env, err := a.callWithAuth(ctx, "/api/fs/get", map[string]any{
		"path": a.Root, "password": "", "refresh": false,
	})
	if err != nil {
		return err
	}
	var data struct {
		IsDir bool   `json:"is_dir"`
		Name  string `json:"name"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || !data.IsDir {
		return model.E(model.CodeUpstreamError, 502, "alist root is not a directory")
	}
	return nil
}

func parentOf(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return ""
	}
	return path[:i]
}

const perPage = 100
