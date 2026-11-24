package stats

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yt-stats/internal/youtube"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// fakeFetcher returns a result with a sequence number or the configured error.
type fakeFetcher struct {
	calls   atomic.Int32
	mu      sync.Mutex
	err     error
	nilRes  bool
	gate    chan struct{} // when set, Fetch waits for it to be closed
	started chan struct{} // signalled when Fetch starts
	waitCtx bool          // Fetch waits for the context to end
}

func (f *fakeFetcher) Fetch(ctx context.Context) (*youtube.Result, error) {
	n := f.calls.Add(1)
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.gate != nil {
		<-f.gate
	}
	if f.waitCtx {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	if f.nilRes {
		return nil, nil
	}
	views := "100"
	return &youtube.Result{
		SubscriberCount: ptr("42"),
		ViewCount:       ptr(strings.Repeat("9", int(n))), // different on every fetch
		Videos: []youtube.Video{
			{ID: "abc", Title: "Pierwszy", ViewCount: &views},
			{ID: "def", Title: "Ukryty licznik"},
		},
	}, nil
}

func (f *fakeFetcher) setErr(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }

func ptr(s string) *string { return &s }

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func newCache(f Fetcher) (*Cache, *clock, *logBuf) {
	clk := &clock{t: time.Date(2026, 11, 23, 21, 45, 10, 0, time.UTC)}
	logs := &logBuf{}
	c := New(f, Options{Now: clk.Now, Log: slog.New(slog.NewTextHandler(logs, nil))})
	return c, clk, logs
}

func mustGet(t *testing.T, c *Cache) *Payload {
	t.Helper()
	p, err := c.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	return p
}

var errAPI = errors.New("awaria API")

// --- freshness ---

func TestFirstGetFetches(t *testing.T) {
	f := &fakeFetcher{}
	c, _, logs := newCache(f)
	p := mustGet(t, c)
	if f.calls.Load() != 1 || p.Stale {
		t.Errorf("calls=%d stale=%v", f.calls.Load(), p.Stale)
	}
	if *p.Stats.SubscriberCount != "42" || len(p.Videos) != 2 || p.Videos[0].VideoID != "abc" {
		t.Errorf("payload: %+v", p)
	}
	if !strings.Contains(logs.String(), "pobrano statystyki") {
		t.Errorf("brak wpisu w logu: %s", logs)
	}
}

func TestWithinTTLNoFetch(t *testing.T) {
	f := &fakeFetcher{}
	c, clk, _ := newCache(f)
	first := mustGet(t, c)
	clk.Add(59 * time.Second)
	second := mustGet(t, c)
	if f.calls.Load() != 1 {
		t.Errorf("w ciągu 60 s wykonano %d pobrań", f.calls.Load())
	}
	if first.UpdateTime != second.UpdateTime {
		t.Error("dane z cache powinny mieć ten sam czas pobrania")
	}
}

func TestAfterTTLFetches(t *testing.T) {
	f := &fakeFetcher{}
	c, clk, _ := newCache(f)
	first := mustGet(t, c)
	clk.Add(61 * time.Second)
	second := mustGet(t, c)
	if f.calls.Load() != 2 {
		t.Errorf("po 61 s wykonano %d pobrań", f.calls.Load())
	}
	if first.UpdateTime == second.UpdateTime || *second.Stats.ViewsCount != "99" {
		t.Errorf("nowe dane: %s %s", second.UpdateTime, *second.Stats.ViewsCount)
	}
}

func TestConcurrentGetsShareOneFetch(t *testing.T) {
	f := &fakeFetcher{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	c, _, _ := newCache(f)

	var wg sync.WaitGroup
	results := make(chan *Payload, 10)
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := c.Get(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			results <- p
		}()
	}
	<-f.started
	time.Sleep(20 * time.Millisecond) // the other requests join the in-flight fetch
	close(f.gate)
	wg.Wait()
	close(results)

	if f.calls.Load() != 1 {
		t.Errorf("10 równoległych żądań wykonało %d pobrań", f.calls.Load())
	}
	n := 0
	for p := range results {
		n++
		if p.Stale || *p.Stats.ViewsCount != "9" {
			t.Errorf("wynik: %+v", p)
		}
	}
	if n != 10 {
		t.Errorf("odpowiedzi: %d", n)
	}
}

// --- errors and stale data ---

func TestErrorWithoutDataUnavailable(t *testing.T) {
	f := &fakeFetcher{err: errAPI}
	c, _, logs := newCache(f)
	if _, err := c.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("oczekiwano ErrUnavailable, jest %v", err)
	}
	if !strings.Contains(logs.String(), "awaria API") {
		t.Errorf("brak błędu w logu: %s", logs)
	}
}

