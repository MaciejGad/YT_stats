package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yt-stats/internal/auth"
	"yt-stats/internal/stats"
)

// fakeStats zastępuje cache statystyk.
type fakeStats struct {
	calls   atomic.Int32
	payload *stats.Payload
	err     error
}

func (f *fakeStats) Get(context.Context) (*stats.Payload, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	if f.payload != nil {
		return f.payload, nil
	}
	views, subs, v1 := "82187", "1380", "4536"
	return &stats.Payload{
		Stats: stats.Stats{SubscriberCount: &subs, ViewsCount: &views},
		Videos: []stats.Video{
			{VideoID: "ho5LfAO7N4Q", Title: "Odcinek 25: jesienne dzierganie", ViewCount: &v1},
			{VideoID: "abc_DEF-123", Title: `<script>alert(1)</script> ☕🧶`},
		},
		UpdateTime: "2026-11-24T00:15:30+01:00",
	}, nil
}

func loggedIn(t *testing.T) (*app, *browser) {
	t.Helper()
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	expectRedirect(t, b.login(testUser, testPass, false), "/yt/")
	return a, b
}

// --- /yt/stat.json ---

func TestStatsWithoutSession(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	rec := a.browser().do("GET", "/yt/stat.json", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, oczekiwano 401", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
		t.Errorf("odpowiedź nie jest JSON z polem error: %s", rec.Body.String())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("Content-Type %q", rec.Header().Get("Content-Type"))
	}
	if a.stats.calls.Load() != 0 {
		t.Error("żądanie bez sesji odpytało źródło statystyk")
	}
}

func TestStatsWithExpiredSession(t *testing.T) {
	a, b := loggedIn(t)
	a.clock.Add(13 * time.Hour)
	if rec := b.do("GET", "/yt/stat.json", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, oczekiwano 401", rec.Code)
	}
	if a.stats.calls.Load() != 0 {
		t.Error("wygasła sesja odpytała źródło statystyk")
	}
}

func TestStatsContract(t *testing.T) {
	_, b := loggedIn(t)
	rec := b.do("GET", "/yt/stat.json", nil)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	h := rec.Header()
	if h.Get("Content-Type") != "application/json; charset=utf-8" || h.Get("Cache-Control") != "no-store" {
		t.Errorf("nagłówki: %v", h)
	}
	var body struct {
		Stats struct {
			SubscriberCount *string `json:"subscriberCount"`
			ViewsCount      *string `json:"viewsCount"`
		} `json:"stats"`
		Videos     []map[string]any `json:"videos"`
		UpdateTime string           `json:"update_time"`
		Stale      *bool            `json:"stale"`
	}
	dec := json.NewDecoder(rec.Body)
	if err := dec.Decode(&body); err != nil {
		t.Fatal(err)
	}
	if *body.Stats.SubscriberCount != "1380" || *body.Stats.ViewsCount != "82187" {
		t.Errorf("stats: %+v", body.Stats)
	}
	if _, err := time.Parse(time.RFC3339, body.UpdateTime); err != nil {
		t.Errorf("update_time %q nie jest RFC 3339", body.UpdateTime)
	}
	if body.Stale == nil || *body.Stale {
		t.Error("pole stale powinno być obecne i równe false")
	}
	if len(body.Videos) != 2 {
		t.Fatalf("videos: %v", body.Videos)
	}
	v0, v1 := body.Videos[0], body.Videos[1]
	if v0["videoId"] != "ho5LfAO7N4Q" || v0["title"] != "Odcinek 25: jesienne dzierganie" || v0["viewCount"] != "4536" {
		t.Errorf("videos[0]: %v", v0)
	}
	if val, ok := v1["viewCount"]; !ok || val != nil {
		t.Errorf("brak licznika powinien być null: %v", v1)
	}
}

