package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"yt-stats/internal/auth"
)

const (
	testUser = "yarn"
	testPass = "poprawne hasło"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type app struct {
	t        *testing.T
	handler  http.Handler
	clock    *testClock
	dir      string // katalog pliku sesji
	sessions *auth.Store
	logs     *syncBuffer
	deps     Deps
	stats    *fakeStats
}

var fastParams = auth.Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func newApp(t *testing.T, params auth.Params, ao auth.AuthOptions) *app {
	t.Helper()
	clk := &testClock{t: time.Date(2026, 11, 23, 20, 0, 0, 0, time.UTC)}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))

	files, err := LoadFiles("../../web", log)
	if err != nil {
		t.Fatal(err)
	}
	hs, err := auth.HashPassword([]byte(testPass), params)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := auth.ParseHash(hs)
	a, err := auth.NewAuthenticator(testUser, h, ao)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := auth.OpenStore(filepath.Join(dir, "sessions.json"), auth.StoreOptions{Now: clk.Now, Log: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close(); os.Chmod(dir, 0o700) })

	fs := &fakeStats{}
	d := Deps{
		Stats:    fs,
		Files:    files,
		Log:      log,
		Sessions: store,
		PreLogin: auth.NewPreLogin(0, 0, clk.Now),
		Auth:     a,
		Limiter:  auth.NewLimiter(auth.LimiterOptions{Now: clk.Now}),
	}
	return &app{t: t, handler: NewHandler(d), clock: clk, dir: dir, sessions: store, logs: logs, deps: d, stats: fs}
}

// restart symuluje restart procesu: nowy magazyn z tego samego pliku.
func (a *app) restart() {
	a.t.Helper()
	a.sessions.Close()
	store, err := auth.OpenStore(filepath.Join(a.dir, "sessions.json"), auth.StoreOptions{Now: a.clock.Now})
	if err != nil {
		a.t.Fatal(err)
	}
	a.t.Cleanup(func() { store.Close() })
	a.sessions = store
	a.deps.Sessions = store
	a.deps.PreLogin = auth.NewPreLogin(0, 0, a.clock.Now)
	a.handler = NewHandler(a.deps)
}

// browser przechowuje ciasteczka jak przeglądarka (z uwzględnieniem usuwania).
type browser struct {
	app     *app
	cookies map[string]*http.Cookie
	remote  string
	headers map[string]string
}

func (a *app) browser() *browser {
	return &browser{app: a, cookies: map[string]*http.Cookie{}, remote: "203.0.113.7:51000", headers: map[string]string{}}
}

func (b *browser) do(method, path string, form url.Values) *httptest.ResponseRecorder {
	b.app.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, "https://maciejgad.pl"+path, body)
	req.RemoteAddr = b.remote
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range b.headers {
		req.Header.Set(k, v)
	}
	for _, c := range b.cookies {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	rec := httptest.NewRecorder()
	b.app.handler.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	return rec
}

var csrfRe = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func csrfFrom(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	m := csrfRe.FindStringSubmatch(rec.Body.String())
	if m == nil {
		t.Fatalf("brak tokenu CSRF w odpowiedzi:\n%s", rec.Body.String())
	}
	return m[1]
}

// login otwiera formularz i wysyła dane.
func (b *browser) login(user, pass string, remember bool) *httptest.ResponseRecorder {
	b.app.t.Helper()
	form := url.Values{"csrf": {csrfFrom(b.app.t, b.do("GET", "/yt/login", nil))}, "username": {user}, "password": {pass}}
	if remember {
		form.Set("remember", "1")
	}
	return b.do("POST", "/yt/login", form)
}

