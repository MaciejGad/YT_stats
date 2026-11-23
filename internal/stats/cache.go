// Package stats trzyma ostatni wynik pobrania statystyk w pamięci,
// łączy równoległe odświeżenia i zwraca dane awaryjne po błędzie.
package stats

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	_ "time/tzdata" // Europe/Warsaw niezależnie od systemu

	"yt-stats/internal/youtube"
)

// Domyślne czasy z wymagań.
const (
	DefaultTTL            = 60 * time.Second
	DefaultMaxStaleAge    = 24 * time.Hour
	DefaultErrorBackoff   = 30 * time.Second
	DefaultRefreshTimeout = 30 * time.Second
)

// ErrUnavailable oznacza brak świeżego i awaryjnego wyniku (HTTP 503).
var ErrUnavailable = errors.New("statystyki są chwilowo niedostępne")

var warsaw = mustLoadLocation("Europe/Warsaw")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// Fetcher pobiera komplet danych. Implementuje go *youtube.Client.
type Fetcher interface {
	Fetch(ctx context.Context) (*youtube.Result, error)
}

// Payload to odpowiedź /yt/stat.json.
type Payload struct {
	Stats      Stats   `json:"stats"`
	Videos     []Video `json:"videos"`
	UpdateTime string  `json:"update_time"`
	Stale      bool    `json:"stale"`
}

// Stats to liczniki kanału.
type Stats struct {
	SubscriberCount *string `json:"subscriberCount"`
	ViewsCount      *string `json:"viewsCount"`
}

// Video to wiersz tabeli filmów.
type Video struct {
	VideoID   string  `json:"videoId"`
	Title     string  `json:"title"`
	ViewCount *string `json:"viewCount"`
}

// Options konfiguruje cache. Zerowe pola dostają wartości domyślne.
type Options struct {
	TTL            time.Duration
	MaxStaleAge    time.Duration
	ErrorBackoff   time.Duration
	RefreshTimeout time.Duration
	Now            func() time.Time
	Log            *slog.Logger
}

type snapshot struct {
	payload   Payload // Stale zawsze false
	fetchedAt time.Time
}

// call to jedno trwające pobranie, na które mogą czekać kolejne żądania.
type call struct {
	done chan struct{}
	err  error
}

// Cache jest bezpieczny dla wielu goroutyn.
type Cache struct {
	fetcher Fetcher
	opt     Options

	mu        sync.Mutex
	last      *snapshot
	lastErrAt time.Time // zero, gdy ostatnie pobranie się udało
	inflight  *call
}

// New tworzy pusty cache.
func New(f Fetcher, o Options) *Cache {
	if o.TTL == 0 {
		o.TTL = DefaultTTL
	}
	if o.MaxStaleAge == 0 {
		o.MaxStaleAge = DefaultMaxStaleAge
	}
	if o.ErrorBackoff == 0 {
		o.ErrorBackoff = DefaultErrorBackoff
	}
	if o.RefreshTimeout == 0 {
		o.RefreshTimeout = DefaultRefreshTimeout
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Cache{fetcher: f, opt: o}
}

// Get zwraca świeże dane (nie starsze niż TTL), w razie potrzeby pobierając je.
// Po błędzie zwraca ostatni wynik nie starszy niż MaxStaleAge ze Stale=true,
// a bez takiego wyniku ErrUnavailable. Przez ErrorBackoff od błędu nie
// ponawia pobrania. Anulowanie ctx przerywa tylko czekanie, nie samo pobranie.
func (c *Cache) Get(ctx context.Context) (*Payload, error) {
	c.mu.Lock()
	now := c.opt.Now()

	if c.last != nil && now.Sub(c.last.fetchedAt) < c.opt.TTL {
		p := c.last.payload
		c.mu.Unlock()
		return &p, nil
	}
	if !c.lastErrAt.IsZero() && now.Sub(c.lastErrAt) < c.opt.ErrorBackoff {
		p, err := c.fallbackLocked(now)
		c.mu.Unlock()
		return p, err
	}

	cl := c.inflight
	if cl == nil {
		cl = &call{done: make(chan struct{})}
		c.inflight = cl
		go c.refresh(cl)
	}
	c.mu.Unlock()

	select {
	case <-cl.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if cl.err == nil {
		p := c.last.payload
		return &p, nil
	}
	return c.fallbackLocked(c.opt.Now())
}

// refresh pobiera dane niezależnie od żądań, które na nie czekają.
func (c *Cache) refresh(cl *call) {
	ctx, cancel := context.WithTimeout(context.Background(), c.opt.RefreshTimeout)
	defer cancel()

	start := c.opt.Now()
	res, err := c.fetcher.Fetch(ctx)
	if err == nil && res == nil {
		err = errors.New("pobranie zwróciło pusty wynik")
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.opt.Now()
	if err != nil {
		c.lastErrAt = now
		c.opt.Log.Warn("odświeżenie statystyk nie powiodło się", "err", err, "duration", now.Sub(start).Round(time.Millisecond))
	} else {
		c.last = &snapshot{payload: toPayload(res, now), fetchedAt: now}
		c.lastErrAt = time.Time{}
		c.opt.Log.Info("pobrano statystyki", "videos", len(res.Videos), "duration", now.Sub(start).Round(time.Millisecond))
	}
	cl.err = err
	c.inflight = nil
	close(cl.done)
}

func (c *Cache) fallbackLocked(now time.Time) (*Payload, error) {
	if c.last == nil || now.Sub(c.last.fetchedAt) > c.opt.MaxStaleAge {
		return nil, ErrUnavailable
	}
	p := c.last.payload
	p.Stale = true
	return &p, nil
}

func toPayload(r *youtube.Result, at time.Time) Payload {
	videos := make([]Video, len(r.Videos))
	for i, v := range r.Videos {
		videos[i] = Video{VideoID: v.ID, Title: v.Title, ViewCount: v.ViewCount}
	}
	return Payload{
		Stats:      Stats{SubscriberCount: r.SubscriberCount, ViewsCount: r.ViewCount},
		Videos:     videos,
		UpdateTime: FormatTime(at),
	}
}

// FormatTime zwraca czas w ISO 8601 z przesunięciem strefy Europe/Warsaw.
func FormatTime(t time.Time) string {
	return t.In(warsaw).Format(time.RFC3339)
}
