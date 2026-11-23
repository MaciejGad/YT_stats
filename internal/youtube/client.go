// Package youtube pobiera statystyki kanału i jego filmów z YouTube Data API v3.
package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	_ "time/tzdata" // America/Los_Angeles do odnowienia limitu
)

// Domyślne wartości z wymagań.
const (
	DefaultBaseURL          = "https://www.googleapis.com/youtube/v3/"
	DefaultRequestTimeout   = 10 * time.Second
	DefaultMaxResponseBytes = 5 << 20
	DefaultMaxPages         = 200
	pageSize                = 50 // maksimum API dla playlistItems i videos
)

// ErrChannelNotFound oznacza, że API nie zwróciło kanału o podanym ID.
var ErrChannelNotFound = errors.New("YouTube API: nie znaleziono kanału")

// ErrTooManyPages oznacza przekroczenie limitu stron playlisty.
var ErrTooManyPages = errors.New("YouTube API: przekroczono limit stron playlisty")

// Result to wynik jednego pełnego pobrania. Liczniki to ciągi cyfr
// albo nil, gdy API ich nie udostępnia.
type Result struct {
	SubscriberCount *string
	ViewCount       *string
	Videos          []Video
}

// Video to film z playlisty przesłanych filmów.
type Video struct {
	ID        string
	Title     string
	ViewCount *string
}

// Options konfiguruje klienta. Zerowe pola dostają wartości domyślne.
type Options struct {
	ChannelID      string
	APIKey         string
	APIKeyFallback string // opcjonalny

	BaseURL          string
	HTTPClient       *http.Client
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	MaxPages         int
	Now              func() time.Time
	Log              *slog.Logger
}

// Client wykonuje zapytania do API. Bezpieczny dla wielu goroutyn.
type Client struct {
	channelID   string
	base        *url.URL
	http        *http.Client
	reqTimeout  time.Duration
	maxBytes    int64
	maxPages    int
	keys        *keyring
	log         *slog.Logger
	secretWords []string
}

