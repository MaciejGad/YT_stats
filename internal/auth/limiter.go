package auth

import (
	"sync"
	"time"
)

// LimiterOptions to limity nieudanych logowań. Zerowe pola dostają wartości
// z wymagań.
type LimiterOptions struct {
	PerIP        int           // 5
	PerIPWindow  time.Duration // 15 min
	Global       int           // 50
	GlobalWindow time.Duration // 1 h
	MaxIPs       int           // 10 000
	Now          func() time.Time
}

// Limiter liczy nieudane logowania w przesuwanych oknach czasu: osobno dla
// każdego adresu IP i łącznie dla wszystkich.
type Limiter struct {
	o LimiterOptions

	mu     sync.Mutex
	perIP  map[string][]time.Time // czasy ostatnich porażek, najstarsza pierwsza
	global []time.Time
}

// NewLimiter tworzy limiter.
func NewLimiter(o LimiterOptions) *Limiter {
	if o.PerIP == 0 {
		o.PerIP = 5
	}
	if o.PerIPWindow == 0 {
		o.PerIPWindow = 15 * time.Minute
	}
	if o.Global == 0 {
		o.Global = 50
	}
	if o.GlobalWindow == 0 {
		o.GlobalWindow = time.Hour
	}
	if o.MaxIPs == 0 {
		o.MaxIPs = 10_000
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Limiter{o: o, perIP: map[string][]time.Time{}}
}

// Allow mówi, czy z adresu ip można teraz spróbować się zalogować.
// Przy odmowie zwraca czas do odblokowania.
func (l *Limiter) Allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.o.Now()

	var wait time.Duration
	fails := prune(l.perIP[ip], now, l.o.PerIPWindow)
	if len(fails) == 0 {
		delete(l.perIP, ip)
	} else {
		l.perIP[ip] = fails
	}
	if len(fails) >= l.o.PerIP {
		wait = fails[len(fails)-l.o.PerIP].Add(l.o.PerIPWindow).Sub(now)
	}
	l.global = prune(l.global, now, l.o.GlobalWindow)
	if len(l.global) >= l.o.Global {
		wait = max(wait, l.global[len(l.global)-l.o.Global].Add(l.o.GlobalWindow).Sub(now))
	}
	return wait <= 0, wait
}

// Failure zapisuje nieudane logowanie z adresu ip.
func (l *Limiter) Failure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.o.Now()

	if _, ok := l.perIP[ip]; !ok && len(l.perIP) >= l.o.MaxIPs {
		l.evictLocked(now)
	}
	fails := append(prune(l.perIP[ip], now, l.o.PerIPWindow), now)
	if len(fails) > l.o.PerIP {
		fails = fails[len(fails)-l.o.PerIP:]
	}
	l.perIP[ip] = fails

	l.global = append(prune(l.global, now, l.o.GlobalWindow), now)
	if len(l.global) > l.o.Global {
		l.global = l.global[len(l.global)-l.o.Global:]
	}
}

// Success zeruje licznik adresu ip po udanym logowaniu.
func (l *Limiter) Success(ip string) {
	l.mu.Lock()
	delete(l.perIP, ip)
	l.mu.Unlock()
}

// TrackedIPs zwraca liczbę pamiętanych adresów.
func (l *Limiter) TrackedIPs() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.perIP)
}

// evictLocked usuwa adresy bez porażek w oknie, a gdy to nie wystarczy,
// adres z najdawniejszą ostatnią porażką.
func (l *Limiter) evictLocked(now time.Time) {
	for ip, f := range l.perIP {
		if len(prune(f, now, l.o.PerIPWindow)) == 0 {
			delete(l.perIP, ip)
		}
	}
	for len(l.perIP) >= l.o.MaxIPs {
		var oldestIP string
		var oldest time.Time
		for ip, f := range l.perIP {
			last := f[len(f)-1]
			if oldestIP == "" || last.Before(oldest) {
				oldestIP, oldest = ip, last
			}
		}
		delete(l.perIP, oldestIP)
	}
}

// prune usuwa czasy starsze niż okno. Zwraca podwycinek tej samej tablicy.
func prune(ts []time.Time, now time.Time, window time.Duration) []time.Time {
	i := 0
	for i < len(ts) && now.Sub(ts[i]) >= window {
		i++
	}
	return ts[i:]
}