func TestStatsStale(t *testing.T) {
	a, b := loggedIn(t)
	p, _ := (&fakeStats{}).Get(context.Background())
	p.Stale = true
	a.stats.payload = p
	rec := b.do("GET", "/yt/stat.json", nil)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"stale":true`) {
		t.Errorf("status %d: %s", rec.Code, rec.Body.String())
	}
}

func TestStatsUnavailable(t *testing.T) {
	a, b := loggedIn(t)
	a.stats.err = stats.ErrUnavailable
	rec := b.do("GET", "/yt/stat.json", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, oczekiwano 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("brak Retry-After")
	}
	var body map[string]string
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] == "" {
		t.Errorf("brak komunikatu: %s", rec.Body.String())
	}
}

func TestStatsInternalErrorHidesDetails(t *testing.T) {
	a, b := loggedIn(t)
	a.stats.err = errors.New("szczegół wewnętrzny: /var/lib/yt-stats FAKEKEYSECRET")
	rec := b.do("GET", "/yt/stat.json", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "wewnętrzny") || strings.Contains(rec.Body.String(), "FAKEKEY") {
		t.Errorf("odpowiedź ujawnia szczegóły: %s", rec.Body.String())
	}
}

func TestStatsClientCancelled(t *testing.T) {
	a, b := loggedIn(t)
	a.stats.err = context.Canceled
	rec := b.do("GET", "/yt/stat.json", nil)
	if strings.Contains(a.logs.String(), "level=ERROR") {
		t.Errorf("przerwanie przez klienta nie powinno być błędem w logu:\n%s", a.logs.String())
	}
	_ = rec
}

func TestStatsMethodNotAllowed(t *testing.T) {
	_, b := loggedIn(t)
	rec := b.do("POST", "/yt/stat.json", nil)
	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Header().Get("Allow"), "GET") {
		t.Errorf("status %d Allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
}

// --- pliki statyczne ---

func TestStaticFiles(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	cases := map[string]string{
		"/yt/script.js":   "text/javascript; charset=utf-8",
		"/yt/style.css":   "text/css; charset=utf-8",
		"/yt/logo.png":    "image/png",
		"/yt/favicon.png": "image/png",
	}
	for path, ctype := range cases {
		rec := b.do("GET", path, nil)
		if rec.Code != 200 {
			t.Errorf("%s: status %d", path, rec.Code)
			continue
		}
		if rec.Header().Get("Content-Type") != ctype {
			t.Errorf("%s: Content-Type %q", path, rec.Header().Get("Content-Type"))
		}
		if rec.Header().Get("ETag") == "" || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: ETag %q Cache-Control %q", path, rec.Header().Get("ETag"), rec.Header().Get("Cache-Control"))
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s: pusta odpowiedź", path)
		}
	}
}

func TestStaticNotModified(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	etag := b.do("GET", "/yt/style.css", nil).Header().Get("ETag")
	b.headers["If-None-Match"] = etag
	rec := b.do("GET", "/yt/style.css", nil)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Errorf("status %d, body %d B, oczekiwano 304", rec.Code, rec.Body.Len())
	}
}

func TestStaticHead(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	rec := a.browser().do("HEAD", "/yt/script.js", nil)
	if rec.Code != 200 || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") == "" {
		t.Errorf("HEAD: %d, body %d B, Content-Length %q", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Length"))
	}
}

func TestStaticReloadWithoutRestart(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	// Kopia web/ w katalogu tymczasowym, żeby móc ją zmieniać.
	dir := t.TempDir()
	for _, n := range append(append([]string{}, TemplateNames...), StaticNames...) {
		data, err := os.ReadFile(filepath.Join("../../web", n))
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, n, string(data), time.Now().Add(-time.Hour))
	}
	files, err := LoadFiles(dir, newLogger(a.logs))
	if err != nil {
		t.Fatal(err)
	}
	a.deps.Files = files
	a.handler = NewHandler(a.deps)
	b := a.browser()

	before := b.do("GET", "/yt/script.js", nil)
	write(t, dir, "script.js", "// nowa wersja", time.Now())
	after := b.do("GET", "/yt/script.js", nil)
	if after.Body.String() != "// nowa wersja" {
		t.Errorf("nowa wersja nie jest serwowana: %q", after.Body.String())
	}
	if before.Header().Get("ETag") == after.Header().Get("ETag") {
		t.Error("ETag nie zmienił się")
	}

	write(t, dir, "login.html", `<p>nowy formularz <input name="csrf" value="{{.CSRF}}"></p>`, time.Now())
	if rec := b.do("GET", "/yt/login", nil); !strings.Contains(rec.Body.String(), "nowy formularz") {
		t.Errorf("nowy szablon nie jest używany: %s", rec.Body.String())
	}
}

// --- nagłówki bezpieczeństwa ---

func TestSecurityHeadersEverywhere(t *testing.T) {
	a, b := loggedIn(t)
	_ = a
	anon := a.browser()
	reqs := []struct {
		b      *browser
		method string
		path   string
	}{
		{anon, "GET", "/yt/"},
		{anon, "GET", "/yt/login"},
		{anon, "GET", "/yt/stat.json"},
		{anon, "GET", "/yt/script.js"},
		{anon, "GET", "/yt/nieznane"},
		{anon, "DELETE", "/yt/login"},
		{b, "GET", "/yt/"},
		{b, "GET", "/yt/stat.json"},
	}
	for _, r := range reqs {
		rec := r.b.do(r.method, r.path, nil)
		h := rec.Header()
		if h.Get("Content-Security-Policy") != contentSecurityPolicy {
			t.Errorf("%s %s: CSP %q", r.method, r.path, h.Get("Content-Security-Policy"))
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" {
			t.Errorf("%s %s: nosniff=%q referrer=%q", r.method, r.path, h.Get("X-Content-Type-Options"), h.Get("Referrer-Policy"))
		}
	}
	if !strings.Contains(contentSecurityPolicy, "frame-ancestors 'none'") || !strings.Contains(contentSecurityPolicy, "default-src 'self'") {
		t.Error("CSP bez wymaganych dyrektyw")
	}
}

// --- trasy i dawne adresy ---

func TestOldAndUnknownPaths(t *testing.T) {
	_, b := loggedIn(t) // nawet z sesją
	for _, p := range []string{
		"/yt/index.php", "/yt/stat.php", "/yt/functions.php", "/yt/require_auth.php",
		"/yt/graph_data.php", "/yt/cron.php", "/yt/cron.sh", "/yt/statistics.csv",
		"/yt/index.html", "/yt/login.html", "/yt/config.json", "/yt/web/script.js",
	} {
		if rec := b.do("GET", p, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, oczekiwano 404", p, rec.Code)
		}
	}
}

func TestPathTraversal(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	for _, p := range []string{"/yt/../config.json", "/yt/%2e%2e/config.json", "/yt/..%2fconfig.json", "/yt/script.js/../../go.mod"} {
		req := httptest.NewRequest("GET", "http://x"+p, nil)
		rec := httptest.NewRecorder()
		a.handler.ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Errorf("%s: 200, treść: %.80s", p, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "listen") || strings.Contains(rec.Body.String(), "module") {
			t.Errorf("%s: wyciek pliku spoza web_dir", p)
		}
	}
}

// --- panel ---

func TestPanelTemplate(t *testing.T) {
	_, b := loggedIn(t)
	rec := b.do("GET", "/yt/", nil)
	body := rec.Body.String()
	for _, want := range []string{`lang="pl"`, `action="/yt/logout"`, `id="refresh"`, `/yt/script.js`, `/yt/style.css`, "Wyloguj"} {
		if !strings.Contains(body, want) {
			t.Errorf("panel bez %q", want)
		}
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "style=") || strings.Contains(body, "onclick") {
		t.Error("panel zawiera skrypt lub styl inline (blokowane przez CSP)")
	}
	// Szablon nie wstawia danych filmów: tytuły trafiają do DOM tylko przez skrypt.
	if strings.Contains(body, "jesienne") {
		t.Error("panel nie powinien renderować danych po stronie serwera")
	}
}

func TestLoginTemplateNoInline(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	body := a.browser().do("GET", "/yt/login", nil).Body.String()
	if strings.Contains(body, "<script") || strings.Contains(body, "style=") {
		t.Error("strona logowania zawiera skrypt lub styl inline")
	}
	for _, want := range []string{`name="csrf"`, `name="remember"`, `autocomplete="current-password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("formularz bez %q", want)
		}
	}
}

