// Package config wczytuje i waliduje konfigurację usługi.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"

	"yt-stats/internal/auth"
)

// maxFileSize ogranicza rozmiar pliku konfiguracji.
const maxFileSize = 64 << 10

// MaxUsernameLen to limit długości nazwy użytkownika w bajtach.
const MaxUsernameLen = 64

var channelIDPattern = regexp.MustCompile(`^UC[0-9A-Za-z_-]{22}$`)

// Config to zawartość pliku konfiguracji.
type Config struct {
	Listen         string `json:"listen"`
	ChannelID      string `json:"channel_id"`
	APIKey         string `json:"api_key"`
	APIKeyFallback string `json:"api_key_fallback"`
	Username       string `json:"username"`
	PasswordHash   string `json:"password_hash"`
	SessionFile    string `json:"session_file"`
	WebDir         string `json:"web_dir"`
}

// ValidationError zbiera wszystkie problemy z konfiguracją naraz.
// Komunikaty zawierają nazwy pól, nigdy ich wartości.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return "niepoprawna konfiguracja:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// Load wczytuje plik konfiguracji i go waliduje.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("otwarcie konfiguracji: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("odczyt konfiguracji: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("konfiguracja %s nie jest zwykłym plikiem", path)
	}

	data, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("odczyt konfiguracji: %w", err)
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("konfiguracja %s jest większa niż %d KiB", path, maxFileSize>>10)
	}

	cfg, err := Parse(data)
	if err != nil {
		return nil, err
	}

	var problems []string
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		problems = append(problems, fmt.Sprintf(
			"plik konfiguracji ma uprawnienia %04o i jest dostępny dla innych użytkowników; ustaw 0600 lub 0640", perm))
	}
	if verr := cfg.Validate(); verr != nil {
		var ve *ValidationError
		if errors.As(verr, &ve) {
			problems = append(problems, ve.Problems...)
		} else {
			return nil, verr
		}
	}
	if len(problems) > 0 {
		return nil, &ValidationError{Problems: problems}
	}
	return cfg, nil
}

// Parse dekoduje JSON bez walidacji pól. Nieznane pola są błędem.
func Parse(data []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("format konfiguracji: %w", sanitizeJSONError(err))
	}
	if dec.More() {
		return nil, errors.New("format konfiguracji: dodatkowe dane po obiekcie JSON")
	}
	return &cfg, nil
}

// sanitizeJSONError usuwa z błędów dekodera fragmenty wartości.
func sanitizeJSONError(err error) error {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return fmt.Errorf("pole %q ma zły typ (oczekiwano %s)", typeErr.Field, typeErr.Type)
	}
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return fmt.Errorf("błąd składni JSON w bajcie %d", syntaxErr.Offset)
	}
	// Pozostałe błędy (np. nieznane pole) zawierają tylko nazwę pola.
	return err
}

// Validate sprawdza wszystkie pola i zwraca *ValidationError z listą problemów.
func (c *Config) Validate() error {
	var p []string
	add := func(format string, args ...any) { p = append(p, fmt.Sprintf(format, args...)) }

	switch {
	case c.Listen == "":
		add("brak pola listen")
	default:
		if err := validateListen(c.Listen); err != nil {
			add("pole listen: %v", err)
		}
	}

	switch {
	case c.ChannelID == "":
		add("brak pola channel_id")
	case !channelIDPattern.MatchString(c.ChannelID):
		add("pole channel_id musi mieć postać UC + 22 znaki")
	}

	switch {
	case c.APIKey == "":
		add("brak pola api_key")
	case !validKey(c.APIKey):
		add("pole api_key zawiera niedozwolone znaki")
	}
	if c.APIKeyFallback != "" {
		switch {
		case !validKey(c.APIKeyFallback):
			add("pole api_key_fallback zawiera niedozwolone znaki")
		case c.APIKeyFallback == c.APIKey:
			add("pole api_key_fallback musi być innym kluczem niż api_key")
		}
	}

	switch {
	case c.Username == "":
		add("brak pola username")
	case len(c.Username) > MaxUsernameLen:
		add("pole username jest dłuższe niż %d bajtów", MaxUsernameLen)
	case strings.TrimSpace(c.Username) != c.Username:
		add("pole username zaczyna się lub kończy białym znakiem")
	}

	switch {
	case c.PasswordHash == "":
		add("brak pola password_hash")
	default:
		if _, err := auth.ParseHash(c.PasswordHash); err != nil {
			add("pole password_hash: %v", err)
		}
	}

	if c.SessionFile == "" {
		add("brak pola session_file")
	}
	if c.WebDir == "" {
		add("brak pola web_dir")
	}

	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	return nil
}

func validateListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return errors.New("oczekiwano postaci host:port, np. 127.0.0.1:8091")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("port musi być liczbą od 1 do 65535")
	}
	if host != "" && host != "localhost" && net.ParseIP(host) == nil {
		return errors.New("host musi być adresem IP lub localhost")
	}
	return nil
}

// validKey dopuszcza znaki występujące w kluczach Google API.
func validKey(k string) bool {
	if len(k) > 256 {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