// New tworzy klienta.
func New(o Options) (*Client, error) {
	if o.ChannelID == "" || o.APIKey == "" {
		return nil, errors.New("youtube: brak ID kanału lub klucza API")
	}
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	base, err := url.Parse(o.BaseURL)
	if err != nil || !strings.HasSuffix(base.Path, "/") {
		return nil, errors.New("youtube: adres API musi być poprawnym URL zakończonym /")
	}
	if o.HTTPClient == nil {
		o.HTTPClient = &http.Client{}
	}
	if o.RequestTimeout == 0 {
		o.RequestTimeout = DefaultRequestTimeout
	}
	if o.MaxResponseBytes == 0 {
		o.MaxResponseBytes = DefaultMaxResponseBytes
	}
	if o.MaxPages == 0 {
		o.MaxPages = DefaultMaxPages
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	secrets := []string{o.APIKey}
	if o.APIKeyFallback != "" {
		secrets = append(secrets, o.APIKeyFallback)
	}
	return &Client{
		channelID:   o.ChannelID,
		base:        base,
		http:        o.HTTPClient,
		reqTimeout:  o.RequestTimeout,
		maxBytes:    o.MaxResponseBytes,
		maxPages:    o.MaxPages,
		keys:        newKeyring(o.APIKey, o.APIKeyFallback, o.Now, o.Log),
		log:         o.Log,
		secretWords: secrets,
	}, nil
}

// Fetch pobiera statystyki kanału i wszystkie filmy z playlisty przesłanych.
// Zwraca wynik tylko wtedy, gdy udało się pobrać całość.
func (c *Client) Fetch(ctx context.Context) (*Result, error) {
	res, err := c.fetch(ctx)
	if err != nil {
		return nil, c.redact(err)
	}
	return res, nil
}

func (c *Client) fetch(ctx context.Context) (*Result, error) {
	ch, err := c.channel(ctx)
	if err != nil {
		return nil, err
	}
	ids, err := c.playlistVideoIDs(ctx, ch.uploads)
	if err != nil {
		return nil, err
	}
	videos, err := c.videos(ctx, ids)
	if err != nil {
		return nil, err
	}
	return &Result{SubscriberCount: ch.subscribers, ViewCount: ch.views, Videos: videos}, nil
}

type channelInfo struct {
	subscribers *string
	views       *string
	uploads     string
}

func (c *Client) channel(ctx context.Context) (*channelInfo, error) {
	var resp struct {
		Items []struct {
			Statistics struct {
				ViewCount             string `json:"viewCount"`
				SubscriberCount       string `json:"subscriberCount"`
				HiddenSubscriberCount bool   `json:"hiddenSubscriberCount"`
			} `json:"statistics"`
			ContentDetails struct {
				RelatedPlaylists struct {
					Uploads string `json:"uploads"`
				} `json:"relatedPlaylists"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	q := url.Values{
		"part":   {"statistics,contentDetails"},
		"id":     {c.channelID},
		"fields": {"items(statistics(viewCount,subscriberCount,hiddenSubscriberCount),contentDetails/relatedPlaylists/uploads)"},
	}
	if err := c.get(ctx, "channels", q, &resp); err != nil {
		return nil, err
	}
	if len(resp.Items) == 0 {
		return nil, ErrChannelNotFound
	}
	it := resp.Items[0]
	if it.ContentDetails.RelatedPlaylists.Uploads == "" {
		return nil, errors.New("YouTube API: kanał nie ma playlisty przesłanych filmów")
	}
	info := &channelInfo{
		views:   counter(it.Statistics.ViewCount),
		uploads: it.ContentDetails.RelatedPlaylists.Uploads,
	}
	if !it.Statistics.HiddenSubscriberCount {
		info.subscribers = counter(it.Statistics.SubscriberCount)
	}
	return info, nil
}

// playlistVideoIDs zwraca ID filmów w kolejności playlisty, bez duplikatów.
func (c *Client) playlistVideoIDs(ctx context.Context, playlistID string) ([]string, error) {
	var ids []string
	seen := map[string]bool{}
	token := ""
	for page := 0; ; page++ {
		if page >= c.maxPages {
			return nil, ErrTooManyPages
		}
		var resp struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				ContentDetails struct {
					VideoID string `json:"videoId"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		q := url.Values{
			"part":       {"contentDetails"},
			"playlistId": {playlistID},
			"maxResults": {fmt.Sprint(pageSize)},
			"fields":     {"nextPageToken,items/contentDetails/videoId"},
		}
		if token != "" {
			q.Set("pageToken", token)
		}
		err := c.get(ctx, "playlistItems", q, &resp)
		var apiErr *APIError
		if page == 0 && errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound && apiErr.Reason == "playlistNotFound" {
			// Kanał bez filmów nie ma playlisty przesłanych.
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		for _, it := range resp.Items {
			id := it.ContentDetails.VideoID
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
		if resp.NextPageToken == "" {
			return ids, nil
		}
		if resp.NextPageToken == token {
			return nil, errors.New("YouTube API: powtórzony token strony playlisty")
		}
		token = resp.NextPageToken
	}
}

// videos pobiera tytuły i liczniki grupami po 50, zachowując kolejność ids.
// Filmy, których API nie zwróciło (prywatne, usunięte), są pomijane.
func (c *Client) videos(ctx context.Context, ids []string) ([]Video, error) {
	byID := make(map[string]Video, len(ids))
	for start := 0; start < len(ids); start += pageSize {
		end := min(start+pageSize, len(ids))
		var resp struct {
			Items []struct {
				ID      string `json:"id"`
				Snippet struct {
					Title string `json:"title"`
				} `json:"snippet"`
				Statistics struct {
					ViewCount string `json:"viewCount"`
				} `json:"statistics"`
			} `json:"items"`
		}
		q := url.Values{
			"part":   {"snippet,statistics"},
			"id":     {strings.Join(ids[start:end], ",")},
			"fields": {"items(id,snippet/title,statistics/viewCount)"},
		}
		if err := c.get(ctx, "videos", q, &resp); err != nil {
			return nil, err
		}
		for _, it := range resp.Items {
			byID[it.ID] = Video{ID: it.ID, Title: it.Snippet.Title, ViewCount: counter(it.Statistics.ViewCount)}
		}
	}
	out := make([]Video, 0, len(ids))
	for _, id := range ids {
		if v, ok := byID[id]; ok {
			out = append(out, v)
		}
	}
	return out, nil
}

// get wykonuje jedno zapytanie, a przy wyczerpaniu limitu klucza głównego
// ponawia je kluczem zapasowym.
func (c *Client) get(ctx context.Context, endpoint string, q url.Values, out any) error {
	key, label := c.keys.current()
	err := c.do(ctx, endpoint, q, key, out)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.quotaExhausted() && c.keys.exhausted(label) {
		key, _ = c.keys.current()
		return c.do(ctx, endpoint, q, key, out)
	}
	return err
}

func (c *Client) do(ctx context.Context, endpoint string, q url.Values, key string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.reqTimeout)
	defer cancel()

	u := c.base.ResolveReference(&url.URL{Path: endpoint, RawQuery: q.Encode()})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	// Klucz w nagłówku, a nie w URL, żeby nie trafiał do komunikatów błędów.
	req.Header.Set("X-Goog-Api-Key", key)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("YouTube API %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return fmt.Errorf("YouTube API %s: odczyt odpowiedzi: %w", endpoint, err)
	}
	if int64(len(body)) > c.maxBytes {
		return fmt.Errorf("YouTube API %s: odpowiedź większa niż %d bajtów", endpoint, c.maxBytes)
	}
	if resp.StatusCode != http.StatusOK {
		return parseAPIError(endpoint, resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("YouTube API %s: niepoprawny JSON: %w", endpoint, err)
	}
	return nil
}

// counter zwraca wskaźnik do licznika albo nil, gdy brak wartości lub nie
// jest ciągiem cyfr.
func counter(s string) *string {
	if s == "" {
		return nil
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return nil
		}
	}
	return &s
}

// redact usuwa klucze API z komunikatu błędu, zachowując możliwość
// sprawdzania errors.Is / errors.As.
func (c *Client) redact(err error) error {
	msg := err.Error()
	clean := msg
	for _, s := range c.secretWords {
		clean = strings.ReplaceAll(clean, s, "[ukryty-klucz]")
	}
	if clean == msg {
		return err
	}
	return &redactedError{msg: clean, err: err}
}

type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }
