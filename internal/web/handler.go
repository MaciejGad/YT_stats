package web

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yt-stats/internal/auth"
	"yt-stats/internal/stats"
)

// Nazwy i parametry ciasteczek.
const (
	SessionCookie  = "yt_session"
	PreLoginCookie = "yt_login"
	cookiePath     = "/yt/"
	maxFormBytes   = 4 << 10
)

// Ścieżki aplikacji.
const (
	pathPanel  = "/yt/"
	pathLogin  = "/yt/login"
	pathLogout = "/yt/logout"
)

// Deps to zależności handlera.
type Deps struct {
	Files    *Files
	Log      *slog.Logger
	Sessions *auth.Store
	PreLogin *auth.PreLogin
	Auth     *auth.Authenticator
	Limiter  *auth.Limiter
	Stats    StatsSource
}

// StatsSource dostarcza dane do /yt/stat.json. Implementuje go *stats.Cache.
type StatsSource interface {
	Get(ctx context.Context) (*stats.Payload, error)
}

type server struct {
	Deps
}

// NewHandler zwraca główny handler HTTP.
func NewHandler(d Deps) http.Handler {
	s := &server{Deps: d}
	mux := http.NewServeMux()
	// Wzorce GET obsługują też HEAD; inna metoda daje 405 z nagłówkiem Allow.
	mux.HandleFunc("GET /yt/{$}", s.handlePanel)
	mux.HandleFunc("GET /yt/login", s.handleLoginForm)
	mux.HandleFunc("POST /yt/login", s.handleLogin)
	mux.HandleFunc("POST /yt/logout", s.handleLogout)
	mux.HandleFunc("GET /yt/stat.json", s.handleStats)
	for _, name := range StaticNames {
		mux.HandleFunc("GET /yt/"+name, s.handleStatic(name))
	}
	return securityHeaders(mux)
}

// --- dane szablonów ---

type loginData struct {
	CSRF  string
	Error string
}

type panelData struct {
	LogoutCSRF string
	Error      string
}

// --- panel ---

func (s *server) handlePanel(w http.ResponseWriter, r *http.Request) {
	token, ok := s.session(w, r)
	if !ok {
		http.Redirect(w, r, pathLogin, http.StatusSeeOther)
		return
	}
	s.render(w, http.StatusOK, "index.html", panelData{LogoutCSRF: auth.LogoutToken(token)})
}

// --- logowanie ---

func (s *server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.session(w, r); ok {
		http.Redirect(w, r, pathPanel, http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, http.StatusOK, "")
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	ip := clientIP(r)

	if ok, wait := s.Limiter.Allow(ip); !ok {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		s.Log.Warn("logowanie zablokowane limitem prób", "ip", ip, "retry_after_s", secs)
		s.renderLogin(w, r, http.StatusTooManyRequests,
			fmt.Sprintf("Zbyt wiele nieudanych prób. Spróbuj ponownie za %s.", humanMinutes(wait)))
		return
	}

	preID := cookieValue(r, PreLoginCookie)
	if !s.PreLogin.Check(preID, r.PostFormValue("csrf")) {
		s.Log.Warn("odrzucono formularz logowania: niepoprawny token CSRF", "ip", ip)
		s.renderLogin(w, r, http.StatusForbidden, "Formularz wygasł. Spróbuj zalogować się ponownie.")
		return
	}

	ok, err := s.Auth.Check(r.Context(), r.PostFormValue("username"), r.PostFormValue("password"))
	switch {
	case errors.Is(err, auth.ErrBusy):
		s.Log.Warn("logowanie odrzucone: zbyt wiele równoległych weryfikacji", "ip", ip)
		w.Header().Set("Retry-After", "5")
		s.renderLogin(w, r, http.StatusServiceUnavailable, "Serwer jest chwilowo zajęty. Spróbuj ponownie za chwilę.")
		return
	case err != nil:
		return // klient przerwał żądanie
	case !ok:
		s.Limiter.Failure(ip)
		s.Log.Warn("nieudane logowanie", "ip", ip)
		s.renderLogin(w, r, http.StatusUnauthorized, "Nieprawidłowa nazwa użytkownika lub hasło.")
		return
	}

	s.Limiter.Success(ip)
	persistent := r.PostFormValue("remember") != ""
	token, sess, err := s.Sessions.Create(persistent)
	if err != nil {
		s.Log.Error("nie udało się zapisać sesji", "err", err)
		s.renderLogin(w, r, http.StatusInternalServerError, "Nie udało się zapisać sesji. Spróbuj ponownie.")
		return
	}
	s.PreLogin.Delete(preID)
	clearCookie(w, PreLoginCookie)
	setSessionCookie(w, token, sess)
	s.Log.Info("zalogowano", "ip", ip, "persistent", persistent)
	http.Redirect(w, r, pathPanel, http.StatusSeeOther)
}

// --- wylogowanie ---

func (s *server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !s.parseForm(w, r) {
		return
	}
	token, ok := s.session(w, r)
	if !ok {
		http.Redirect(w, r, pathLogin, http.StatusSeeOther)
		return
	}
	if !auth.CheckLogoutToken(token, r.PostFormValue("csrf")) {
		s.Log.Warn("odrzucono wylogowanie: niepoprawny token CSRF", "ip", clientIP(r))
		s.render(w, http.StatusForbidden, "index.html", panelData{
			LogoutCSRF: auth.LogoutToken(token),
			Error:      "Nie udało się potwierdzić wylogowania. Spróbuj ponownie.",
		})
		return
	}
	if err := s.Sessions.Delete(token); err != nil {
		s.Log.Error("nie udało się zapisać wylogowania", "err", err)
		s.render(w, http.StatusInternalServerError, "index.html", panelData{
			LogoutCSRF: auth.LogoutToken(token),
			Error:      "Nie udało się wylogować. Spróbuj ponownie.",
		})
		return
	}
	clearCookie(w, SessionCookie)
	s.Log.Info("wylogowano", "ip", clientIP(r))
	http.Redirect(w, r, pathLogin, http.StatusSeeOther)
}

// --- pomocnicze ---

// session zwraca identyfikator ważnej sesji z ciasteczka. Nieważne
// ciasteczko jest usuwane z przeglądarki.
func (s *server) session(w http.ResponseWriter, r *http.Request) (string, bool) {
	token := cookieValue(r, SessionCookie)
	if token == "" {
		return "", false
	}
	if _, ok := s.Sessions.Lookup(token); !ok {
		clearCookie(w, SessionCookie)
		return "", false
	}
	return token, true
}

// parseForm ogranicza rozmiar formularza do 4 KiB.
func (s *server) parseForm(w http.ResponseWriter, r *http.Request) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxFormBytes)
	if err := r.ParseForm(); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "Formularz jest za duży.", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "Niepoprawny formularz.", http.StatusBadRequest)
		return false
	}
	return true
}

