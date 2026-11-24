package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 11, 23, 20, 0, 0, 0, time.UTC)}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func openTestStore(t *testing.T, path string, clk *testClock) *Store {
	t.Helper()
	s, err := OpenStore(path, StoreOptions{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func storePath(t *testing.T) string { return filepath.Join(t.TempDir(), "sessions.json") }

// --- lifetimes ---

func TestBrowserSessionLifetime(t *testing.T) {
	clk := newTestClock()
	s := openTestStore(t, storePath(t), clk)
	token, sess, err := s.Create(false)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Persistent || sess.Expires.Sub(sess.Created) != 12*time.Hour {
		t.Errorf("sesja: %+v", sess)
	}
	clk.Add(11*time.Hour + 59*time.Minute)
	if _, ok := s.Lookup(token); !ok {
		t.Error("sesja powinna być ważna po 11 h 59 min")
	}
	clk.Add(time.Minute)
	if _, ok := s.Lookup(token); ok {
		t.Error("sesja powinna wygasnąć po 12 h")
	}
}

func TestPersistentSessionLifetimeNoSliding(t *testing.T) {
	clk := newTestClock()
	s := openTestStore(t, storePath(t), clk)
	token, sess, err := s.Create(true)
	if err != nil {
		t.Fatal(err)
	}
	if !sess.Persistent || sess.Expires.Sub(sess.Created) != 90*24*time.Hour {
		t.Errorf("sesja: %+v", sess)
	}
	// Daily use does not move the expiry.
	for range 89 {
		clk.Add(24 * time.Hour)
		got, ok := s.Lookup(token)
		if !ok {
			t.Fatal("sesja wygasła przed 90 dniami")
		}
		if !got.Expires.Equal(sess.Expires) {
			t.Fatalf("termin przesunął się: %v → %v", sess.Expires, got.Expires)
		}
	}
	clk.Add(23 * time.Hour) // 89 days 23 h
	if _, ok := s.Lookup(token); !ok {
		t.Error("sesja powinna być ważna po 89 dniach 23 h")
	}
	clk.Add(time.Hour)
	if _, ok := s.Lookup(token); ok {
		t.Error("sesja powinna wygasnąć po 90 dniach")
	}
}

// --- format and file ---

func TestTokenFormatAndHashOnlyInFile(t *testing.T) {
	path := storePath(t)
	s := openTestStore(t, path, newTestClock())
	token, _, err := s.Create(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 || !validToken(token) {
		t.Errorf("token %q ma zły format", token)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) {
		t.Error("plik sesji zawiera identyfikator sesji")
	}
	if !strings.Contains(string(data), hashToken(token)) {
		t.Error("plik sesji nie zawiera hasha identyfikatora")
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("uprawnienia pliku %04o, oczekiwano 0600", perm)
	}
}

func TestTokensAreUnique(t *testing.T) {
	s := openTestStore(t, storePath(t), newTestClock())
	seen := map[string]bool{}
	for range 20 {
		tok, _, err := s.Create(false)
		if err != nil {
			t.Fatal(err)
		}
		if seen[tok] {
			t.Fatal("powtórzony identyfikator")
		}
		seen[tok] = true
	}
}

func TestRestartKeepsSessions(t *testing.T) {
	path := storePath(t)
	clk := newTestClock()
	s1, err := OpenStore(path, StoreOptions{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	token, _, _ := s1.Create(true)
	s1.Close()

	s2 := openTestStore(t, path, clk)
	if _, ok := s2.Lookup(token); !ok {
		t.Error("sesja nie przetrwała ponownego otwarcia magazynu")
	}
}

func TestExpiredRemovedOnWrite(t *testing.T) {
	path := storePath(t)
	clk := newTestClock()
	s := openTestStore(t, path, clk)
	old, _, _ := s.Create(false)
	clk.Add(13 * time.Hour)
	if _, _, err := s.Create(false); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), hashToken(old)) {
		t.Error("wygasła sesja została w pliku po zapisie")
	}
	if s.Len() != 1 {
		t.Errorf("Len = %d", s.Len())
	}
}

func TestExpiredDroppedOnLoad(t *testing.T) {
	path := storePath(t)
	clk := newTestClock()
	s1, _ := OpenStore(path, StoreOptions{Now: clk.Now})
	s1.Create(false)
	s1.Close()
	clk.Add(13 * time.Hour)
	s2 := openTestStore(t, path, clk)
	if s2.Len() != 0 {
		t.Errorf("wygasła sesja wczytana z pliku: %d", s2.Len())
	}
}

func TestMaxSessionsRemovesOldest(t *testing.T) {
	clk := newTestClock()
	s, err := OpenStore(storePath(t), StoreOptions{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var tokens []string
	for range 101 {
		tok, _, err := s.Create(true)
		if err != nil {
			t.Fatal(err)
		}
		tokens = append(tokens, tok)
		clk.Add(time.Second)
	}
	if s.Len() != 100 {
		t.Errorf("Len = %d, oczekiwano 100", s.Len())
	}
	if _, ok := s.Lookup(tokens[0]); ok {
		t.Error("najstarsza sesja powinna zostać usunięta")
	}
	if _, ok := s.Lookup(tokens[100]); !ok {
		t.Error("najnowsza sesja powinna zostać")
	}
}

func TestLookupRejectsBadFormat(t *testing.T) {
	s := openTestStore(t, storePath(t), newTestClock())
	for _, tok := range []string{"", "abc", strings.Repeat("a", 42), strings.Repeat("!", 43), strings.Repeat("a", 44)} {
		if _, ok := s.Lookup(tok); ok {
			t.Errorf("token %q przyjęty", tok)
		}
	}
}

func TestDelete(t *testing.T) {
	path := storePath(t)
	clk := newTestClock()
	s := openTestStore(t, path, clk)
	a, _, _ := s.Create(false)
	b, _, _ := s.Create(false)
	if err := s.Delete(a); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(a); ok {
		t.Error("usunięta sesja nadal ważna")
	}
	if _, ok := s.Lookup(b); !ok {
		t.Error("usunięto nie tę sesję")
	}
	if err := s.Delete(a); err != nil {
		t.Errorf("ponowne usunięcie: %v", err)
	}
	if err := s.Delete("zły format"); err != nil {
		t.Errorf("usunięcie złego tokenu: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), hashToken(a)) {
		t.Error("usunięta sesja została w pliku")
	}
}

// --- file errors ---

func TestCorruptFileMovedAside(t *testing.T) {
	for name, content := range map[string]string{
		"zły JSON":       `{"version":1,"sessions":[`,
		"zła wersja":     `{"version":99,"sessions":[]}`,
		"zły wpis":       `{"version":1,"sessions":[{"id_hash":"abc","expires":"2030-01-01T00:00:00Z"}]}`,
		"nieznane pole":  `{"version":1,"sessions":[],"extra":1}`,
		"za duży plik":   `{"version":1,"sessions":[]}` + strings.Repeat(" ", maxStoreFileSize),
		"nie obiekt":     `[]`,
		"pusty":          ``,
		"null jako wpis": `{"version":1,"sessions":[null]}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := storePath(t)
			os.WriteFile(path, []byte(content), 0o600)
			var logs syncBuf
			s, err := OpenStore(path, StoreOptions{Now: newTestClock().Now, Log: slog.New(slog.NewTextHandler(&logs, nil))})
			if err != nil {
				t.Fatalf("uszkodzony plik powinien dać pusty magazyn, jest błąd: %v", err)
			}
			defer s.Close()
			if s.Len() != 0 {
				t.Errorf("Len = %d", s.Len())
			}
			matches, _ := filepath.Glob(path + ".corrupt-*")
			if len(matches) != 1 {
				t.Errorf("uszkodzony plik nie został przeniesiony: %v", matches)
			}
			if !strings.Contains(logs.String(), "uszkodzony") {
				t.Errorf("brak wpisu w logu: %s", logs.String())
			}
			// The store keeps working.
			if _, _, err := s.Create(false); err != nil {
				t.Errorf("Create po odzyskaniu: %v", err)
			}
		})
	}
}

func TestUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root omija uprawnienia")
	}
	path := storePath(t)
	os.WriteFile(path, []byte(`{"version":1,"sessions":[]}`), 0o000)
	if _, err := OpenStore(path, StoreOptions{}); err == nil {
		t.Fatal("nieczytelny plik powinien dać błąd startu")
	}
}

func TestCreateFailsWhenDirReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root omija uprawnienia")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	s := openTestStore(t, path, newTestClock())
	os.Chmod(dir, 0o500)
	defer os.Chmod(dir, 0o700)

	token, _, err := s.Create(true)
	if err == nil {
		t.Fatal("oczekiwano błędu zapisu")
	}
	if s.Len() != 0 || token != "" {
		t.Error("sesja powstała w pamięci mimo błędu zapisu")
	}
}

func TestDeleteFailsKeepsSession(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root omija uprawnienia")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	s := openTestStore(t, path, newTestClock())
	token, _, _ := s.Create(true)

	os.Chmod(dir, 0o500)
	if err := s.Delete(token); err == nil {
		t.Fatal("oczekiwano błędu zapisu")
	}
	if _, ok := s.Lookup(token); !ok {
		t.Error("sesja zniknęła mimo błędu zapisu")
	}
	os.Chmod(dir, 0o700)

	// Retrying works.
	if err := s.Delete(token); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Lookup(token); ok {
		t.Error("sesja nadal ważna po ponowieniu")
	}
}

func TestNoTempFilesLeft(t *testing.T) {
	dir := t.TempDir()
	s := openTestStore(t, filepath.Join(dir, "sessions.json"), newTestClock())
	for range 5 {
		s.Create(false)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("pozostał plik tymczasowy %s", e.Name())
		}
	}
}

func TestFileIsValidJSON(t *testing.T) {
	path := storePath(t)
	s := openTestStore(t, path, newTestClock())
	s.Create(true)
	data, _ := os.ReadFile(path)
	var ff fileFormat
	if err := json.Unmarshal(data, &ff); err != nil || ff.Version != 1 || len(ff.Sessions) != 1 {
		t.Errorf("plik: %s (%v)", data, err)
	}
}

// --- lock ---

func TestSecondStoreLocked(t *testing.T) {
	path := storePath(t)
	openTestStore(t, path, newTestClock())
	if _, err := OpenStore(path, StoreOptions{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("oczekiwano ErrLocked, jest %v", err)
	}
}

func TestLockReleasedOnClose(t *testing.T) {
	path := storePath(t)
	s, _ := OpenStore(path, StoreOptions{})
	s.Close()
	s2, err := OpenStore(path, StoreOptions{})
	if err != nil {
		t.Fatalf("blokada nie została zwolniona: %v", err)
	}
	s2.Close()
	if err := s2.Close(); err != nil {
		t.Errorf("podwójne Close: %v", err)
	}
}

func TestRevokeAllLockedWhileRunning(t *testing.T) {
	path := storePath(t)
	s := openTestStore(t, path, newTestClock())
	token, _, _ := s.Create(true)
	if _, err := RevokeAll(path, StoreOptions{}); !errors.Is(err, ErrLocked) {
		t.Fatalf("oczekiwano ErrLocked, jest %v", err)
	}
	if _, ok := s.Lookup(token); !ok {
		t.Error("sesja zniknęła mimo odmowy")
	}
}

func TestRevokeAll(t *testing.T) {
	path := storePath(t)
	clk := newTestClock()
	s, _ := OpenStore(path, StoreOptions{Now: clk.Now})
	t1, _, _ := s.Create(true)
	s.Create(false)
	s.Close()

	n, err := RevokeAll(path, StoreOptions{Now: clk.Now})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("usunięto %d, oczekiwano 2", n)
	}
	s2 := openTestStore(t, path, clk)
	if _, ok := s2.Lookup(t1); ok || s2.Len() != 0 {
		t.Error("sesje przetrwały revoke-sessions")
	}
}

func TestConcurrentCreateLookup(t *testing.T) {
	s := openTestStore(t, storePath(t), newTestClock())
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 5 {
				tok, _, err := s.Create(false)
				if err != nil {
					t.Error(err)
					return
				}
				if _, ok := s.Lookup(tok); !ok {
					t.Error("świeża sesja nieważna")
				}
			}
		}()
	}
	wg.Wait()
	if s.Len() != 40 {
		t.Errorf("Len = %d, oczekiwano 40", s.Len())
	}
}

// --- logout token ---

func TestLogoutToken(t *testing.T) {
	a, _ := newToken()
	b, _ := newToken()
	if LogoutToken(a) != LogoutToken(a) {
		t.Error("token wylogowania nie jest deterministyczny")
	}
	if LogoutToken(a) == LogoutToken(b) {
		t.Error("różne sesje mają ten sam token wylogowania")
	}
	if strings.Contains(LogoutToken(a), a) {
		t.Error("token wylogowania zawiera identyfikator sesji")
	}
	if !CheckLogoutToken(a, LogoutToken(a)) || CheckLogoutToken(a, LogoutToken(b)) || CheckLogoutToken(a, "") {
		t.Error("CheckLogoutToken")
	}
}
