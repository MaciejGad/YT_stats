package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yt-stats/internal/auth"
)

// syncBuffer to bufor bezpieczny przy równoległych zapisach (logi serwera).
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// testEnv zwraca środowisko z buforami i nasłuchem, który zawsze
// odrzuca próbę otwarcia gniazda.
func testEnv(stdin string) (env, *syncBuffer, *syncBuffer) {
	var out, errOut syncBuffer
	return env{
		stdin:  strings.NewReader(stdin),
		stdout: &out,
		stderr: &errOut,
		listen: func(string, string) (net.Listener, error) {
			return nil, errors.New("listen nie powinien być wywołany")
		},
	}, &out, &errOut
}

// localListen otwiera gniazdo na losowym wolnym porcie zamiast adresu
// z konfiguracji i przekazuje je testowi przez kanał.
func localListen(t *testing.T, got chan<- net.Listener) func(string, string) (net.Listener, error) {
	return func(network, _ string) (net.Listener, error) {
		ln, err := net.Listen(network, "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		got <- ln
		return ln, nil
	}
}

// writeValidConfig tworzy poprawną konfigurację w katalogu tymczasowym.
func writeValidConfig(t *testing.T, webDir string) string {
	t.Helper()
	hash, err := auth.HashPassword([]byte("sekret"), auth.Params{Memory: 8 * 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	cfg := map[string]string{
		"listen":        "127.0.0.1:8091",
		"channel_id":    "UC5711W83ebIxkC8bHjOKI7g",
		"api_key":       "FAKEKEYFAKEKEYforTests",
		"username":      "yarn",
		"password_hash": hash,
		"session_file":  filepath.Join(dir, "sessions.json"),
		"web_dir":       webDir,
	}
	data, _ := json.Marshal(cfg)
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestLogger(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// --- hash-password ---

// fakeReader zwraca kolejne hasła z listy.
func fakeReader(answers ...string) secretReader {
	return func(string) ([]byte, error) {
		if len(answers) == 0 {
			return nil, errors.New("brak danych")
		}
		a := answers[0]
		answers = answers[1:]
		return []byte(a), nil
	}
}

func TestHashPasswordInteractive(t *testing.T) {
	h, err := hashPasswordInteractive(fakeReader("dobre hasło", "dobre hasło"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := auth.ParseHash(h)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Verify([]byte("dobre hasło")) || p.Verify([]byte("inne")) {
		t.Error("weryfikacja wygenerowanego hasha nie działa")
	}
	if p.Params != auth.DefaultParams {
		t.Errorf("parametry %+v, oczekiwano %+v", p.Params, auth.DefaultParams)
	}
}

func TestHashPasswordMismatch(t *testing.T) {
	if _, err := hashPasswordInteractive(fakeReader("a", "b")); err == nil || !strings.Contains(err.Error(), "różnią") {
		t.Fatalf("oczekiwano błędu niezgodności, jest %v", err)
	}
}

func TestHashPasswordEmpty(t *testing.T) {
	if _, err := hashPasswordInteractive(fakeReader("", "")); err == nil {
		t.Fatal("puste hasło powinno dać błąd")
	}
}

func TestHashPasswordSecondReadFails(t *testing.T) {
	if _, err := hashPasswordInteractive(fakeReader("tylko raz")); err == nil {
		t.Fatal("brak powtórzenia hasła powinien dać błąd")
	}
}

func TestHashPasswordFromPipe(t *testing.T) {
	e, out, errOut := testEnv("z potoku\nz potoku\n")
	if code := run(context.Background(), []string{"hash-password"}, e); code != 0 {
		t.Fatalf("kod %d, stderr: %s", code, errOut)
	}
	if strings.Contains(errOut.String()+out.String(), "z potoku") {
		t.Error("hasło pojawiło się na wyjściu")
	}
	p, err := auth.ParseHash(strings.TrimSpace(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !p.Verify([]byte("z potoku")) {
		t.Error("hash nie pasuje do hasła z potoku")
	}
}

func TestHashPasswordFromPipeWithoutTrailingNewline(t *testing.T) {
	e, out, errOut := testEnv("abc\r\nabc")
	if code := run(context.Background(), []string{"hash-password"}, e); code != 0 {
		t.Fatalf("kod %d, stderr: %s", code, errOut)
	}
	p, _ := auth.ParseHash(strings.TrimSpace(out.String()))
	if p == nil || !p.Verify([]byte("abc")) {
		t.Error("hasło bez końcowego znaku nowej linii lub z CRLF nie zostało odczytane poprawnie")
	}
}

func TestHashPasswordNoInput(t *testing.T) {
	e, _, _ := testEnv("")
	if code := run(context.Background(), []string{"hash-password"}, e); code != 1 {
		t.Fatalf("kod %d, oczekiwano 1", code)
	}
}

func TestHashPasswordRejectsArgs(t *testing.T) {
	e, _, errOut := testEnv("")
	if code := run(context.Background(), []string{"hash-password", "jawne-haslo"}, e); code != 2 {
		t.Fatalf("kod %d, oczekiwano 2", code)
	}
	if !strings.Contains(errOut.String(), "nie przyjmuje argumentów") {
		t.Errorf("stderr: %s", errOut)
	}
}

// --- pozostałe polecenia ---

func TestVersion(t *testing.T) {
	e, out, _ := testEnv("")
	if code := run(context.Background(), []string{"version"}, e); code != 0 {
		t.Fatalf("kod %d", code)
	}
	if strings.TrimSpace(out.String()) != version {
		t.Errorf("wyjście %q, oczekiwano %q", out, version)
	}
}

func TestHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		e, out, _ := testEnv("")
		if code := run(context.Background(), []string{arg}, e); code != 0 {
			t.Fatalf("%s: kod %d", arg, code)
		}
		if !strings.Contains(out.String(), "hash-password") {
			t.Errorf("%s: brak opisu poleceń: %s", arg, out)
		}
	}
}

func TestUnknownCommand(t *testing.T) {
	e, _, errOut := testEnv("")
	if code := run(context.Background(), []string{"cokolwiek"}, e); code != 2 {
		t.Fatalf("kod %d, oczekiwano 2", code)
	}
	if !strings.Contains(errOut.String(), "cokolwiek") {
		t.Errorf("stderr: %s", errOut)
	}
}

func TestUnknownFlag(t *testing.T) {
	e, _, _ := testEnv("")
	if code := run(context.Background(), []string{"-nieznana"}, e); code != 2 {
		t.Fatalf("kod %d, oczekiwano 2", code)
	}
}

// --- serwer: błędy startu ---

func TestServeExampleConfigFails(t *testing.T) {
	data, err := os.ReadFile("../../config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	e, _, errOut := testEnv("")
	if code := run(context.Background(), []string{"-config", path}, e); code != 1 {
		t.Fatalf("kod %d, oczekiwano 1", code)
	}
	for _, f := range []string{"channel_id", "api_key", "username", "password_hash", "session_file"} {
		if !strings.Contains(errOut.String(), "brak pola "+f) {
			t.Errorf("komunikat nie wymienia %s:\n%s", f, errOut)
		}
	}
}

func TestServeMissingConfig(t *testing.T) {
	e, _, _ := testEnv("")
	if code := run(context.Background(), []string{"-config", filepath.Join(t.TempDir(), "brak.json")}, e); code != 1 {
		t.Fatalf("kod %d, oczekiwano 1", code)
	}
}

func TestServeMissingWebDir(t *testing.T) {
	path := writeValidConfig(t, filepath.Join(t.TempDir(), "brak-web"))
	e, _, errOut := testEnv("")
	if code := run(context.Background(), []string{"-config", path}, e); code != 1 {
		t.Fatalf("kod %d, oczekiwano 1", code)
	}
	if !strings.Contains(errOut.String(), "pliki interfejsu") {
		t.Errorf("stderr: %s", errOut)
	}
}

func TestServeListenError(t *testing.T) {
	path := writeValidConfig(t, "../../web")
	e, _, errOut := testEnv("")
	e.listen = func(string, string) (net.Listener, error) { return nil, errors.New("adres zajęty") }
	if code := run(context.Background(), []string{"-config", path}, e); code != 1 {
		t.Fatalf("kod %d, oczekiwano 1", code)
	}
	if !strings.Contains(errOut.String(), "127.0.0.1:8091") || !strings.Contains(errOut.String(), "adres zajęty") {
		t.Errorf("komunikat powinien zawierać adres i przyczynę: %s", errOut)
	}
}

// --- serwer: pełny cykl ---

func TestServeStartsAndStops(t *testing.T) {
	path := writeValidConfig(t, "../../web")
	e, _, errOut := testEnv("")
	lnc := make(chan net.Listener, 1)
	e.listen = localListen(t, lnc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- run(ctx, []string{"-config", path}, e) }()

	var ln net.Listener
	select {
	case ln = <-lnc:
	case code := <-done:
		t.Fatalf("serwer zakończył się przed startem, kod %d: %s", code, errOut)
	case <-time.After(5 * time.Second):
		t.Fatal("serwer nie otworzył gniazda w 5 s")
	}

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := noRedirect.Get("http://" + ln.Addr().String() + "/yt/")
	if err != nil {
		t.Fatalf("żądanie do działającego serwera: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/yt/login" {
		t.Errorf("bez sesji: status %d Location %q, oczekiwano 303 /yt/login", resp.StatusCode, resp.Header.Get("Location"))
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("kod %d po zatrzymaniu, oczekiwano 0: %s", code, errOut)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serwer nie zatrzymał się w 5 s")
	}

	logs := errOut.String()
	for _, want := range []string{"serwer uruchomiony", "zatrzymywanie serwera", "serwer zatrzymany"} {
		if !strings.Contains(logs, want) {
			t.Errorf("brak w logach %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "FAKEKEYFAKEKEYforTests") || strings.Contains(logs, "$argon2id$") {
		t.Errorf("logi zawierają sekret:\n%s", logs)
	}

	// Gniazdo jest zamknięte — nowe połączenie się nie uda.
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
		c.Close()
		t.Error("gniazdo przyjmuje połączenia po zatrzymaniu")
	}
}

func TestServeWaitsForActiveRequest(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		io.WriteString(w, "gotowe")
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, h, newTestLogger(io.Discard)) }()

	body := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err != nil {
			body <- "błąd: " + err.Error()
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		body <- string(b)
	}()

	<-started
	cancel()
	select {
	case err := <-done:
		t.Fatalf("serwer zakończył się przed dokończeniem żądania: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)

	if got := <-body; got != "gotowe" {
		t.Errorf("odpowiedź %q, oczekiwano dokończenia żądania", got)
	}
	if err := <-done; err != nil {
		t.Errorf("serve: %v", err)
	}
}

func TestServeShutdownTimeout(t *testing.T) {
	old := shutdownTimeout
	shutdownTimeout = 50 * time.Millisecond
	defer func() { shutdownTimeout = old }()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})

	var logs syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, ln, h, newTestLogger(&logs)) }()
	go func() {
		if resp, err := http.Get("http://" + ln.Addr().String() + "/"); err == nil {
			resp.Body.Close()
		}
	}()

	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("oczekiwano przekroczenia czasu zatrzymania, jest %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve nie zakończył się po przekroczeniu czasu zatrzymania")
	}
	if !strings.Contains(logs.String(), "zatrzymanie serwera") {
		t.Errorf("brak wpisu o błędzie zatrzymania:\n%s", logs.String())
	}
}

func TestServeListenerFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close() // Serve od razu dostanie błąd Accept.

	var logs syncBuffer
	err = serve(context.Background(), ln, http.NotFoundHandler(), newTestLogger(&logs))
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("oczekiwano błędu gniazda, jest %v", err)
	}
	if !strings.Contains(logs.String(), "serwer zakończył działanie") {
		t.Errorf("brak wpisu o błędzie:\n%s", logs.String())
	}
}

func TestNewServerLimits(t *testing.T) {
	srv := newServer(http.NotFoundHandler(), newTestLogger(io.Discard))
	checks := []struct {
		name      string
		got, want time.Duration
	}{
		{"ReadHeaderTimeout", srv.ReadHeaderTimeout, 5 * time.Second},
		{"ReadTimeout", srv.ReadTimeout, 10 * time.Second},
		{"WriteTimeout", srv.WriteTimeout, 40 * time.Second},
		{"IdleTimeout", srv.IdleTimeout, 60 * time.Second},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, oczekiwano %v", c.name, c.got, c.want)
		}
	}
	if srv.MaxHeaderBytes != 16<<10 {
		t.Errorf("MaxHeaderBytes = %d, oczekiwano %d", srv.MaxHeaderBytes, 16<<10)
	}
}

// --- revoke-sessions ---

func sessionFileOf(t *testing.T, configPath string) string {
	t.Helper()
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]string
	json.Unmarshal(data, &cfg)
	return cfg["session_file"]
}

func TestRevokeSessions(t *testing.T) {
	path := writeValidConfig(t, "../../web")
	store, err := auth.OpenStore(sessionFileOf(t, path), auth.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	token, _, _ := store.Create(true)
	store.Create(false)
	store.Close()

	e, out, errOut := testEnv("")
	if code := run(context.Background(), []string{"revoke-sessions", "-config", path}, e); code != 0 {
		t.Fatalf("kod %d: %s", code, errOut)
	}
	if !strings.Contains(out.String(), "Usunięto sesje: 2") {
		t.Errorf("wyjście: %s", out)
	}
	store, _ = auth.OpenStore(sessionFileOf(t, path), auth.StoreOptions{})
	defer store.Close()
	if _, ok := store.Lookup(token); ok {
		t.Error("sesja przetrwała revoke-sessions")
	}
}

func TestRevokeSessionsWhileRunning(t *testing.T) {
	path := writeValidConfig(t, "../../web")
	store, err := auth.OpenStore(sessionFileOf(t, path), auth.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	token, _, _ := store.Create(true)

	e, _, errOut := testEnv("")
	if code := run(context.Background(), []string{"revoke-sessions", "-config", path}, e); code != 1 {
		t.Fatalf("kod %d, oczekiwano 1", code)
	}
	if !strings.Contains(errOut.String(), "zatrzymaj") {
		t.Errorf("komunikat powinien radzić zatrzymanie usługi: %s", errOut)
	}
	if _, ok := store.Lookup(token); !ok {
		t.Error("sesja zniknęła mimo odmowy")
	}
}

func TestRevokeSessionsBadArgs(t *testing.T) {
	e, _, _ := testEnv("")
	if code := run(context.Background(), []string{"revoke-sessions", "-x"}, e); code != 2 {
		t.Errorf("nieznana flaga: kod %d", code)
	}
	if code := run(context.Background(), []string{"revoke-sessions", "nadmiarowy"}, e); code != 2 {
		t.Errorf("nadmiarowy argument: kod %d", code)
	}
	if code := run(context.Background(), []string{"revoke-sessions", "-config", filepath.Join(t.TempDir(), "brak.json")}, e); code != 1 {
		t.Errorf("brak konfiguracji: kod %d", code)
	}
}

func TestSecondInstanceRefusesSessionFile(t *testing.T) {
	path := writeValidConfig(t, "../../web")
	store, err := auth.OpenStore(sessionFileOf(t, path), auth.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	e, _, errOut := testEnv("")
	if code := run(context.Background(), []string{"-config", path}, e); code != 1 {
		t.Fatalf("kod %d, oczekiwano 1", code)
	}
	if !strings.Contains(errOut.String(), "zablokowany") {
		t.Errorf("stderr: %s", errOut)
	}
}
