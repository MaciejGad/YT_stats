package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// tokenBytes is the length of random identifiers (sessions, CSRF).
const tokenBytes = 32

// tokenLen is the identifier length after base64url encoding without padding.
var tokenLen = base64.RawURLEncoding.EncodedLen(tokenBytes)

// newToken returns 32 random bytes encoded as base64url (43 characters).
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// validToken checks the identifier format before any further processing.
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

// hashToken returns the SHA-256 hash of an identifier in hex.
// Only this form goes into the session file.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// LogoutToken derives the logout form's CSRF token from the session
// identifier. It needs no storage, and it cannot be computed without the
// session identifier.
func LogoutToken(sessionToken string) string {
	m := hmac.New(sha256.New, []byte(sessionToken))
	m.Write([]byte("yt-stats logout csrf"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckLogoutToken compares the logout form token in constant time.
func CheckLogoutToken(sessionToken, formToken string) bool {
	return hmac.Equal([]byte(LogoutToken(sessionToken)), []byte(formToken))
}

func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