func expectRedirect(t *testing.T, rec *httptest.ResponseRecorder, to string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != to {
		t.Fatalf("oczekiwano 303 → %s, jest %d → %q\n%s", to, rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
}

func setCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("brak Set-Cookie %s", name)
	return nil
}

func checkCookieAttrs(t *testing.T, c *http.Cookie) {
	t.Helper()
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/yt/" || c.Domain != "" {
		t.Errorf("atrybuty ciasteczka %s: Secure=%v HttpOnly=%v SameSite=%v Path=%q Domain=%q",
			c.Name, c.Secure, c.HttpOnly, c.SameSite, c.Path, c.Domain)
	}
}

// --- dostęp bez sesji ---

func TestPanelWithoutSessionRedirects(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	expectRedirect(t, a.browser().do("GET", "/yt/", nil), "/yt/login")
}

func TestInvalidSessionCookieCleared(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	b.cookies[SessionCookie] = &http.Cookie{Name: SessionCookie, Value: strings.Repeat("x", 43)}
	rec := b.do("GET", "/yt/", nil)
	expectRedirect(t, rec, "/yt/login")
	if c := setCookie(t, rec, SessionCookie); c.MaxAge >= 0 {
		t.Error("nieważne ciasteczko powinno zostać usunięte")
	}
}

func TestLoginForm(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	rec := a.browser().do("GET", "/yt/login", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("brak Cache-Control: no-store")
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Errorf("Content-Type %q", rec.Header().Get("Content-Type"))
	}
	c := setCookie(t, rec, PreLoginCookie)
	checkCookieAttrs(t, c)
	if c.MaxAge != 30*60 {
		t.Errorf("MaxAge sesji przed logowaniem %d", c.MaxAge)
	}
	csrfFrom(t, rec)
}

func TestLoginFormReusesPreSession(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	first := csrfFrom(t, b.do("GET", "/yt/login", nil))
	rec := b.do("GET", "/yt/login", nil)
	if csrfFrom(t, rec) != first {
		t.Error("odświeżenie formularza powinno zachować token")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("ponowne ustawienie ciasteczka bez potrzeby")
	}
}

// --- logowanie ---

func TestLoginBrowserSession(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	preID := csrfFrom(t, b.do("GET", "/yt/login", nil))
	_ = preID
	preCookie := b.cookies[PreLoginCookie].Value

	rec := b.login(testUser, testPass, false)
	expectRedirect(t, rec, "/yt/")
	c := setCookie(t, rec, SessionCookie)
	checkCookieAttrs(t, c)
	if c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Errorf("sesja bez „Zapamiętaj” powinna mieć ciasteczko sesyjne: MaxAge=%d Expires=%v", c.MaxAge, c.Expires)
	}
	if c.Value == preCookie {
		t.Error("identyfikator sesji powinien być nowy, nie ten sprzed logowania")
	}
	if _, ok := b.cookies[PreLoginCookie]; ok {
		t.Error("ciasteczko sesji przed logowaniem powinno zostać usunięte")
	}

	panel := b.do("GET", "/yt/", nil)
	if panel.Code != 200 || !strings.Contains(panel.Body.String(), "Wyloguj") {
		t.Fatalf("panel po zalogowaniu: %d", panel.Code)
	}
	if panel.Header().Get("Cache-Control") != "no-store" {
		t.Error("panel bez Cache-Control: no-store")
	}

	a.clock.Add(12 * time.Hour)
	expectRedirect(t, b.do("GET", "/yt/", nil), "/yt/login")
}

func TestLoginRememberMe(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	rec := b.login(testUser, testPass, true)
	expectRedirect(t, rec, "/yt/")
	c := setCookie(t, rec, SessionCookie)
	checkCookieAttrs(t, c)
	if c.MaxAge != 90*24*3600 {
		t.Errorf("MaxAge = %d, oczekiwano %d", c.MaxAge, 90*24*3600)
	}
	if want := a.clock.Now().Add(90 * 24 * time.Hour); !c.Expires.Equal(want.Truncate(time.Second)) {
		t.Errorf("Expires = %v, oczekiwano %v", c.Expires, want)
	}

	a.clock.Add(89*24*time.Hour + 23*time.Hour)
	if rec := b.do("GET", "/yt/", nil); rec.Code != 200 {
		t.Errorf("po 89 dniach 23 h: %d", rec.Code)
	}
	a.clock.Add(time.Hour)
	expectRedirect(t, b.do("GET", "/yt/", nil), "/yt/login")
}

