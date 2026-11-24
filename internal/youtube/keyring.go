package youtube

import (
	"log/slog"
	"sync"
	"time"
)

// Key labels in logs. Key values are never logged.
const (
	keyPrimary  = "primary"
	keyFallback = "fallback"
)

// quotaZone is the zone in which YouTube resets the daily quota (Pacific midnight).
var quotaZone = mustLoadLocation("America/Los_Angeles")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// nextQuotaReset returns the next Pacific midnight after t.
func nextQuotaReset(t time.Time) time.Time {
	l := t.In(quotaZone)
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, quotaZone)
}

// keyring chooses the API key. After the primary key's quota is exhausted it uses
// the fallback until the next quota reset.
type keyring struct {
	primary, fallback string
	now               func() time.Time
	log               *slog.Logger

	mu            sync.Mutex
	fallbackUntil time.Time
}

func newKeyring(primary, fallback string, now func() time.Time, log *slog.Logger) *keyring {
	return &keyring{primary: primary, fallback: fallback, now: now, log: log}
}

// current returns the current key and its label.
func (k *keyring) current() (key, label string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fallback != "" && k.now().Before(k.fallbackUntil) {
		return k.fallback, keyFallback
	}
	if !k.fallbackUntil.IsZero() {
		k.fallbackUntil = time.Time{}
		k.log.Info("powrót do głównego klucza API po odnowieniu limitu")
	}
	return k.primary, keyPrimary
}

// exhausted reports that the quota of the key labelled label is exhausted. It returns true
// when it is worth retrying the request with another key.
func (k *keyring) exhausted(label string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fallback == "" {
		k.log.Warn("wyczerpany limit głównego klucza API, brak klucza zapasowego")
		return false
	}
	if label != keyPrimary {
		k.log.Warn("wyczerpany limit zapasowego klucza API")
		return false
	}
	now := k.now()
	if now.Before(k.fallbackUntil) {
		// Another request has already switched the key.
		return true
	}
	k.fallbackUntil = nextQuotaReset(now)
	k.log.Warn("wyczerpany limit głównego klucza API, przełączam na zapasowy",
		"until", k.fallbackUntil.Format(time.RFC3339))
	return true
}
