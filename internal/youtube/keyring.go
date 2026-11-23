package youtube

import (
	"log/slog"
	"sync"
	"time"
)

// Etykiety kluczy w logach. Wartości kluczy nigdy nie są logowane.
const (
	keyPrimary  = "primary"
	keyFallback = "fallback"
)

// quotaZone to strefa, w której YouTube odnawia dzienny limit (północ czasu pacyficznego).
var quotaZone = mustLoadLocation("America/Los_Angeles")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// nextQuotaReset zwraca najbliższą północ czasu pacyficznego po t.
func nextQuotaReset(t time.Time) time.Time {
	l := t.In(quotaZone)
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, quotaZone)
}

// keyring wybiera klucz API. Po wyczerpaniu limitu klucza głównego używa
// zapasowego do najbliższego odnowienia limitu.
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

// current zwraca aktualny klucz i jego etykietę.
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

// exhausted zgłasza wyczerpanie limitu klucza o etykiecie label. Zwraca true,
// gdy po zgłoszeniu warto ponowić żądanie innym kluczem.
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
		// Inne żądanie już przełączyło klucz.
		return true
	}
	k.fallbackUntil = nextQuotaReset(now)
	k.log.Warn("wyczerpany limit głównego klucza API, przełączam na zapasowy",
		"until", k.fallbackUntil.Format(time.RFC3339))
	return true
}
