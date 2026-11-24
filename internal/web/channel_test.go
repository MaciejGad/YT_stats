package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yt-stats/internal/auth"
	"yt-stats/internal/stats"
)

var testLogo = &stats.Logo{
	Data:        []byte("\x89PNG\r\n\x1a\nfake"),
	ContentType: "image/png",
	Version:     "0123456789abcdef",
	ModTime:     time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC),
}

func withChannel(a *app) {
	a.stats.channel = &stats.Channel{
		Title:  `Włóczkowo <script>alert(1)</script>`,
		Handle: "@wloczkowo",
		URL:    "https://www.youtube.com/@wloczkowo",
	}
	a.stats.logo = testLogo
}

func TestHeaderBeforeChannelIsKnown(t *testing.T) {
	a, b := loggedIn(t)
	body := b.do("GET", "/yt/", nil).Body.String()
	for _, want := range []string{
		"<title>" + defaultTitle + "</title>",
		`id="channel-title" class="brand-name">` + defaultTitle,
		`id="channel-sub" class="brand-sub" hidden`,
		`src="/yt/logo.png?v=`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel nie zawiera %q", want)
		}
	}
	if strings.Contains(body, "channel-logo?v=") {
		t.Error("bez pobranego logo panel nie może wskazywać /yt/channel-logo")
	}
	login := a.browser().do("GET", "/yt/login", nil).Body.String()
	if !strings.Contains(login, "<title>Logowanie — "+defaultTitle+"</title>") {
		t.Errorf("tytuł strony logowania:\n%s", login)
	}
}

func TestHeaderShowsChannel(t *testing.T) {
	a, b := loggedIn(t)
	withChannel(a)
	body := b.do("GET", "/yt/", nil).Body.String()
	escaped := `Włóczkowo &lt;script&gt;alert(1)&lt;/script&gt;`
	for _, want := range []string{
		"<title>" + escaped + " — statystyki</title>",
		`id="channel-title" class="brand-name">` + escaped + "</h1>",
		`href="https://www.youtube.com/@wloczkowo"`,
		">@wloczkowo</a>",
		`class="brand-logo is-avatar" src="/yt/channel-logo?v=0123456789abcdef"`,
		`rel="icon" href="/yt/channel-logo?v=0123456789abcdef"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("panel nie zawiera %q", want)
		}
	}
	if strings.Contains(body, "<script>alert(1)") || strings.Contains(body, `id="channel-sub" class="brand-sub" hidden`) {
		t.Error("nazwa kanału nieescapowana albo podtytuł ukryty")
	}

	login := a.browser().do("GET", "/yt/login", nil).Body.String()
	for _, want := range []string{
		"<title>Logowanie — " + escaped + "</title>",
		`<h1 class="login-title">` + escaped + "</h1>",
		`class="login-logo is-avatar" src="/yt/channel-logo?v=0123456789abcdef"`,
	} {
		if !strings.Contains(login, want) {
			t.Errorf("logowanie nie zawiera %q", want)
		}
	}
}

func TestHeaderShowsChannelWithoutLogo(t *testing.T) {
	a, b := loggedIn(t)
	withChannel(a)
	a.stats.logo = nil
	body := b.do("GET", "/yt/", nil).Body.String()
	if !strings.Contains(body, `class="brand-logo" src="/yt/logo.png?v=`) {
		t.Error("bez logo kanału powinno być logo z web_dir")
	}
}

func TestChannelLogo(t *testing.T) {
	a := newApp(t, fastParams, auth.AuthOptions{})
	get := func(method, target string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		a.handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := get("GET", "/yt/channel-logo", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bez logo: %d", rec.Code)
	}
	a.stats.logo = testLogo

	// Without a session: the login page shows the avatar too.
	rec := get("GET", "/yt/channel-logo?v=0123456789abcdef", nil)
	h := rec.Header()
	if rec.Code != 200 || rec.Body.String() != string(testLogo.Data) {
		t.Fatalf("logo: %d %q", rec.Code, rec.Body.String())
	}
	if h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("ETag") != `"0123456789abcdef"` {
		t.Errorf("nagłówki: %v", h)
	}
	if h.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("Cache-Control z aktualną wersją: %q", h.Get("Cache-Control"))
	}
	for _, target := range []string{"/yt/channel-logo", "/yt/channel-logo?v=stara"} {
		if cc := get("GET", target, nil).Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control %q", target, cc)
		}
	}
	if rec := get("GET", "/yt/channel-logo", map[string]string{"If-None-Match": `"0123456789abcdef"`}); rec.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rec.Code)
	}
	if rec := get("HEAD", "/yt/channel-logo", nil); rec.Code != 200 || rec.Body.Len() != 0 {
		t.Errorf("HEAD: %d, %d bajtów", rec.Code, rec.Body.Len())
	}
	if rec := get("POST", "/yt/channel-logo", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}
