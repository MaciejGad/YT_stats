package stats

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"yt-stats/internal/youtube"
)

// fakeImages returns an image whose content depends on the URL.
type fakeImages struct {
	mu   sync.Mutex
	urls []string
	err  error
}

func (f *fakeImages) FetchImage(_ context.Context, u string) ([]byte, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, u)
	if f.err != nil {
		return nil, "", f.err
	}
	return []byte("img:" + u), "image/png", nil
}

func (f *fakeImages) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.urls) }

var testChannel = youtube.Channel{
	ID:           "UC5711W83ebIxkC8bHjOKI7g",
	Title:        "Kanał testowy",
	Handle:       "@kanaltestowy",
	ThumbnailURL: "https://yt3.ggpht.com/a",
}

func newLogoCache(f *fakeFetcher, img ImageFetcher) (*Cache, *clock, *logBuf) {
	clk := &clock{t: time.Date(2026, 11, 23, 21, 45, 10, 0, time.UTC)}
	logs := &logBuf{}
	c := New(f, Options{Now: clk.Now, Images: img, Log: slog.New(slog.NewTextHandler(logs, nil))})
	return c, clk, logs
}

func TestPayloadChannel(t *testing.T) {
	cases := []struct {
		name string
		in   youtube.Channel
		want Channel
	}{
		{"z handle", testChannel, Channel{Title: "Kanał testowy", Handle: "@kanaltestowy", URL: "https://www.youtube.com/@kanaltestowy"}},
		{"bez handle", youtube.Channel{ID: "UCabc", Title: "X"}, Channel{Title: "X", URL: "https://www.youtube.com/channel/UCabc"}},
		{"handle z polskimi znakami", youtube.Channel{ID: "UCabc", Title: "X", Handle: "@włóczka"}, Channel{Title: "X", Handle: "@włóczka", URL: "https://www.youtube.com/@w%C5%82%C3%B3czka"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, _ := newLogoCache(&fakeFetcher{channel: tc.in}, nil)
			if got := mustGet(t, c).Channel; got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestChannelPeekDoesNotFetch(t *testing.T) {
	f := &fakeFetcher{channel: testChannel}
	c, clk, _ := newLogoCache(f, nil)
	if _, ok := c.Channel(); ok {
		t.Fatal("przed pierwszym pobraniem kanał powinien być nieznany")
	}
	if f.calls.Load() != 0 {
		t.Fatal("Channel nie może pobierać danych")
	}
	mustGet(t, c)
	clk.Add(48 * time.Hour) // much older than MaxStaleAge
	f.setErr(errAPI)
	ch, ok := c.Channel()
	if !ok || ch.Title != "Kanał testowy" {
		t.Errorf("Channel() = %+v, %v", ch, ok)
	}
	if f.calls.Load() != 1 {
		t.Errorf("Channel wywołał pobranie: %d", f.calls.Load())
	}
}

func TestLogoDownloadedOnceAndOnChange(t *testing.T) {
	f := &fakeFetcher{channel: testChannel}
	img := &fakeImages{}
	c, clk, _ := newLogoCache(f, img)

	p := mustGet(t, c)
	logo, ok := c.Logo()
	if !ok || string(logo.Data) != "img:https://yt3.ggpht.com/a" || logo.ContentType != "image/png" {
		t.Fatalf("logo: %+v, %v", logo, ok)
	}
	if p.Channel.LogoVersion == "" || p.Channel.LogoVersion != logo.Version || len(logo.Version) != 16 {
		t.Errorf("wersja logo w danych %q, w logo %q", p.Channel.LogoVersion, logo.Version)
	}

	clk.Add(2 * time.Minute)
	mustGet(t, c)
	if img.calls() != 1 {
		t.Errorf("to samo logo pobrane ponownie: %d", img.calls())
	}

	f.mu.Lock()
	f.channel.ThumbnailURL = "https://yt3.ggpht.com/b"
	f.mu.Unlock()
	clk.Add(2 * time.Minute)
	p2 := mustGet(t, c)
	logo2, _ := c.Logo()
	if img.calls() != 2 || string(logo2.Data) != "img:https://yt3.ggpht.com/b" {
		t.Errorf("zmienione logo: %d pobrań, %q", img.calls(), logo2.Data)
	}
	if p2.Channel.LogoVersion == p.Channel.LogoVersion {
		t.Error("wersja logo powinna się zmienić")
	}
}

func TestLogoFailureDoesNotFailRefresh(t *testing.T) {
	f := &fakeFetcher{channel: testChannel}
	img := &fakeImages{err: errors.New("obrazek niedostępny")}
	c, clk, logs := newLogoCache(f, img)

	p := mustGet(t, c)
	if p.Stale || p.Channel.LogoVersion != "" {
		t.Errorf("dane: stale=%v logoVersion=%q", p.Stale, p.Channel.LogoVersion)
	}
	if _, ok := c.Logo(); ok {
		t.Error("logo nie powinno istnieć")
	}
	if !strings.Contains(logs.String(), "logo kanału") {
		t.Errorf("brak ostrzeżenia w logu:\n%s", logs.String())
	}

	img.mu.Lock()
	img.err = nil
	img.mu.Unlock()
	clk.Add(2 * time.Minute)
	if p := mustGet(t, c); p.Channel.LogoVersion == "" {
		t.Error("kolejne odświeżenie powinno pobrać logo")
	}
}

func TestNoLogoWithoutThumbnailOrFetcher(t *testing.T) {
	noThumb := testChannel
	noThumb.ThumbnailURL = ""
	img := &fakeImages{}
	c, _, _ := newLogoCache(&fakeFetcher{channel: noThumb}, img)
	mustGet(t, c)
	if img.calls() != 0 {
		t.Error("bez miniatury nie powinno być pobrania")
	}

	c2, _, _ := newLogoCache(&fakeFetcher{channel: testChannel}, nil)
	if p := mustGet(t, c2); p.Channel.LogoVersion != "" {
		t.Errorf("bez pobierania obrazków logoVersion = %q", p.Channel.LogoVersion)
	}
}
