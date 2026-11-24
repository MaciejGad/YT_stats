package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testChannel  = "UC5711W83ebIxkC8bHjOKI7g"
	testUploads  = "UU5711W83ebIxkC8bHjOKI7g"
	primaryKey   = "FAKEKEYPRIMARYkey_0000000000000000000000"
	fallbackKey  = "FAKEKEYFALLBACKkey_111111111111111111111"
	apiQuotaBody = `{"error":{"code":403,"message":"The request cannot be completed because you have exceeded your quota.","errors":[{"reason":"quotaExceeded"}]}}`
)

// fakeYT simulates the three YouTube Data API endpoints in use.
type fakeYT struct {
	t *testing.T

	mu              sync.Mutex
	hiddenSubs      bool
	noChannel       bool
	noUploads       bool
	playlist        []string          // IDs in playlist order, may repeat
	playlistMissing bool              // 404 playlistNotFound
	titles          map[string]string // videos returned by videos.list
	noViews         map[string]bool
	reverseVideos   bool                                   // videos.list returns them in reverse order
	quotaKeys       map[string]bool                        // keys with an exhausted quota
	fail            map[string]func(w http.ResponseWriter) // endpoint → error response
	delay           time.Duration
	samePageToken   bool
	snippet         map[string]any // channels.list snippet; nil gives the default

	calls      map[string]int
	keysUsed   []string
	batchSizes []int
}

func newFake(t *testing.T, n int) *fakeYT {
	f := &fakeYT{
		t:         t,
		titles:    map[string]string{},
		noViews:   map[string]bool{},
		quotaKeys: map[string]bool{},
		fail:      map[string]func(http.ResponseWriter){},
		calls:     map[string]int{},
	}
	for i := range n {
		id := fmt.Sprintf("vid%08d", i)
		f.playlist = append(f.playlist, id)
		f.titles[id] = "Film " + strconv.Itoa(i)
	}
	return f
}

