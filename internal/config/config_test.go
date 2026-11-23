package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yt-stats/internal/auth"
)

const testKey = "FAKEKEYTESTKEYshouldNeverAppearInErrors_123"

func validConfig(t *testing.T) Config {
	t.Helper()
	hash, err := auth.HashPassword([]byte("sekret"), auth.Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Listen:       "127.0.0.1:8091",
		ChannelID:    "UC5711W83ebIxkC8bHjOKI7g",
		APIKey:       testKey,
		Username:     "yarn",
		PasswordHash: hash,
		SessionFile:  "/var/lib/yt-stats/sessions.json",
		WebDir:       "/opt/yt-stats/web",
	}
}

func writeConfig(t *testing.T, v any, perm os.FileMode) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, perm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, perm); err != nil { // omija umask
		t.Fatal(err)
	}
	return path
}

func problems(t *testing.T, err error) []string {
	t.Helper()
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("oczekiwano ValidationError, jest %T: %v", err, err)
	}
	return ve.Problems
}

func TestLoadValid(t *testing.T) {
	cfg := validConfig(t)
	got, err := Load(writeConfig(t, cfg, 0o600))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != cfg {
		t.Errorf("wczytana konfiguracja różni się:\n got %+v\nwant %+v", *got, cfg)
	}
}

func TestLoadPermissions640Allowed(t *testing.T) {
	if _, err := Load(writeConfig(t, validConfig(t), 0o640)); err != nil {
		t.Fatalf("0640 powinno być dozwolone: %v", err)
	}
}

func TestLoadWorldReadableRejected(t *testing.T) {
	err := func() error { _, err := Load(writeConfig(t, validConfig(t), 0o644)); return err }()
	p := problems(t, err)
	if len(p) != 1 || !strings.Contains(p[0], "0644") {
		t.Errorf("oczekiwano jednego problemu z uprawnieniami, jest %q", p)
	}
}

func TestEachRequiredFieldMissing(t *testing.T) {
	fields := map[string]func(*Config){
		"listen":        func(c *Config) { c.Listen = "" },
		"channel_id":    func(c *Config) { c.ChannelID = "" },
		"api_key":       func(c *Config) { c.APIKey = "" },
		"username":      func(c *Config) { c.Username = "" },
		"password_hash": func(c *Config) { c.PasswordHash = "" },
		"session_file":  func(c *Config) { c.SessionFile = "" },
		"web_dir":       func(c *Config) { c.WebDir = "" },
	}
	for name, clear := range fields {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(t)
			clear(&cfg)
			p := problems(t, cfg.Validate())
			if len(p) != 1 || p[0] != "brak pola "+name {
				t.Errorf("oczekiwano [brak pola %s], jest %q", name, p)
			}
		})
	}
}

func TestAllProblemsReportedAtOnce(t *testing.T) {
	path := writeConfig(t, map[string]string{"listen": "127.0.0.1:8091", "web_dir": "web"}, 0o600)
	_, err := Load(path)
	p := problems(t, err)
	want := []string{"channel_id", "api_key", "username", "password_hash", "session_file"}
	if len(p) != len(want) {
		t.Fatalf("oczekiwano %d problemów, jest %d: %q", len(want), len(p), p)
	}
	msg := err.Error()
	for _, f := range want {
		if !strings.Contains(msg, "brak pola "+f) {
			t.Errorf("komunikat nie wymienia pola %s:\n%s", f, msg)
		}
	}
}

func TestExampleConfigListsMissingFields(t *testing.T) {
	data, err := os.ReadFile("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("przykładowa konfiguracja nie parsuje się: %v", err)
	}
	if len(problems(t, cfg.Validate())) == 0 {
		t.Fatal("przykładowa konfiguracja z pustymi polami nie powinna przejść walidacji")
	}
}

func TestUnknownField(t *testing.T) {
	_, err := Parse([]byte(`{"listen":"127.0.0.1:8091","api_kye":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "api_kye") {
		t.Fatalf("oczekiwano błędu z nazwą nieznanego pola, jest %v", err)
	}
}

func TestWrongTypeHidesValue(t *testing.T) {
	_, err := Parse([]byte(`{"api_key": 12345678987654321}`))
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("oczekiwano błędu typu dla api_key, jest %v", err)
	}
	if strings.Contains(err.Error(), "12345678987654321") {
		t.Errorf("komunikat zawiera wartość: %v", err)
	}
}

func TestTrailingData(t *testing.T) {
	if _, err := Parse([]byte(`{} {}`)); err == nil {
		t.Fatal("oczekiwano błędu dla dodatkowych danych")
	}
}

func TestInvalidValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"listen bez portu", func(c *Config) { c.Listen = "127.0.0.1" }, "listen"},
		{"listen port poza zakresem", func(c *Config) { c.Listen = "127.0.0.1:70000" }, "listen"},
		{"listen nazwa hosta", func(c *Config) { c.Listen = "example.com:80" }, "listen"},
		{"channel_id bez UC", func(c *Config) { c.ChannelID = "5711W83ebIxkC8bHjOKI7g" }, "channel_id"},
		{"channel_id za krótki", func(c *Config) { c.ChannelID = "UC123" }, "channel_id"},
		{"api_key ze spacją", func(c *Config) { c.APIKey = "abc def" }, "api_key"},
		{"fallback ze spacją", func(c *Config) { c.APIKeyFallback = "abc def" }, "api_key_fallback"},
		{"fallback taki sam", func(c *Config) { c.APIKeyFallback = c.APIKey }, "api_key_fallback"},
		{"username za długi", func(c *Config) { c.Username = strings.Repeat("a", 65) }, "username"},
		{"username ze spacją", func(c *Config) { c.Username = " yarn" }, "username"},
		{"hash bcrypt", func(c *Config) { c.PasswordHash = "$2y$10$abcdefghijklmnopqrstuv" }, "password_hash"},
		{"hash argon2i", func(c *Config) { c.PasswordHash = strings.Replace(c.PasswordHash, "argon2id", "argon2i", 1) }, "password_hash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			tc.mutate(&cfg)
			err := cfg.Validate()
			p := problems(t, err)
			if len(p) != 1 || !strings.Contains(p[0], tc.want) {
				t.Errorf("oczekiwano jednego problemu z %s, jest %q", tc.want, p)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Errorf("komunikat zawiera klucz API: %v", err)
			}
		})
	}
}

func TestValidListenVariants(t *testing.T) {
	for _, l := range []string{"127.0.0.1:8091", "[::1]:8091", "localhost:8091", ":8091"} {
		cfg := validConfig(t)
		cfg.Listen = l
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: %v", l, err)
		}
	}
}

func TestFallbackOptional(t *testing.T) {
	cfg := validConfig(t)
	cfg.APIKeyFallback = ""
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.APIKeyFallback = "FAKEKEYOtherKey_456"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	cfg := validConfig(t)
	cfg.APIKeyFallback = testKey // ten sam klucz — błąd
	cfg.PasswordHash = cfg.PasswordHash[:len(cfg.PasswordHash)-3] + "!!!"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("oczekiwano błędu")
	}
	if strings.Contains(err.Error(), testKey) || strings.Contains(err.Error(), "$argon2id$") {
		t.Errorf("komunikat zawiera sekret: %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "brak.json")); err == nil {
		t.Fatal("oczekiwano błędu")
	}
}

func TestLoadTooLarge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", maxFileSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "KiB") {
		t.Fatalf("oczekiwano błędu rozmiaru, jest %v", err)
	}
}

func TestLoadDirectory(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("oczekiwano błędu dla katalogu")
	}
}
