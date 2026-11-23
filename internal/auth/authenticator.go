package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"time"
)

// Limity z wymagań.
const (
	MaxUsernameLen       = 64
	DefaultMaxConcurrent = 2
	DefaultWaitTimeout   = 3 * time.Second
)

// ErrBusy oznacza, że wszystkie miejsca na weryfikację hasła są zajęte
// dłużej niż czas oczekiwania (HTTP 503).
var ErrBusy = errors.New("zbyt wiele równoległych weryfikacji hasła")

// Verifier sprawdza hasło. Implementuje go *Hash.
type Verifier interface {
	Verify(password []byte) bool
}

// Authenticator sprawdza nazwę użytkownika i hasło jednego konta.
type Authenticator struct {
	username []byte
	real     Verifier
	dummy    Verifier
	sem      chan struct{}
	wait     time.Duration
}

// AuthOptions konfiguruje Authenticator. Zerowe pola dają wartości domyślne.
type AuthOptions struct {
	MaxConcurrent int
	WaitTimeout   time.Duration
	// Dummy zastępuje fikcyjny hash (testy). Domyślnie hash losowego hasła
	// z tymi samymi parametrami co prawdziwy, żeby czas był taki sam.
	Dummy Verifier
}

// NewAuthenticator tworzy weryfikator dla konta z konfiguracji.
func NewAuthenticator(username string, hash *Hash, o AuthOptions) (*Authenticator, error) {
	return newAuthenticator(username, hash, hash.Params, o)
}

func newAuthenticator(username string, real Verifier, p Params, o AuthOptions) (*Authenticator, error) {
	if o.MaxConcurrent == 0 {
		o.MaxConcurrent = DefaultMaxConcurrent
	}
	if o.WaitTimeout == 0 {
		o.WaitTimeout = DefaultWaitTimeout
	}
	if o.Dummy == nil {
		pw := make([]byte, 32)
		if _, err := rand.Read(pw); err != nil {
			return nil, err
		}
		s, err := HashPassword([]byte(base64URL(pw)), p)
		if err != nil {
			return nil, err
		}
		d, err := ParseHash(s)
		if err != nil {
			return nil, err
		}
		o.Dummy = d
	}
	return &Authenticator{
		username: []byte(username),
		real:     real,
		dummy:    o.Dummy,
		sem:      make(chan struct{}, o.MaxConcurrent),
		wait:     o.WaitTimeout,
	}, nil
}

// Check zwraca true dla poprawnej pary nazwa–hasło. Przy nieznanej nazwie
// i tak wykonuje Argon2id (na fikcyjnym hashu), żeby czas odpowiedzi nie
// zdradzał poprawności nazwy. Za długie dane są odrzucane bez Argon2id.
func (a *Authenticator) Check(ctx context.Context, username, password string) (bool, error) {
	if username == "" || password == "" || len(username) > MaxUsernameLen || len(password) > MaxPasswordLen {
		return false, nil
	}

	// select wybiera losowo spośród gotowych przypadków, więc anulowane
	// żądanie sprawdzamy przed próbą zajęcia miejsca.
	if err := ctx.Err(); err != nil {
		return false, err
	}

	timer := time.NewTimer(a.wait)
	defer timer.Stop()
	select {
	case a.sem <- struct{}{}:
	case <-timer.C:
		return false, ErrBusy
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-a.sem }()

	userOK := subtle.ConstantTimeCompare([]byte(username), a.username) == 1
	v := a.dummy
	if userOK {
		v = a.real
	}
	passOK := v.Verify([]byte(password))
	return userOK && passOK, nil
}