func (f *fakeYT) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if r.URL.Query().Has("key") {
		f.t.Errorf("klucz API w URL: %s", r.URL)
	}
	key := r.Header.Get("X-Goog-Api-Key")
	endpoint := strings.TrimPrefix(r.URL.Path, "/youtube/v3/")
	f.calls[endpoint]++
	f.keysUsed = append(f.keysUsed, key)

	if f.delay > 0 {
		f.mu.Unlock()
		select {
		case <-time.After(f.delay):
		case <-r.Context().Done():
		}
		f.mu.Lock()
	}
	if f.quotaKeys[key] {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(apiQuotaBody))
		return
	}
	if fn := f.fail[endpoint]; fn != nil {
		fn(w)
		return
	}

	q := r.URL.Query()
	switch endpoint {
	case "channels":
		if q.Get("id") != testChannel {
			f.t.Errorf("channels id = %q", q.Get("id"))
		}
		if f.noChannel {
			writeJSON(w, map[string]any{})
			return
		}
		stats := map[string]any{"viewCount": "123456", "subscriberCount": "789", "hiddenSubscriberCount": f.hiddenSubs}
		if f.hiddenSubs {
			delete(stats, "subscriberCount")
		}
		uploads := testUploads
		if f.noUploads {
			uploads = ""
		}
		snippet := f.snippet
		if snippet == nil {
			snippet = map[string]any{
				"title":     "Kanał testowy",
				"customUrl": "@kanaltestowy",
				"thumbnails": map[string]any{
					"default": map[string]any{"url": "https://yt3.ggpht.com/a=s88"},
					"medium":  map[string]any{"url": "https://yt3.ggpht.com/a=s240"},
					"high":    map[string]any{"url": "https://yt3.ggpht.com/a=s800"},
				},
			}
		}
		writeJSON(w, map[string]any{"items": []any{map[string]any{
			"snippet":        snippet,
			"statistics":     stats,
			"contentDetails": map[string]any{"relatedPlaylists": map[string]any{"uploads": uploads}},
		}}})
	case "playlistItems":
		if q.Get("playlistId") != testUploads {
			f.t.Errorf("playlistId = %q", q.Get("playlistId"))
		}
		if f.playlistMissing {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"not found","errors":[{"reason":"playlistNotFound"}]}}`))
			return
		}
		size, _ := strconv.Atoi(q.Get("maxResults"))
		start := 0
		if tok := q.Get("pageToken"); tok != "" {
			start, _ = strconv.Atoi(strings.TrimPrefix(tok, "p"))
		}
		end := min(start+size, len(f.playlist))
		var items []any
		for _, id := range f.playlist[start:end] {
			items = append(items, map[string]any{"contentDetails": map[string]any{"videoId": id}})
		}
		resp := map[string]any{"items": items}
		if end < len(f.playlist) {
			resp["nextPageToken"] = "p" + strconv.Itoa(end)
			if f.samePageToken {
				resp["nextPageToken"] = "p0"
			}
		}
		writeJSON(w, resp)
	case "videos":
		ids := strings.Split(q.Get("id"), ",")
		f.batchSizes = append(f.batchSizes, len(ids))
		if q.Get("part") != "snippet,statistics" {
			f.t.Errorf("videos part = %q", q.Get("part"))
		}
		var items []any
		for _, id := range ids {
			title, ok := f.titles[id]
			if !ok {
				continue
			}
			stats := map[string]any{"viewCount": "1" + strings.TrimLeft(id[3:], "0") + "0"}
			if f.noViews[id] {
				stats = map[string]any{}
			}
			items = append(items, map[string]any{"id": id, "snippet": map[string]any{"title": title}, "statistics": stats})
		}
		if f.reverseVideos {
			for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
				items[i], items[j] = items[j], items[i]
			}
		}
		writeJSON(w, map[string]any{"items": items})
	default:
		f.t.Errorf("nieznany endpoint %q", endpoint)
		w.WriteHeader(http.StatusNotFound)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// clock is a controlled clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time       { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Set(t time.Time)      { c.mu.Lock(); c.t = t; c.mu.Unlock() }
func (c *clock) Add(d time.Duration)  { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func newClock(t time.Time) *clock     { return &clock{t: t} }
func (f *fakeYT) count(ep string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls[ep] }
func (f *fakeYT) lastKey() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.keysUsed[len(f.keysUsed)-1]
}
func (f *fakeYT) set(fn func(f *fakeYT)) { f.mu.Lock(); fn(f); f.mu.Unlock() }

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type setup struct {
	fake   *fakeYT
	client *Client
	clock  *clock
	logs   *logBuf
}

func newSetup(t *testing.T, n int, mutate func(*Options)) *setup {
	t.Helper()
	fake := newFake(t, n)
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	clk := newClock(time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC))
	logs := &logBuf{}
	o := Options{
		ChannelID:      testChannel,
		APIKey:         primaryKey,
		APIKeyFallback: fallbackKey,
		BaseURL:        srv.URL + "/youtube/v3/",
		HTTPClient:     srv.Client(),
		Now:            clk.Now,
		Log:            slog.New(slog.NewTextHandler(logs, nil)),
	}
	if mutate != nil {
		mutate(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return &setup{fake: fake, client: c, clock: clk, logs: logs}
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func assertNoSecrets(t *testing.T, s string) {
	t.Helper()
	if strings.Contains(s, primaryKey) || strings.Contains(s, fallbackKey) {
		t.Errorf("tekst zawiera klucz API: %s", s)
	}
}

// --- channel ---

func TestFetchChannelStats(t *testing.T) {
	s := newSetup(t, 3, nil)
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if str(res.SubscriberCount) != "789" || str(res.ViewCount) != "123456" {
		t.Errorf("liczniki kanału: subs=%s views=%s", str(res.SubscriberCount), str(res.ViewCount))
	}
	if len(res.Videos) != 3 || res.Videos[0].ID != "vid00000000" || res.Videos[0].Title != "Film 0" {
		t.Errorf("filmy: %+v", res.Videos)
	}
}

func TestFetchChannelSnippet(t *testing.T) {
	s := newSetup(t, 1, nil)
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Channel{ID: testChannel, Title: "Kanał testowy", Handle: "@kanaltestowy", ThumbnailURL: "https://yt3.ggpht.com/a=s240"}
	if res.Channel != want {
		t.Errorf("kanał:\n got %+v\nwant %+v", res.Channel, want)
	}
}

func TestChannelSnippetFallbacks(t *testing.T) {
	cases := []struct {
		name    string
		snippet map[string]any
		want    Channel
	}{
		{"tylko mała miniatura", map[string]any{"title": " Nazwa ", "thumbnails": map[string]any{"default": map[string]any{"url": "https://yt3.ggpht.com/x"}}},
			Channel{ID: testChannel, Title: "Nazwa", ThumbnailURL: "https://yt3.ggpht.com/x"}},
		{"brak miniatur i handle", map[string]any{"title": "Nazwa"}, Channel{ID: testChannel, Title: "Nazwa"}},
		{"handle bez @", map[string]any{"title": "N", "customUrl": "nazwa"}, Channel{ID: testChannel, Title: "N"}},
		{"handle ze ścieżką", map[string]any{"title": "N", "customUrl": "@a/../b"}, Channel{ID: testChannel, Title: "N"}},
		{"handle z polskimi znakami", map[string]any{"title": "N", "customUrl": "@włóczka.pl"}, Channel{ID: testChannel, Title: "N", Handle: "@włóczka.pl"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSetup(t, 1, nil)
			s.fake.snippet = tc.snippet
			res, err := s.client.Fetch(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if res.Channel != tc.want {
				t.Errorf("got %+v, want %+v", res.Channel, tc.want)
			}
		})
	}
}

func TestChannelRequestAsksForSnippet(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/channels") {
			got = r.URL.Query()
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	c, err := New(Options{ChannelID: testChannel, APIKey: primaryKey, BaseURL: srv.URL + "/youtube/v3/", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	c.Fetch(context.Background())
	if !strings.Contains(got.Get("part"), "snippet") || !strings.Contains(got.Get("fields"), "snippet(title,customUrl,thumbnails)") {
		t.Errorf("zapytanie channels.list: part=%q fields=%q", got.Get("part"), got.Get("fields"))
	}
}

func TestHiddenSubscriberCount(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.hiddenSubs = true
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.SubscriberCount != nil {
		t.Errorf("ukryty licznik powinien dać nil, jest %s", str(res.SubscriberCount))
	}
	if str(res.ViewCount) != "123456" {
		t.Errorf("views = %s", str(res.ViewCount))
	}
}

func TestChannelNotFound(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.noChannel = true
	if _, err := s.client.Fetch(context.Background()); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("oczekiwano ErrChannelNotFound, jest %v", err)
	}
}

func TestChannelWithoutUploadsPlaylist(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.noUploads = true
	if _, err := s.client.Fetch(context.Background()); err == nil {
		t.Fatal("oczekiwano błędu")
	}
}

// --- pagination ---

func TestPagination(t *testing.T) {
	for _, tc := range []struct{ videos, pages int }{{1, 1}, {50, 1}, {51, 2}, {150, 3}} {
		t.Run(strconv.Itoa(tc.videos), func(t *testing.T) {
			s := newSetup(t, tc.videos, nil)
			res, err := s.client.Fetch(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Videos) != tc.videos {
				t.Errorf("filmów %d, oczekiwano %d", len(res.Videos), tc.videos)
			}
			if got := s.fake.count("playlistItems"); got != tc.pages {
				t.Errorf("stron %d, oczekiwano %d", got, tc.pages)
			}
		})
	}
}

func TestEmptyChannel(t *testing.T) {
	s := newSetup(t, 0, nil)
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Videos) != 0 || s.fake.count("videos") != 0 {
		t.Errorf("pusty kanał: %d filmów, %d wywołań videos", len(res.Videos), s.fake.count("videos"))
	}
}

func TestEmptyChannelPlaylistNotFound(t *testing.T) {
	s := newSetup(t, 0, nil)
	s.fake.playlistMissing = true
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("kanał bez playlisty przesłanych powinien dać pustą listę: %v", err)
	}
	if len(res.Videos) != 0 {
		t.Errorf("filmy: %+v", res.Videos)
	}
}

func TestTooManyPages(t *testing.T) {
	s := newSetup(t, 151, func(o *Options) { o.MaxPages = 3 })
	if _, err := s.client.Fetch(context.Background()); !errors.Is(err, ErrTooManyPages) {
		t.Fatalf("oczekiwano ErrTooManyPages, jest %v", err)
	}
	if s.fake.count("playlistItems") != 3 {
		t.Errorf("wykonano %d stron", s.fake.count("playlistItems"))
	}
}

func TestDefaultMaxPagesIs200(t *testing.T) {
	s := newSetup(t, 1, nil)
	if s.client.maxPages != 200 {
		t.Errorf("maxPages = %d", s.client.maxPages)
	}
}

func TestRepeatedPageToken(t *testing.T) {
	s := newSetup(t, 120, nil)
	s.fake.samePageToken = true
	if _, err := s.client.Fetch(context.Background()); err == nil {
		t.Fatal("pętla tokenów stron powinna dać błąd")
	}
}

// --- batching and order ---

func TestBatching(t *testing.T) {
	for _, tc := range []struct {
		videos int
		calls  int
	}{{0, 0}, {1, 1}, {50, 1}, {51, 2}, {120, 3}} {
		t.Run(strconv.Itoa(tc.videos), func(t *testing.T) {
			s := newSetup(t, tc.videos, nil)
			if _, err := s.client.Fetch(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := s.fake.count("videos"); got != tc.calls {
				t.Errorf("wywołań videos %d, oczekiwano %d", got, tc.calls)
			}
			for _, n := range s.fake.batchSizes {
				if n > 50 {
					t.Errorf("grupa %d ID przekracza 50", n)
				}
			}
		})
	}
}

func TestPlaylistOrderPreserved(t *testing.T) {
	s := newSetup(t, 120, nil)
	s.fake.reverseVideos = true
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range res.Videos {
		if want := fmt.Sprintf("vid%08d", i); v.ID != want {
			t.Fatalf("pozycja %d: %s, oczekiwano %s", i, v.ID, want)
		}
	}
}

func TestDuplicatesRemoved(t *testing.T) {
	s := newSetup(t, 3, nil)
	s.fake.playlist = []string{"vid00000000", "vid00000001", "vid00000000", "vid00000002", "vid00000001"}
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, v := range res.Videos {
		ids = append(ids, v.ID)
	}
	if strings.Join(ids, ",") != "vid00000000,vid00000001,vid00000002" {
		t.Errorf("ids = %v", ids)
	}
}

func TestMissingVideoSkipped(t *testing.T) {
	s := newSetup(t, 3, nil)
	delete(s.fake.titles, "vid00000001") // private or deleted
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Videos) != 2 || res.Videos[0].ID != "vid00000000" || res.Videos[1].ID != "vid00000002" {
		t.Errorf("filmy: %+v", res.Videos)
	}
}

func TestHiddenVideoViewCount(t *testing.T) {
	s := newSetup(t, 2, nil)
	s.fake.noViews["vid00000001"] = true
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Videos[0].ViewCount == nil || res.Videos[1].ViewCount != nil {
		t.Errorf("viewCount: %s, %s", str(res.Videos[0].ViewCount), str(res.Videos[1].ViewCount))
	}
}

func TestCounter(t *testing.T) {
	for in, want := range map[string]string{"": "<nil>", "0": "0", "123": "123", "-1": "<nil>", "1e5": "<nil>", " 1": "<nil>"} {
		if got := str(counter(in)); got != want {
			t.Errorf("counter(%q) = %s, oczekiwano %s", in, got, want)
		}
	}
}

// --- errors ---

func TestRequestTimeout(t *testing.T) {
	s := newSetup(t, 1, func(o *Options) { o.RequestTimeout = 50 * time.Millisecond })
	s.fake.delay = time.Second
	start := time.Now()
	_, err := s.client.Fetch(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("oczekiwano przekroczenia czasu, jest %v", err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Errorf("timeout zadziałał po %v", time.Since(start))
	}
}

func TestTotalTimeoutFromContext(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.delay = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.client.Fetch(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("oczekiwano przekroczenia czasu całości, jest %v", err)
	}
}

func TestResponseTooLarge(t *testing.T) {
	s := newSetup(t, 1, func(o *Options) { o.MaxResponseBytes = 100 })
	s.fake.fail["channels"] = func(w http.ResponseWriter) {
		w.Write([]byte(`{"items":[` + strings.Repeat(" ", 200) + `]}`))
	}
	_, err := s.client.Fetch(context.Background())
	if err == nil || !strings.Contains(err.Error(), "większa niż 100") {
		t.Fatalf("oczekiwano błędu rozmiaru, jest %v", err)
	}
}

func TestInvalidJSON(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.fail["videos"] = func(w http.ResponseWriter) { w.Write([]byte(`{"items": [`)) }
	if _, err := s.client.Fetch(context.Background()); err == nil || !strings.Contains(err.Error(), "JSON") {
		t.Fatalf("oczekiwano błędu JSON, jest %v", err)
	}
}

func TestServerError(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.fail["playlistItems"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"code":500,"message":"Backend Error","errors":[{"reason":"backendError"}]}}`))
	}
	_, err := s.client.Fetch(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 500 || apiErr.Reason != "backendError" {
		t.Fatalf("oczekiwano APIError 500 backendError, jest %v", err)
	}
	if s.fake.lastKey() != primaryKey {
		t.Error("błąd 500 nie powinien przełączać klucza")
	}
}

