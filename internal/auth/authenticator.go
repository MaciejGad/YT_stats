package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"time"
)

// Limits from the requirements.
const (
	MaxUsernameLen       = 64
	DefaultMaxConcurrent = 2
	DefaultWaitTimeout   = 3 * time.Second
)

// ErrBusy means that all password verification slots have been busy
// for longer than the wait time (HTTP 503).
var ErrBusy = errors.New("zbyt wiele równoległych weryfikacji hasła")

// Verifier checks a password. *Hash implements it.
type Verifier interface {
	Verify(password []byte) bool
}

// Authenticator checks the username and password of the single account.
type Authenticator struct {
	username []byte
	real     Verifier
	dummy    Verifier
	sem      chan struct{}
	wait     time.Duration
}

// AuthOptions configures an Authenticator. Zero fields get default values.
type AuthOptions struct {
	MaxConcurrent int
	WaitTimeout   time.Duration
	// Dummy replaces the dummy hash (tests). By default it is the hash of a random
	// password with the same parameters as the real one, so timing is the same.
	Dummy Verifier
}

// NewAuthenticator creates a verifier for the account from the configuration.
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

// Check returns true for a correct username–password pair. For an unknown
// username it still runs Argon2id (on the dummy hash), so the response time
// does not reveal whether the username is correct. Oversized input is rejected without Argon2id.
func (a *Authenticator) Check(ctx context.Context, username, password string) (bool, error) {
	if username == "" || password == "" || len(username) > MaxUsernameLen || len(password) > MaxPasswordLen {
		return false, nil
	}

	// select picks randomly among ready cases, so a cancelled request
	// is checked before trying to take a slot.
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
