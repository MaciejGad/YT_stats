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

// contentSecurityPolicy pozwala tylko na zasoby z własnego adresu. Strona
// nie ma skryptów ani stylów inline.
const contentSecurityPolicy = "default-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// securityHeaders dodaje nagłówki bezpieczeństwa do każdej odpowiedzi,
// także 404 i 405.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// handleStats zwraca statystyki jako JSON. Bez sesji zwraca 401 i nie
// odpytuje YouTube.
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
		// Klient zamknął połączenie; pobranie trwa dalej w tle.
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

// staticTypes to typy MIME plików statycznych.
var staticTypes = map[string]string{
	".js":  "text/javascript; charset=utf-8",
	".css": "text/css; charset=utf-8",
	".svg": "image/svg+xml",
	".png": "image/png",
}

// handleStatic serwuje plik z web_dir. Przeglądarka przy każdym użyciu
// pyta o zmiany (no-cache) i dostaje 304, gdy ETag się zgadza. Dzięki temu
// zmiana pliku na serwerze jest widoczna po zwykłym przeładowaniu strony.
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
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", f.ETag)
		http.ServeContent(w, r, name, f.ModTime, bytes.NewReader(f.Content))
	}
}