func TestForbiddenNonQuota(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.fail["channels"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"forbidden","errors":[{"reason":"forbidden"}]}}`))
	}
	_, err := s.client.Fetch(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "forbidden" {
		t.Fatalf("oczekiwano APIError forbidden, jest %v", err)
	}
	if s.fake.count("channels") != 1 {
		t.Errorf("odmowa dostępu nie powinna być ponawiana innym kluczem (%d wywołań)", s.fake.count("channels"))
	}
}

func TestNonJSONErrorBody(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.fail["channels"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("<html>bad gateway</html>"))
	}
	_, err := s.client.Fetch(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 502 || apiErr.Reason != "" {
		t.Fatalf("oczekiwano APIError 502, jest %v", err)
	}
}

func TestLongErrorMessageTruncated(t *testing.T) {
	e := parseAPIError("videos", 400, []byte(`{"error":{"message":"`+strings.Repeat("ż", 300)+`"}}`))
	if len(e.Message) > maxMessageLen+len("…") {
		t.Errorf("komunikat ma %d bajtów", len(e.Message))
	}
	if !strings.HasSuffix(e.Message, "…") {
		t.Error("brak znaku obcięcia")
	}
}

func TestErrorsRedactKeys(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.fail["channels"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"API key ` + primaryKey + ` not valid","errors":[{"reason":"keyInvalid"}]}}`))
	}
	_, err := s.client.Fetch(context.Background())
	if err == nil {
		t.Fatal("oczekiwano błędu")
	}
	assertNoSecrets(t, err.Error())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "keyInvalid" {
		t.Errorf("errors.As po ukryciu klucza: %v", err)
	}
	if s.fake.count("channels") != 1 {
		t.Error("keyInvalid nie powinien przełączać klucza")
	}
}