func TestSessionSurvivesRestart(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	b.login(testUser, testPass, true)
	a.restart()
	if rec := b.do("GET", "/yt/", nil); rec.Code != 200 {
		t.Fatalf("po restarcie: %d", rec.Code)
	}
}

func TestLoginFormWithSessionRedirectsToPanel(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	b.login(testUser, testPass, false)
	expectRedirect(t, b.do("GET", "/yt/login", nil), "/yt/")
}

func TestWrongCredentialsSameMessage(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	badPass := a.browser().login(testUser, "złe hasło", false)
	badUser := a.browser().login("ktoś", testPass, false)
	for name, rec := range map[string]*httptest.ResponseRecorder{"złe hasło": badPass, "zła nazwa": badUser} {
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d", name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Nieprawidłowa nazwa użytkownika lub hasło.") {
			t.Errorf("%s: brak wspólnego komunikatu", name)
		}
		if _, ok := setCookieOpt(rec, SessionCookie); ok {
			t.Errorf("%s: ustawiono ciasteczko sesji", name)
		}
	}
	strip := func(s string) string { return csrfRe.ReplaceAllString(s, "") }
	if strip(badPass.Body.String()) != strip(badUser.Body.String()) {
		t.Error("odpowiedzi dla złej nazwy i złego hasła się różnią")
	}
}

func setCookieOpt(rec *httptest.ResponseRecorder, name string) (*http.Cookie, bool) {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c, true
		}
	}
	return nil, false
}

func TestRetryAfterWrongPasswordKeepsForm(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	rec := b.login(testUser, "złe", false)
	form := url.Values{"csrf": {csrfFrom(t, rec)}, "username": {testUser}, "password": {testPass}}
	expectRedirect(t, b.do("POST", "/yt/login", form), "/yt/")
}

// --- CSRF ---

func TestLoginCSRFRejected(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	other := a.browser()
	otherCSRF := csrfFrom(t, other.do("GET", "/yt/login", nil))

	cases := map[string]func(b *browser) url.Values{
		"brak tokenu": func(b *browser) url.Values {
			b.do("GET", "/yt/login", nil)
			return url.Values{"username": {testUser}, "password": {testPass}}
		},
		"zły token": func(b *browser) url.Values {
			b.do("GET", "/yt/login", nil)
			return url.Values{"csrf": {strings.Repeat("a", 43)}, "username": {testUser}, "password": {testPass}}
		},
		"token innej przeglądarki": func(b *browser) url.Values {
			b.do("GET", "/yt/login", nil)
			return url.Values{"csrf": {otherCSRF}, "username": {testUser}, "password": {testPass}}
		},
		"brak ciasteczka": func(b *browser) url.Values {
			return url.Values{"csrf": {otherCSRF}, "username": {testUser}, "password": {testPass}}
		},
		"wygasła sesja przed logowaniem": func(b *browser) url.Values {
			csrf := csrfFrom(t, b.do("GET", "/yt/login", nil))
			a.clock.Add(31 * time.Minute)
			return url.Values{"csrf": {csrf}, "username": {testUser}, "password": {testPass}}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			b := a.browser()
			rec := b.do("POST", "/yt/login", mk(b))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status %d, oczekiwano 403", rec.Code)
			}
			if _, ok := setCookieOpt(rec, SessionCookie); ok {
				t.Error("ustawiono ciasteczko sesji")
			}
			// Odpowiedź daje nowy, działający formularz.
			form := url.Values{"csrf": {csrfFrom(t, rec)}, "username": {testUser}, "password": {testPass}}
			expectRedirect(t, b.do("POST", "/yt/login", form), "/yt/")
		})
	}
}