func TestErrorReturnsStaleWithin24h(t *testing.T) {
	f := &fakeFetcher{}
	c, clk, _ := newCache(f)
	fresh := mustGet(t, c)

	f.setErr(errAPI)
	clk.Add(23 * time.Hour)
	p := mustGet(t, c)
	if !p.Stale {
		t.Error("oczekiwano stale=true")
	}
	if p.UpdateTime != fresh.UpdateTime {
		t.Errorf("dane awaryjne powinny mieć pierwotny czas: %s vs %s", p.UpdateTime, fresh.UpdateTime)
	}
	if *p.Stats.ViewsCount != *fresh.Stats.ViewsCount {
		t.Error("nieudane pobranie podmieniło poprzedni wynik")
	}
}

func TestErrorAfter24hUnavailable(t *testing.T) {
	f := &fakeFetcher{}
	c, clk, _ := newCache(f)
	mustGet(t, c)
	f.setErr(errAPI)
	clk.Add(25 * time.Hour)
	if _, err := c.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("oczekiwano ErrUnavailable, jest %v", err)
	}
}

func TestBackoffAfterError(t *testing.T) {
	f := &fakeFetcher{}
	c, clk, _ := newCache(f)
	mustGet(t, c)
	f.setErr(errAPI)
	clk.Add(61 * time.Second)
	if p := mustGet(t, c); !p.Stale {
		t.Error("oczekiwano danych awaryjnych")
	}
	calls := f.calls.Load()

	for range 5 {
		clk.Add(5 * time.Second) // 25 s since the error in total
		if p := mustGet(t, c); !p.Stale {
			t.Error("w oknie przerwy oczekiwano danych awaryjnych")
		}
	}
	if f.calls.Load() != calls {
		t.Errorf("w ciągu 30 s od błędu wykonano %d nowych pobrań", f.calls.Load()-calls)
	}

	clk.Add(6 * time.Second) // 31 s since the error
	mustGet(t, c)
	if f.calls.Load() != calls+1 {
		t.Errorf("po 30 s oczekiwano nowej próby, pobrań: %d", f.calls.Load()-calls)
	}
}

func TestBackoffWithoutDataUnavailable(t *testing.T) {
	f := &fakeFetcher{err: errAPI}
	c, clk, _ := newCache(f)
	c.Get(context.Background())
	clk.Add(10 * time.Second)
	if _, err := c.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("oczekiwano ErrUnavailable w oknie przerwy, jest %v", err)
	}
	if f.calls.Load() != 1 {
		t.Errorf("pobrań: %d", f.calls.Load())
	}
}

func TestRecoveryAfterError(t *testing.T) {
	f := &fakeFetcher{err: errAPI}
	c, clk, _ := newCache(f)
	c.Get(context.Background())
	f.setErr(nil)
	clk.Add(31 * time.Second)
	if p := mustGet(t, c); p.Stale {
		t.Error("po udanym pobraniu stale powinno być false")
	}
	// The error state is cleared: a normal refresh after TTL.
	clk.Add(61 * time.Second)
	mustGet(t, c)
	if f.calls.Load() != 3 {
		t.Errorf("pobrań: %d, oczekiwano 3", f.calls.Load())
	}
}

func TestNilResultIsError(t *testing.T) {
	c, _, _ := newCache(&fakeFetcher{nilRes: true})
	if _, err := c.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("oczekiwano ErrUnavailable, jest %v", err)
	}
}

