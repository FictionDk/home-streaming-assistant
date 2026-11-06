package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"home-streaming-assistant/internal/model"
)

// handleStatic serves hashed /assets/* files with immutable caching and
// falls back to index.html (no-store) for SPA routes. Missing assets are
// 404, never the SPA.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, r, model.E(model.CodeMethodNotAllowed, 405, "method not allowed"))
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" || p == "." {
		p = "index.html"
	}
	if strings.HasPrefix(p, "assets/") {
		f, err := s.webFS.Open(p)
		if err != nil {
			writeError(w, r, notFound())
			return
		}
		defer f.Close()
		data, err := fs.ReadFile(s.webFS, p)
		if err != nil {
			writeError(w, r, notFound())
			return
		}
		w.Header().Set("Content-Type", contentType(p))
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.WriteHeader(200)
		_, _ = w.Write(data)
		return
	}
	// SPA fallback: known pages and nested routes refresh to index.
	data, err := fs.ReadFile(s.webFS, "index.html")
	if err != nil {
		writeError(w, r, notFound())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	_, _ = w.Write(data)
}

func contentType(p string) string {
	switch {
	case strings.HasSuffix(p, ".js"), strings.HasSuffix(p, ".mjs"):
		return "text/javascript; charset=utf-8"
	case strings.HasSuffix(p, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(p, ".svg"):
		return "image/svg+xml"
	case strings.HasSuffix(p, ".png"):
		return "image/png"
	case strings.HasSuffix(p, ".woff2"):
		return "font/woff2"
	case strings.HasSuffix(p, ".html"):
		return "text/html; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func notFound() error {
	return errNotFound
}

var errNotFound = model.E(model.CodeNotFound, 404, "asset not found")
