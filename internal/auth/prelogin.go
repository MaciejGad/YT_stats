package auth

import (
	"crypto/subtle"
	"sync"
	"time"
)

// Values from the requirements for pre-login sessions.
const (
	DefaultPreLoginTTL = 30 * time.Minute
	DefaultPreLoginMax = 1000
)

// PreLogin keeps short pre-login sessions in memory; they bind the
// login form's CSRF token to the browser's cookie.
type PreLogin struct {
	ttl time.Duration
	max int
	now func() time.Time

	mu      sync.Mutex
	entries map[string]preEntry
}

type preEntry struct {
	csrf    string
	created time.Time
}

// NewPreLogin creates the store. Zero values give the defaults.
func NewPreLogin(ttl time.Duration, max int, now func() time.Time) *PreLogin {
	if ttl == 0 {
		ttl = DefaultPreLoginTTL
	}
	if max == 0 {
		max = DefaultPreLoginMax
	}
	if now == nil {
		now = time.Now
	}
	return &PreLogin{ttl: ttl, max: max, now: now, entries: map[string]preEntry{}}
}

// TTL returns the lifetime of a pre-login session.
func (p *PreLogin) TTL() time.Duration { return p.ttl }

// Ensure returns the CSRF token of the existing session id or creates a new session.
// created says whether a new cookie must be set.
func (p *PreLogin) Ensure(id string) (newID, csrf string, created bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if e, ok := p.entries[id]; ok && validToken(id) && now.Sub(e.created) < p.ttl {
		return id, e.csrf, false, nil
	}

	newID, err = newToken()
	if err != nil {
		return "", "", false, err
	}
	csrf, err = newToken()
	if err != nil {
		return "", "", false, err
	}
	p.evictLocked(now)
	p.entries[newID] = preEntry{csrf: csrf, created: now}
	return newID, csrf, true, nil
}

// Check verifies in constant time that the form comes from session id.
func (p *PreLogin) Check(id, csrf string) bool {
	if !validToken(id) || csrf == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.entries[id]
	if !ok || p.now().Sub(e.created) >= p.ttl {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(e.csrf), []byte(csrf)) == 1
}

// Delete removes the session after a successful login.
func (p *PreLogin) Delete(id string) {
	p.mu.Lock()
	delete(p.entries, id)
	p.mu.Unlock()
}

// Len returns the number of stored sessions.
func (p *PreLogin) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// evictLocked makes room for a new entry: it removes expired ones and, when
// that is not enough, the oldest ones.
func (p *PreLogin) evictLocked(now time.Time) {
	if len(p.entries) < p.max {
		return
	}
	for k, e := range p.entries {
		if now.Sub(e.created) >= p.ttl {
			delete(p.entries, k)
		}
	}
	for len(p.entries) >= p.max {
		var oldestK string
		var oldest time.Time
		for k, e := range p.entries {
			if oldestK == "" || e.created.Before(oldest) {
				oldestK, oldest = k, e.created
			}
		}
		delete(p.entries, oldestK)
	}
}