func TestNetworkErrorHasNoKey(t *testing.T) {
	s := newSetup(t, 1, func(o *Options) { o.BaseURL = "http://127.0.0.1:1/youtube/v3/" })
	_, err := s.client.Fetch(context.Background())
	if err == nil {
		t.Fatal("oczekiwano błędu połączenia")
	}
	assertNoSecrets(t, err.Error())
}

func TestNewValidation(t *testing.T) {
	if _, err := New(Options{APIKey: "k"}); err == nil {
		t.Error("brak kanału powinien dać błąd")
	}
	if _, err := New(Options{ChannelID: testChannel}); err == nil {
		t.Error("brak klucza powinien dać błąd")
	}
	if _, err := New(Options{ChannelID: testChannel, APIKey: "k", BaseURL: "http://x/bez-ukośnika"}); err == nil {
		t.Error("adres bez końcowego / powinien dać błąd")
	}
	c, err := New(Options{ChannelID: testChannel, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if c.base.String() != DefaultBaseURL || c.reqTimeout != DefaultRequestTimeout || c.maxBytes != DefaultMaxResponseBytes {
		t.Errorf("wartości domyślne: %s %v %d", c.base, c.reqTimeout, c.maxBytes)
	}
}

// --- fallback key ---

func TestQuotaSwitchesToFallback(t *testing.T) {
	s := newSetup(t, 3, nil)
	s.fake.quotaKeys[primaryKey] = true
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatalf("pobranie z kluczem zapasowym: %v", err)
	}
	if len(res.Videos) != 3 {
		t.Errorf("filmów %d", len(res.Videos))
	}
	// channels: 1 attempt with the primary key + 1 with the fallback; the rest go straight to the fallback.
	if s.fake.count("channels") != 2 || s.fake.count("playlistItems") != 1 || s.fake.count("videos") != 1 {
		t.Errorf("wywołania: %v", s.fake.calls)
	}
	if s.fake.lastKey() != fallbackKey {
		t.Error("ostatnie żądanie nie użyło klucza zapasowego")
	}
	logs := s.logs.String()
	if !strings.Contains(logs, "przełączam na zapasowy") {
		t.Errorf("brak wpisu o przełączeniu:\n%s", logs)
	}
	assertNoSecrets(t, logs)
}

func TestQuotaMidFetchRetriesSameRequest(t *testing.T) {
	s := newSetup(t, 3, nil)
	s.fake.fail["videos"] = nil
	// The quota runs out only at videos.list.
	served := 0
	s.fake.fail["videos"] = func(w http.ResponseWriter) {
		served++
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(apiQuotaBody))
		delete(s.fake.fail, "videos")
		s.fake.quotaKeys[primaryKey] = true
	}
	res, err := s.client.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Videos) != 3 || served != 1 || s.fake.count("videos") != 2 {
		t.Errorf("filmów %d, odmów %d, wywołań videos %d", len(res.Videos), served, s.fake.count("videos"))
	}
}