func TestCSRFFailureDoesNotCountAsAttempt(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	for range 10 {
		b.do("POST", "/yt/login", url.Values{"username": {testUser}, "password": {"x"}})
	}
	expectRedirect(t, b.login(testUser, testPass, false), "/yt/")
}

// --- limity ---

func TestLoginRateLimit(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	for i := range 5 {
		if rec := b.login(testUser, "złe", false); rec.Code != http.StatusUnauthorized {
			t.Fatalf("próba %d: %d", i+1, rec.Code)
		}
	}
	rec := b.login(testUser, testPass, false) // nawet poprawne hasło
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("6. próba: %d, oczekiwano 429", rec.Code)
	}
	if rec.Header().Get("Retry-After") != "900" {
		t.Errorf("Retry-After = %q, oczekiwano 900", rec.Header().Get("Retry-After"))
	}
	if !strings.Contains(rec.Body.String(), "15 minut") {
		t.Errorf("komunikat bez czasu oczekiwania:\n%s", rec.Body.String())
	}

	// Inny adres może się logować.
	other := a.browser()
	other.remote = "198.51.100.1:1234"
	expectRedirect(t, other.login(testUser, testPass, false), "/yt/")

	a.clock.Add(15 * time.Minute)
	expectRedirect(t, b.login(testUser, testPass, false), "/yt/")
}

func TestXRealIPTrustedOnlyFromLoopback(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})

	// Za Nginx: połączenie z loopbacka, adres klienta w X-Real-IP.
	attacker := a.browser()
	attacker.remote = "127.0.0.1:40000"
	attacker.headers["X-Real-IP"] = "203.0.113.50"
	for range 5 {
		attacker.login(testUser, "złe", false)
	}
	if rec := attacker.login(testUser, testPass, false); rec.Code != http.StatusTooManyRequests {
		t.Errorf("adres z X-Real-IP powinien być zablokowany: %d", rec.Code)
	}

	owner := a.browser()
	owner.remote = "127.0.0.1:40001"
	owner.headers["X-Real-IP"] = "198.51.100.9"
	expectRedirect(t, owner.login(testUser, testPass, false), "/yt/")

	// Połączenie spoza loopbacka nie może podać innego adresu.
	spoof := a.browser()
	spoof.remote = "203.0.113.50:5555"
	spoof.headers["X-Real-IP"] = "198.51.100.200"
	if got := clientIPFor(spoof); got != "203.0.113.50" {
		t.Errorf("clientIP = %s", got)
	}
}

func clientIPFor(b *browser) string {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = b.remote
	for k, v := range b.headers {
		r.Header.Set(k, v)
	}
	return clientIP(r)
}

func TestClientIP(t *testing.T) {
	cases := []struct{ remote, header, want string }{
		{"127.0.0.1:1", "203.0.113.1", "203.0.113.1"},
		{"[::1]:1", "2001:db8::1", "2001:db8::1"},
		{"127.0.0.1:1", "", "127.0.0.1"},
		{"127.0.0.1:1", "nie-adres", "127.0.0.1"},
		{"203.0.113.9:1", "1.2.3.4", "203.0.113.9"},
		{"bez-portu", "1.2.3.4", "bez-portu"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.header != "" {
			r.Header.Set("X-Real-IP", c.header)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("clientIP(%s, %q) = %s, oczekiwano %s", c.remote, c.header, got, c.want)
		}
	}
}

func TestFormTooLarge(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	csrf := csrfFrom(t, b.do("GET", "/yt/login", nil))
	rec := b.do("POST", "/yt/login", url.Values{"csrf": {csrf}, "username": {testUser}, "password": {strings.Repeat("a", 5000)}})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, oczekiwano 413", rec.Code)
	}
}

func TestLongCredentialsRejected(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	rec := a.browser().login(testUser, strings.Repeat("a", 257), false)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, oczekiwano 401", rec.Code)
	}
}

