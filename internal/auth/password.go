// Package auth obsługuje hasła, sesje, CSRF i limity logowania.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// MaxPasswordLen to limit długości hasła w bajtach.
const MaxPasswordLen = 256

// Params to parametry Argon2id.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

// DefaultParams to minimum zalecane przez OWASP: m=19 MiB, t=2, p=1.
var DefaultParams = Params{Memory: 19 * 1024, Time: 2, Threads: 1, SaltLen: 16, KeyLen: 32}

// Granice akceptowanych parametrów chronią przed konfiguracją, która
// zużyłaby całą pamięć serwera albo byłaby trywialna do złamania.
const (
	minMemory = 8 * 1024  // 8 MiB
	maxMemory = 50 * 1024 // 50 MiB
	maxTime   = 10
	maxThread = 4
	minSalt   = 8
	minKey    = 16
	maxKey    = 64
)

var b64 = base64.RawStdEncoding

// Hash to sparsowany hash Argon2id w formacie PHC.
type Hash struct {
	Params Params
	salt   []byte
	key    []byte
}

// HashPassword tworzy hash w formacie
// $argon2id$v=19$m=19456,t=2,p=1$<sól>$<klucz>.
func HashPassword(password []byte, p Params) (string, error) {
	if len(password) == 0 {
		return "", errors.New("hasło nie może być puste")
	}
	if len(password) > MaxPasswordLen {
		return "", fmt.Errorf("hasło jest dłuższe niż %d bajtów", MaxPasswordLen)
	}
	if err := p.check(); err != nil {
		return "", err
	}
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("losowanie soli: %w", err)
	}
	key := argon2.IDKey(password, salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// ParseHash parsuje hash PHC i sprawdza parametry.
// Komunikaty błędów nie zawierają fragmentów hasha.
func ParseHash(s string) (*Hash, error) {
	parts := strings.Split(s, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", sól, klucz
	if len(parts) != 6 || parts[0] != "" {
		return nil, errors.New("oczekiwano formatu PHC $argon2id$v=19$m=..,t=..,p=..$sól$klucz")
	}
	if parts[1] != "argon2id" {
		return nil, errors.New("obsługiwany jest tylko algorytm argon2id")
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return nil, fmt.Errorf("obsługiwana jest tylko wersja v=%d", argon2.Version)
	}

	var p Params
	seen := map[string]bool{}
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || seen[k] {
			return nil, errors.New("niepoprawne parametry m,t,p")
		}
		seen[k] = true
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return nil, errors.New("niepoprawne parametry m,t,p")
		}
		switch k {
		case "m":
			p.Memory = uint32(n)
		case "t":
			p.Time = uint32(n)
		case "p":
			if n > 255 {
				return nil, errors.New("niepoprawne parametry m,t,p")
			}
			p.Threads = uint8(n)
		default:
			return nil, errors.New("niepoprawne parametry m,t,p")
		}
	}
	if len(seen) != 3 {
		return nil, errors.New("brak któregoś z parametrów m,t,p")
	}

	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return nil, errors.New("sól nie jest poprawnym base64")
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil {
		return nil, errors.New("klucz nie jest poprawnym base64")
	}
	p.SaltLen = uint32(len(salt))
	p.KeyLen = uint32(len(key))
	if err := p.check(); err != nil {
		return nil, err
	}
	return &Hash{Params: p, salt: salt, key: key}, nil
}

// Verify porównuje hasło z hashem w stałym czasie.
func (h *Hash) Verify(password []byte) bool {
	if len(password) == 0 || len(password) > MaxPasswordLen {
		return false
	}
	got := argon2.IDKey(password, h.salt, h.Params.Time, h.Params.Memory, h.Params.Threads, h.Params.KeyLen)
	return subtle.ConstantTimeCompare(got, h.key) == 1
}

func (p Params) check() error {
	switch {
	case p.Memory < minMemory || p.Memory > maxMemory:
		return fmt.Errorf("parametr m musi być w zakresie %d–%d KiB", minMemory, maxMemory)
	case p.Time < 1 || p.Time > maxTime:
		return fmt.Errorf("parametr t musi być w zakresie 1–%d", maxTime)
	case p.Threads < 1 || p.Threads > maxThread:
		return fmt.Errorf("parametr p musi być w zakresie 1–%d", maxThread)
	case p.SaltLen < minSalt:
		return fmt.Errorf("sól musi mieć co najmniej %d bajtów", minSalt)
	case p.KeyLen < minKey || p.KeyLen > maxKey:
		return fmt.Errorf("klucz musi mieć %d–%d bajtów", minKey, maxKey)
	}
	return nil
}
