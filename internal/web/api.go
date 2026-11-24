package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path"

	"yt-stats/internal/stats"
)

// contentSecurityPolicy allows only resources from our own origin. The page
// has no inline scripts or styles.
const contentSecurityPolicy = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// securityHeaders adds security headers to every response,
// including 404 and 405.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// handleStats returns the statistics as JSON. Without a session it returns 401
// and does not query YouTube.
func (s *server) handleStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(w, r); !ok {
		writeJSON(w, http.StatusUnauthorized, errorBody{"Brak ważnej sesji. Zaloguj się ponownie."})
		return
	}
	p, err := s.Stats.Get(r.Context())
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, p)
	case errors.Is(err, stats.ErrUnavailable):
		w.Header().Set("Retry-After", "30")
		writeJSON(w, http.StatusServiceUnavailable, errorBody{"Nie udało się pobrać statystyk z YouTube. Spróbuj ponownie za chwilę."})
	case errors.Is(err, context.Canceled):
		// The client closed the connection; the fetch continues in the background.
	default:
		s.Log.Error("statystyki", "err", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{"Błąd serwera."})
	}
}

type errorBody struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		http.Error(w, `{"error":"Błąd serwera."}`, http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(data)
}

// handleLogo serves the channel avatar downloaded from YouTube. It is public
// like the other static files, because the login page shows it too.
func (s *server) handleLogo(w http.ResponseWriter, r *http.Request) {
	logo, ok := s.Stats.Logo()
	if !ok {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", logo.ContentType)
	h.Set("ETag", `"`+logo.Version+`"`)
	setAssetCache(h, r, logo.Version)
	http.ServeContent(w, r, "", logo.ModTime, bytes.NewReader(logo.Data))
}

// setAssetCache lets a URL carrying the current version be cached forever:
// its content never changes. Any other URL is revalidated on every use.
func setAssetCache(h http.Header, r *http.Request, version string) {
	if v := r.URL.Query().Get("v"); v != "" && v == version {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
}

// staticTypes are the MIME types of static files.
var staticTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
	".svg": "image/svg+xml",
	".png": "image/png",
}

// handleStatic serves a file from web_dir. The browser asks for changes
// on every use (no-cache) and gets 304 when the ETag matches. Thanks to that
// a change to a file on the server is visible after a normal page reload.
func (s *server) handleStatic(name string) http.HandlerFunc {
	ctype := staticTypes[path.Ext(name)]
	return func(w http.ResponseWriter, r *http.Request) {
		f, err := s.Files.Static(name)
		if err != nil {
			s.Log.Error("plik statyczny", "file", name, "err", err)
			http.Error(w, "Błąd serwera.", http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", ctype)
		h.Set("ETag", f.ETag)
		setAssetCache(h, r, f.Version())
		http.ServeContent(w, r, name, f.ModTime, bytes.NewReader(f.Content))
	}
}