func TestLoginBusy(t *testing.T) {
	// Wolny hash (ok. 0,5–1 s) i jedno miejsce na weryfikację.
	slow := auth.Params{Memory: 32 * 1024, Time: 8, Threads: 1, SaltLen: 16, KeyLen: 32}
	a := newApp(t, slow, auth.AuthOptions{MaxConcurrent: 1, WaitTimeout: 10 * time.Millisecond})

	first := a.browser()
	csrf1 := csrfFrom(t, first.do("GET", "/yt/login", nil))
	second := a.browser()
	second.remote = "198.51.100.2:1"
	csrf2 := csrfFrom(t, second.do("GET", "/yt/login", nil))

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- first.do("POST", "/yt/login", url.Values{"csrf": {csrf1}, "username": {testUser}, "password": {testPass}})
	}()
	time.Sleep(50 * time.Millisecond)
	rec := second.do("POST", "/yt/login", url.Values{"csrf": {csrf2}, "username": {testUser}, "password": {testPass}})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, oczekiwano 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("brak Retry-After")
	}
	expectRedirect(t, <-done, "/yt/")
}

func TestLoginCancelledRequest(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	csrf := csrfFrom(t, b.do("GET", "/yt/login", nil))
	form := url.Values{"csrf": {csrf}, "username": {testUser}, "password": {testPass}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/yt/login", strings.NewReader(form.Encode())).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: PreLoginCookie, Value: b.cookies[PreLoginCookie].Value})
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, req)
	if _, ok := setCookieOpt(rec, SessionCookie); ok {
		t.Error("przerwane żądanie nie powinno tworzyć sesji")
	}
}

// --- błędy pliku sesji ---

func TestLoginStoreWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root omija uprawnienia")
	}
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	csrf := csrfFrom(t, b.do("GET", "/yt/login", nil))
	os.Chmod(a.dir, 0o500)
	rec := b.do("POST", "/yt/login", url.Values{"csrf": {csrf}, "username": {testUser}, "password": {testPass}})
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "Nie udało się zapisać sesji") {
		t.Errorf("status %d:\n%s", rec.Code, rec.Body.String())
	}
	if _, ok := setCookieOpt(rec, SessionCookie); ok {
		t.Error("ustawiono ciasteczko mimo błędu zapisu")
	}
	os.Chmod(a.dir, 0o700)
	form := url.Values{"csrf": {csrfFrom(t, rec)}, "username": {testUser}, "password": {testPass}}
	expectRedirect(t, b.do("POST", "/yt/login", form), "/yt/")
}

// --- wylogowanie ---

func logoutForm(t *testing.T, b *browser) url.Values {
	t.Helper()
	return url.Values{"csrf": {csrfFrom(t, b.do("GET", "/yt/", nil))}}
}

func TestLogout(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	b.login(testUser, testPass, true)
	token := b.cookies[SessionCookie].Value

	rec := b.do("POST", "/yt/logout", logoutForm(t, b))
	expectRedirect(t, rec, "/yt/login")
	c := setCookie(t, rec, SessionCookie)
	if c.MaxAge >= 0 || c.Path != "/yt/" {
		t.Errorf("ciasteczko nie zostało usunięte z tą samą ścieżką: MaxAge=%d Path=%q", c.MaxAge, c.Path)
	}
	if _, ok := a.sessions.Lookup(token); ok {
		t.Error("sesja nadal ważna po wylogowaniu")
	}

	// Stare ciasteczko (np. z kopii) już nie działa.
	b.cookies[SessionCookie] = &http.Cookie{Name: SessionCookie, Value: token}
	expectRedirect(t, b.do("GET", "/yt/", nil), "/yt/login")
}

