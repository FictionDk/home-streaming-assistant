package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"home-streaming-assistant/internal/catalog"
	"home-streaming-assistant/internal/config"
	"home-streaming-assistant/internal/downloads"
	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/schedule"
	"home-streaming-assistant/internal/storage"
)

func testConfig(t *testing.T, dir string) (*config.Config, string) {
	t.Helper()
	passFile := filepath.Join(dir, "admin-password")
	if err := os.WriteFile(passFile, []byte("test-password-123"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Listen:            "127.0.0.1:0",
		Origins:           []string{"http://127.0.0.1:18080"},
		AdminUser:         "admin",
		AdminPasswordFile: passFile,
		StateDir:          filepath.Join(dir, "state"),
		FilesDir:          filepath.Join(dir, "files"),
		RcloneBinary:      "/bin/false",
		RcloneConfig:      filepath.Join(dir, "rclone.conf"),
		MinFreeBytes:      1,
	}
	if err := os.MkdirAll(cfg.FilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg, "test-password-123"
}

type newTestServerOut struct {
	server *Server
	cfg    *config.Config
	store  *storage.Store
	ts     *httptest.Server
}

func newTestServer(t *testing.T) newTestServerOut {
	t.Helper()
	dir := t.TempDir()
	cfg, password := testConfig(t, dir)
	store, err := storage.Open(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cat := catalog.NewManager(nil, nil)
	dlm := downloads.New(store, cfg, cat)
	sched := schedule.New(store, dlm)
	webFS := fstest.MapFS{
		"index.html":         {Data: []byte("<html>spa</html>")},
		"assets/app.test.js": {Data: []byte("//js")},
	}
	srv := New(cfg, store, cat, dlm, sched, nil, nil, password, webFS)
	srv.SetReady(true)
	ts := httptest.NewServer(srv.Handler())
	cfg.Origins = []string{ts.URL}
	t.Cleanup(ts.Close)
	return newTestServerOut{srv, cfg, store, ts}
}

func login(t *testing.T, ts *httptest.Server) (csrf string, cookie string) {
	t.Helper()
	body := `{"username":"admin","password":"test-password-123"}`
	req, _ := http.NewRequest("POST", ts.URL+"/api/auth/login", strings.NewReader(body))
	req.Header.Set("Origin", ts.URL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login status %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			cookie = c.Name + "=" + c.Value
		}
	}
	if cookie == "" {
		t.Fatal("no session cookie")
	}
	// Fetch CSRF via session.
	req2, _ := http.NewRequest("GET", ts.URL+"/api/auth/session", nil)
	req2.Header.Set("Cookie", cookie)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp2.Body.Read(buf)
	if !strings.Contains(string(buf[:n]), `"csrfToken"`) {
		t.Fatalf("session response missing csrf: %s", string(buf[:n]))
	}
	i := strings.Index(string(buf[:n]), `"csrfToken":"`)
	rest := string(buf[:n])[i+len(`"csrfToken":"`):]
	csrf = rest[:strings.Index(rest, `"`)]
	return csrf, cookie
}

func TestLoginRequiresExactOrigin(t *testing.T) {
	o := newTestServer(t)
	body := `{"username":"admin","password":"test-password-123"}`
	req, _ := http.NewRequest("POST", o.ts.URL+"/api/auth/login", strings.NewReader(body))
	req.Header.Set("Origin", "http://evil.example")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("wrong origin: got %d want 403", resp.StatusCode)
	}
}

func TestLoginRejectsNonJSON(t *testing.T) {
	o := newTestServer(t)
	req, _ := http.NewRequest("POST", o.ts.URL+"/api/auth/login", strings.NewReader("x=1"))
	req.Header.Set("Origin", o.ts.URL)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("non-json login: got %d want 400", resp.StatusCode)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	o := newTestServer(t)
	body := `{"username":"admin","password":"nope"}`
	req, _ := http.NewRequest("POST", o.ts.URL+"/api/auth/login", strings.NewReader(body))
	req.Header.Set("Origin", o.ts.URL)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("wrong password: got %d want 401", resp.StatusCode)
	}
}

func TestUnauthenticatedBusinessAPIIs401(t *testing.T) {
	o := newTestServer(t)
	for _, p := range []string{"/api/sources", "/api/entries", "/api/downloads", "/api/library", "/api/schedules/gdrive"} {
		resp, err := http.Get(o.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("GET %s: got %d want 401", p, resp.StatusCode)
		}
	}
}

func TestWriteRequiresCSRF(t *testing.T) {
	o := newTestServer(t)
	csrf, cookie := login(t, o.ts)
	_ = csrf
	req, _ := http.NewRequest("POST", o.ts.URL+"/api/downloads", strings.NewReader(`{"source":"aliyun","path":"x"}`))
	req.Header.Set("Cookie", cookie)
	req.Header.Set("X-CSRF-Token", "wrong")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("bad csrf: got %d want 403", resp.StatusCode)
	}
}

func TestUnknownAPIPathJSON404(t *testing.T) {
	o := newTestServer(t)
	csrf, cookie := login(t, o.ts)
	req, _ := http.NewRequest("GET", o.ts.URL+"/api/unknown", nil)
	req.Header.Set("Cookie", cookie)
	_ = csrf
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("unknown api: got %d want 404", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("unknown api content-type %q want json", ct)
	}
}

func TestMissingAssetIs404NotSPA(t *testing.T) {
	o := newTestServer(t)
	resp, err := http.Get(o.ts.URL + "/assets/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("missing asset: got %d want 404", resp.StatusCode)
	}
}

func TestSPAIndexServedForRoutes(t *testing.T) {
	o := newTestServer(t)
	for _, p := range []string{"/", "/library", "/downloads/nonexistent-id", "/settings"} {
		resp, err := http.Get(o.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		resp.Body.Read(buf)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(buf), "spa") {
			t.Fatalf("route %s status=%d body=%q", p, resp.StatusCode, string(buf))
		}
	}
}

func TestHashedAssetCacheHeaders(t *testing.T) {
	o := newTestServer(t)
	resp, err := http.Get(o.ts.URL + "/assets/app.test.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cc := resp.Header.Get("Cache-Control")
	if resp.StatusCode != 200 || cc != "public, max-age=31536000, immutable" {
		t.Fatalf("asset status=%d cache-control=%q", resp.StatusCode, cc)
	}
}

func TestUntrustedHostRejected(t *testing.T) {
	o := newTestServer(t)
	req, _ := http.NewRequest("GET", o.ts.URL+"/healthz", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("untrusted host: got %d want 403", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	o := newTestServer(t)
	resp, err := http.Get(o.ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("healthz: got %d", resp.StatusCode)
	}
}

func TestJobErrorMapping(t *testing.T) {
	if model.HTTPStatus(model.CodeNoSpace) != 409 {
		t.Fatal("NO_SPACE must be 409")
	}
	if model.HTTPStatus(model.CodeNotConfigured) != 503 {
		t.Fatal("NOT_CONFIGURED must be 503")
	}
}