func TestTemplateEscapesError(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	rec := httptest.NewRecorder()
	s := &server{Deps: a.deps}
	s.render(rec, 200, "login.html", loginData{CSRF: `"><script>x</script>`, Error: "<b>błąd</b>"})
	if strings.Contains(rec.Body.String(), "<script>x") || strings.Contains(rec.Body.String(), "<b>błąd") {
		t.Error("szablon nie escapuje danych")
	}
}

func TestStaticVersionedCaching(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	b := a.browser()
	url, err := a.deps.Files.AssetURL("script.js")
	if err != nil {
		t.Fatal(err)
	}
	rec := b.do("GET", url, nil)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("aktualna wersja: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	for _, u := range []string{"/yt/script.js", "/yt/script.js?v=staraWersja", "/yt/script.js?v=no_php"} {
		rec := b.do("GET", u, nil)
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("%s: %d %q, oczekiwano no-cache", u, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}
}

func TestPagesUseVersionedAssets(t *testing.T) {
	a, b := loggedIn(t)
	panel := b.do("GET", "/yt/", nil).Body.String()
	login := a.browser().do("GET", "/yt/login", nil).Body.String()
	for name, body := range map[string]string{"panel": panel, "logowanie": login} {
		for _, f := range []string{"style.css", "logo.png", "favicon.png"} {
			if !strings.Contains(body, "/yt/"+f+"?v=") {
				t.Errorf("%s: %s bez wersji", name, f)
			}
		}
	}
	if !strings.Contains(panel, "/yt/script.js?v=") {
		t.Error("panel: script.js bez wersji")
	}
}