func TestFallbackStaysUntilPacificMidnight(t *testing.T) {
	s := newSetup(t, 1, nil)
	// 12:00 UTC = 04:00 PST, the next PST midnight = 08:00 UTC the following day.
	s.fake.quotaKeys[primaryKey] = true
	if _, err := s.client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.fake.set(func(f *fakeYT) { f.quotaKeys = map[string]bool{} })

	s.clock.Set(time.Date(2026, 11, 24, 7, 59, 0, 0, time.UTC))
	before := s.fake.count("channels")
	if _, err := s.client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.fake.count("channels") != before+1 || s.fake.lastKey() != fallbackKey {
		t.Error("przed północą PST powinien być używany od razu klucz zapasowy")
	}

	s.clock.Set(time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC))
	if _, err := s.client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.fake.lastKey() != primaryKey {
		t.Error("po północy PST powinien wrócić klucz główny")
	}
	if !strings.Contains(s.logs.String(), "powrót do głównego klucza") {
		t.Errorf("brak wpisu o powrocie:\n%s", s.logs.String())
	}
}

func TestNextQuotaReset(t *testing.T) {
	cases := []struct{ in, want string }{
		// winter (PST, UTC−8)
		{"2026-11-23T12:00:00Z", "2026-11-24T08:00:00Z"},
		{"2026-11-24T07:59:59Z", "2026-11-24T08:00:00Z"},
		{"2026-11-24T08:00:00Z", "2026-11-25T08:00:00Z"},
		// summer (PDT, UTC−7)
		{"2026-07-01T12:00:00Z", "2026-07-02T07:00:00Z"},
		// US switch to daylight saving time: 8 March 2026
		{"2026-03-08T12:00:00Z", "2026-03-09T07:00:00Z"},
		// US switch back to standard time: 1 November 2026
		{"2026-11-01T12:00:00Z", "2026-11-02T08:00:00Z"},
	}
	for _, tc := range cases {
		in, _ := time.Parse(time.RFC3339, tc.in)
		if got := nextQuotaReset(in).UTC().Format(time.RFC3339); got != tc.want {
			t.Errorf("nextQuotaReset(%s) = %s, oczekiwano %s", tc.in, got, tc.want)
		}
	}
}

