// Package youtube fetches channel and video statistics from YouTube Data API v3.
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

	_ "time/tzdata" // America/Los_Angeles for the quota reset
)

// Default values from the requirements.
const (
	DefaultBaseURL          = "https://www.googleapis.com/youtube/v3/"
	DefaultRequestTimeout   = 10 * time.Second
	DefaultMaxResponseBytes = 5 << 20
	DefaultMaxPages         = 200
	pageSize                = 50 // the API maximum for playlistItems and videos
)

// ErrChannelNotFound means the API returned no channel with the given ID.
var ErrChannelNotFound = errors.New("YouTube API: nie znaleziono kanału")

// ErrTooManyPages means the playlist page limit was exceeded.
var ErrTooManyPages = errors.New("YouTube API: przekroczono limit stron playlisty")

// Result is the result of one complete fetch. Counters are digit strings
// or nil when the API does not provide them.
type Result struct {
	Channel         Channel
	SubscriberCount *string
	ViewCount       *string
	Videos          []Video
}

// Channel describes the channel: its name, handle and avatar.
type Channel struct {
	ID           string
	Title        string
	Handle       string // e.g. @yarnpowerpl; empty when the channel has none
	ThumbnailURL string // avatar on a Google image host; empty when missing
}

// Video is a video from the uploads playlist.
type Video struct {
	ID        string
	Title     string
	ViewCount *string
}

// Options configures the client. Zero fields get default values.
type Options struct {
	ChannelID      string
	APIKey         string
	APIKeyFallback string // optional

	BaseURL          string
	HTTPClient       *http.Client
	RequestTimeout   time.Duration
	MaxResponseBytes int64
	MaxPages         int
	MaxImageBytes    int64
	ImageHosts       []string // hosts allowed for FetchImage; default DefaultImageHosts
	Now              func() time.Time
	Log              *slog.Logger
}

// Client makes API requests. Safe for use by multiple goroutines.
type Client struct {
	channelID   string
	base        *url.URL
	http        *http.Client
	reqTimeout  time.Duration
	maxBytes    int64
	maxPages    int
	maxImage    int64
	imageHosts  []string
	keys        *keyring
	log         *slog.Logger
	secretWords []string
}

// New creates a client.
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
	if o.MaxImageBytes == 0 {
		o.MaxImageBytes = DefaultMaxImageBytes
	}
	if o.ImageHosts == nil {
		o.ImageHosts = DefaultImageHosts
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
		maxImage:    o.MaxImageBytes,
		imageHosts:  o.ImageHosts,
		keys:        newKeyring(o.APIKey, o.APIKeyFallback, o.Now, o.Log),
		log:         o.Log,
		secretWords: secrets,
	}, nil
}

// Fetch gets the channel statistics and all videos from the uploads playlist.
// It returns a result only when everything was fetched.
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
	return &Result{Channel: ch.Channel, SubscriberCount: ch.subscribers, ViewCount: ch.views, Videos: videos}, nil
}

type channelInfo struct {
	Channel
	subscribers *string
	views       *string
	uploads     string
}

func (c *Client) channel(ctx context.Context) (*channelInfo, error) {
	var resp struct {
		Items []struct {
			Snippet struct {
				Title      string `json:"title"`
				CustomURL  string `json:"customUrl"`
				Thumbnails map[string]struct {
					URL string `json:"url"`
				} `json:"thumbnails"`
			} `json:"snippet"`
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
		"part":   {"snippet,statistics,contentDetails"},
		"id":     {c.channelID},
		"fields": {"items(snippet(title,customUrl,thumbnails),statistics(viewCount,subscriberCount,hiddenSubscriberCount),contentDetails/relatedPlaylists/uploads)"},
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
		Channel: Channel{
			ID:           c.channelID,
			Title:        strings.TrimSpace(it.Snippet.Title),
			Handle:       validHandle(it.Snippet.CustomURL),
			ThumbnailURL: pickThumbnail(it.Snippet.Thumbnails),
		},
		views:   counter(it.Statistics.ViewCount),
		uploads: it.ContentDetails.RelatedPlaylists.Uploads,
	}
	if !it.Statistics.HiddenSubscriberCount {
		info.subscribers = counter(it.Statistics.SubscriberCount)
	}
	return info, nil
}

// playlistVideoIDs returns video IDs in playlist order, without duplicates.
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
			// A channel without videos has no uploads playlist.
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

// videos fetches titles and counters in batches of 50, keeping the order of ids.
// Videos the API did not return (private, deleted) are skipped.
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

// get makes one request and, when the primary key's quota is exhausted,
// retries it with the fallback key.
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
	// The key goes in a header, not in the URL, so it never ends up in error messages.
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

// counter returns a pointer to the counter, or nil when there is no value or it
// is not a digit string.
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

// redact removes API keys from an error message while keeping
// errors.Is / errors.As working.
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