// --- contexts and timeouts ---

func TestCancelledRequestDoesNotAbortFetch(t *testing.T) {
	f := &fakeFetcher{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	c, _, _ := newCache(f)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := c.Get(ctx); errc <- err }()
	<-f.started
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("oczekiwano context.Canceled, jest %v", err)
	}

	close(f.gate)
	// The fetch finished in the background; the next request uses the result.
	deadline := time.Now().Add(2 * time.Second)
	for {
		p, err := c.Get(context.Background())
		if err == nil && !p.Stale {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wynik pobrania w tle nie trafił do cache")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if f.calls.Load() != 1 {
		t.Errorf("pobrań: %d, oczekiwano 1", f.calls.Load())
	}
}

func TestRefreshTimeout(t *testing.T) {
	f := &fakeFetcher{waitCtx: true}
	c := New(f, Options{RefreshTimeout: 50 * time.Millisecond})
	start := time.Now()
	if _, err := c.Get(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("oczekiwano ErrUnavailable, jest %v", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("limit czasu odświeżenia zadziałał po %v", time.Since(start))
	}
}

func TestDefaults(t *testing.T) {
	c := New(&fakeFetcher{}, Options{})
	if c.opt.TTL != 60*time.Second || c.opt.MaxStaleAge != 24*time.Hour ||
		c.opt.ErrorBackoff != 30*time.Second || c.opt.RefreshTimeout != 30*time.Second {
		t.Errorf("wartości domyślne: %+v", c.opt)
	}
}

// --- format ---

func TestUpdateTimeFormat(t *testing.T) {
	cases := map[string]string{
		"2026-11-23T21:45:10Z": "2026-11-23T22:45:10+01:00", // winter
		"2026-07-01T10:00:00Z": "2026-07-01T12:00:00+02:00", // summer
		"2026-03-29T00:59:59Z": "2026-03-29T01:59:59+01:00", // just before the switch to summer time
		"2026-03-29T01:00:00Z": "2026-03-29T03:00:00+02:00", // just after the switch
	}
	for in, want := range cases {
		ts, _ := time.Parse(time.RFC3339, in)
		got := FormatTime(ts)
		if got != want {
			t.Errorf("FormatTime(%s) = %s, oczekiwano %s", in, got, want)
		}
		if _, err := time.Parse(time.RFC3339, got); err != nil {
			t.Errorf("%s nie jest poprawnym RFC 3339: %v", got, err)
		}
	}
}

func TestJSONContract(t *testing.T) {
	c, _, _ := newCache(&fakeFetcher{})
	p := mustGet(t, c)
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal(data, &got)

	stats := got["stats"].(map[string]any)
	if stats["subscriberCount"] != "42" || stats["viewsCount"] != "9" {
		t.Errorf("stats: %v", stats)
	}
	videos := got["videos"].([]any)
	v0 := videos[0].(map[string]any)
	v1 := videos[1].(map[string]any)
	if v0["videoId"] != "abc" || v0["title"] != "Pierwszy" || v0["viewCount"] != "100" {
		t.Errorf("videos[0]: %v", v0)
	}
	if val, ok := v1["viewCount"]; !ok || val != nil {
		t.Errorf("brak licznika powinien być null, jest %v (obecny=%v)", val, ok)
	}
	if got["update_time"] != "2026-11-23T22:45:10+01:00" || got["stale"] != false {
		t.Errorf("update_time=%v stale=%v", got["update_time"], got["stale"])
	}
}

func TestEmptyVideosIsArray(t *testing.T) {
	p := toPayload(&youtube.Result{}, time.Now())
	data, _ := json.Marshal(p)
	if !strings.Contains(string(data), `"videos":[]`) {
		t.Errorf("pusta lista powinna być [], jest %s", data)
	}
	if !strings.Contains(string(data), `"subscriberCount":null`) {
		t.Errorf("brak licznika powinien być null: %s", data)
	}
}