func TestLogoutCSRF(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	b.login(testUser, testPass, false)

	other := a.browser()
	other.login(testUser, testPass, false)
	otherCSRF := logoutForm(t, other).Get("csrf")

	for name, form := range map[string]url.Values{
		"brak tokenu":       {},
		"zły token":         {"csrf": {"x"}},
		"token innej sesji": {"csrf": {otherCSRF}},
	} {
		rec := b.do("POST", "/yt/logout", form)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d, oczekiwano 403", name, rec.Code)
		}
	}
	if rec := b.do("GET", "/yt/", nil); rec.Code != 200 {
		t.Error("sesja powinna pozostać ważna po odrzuconym wylogowaniu")
	}
}

func TestLogoutWithoutSession(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	expectRedirect(t, a.browser().do("POST", "/yt/logout", url.Values{"csrf": {"x"}}), "/yt/login")
}

func TestLogoutWriteFailureAllowsRetry(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root omija uprawnienia")
	}
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	b.login(testUser, testPass, true)
	form := logoutForm(t, b)

	os.Chmod(a.dir, 0o500)
	rec := b.do("POST", "/yt/logout", form)
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "Nie udało się wylogować") {
		t.Fatalf("status %d:\n%s", rec.Code, rec.Body.String())
	}
	if _, ok := b.cookies[SessionCookie]; !ok {
		t.Fatal("ciasteczko usunięte mimo błędu")
	}
	if !strings.Contains(rec.Body.String(), `action="/yt/logout"`) {
		t.Error("strona błędu powinna pozwalać ponowić wylogowanie")
	}

	os.Chmod(a.dir, 0o700)
	expectRedirect(t, b.do("POST", "/yt/logout", url.Values{"csrf": {csrfFrom(t, rec)}}), "/yt/login")
}

// --- trasy ---

func TestRoutes(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()

	if rec := b.do("HEAD", "/yt/login", nil); rec.Code != 200 {
		t.Errorf("HEAD /yt/login: %d", rec.Code)
	}
	rec := b.do("PUT", "/yt/login", nil)
	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Header().Get("Allow"), "POST") {
		t.Errorf("PUT /yt/login: %d Allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := b.do("GET", "/yt/logout", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /yt/logout: %d", rec.Code)
	}
	for _, p := range []string{"/yt/nieznane", "/yt/index.php", "/yt/stat.php", "/"} {
		if rec := b.do("GET", p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, oczekiwano 404", p, rec.Code)
		}
	}
	// /yt bez ukośnika kieruje na /yt/ (Nginx robi to samo przed proxy).
	if rec := b.do("GET", "/yt", nil); rec.Header().Get("Location") != "/yt/" {
		t.Errorf("GET /yt: %d → %q", rec.Code, rec.Header().Get("Location"))
	}
}

// --- logi ---

func TestLogsContainNoSecrets(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	b.login(testUser, "złe-hasło-123", false)
	b.login(testUser, testPass, true)
	token := b.cookies[SessionCookie].Value
	b.do("POST", "/yt/logout", logoutForm(t, b))

	logs := a.logs.String()
	for _, secret := range []string{token, testPass, "złe-hasło-123", auth.LogoutToken(token)} {
		if strings.Contains(logs, secret) {
			t.Errorf("logi zawierają sekret %q:\n%s", secret, logs)
		}
	}
	for _, want := range []string{"nieudane logowanie", "zalogowano", "wylogowano"} {
		if !strings.Contains(logs, want) {
			t.Errorf("brak w logach %q", want)
		}
	}
}

func TestHumanMinutes(t *testing.T) {
	for d, want := range map[time.Duration]string{
		10 * time.Second: "1 minutę",
		90 * time.Second: "2 minuty",
		5 * time.Minute:  "5 minut",
		12 * time.Minute: "12 minut",
		15 * time.Minute: "15 minut",
		22 * time.Minute: "22 minuty",
		59 * time.Minute: "59 minut",
	} {
		if got := humanMinutes(d); got != want {
			t.Errorf("humanMinutes(%v) = %q, oczekiwano %q", d, got, want)
		}
	}
}
