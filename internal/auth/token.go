package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// tokenBytes to długość losowych identyfikatorów (sesje, CSRF).
const tokenBytes = 32

// tokenLen to długość identyfikatora po zakodowaniu base64url bez dopełnienia.
var tokenLen = base64.RawURLEncoding.EncodedLen(tokenBytes)

// newToken zwraca 32 losowe bajty zakodowane base64url (43 znaki).
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validToken sprawdza format identyfikatora, zanim trafi do dalszej obróbki.
func validToken(s string) bool {
	if len(s) != tokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// hashToken zwraca hash SHA-256 identyfikatora w zapisie szesnastkowym.
// Tylko ta postać trafia do pliku sesji.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// LogoutToken wyprowadza token CSRF formularza wylogowania z identyfikatora
// sesji. Nie wymaga przechowywania, a bez identyfikatora sesji nie da się go
// obliczyć.
func LogoutToken(sessionToken string) string {
	m := hmac.New(sha256.New, []byte(sessionToken))
	m.Write([]byte("yt-stats logout csrf"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckLogoutToken porównuje token formularza wylogowania w stałym czasie.
func CheckLogoutToken(sessionToken, formToken string) bool {
	return hmac.Equal([]byte(LogoutToken(sessionToken)), []byte(formToken))
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
