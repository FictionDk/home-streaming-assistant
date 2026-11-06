// Package api wires the HTTP surface: authentication, CSRF, business
// handlers, JSON errors and SPA static serving.
package api

import (
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"home-streaming-assistant/internal/catalog"
	"home-streaming-assistant/internal/config"
	"home-streaming-assistant/internal/downloads"
	"home-streaming-assistant/internal/jellyfin"
	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/schedule"
	"home-streaming-assistant/internal/storage"
)

// Server is the HTTP application.
type Server struct {
	cfg     *config.Config
	store   *storage.Store
	cat     *catalog.Manager
	dl      *downloads.Manager
	sched   *schedule.Runner
	jelly   *jellyfin.Client
	indexer *jellyfin.Indexer

	adminHash []byte // SHA-256 of the admin password
	webFS     fs.FS
	mux       *http.ServeMux
	fileLocks sync.Map // library id -> *sync.Mutex
	ready     atomic.Bool
}

// New assembles the server. jelly/indexer may be nil when Jellyfin is
// not configured.
func New(cfg *config.Config, store *storage.Store, cat *catalog.Manager,
	dl *downloads.Manager, sched *schedule.Runner,
	jelly *jellyfin.Client, indexer *jellyfin.Indexer,
	adminPassword string, webFS fs.FS) *Server {

	sum := sha256.Sum256([]byte(adminPassword))
	s := &Server{
		cfg: cfg, store: store, cat: cat, dl: dl, sched: sched,
		jelly: jelly, indexer: indexer,
		adminHash: sum[:], webFS: webFS,
		mux: http.NewServeMux(),
	}
	s.routes()
	return s
}

// SetReady flips the health gate once all components are running.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /healthz", s.handleHealthz)
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("GET /api/auth/session", s.handleSession)
	m.HandleFunc("POST /api/auth/logout", s.handleLogout)
	m.HandleFunc("GET /api/sources", s.handleSources)
	m.HandleFunc("GET /api/entries", s.handleEntries)
	m.HandleFunc("GET /api/entry", s.handleEntry)
	m.HandleFunc("POST /api/downloads", s.handleCreateDownload)
	m.HandleFunc("GET /api/downloads", s.handleListDownloads)
	m.HandleFunc("GET /api/downloads/{id}", s.handleGetDownload)
	m.HandleFunc("POST /api/downloads/{id}/cancel", s.handleCancelDownload)
	m.HandleFunc("POST /api/downloads/{id}/retry", s.handleRetryDownload)
	m.HandleFunc("GET /api/library", s.handleListLibrary)
	m.HandleFunc("DELETE /api/library/{id}", s.handleDeleteLibrary)
	m.HandleFunc("GET /api/schedules/gdrive", s.handleGetSchedule)
	m.HandleFunc("PUT /api/schedules/gdrive", s.handlePutSchedule)
	// Unknown /api/* is a JSON 404, never the SPA.
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, r, model.E(model.CodeNotFound, 404, "unknown API path"))
	})
	// Static: /assets/* hashed files, everything else the SPA index.
	m.HandleFunc("/", s.handleStatic)
}

// Handler returns the fully wrapped handler.
func (s *Server) Handler() http.Handler {
	return s.withCommon(s.mux)
}

// withCommon applies body limits, trusted-Host enforcement and auth.
func (s *Server) withCommon(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if !s.trustedHost(r.Host) {
			writeError(w, r, model.E(model.CodeForbidden, 403, "untrusted host"))
			return
		}
		if !s.ready.Load() {
			writeError(w, r, model.E(model.CodeInternal, 503, "starting up"))
			return
		}
		// API requests: JSON 401 for unauthenticated business calls;
		// CSRF for writes. /healthz and static bypass auth.
		if strings.HasPrefix(r.URL.Path, "/api/") &&
			r.URL.Path != "/api/auth/login" {
			sess := s.sessionOf(r)
			if sess == nil {
				writeError(w, r, model.E(model.CodeUnauthorized, 401, "not signed in"))
				return
			}
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
			default:
				tok := r.Header.Get("X-CSRF-Token")
				if tok == "" || tok != sess.CSRFToken {
					writeError(w, r, model.E(model.CodeForbidden, 403, "missing or invalid CSRF token"))
					return
				}
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		next.ServeHTTP(w, r)
	})
}

// trustedHost requires the exact Host of one configured origin. This
// blocks attacker-chosen Host values from constructing playback URLs.
func (s *Server) trustedHost(host string) bool {
	for _, o := range s.cfg.Origins {
		i := strings.Index(o, "://")
		if i >= 0 && o[i+3:] == host {
			return true
		}
	}
	return false
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, r, 503, map[string]any{"status": "starting"})
		return
	}
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, r, 503, map[string]any{"status": "db_unavailable"})
		return
	}
	writeJSON(w, r, 200, map[string]string{"status": "ok"})
}

// ---- JSON helpers ----

func writeJSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	code := model.ErrCode(err)
	writeJSON(w, r, model.HTTPStatus(code), map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": model.Messages[code],
		},
	})
}

func decodeJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return model.E(model.CodeInvalidRequest, 400, "invalid JSON body")
	}
	return nil
}