func (s *server) renderLogin(w http.ResponseWriter, r *http.Request, status int, msg string) {
	id, csrf, created, err := s.PreLogin.Ensure(cookieValue(r, PreLoginCookie))
	if err != nil {
		s.Log.Error("sesja przed logowaniem", "err", err)
		http.Error(w, "Błąd serwera.", http.StatusInternalServerError)
		return
	}
	if created {
		http.SetCookie(w, &http.Cookie{
			Name:     PreLoginCookie,
			Value:    id,
			Path:     cookiePath,
			MaxAge:   int(s.PreLogin.TTL().Seconds()),
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}
	s.render(w, status, "login.html", loginData{CSRF: csrf, Error: msg})
}

// render wykonuje szablon do bufora, żeby błąd szablonu dał czyste 500.
func (s *server) render(w http.ResponseWriter, status int, name string, data any) {
	tmpl, err := s.Files.Template(name)
	if err != nil {
		s.Log.Error("brak szablonu", "file", name, "err", err)
		http.Error(w, "Błąd serwera.", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		s.Log.Error("błąd wykonania szablonu", "file", name, "err", err)
		http.Error(w, "Błąd serwera.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func setSessionCookie(w http.ResponseWriter, token string, sess auth.Session) {
	c := &http.Cookie{
		Name:     SessionCookie,
		Value:    token,
		Path:     cookiePath,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if sess.Persistent {
		// Ten sam termin co sesja na serwerze, liczony od logowania.
		c.Expires = sess.Expires.UTC()
		c.MaxAge = int(sess.Expires.Sub(sess.Created).Seconds())
	}
	http.SetCookie(w, c)
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     cookiePath,
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// clientIP zwraca adres klienta. X-Real-IP (ustawiany przez Nginx) jest
// brany pod uwagę tylko dla połączeń z loopbacka.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if x := strings.TrimSpace(r.Header.Get("X-Real-IP")); net.ParseIP(x) != nil {
			return x
		}
	}
	return host
}

// humanMinutes zaokrągla czas oczekiwania w górę do pełnych minut.
func humanMinutes(d time.Duration) string {
	m := int(math.Ceil(d.Minutes()))
	switch {
	case m <= 1:
		return "1 minutę"
	case m%10 >= 2 && m%10 <= 4 && (m%100 < 12 || m%100 > 14):
		return fmt.Sprintf("%d minuty", m)
	default:
		return fmt.Sprintf("%d minut", m)
	}
}