func TestQuotaWithoutFallback(t *testing.T) {
	s := newSetup(t, 1, func(o *Options) { o.APIKeyFallback = "" })
	s.fake.quotaKeys[primaryKey] = true
	_, err := s.client.Fetch(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "quotaExceeded" {
		t.Fatalf("oczekiwano quotaExceeded, jest %v", err)
	}
	if s.fake.count("channels") != 1 {
		t.Errorf("bez klucza zapasowego nie powinno być ponowienia (%d)", s.fake.count("channels"))
	}
	if !strings.Contains(s.logs.String(), "brak klucza zapasowego") {
		t.Errorf("brak wpisu w logu:\n%s", s.logs.String())
	}
}

func TestQuotaOnBothKeys(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.quotaKeys[primaryKey] = true
	s.fake.quotaKeys[fallbackKey] = true
	_, err := s.client.Fetch(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Reason != "quotaExceeded" {
		t.Fatalf("oczekiwano quotaExceeded, jest %v", err)
	}
	if s.fake.count("channels") != 2 {
		t.Errorf("oczekiwano 2 prób (główny + zapasowy), jest %d", s.fake.count("channels"))
	}

	// The next fetch uses the fallback straight away and does not retry.
	if _, err := s.client.Fetch(context.Background()); err == nil {
		t.Fatal("oczekiwano błędu")
	}
	if s.fake.count("channels") != 3 {
		t.Errorf("oczekiwano 1 dodatkowej próby, jest %d", s.fake.count("channels")-2)
	}
	if !strings.Contains(s.logs.String(), "wyczerpany limit zapasowego") {
		t.Errorf("brak wpisu w logu:\n%s", s.logs.String())
	}
}

func TestDailyLimitExceededAlsoSwitches(t *testing.T) {
	s := newSetup(t, 1, nil)
	s.fake.fail["channels"] = func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"errors":[{"reason":"dailyLimitExceeded"}]}}`))
		delete(s.fake.fail, "channels")
	}
	if _, err := s.client.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.fake.lastKey() != fallbackKey {
		t.Error("dailyLimitExceeded powinien przełączyć na klucz zapasowy")
	}
}

func TestConcurrentQuotaSwitch(t *testing.T) {
	s := newSetup(t, 60, nil)
	s.fake.quotaKeys[primaryKey] = true
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.client.Fetch(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := strings.Count(s.logs.String(), "przełączam na zapasowy"); n != 1 {
		t.Errorf("przełączenie zalogowane %d razy, oczekiwano 1", n)
	}
}
