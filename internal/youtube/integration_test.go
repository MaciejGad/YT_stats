//go:build integration

// Test z prawdziwym YouTube Data API. Uruchamiany ręcznie:
//
//	YT_API_KEY=... go test -tags integration -v -run Integration ./internal/youtube
//
// Opcjonalnie YT_CHANNEL_ID (domyślnie kanał @YarnPowerPL).
// Zużywa kilka jednostek dziennego limitu klucza.
package youtube

import (
	"context"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"
)

const (
	integrationChannel = "UC5711W83ebIxkC8bHjOKI7g"
	integrationHandle  = "@YarnPowerPL"
)

func integrationClient(t *testing.T) *Client {
	t.Helper()
	key := os.Getenv("YT_API_KEY")
	if key == "" {
		t.Skip("ustaw YT_API_KEY, żeby uruchomić test z prawdziwym API")
	}
	channel := os.Getenv("YT_CHANNEL_ID")
	if channel == "" {
		channel = integrationChannel
	}
	c, err := New(Options{
		ChannelID: channel,
		APIKey:    key,
		Log:       slog.New(slog.NewTextHandler(os.Stderr, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestIntegrationChannelIDMatchesHandle(t *testing.T) {
	c := integrationClient(t)
	var resp struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	q := url.Values{"part": {"id"}, "forHandle": {integrationHandle}}
	if err := c.get(context.Background(), "channels", q, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("forHandle=%s zwrócił %d kanałów", integrationHandle, len(resp.Items))
	}
	if got := resp.Items[0].ID; got != c.channelID {
		t.Errorf("ID kanału %s = %s, w konfiguracji %s", integrationHandle, got, c.channelID)
	}
	t.Logf("%s → %s", integrationHandle, resp.Items[0].ID)
}

func TestIntegrationFetch(t *testing.T) {
	c := integrationClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	res, err := c.Fetch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pobrano w %v: subskrybenci=%s, wyświetlenia=%s, filmów=%d",
		time.Since(start).Round(time.Millisecond), str(res.SubscriberCount), str(res.ViewCount), len(res.Videos))

	if res.ViewCount == nil {
		t.Error("brak liczby wyświetleń kanału")
	}
	if len(res.Videos) == 0 {
		t.Fatal("brak filmów")
	}
	seen := map[string]bool{}
	for _, v := range res.Videos {
		if v.ID == "" || v.Title == "" {
			t.Errorf("niekompletny film: %+v", v)
		}
		if seen[v.ID] {
			t.Errorf("duplikat %s", v.ID)
		}
		seen[v.ID] = true
	}
	for i, v := range res.Videos[:min(5, len(res.Videos))] {
		t.Logf("%d. %s (%s) — %s wyświetleń", i+1, v.Title, v.ID, str(v.ViewCount))
	}
}
