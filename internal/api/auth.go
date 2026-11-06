package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"home-streaming-assistant/internal/model"
	"home-streaming-assistant/internal/storage"
)

const sessionCookie = "hsa_session"

const sessionTTL = 12 * time.Hour

// trustedOrigin checks an Origin header against the exact configured list.
func (s *Server) trustedOrigin(origin string) bool {
	for _, o := range s.cfg.Origins {
		if subtle.ConstantTimeCompare([]byte(o), []byte(origin)) == 1 {
			return true
		}
	}
	return false
}

// sessionOf resolves the current session from the cookie.
func (s *Server) sessionOf(r *http.Request) *storage.SessionRow {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	sum := sha256.Sum256([]byte(c.Value))
	sess, err := s.store.GetSession(r.Context(), hex.EncodeToString(sum[:]))
	if err != nil {
		return nil
	}
	return sess
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, maxAge int) {
	secure := false
	for _, o := range s.cfg.Origins {
		if strings.HasPrefix(o, "https://") {
			secure = true
			break
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
		MaxAge:   maxAge,
	})
}

// handleLogin issues a session after constant-time password check.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Exact Origin match and JSON only.
	if !s.trustedOrigin(r.Header.Get("Origin")) {
		writeError(w, r, model.E(model.CodeForbidden, 403, "untrusted origin"))
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, r, model.E(model.CodeInvalidRequest, 400, "JSON body required"))
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Username != s.cfg.AdminUser {
		// Consume comparable work to blunt username probing.
		subtle.ConstantTimeCompare([]byte(req.Password), []byte("x"))
		writeError(w, r, model.E(model.CodeUnauthorized, 401, "wrong username or password"))
		return
	}
	sum := sha256.Sum256([]byte(req.Password))
	if subtle.ConstantTimeCompare(sum[:], s.adminHash) != 1 {
		writeError(w, r, model.E(model.CodeUnauthorized, 401, "wrong username or password"))
		return
	}
	token := randomToken()
	csrf := randomToken()
	sumTok := sha256.Sum256([]byte(token))
	expires := time.Now().UTC().Add(sessionTTL)
	if err := s.store.CreateSession(r.Context(), hex.EncodeToString(sumTok[:]), csrf, expires); err != nil {
		writeError(w, r, model.E(model.CodeInternal, 500, "cannot create session"))
		return
	}
	s.setSessionCookie(w, r, token, int(sessionTTL.Seconds()))
	writeJSON(w, r, 200, map[string]any{"user": s.cfg.AdminUser, "csrfToken": csrf})
}

// handleSession returns the current session shape or 401.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	sess := s.sessionOf(r)
	if sess == nil {
		writeError(w, r, model.E(model.CodeUnauthorized, 401, "not signed in"))
		return
	}
	writeJSON(w, r, 200, map[string]any{"user": s.cfg.AdminUser, "csrfToken": sess.CSRFToken})
}

// handleLogout deletes the session.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		sum := sha256.Sum256([]byte(c.Value))
		_ = s.store.DeleteSession(r.Context(), hex.EncodeToString(sum[:]))
	}
	s.setSessionCookie(w, r, "", -1)
	w.WriteHeader(http.StatusNoContent)
}
