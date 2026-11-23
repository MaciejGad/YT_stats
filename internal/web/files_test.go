package web

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer to bufor logów bezpieczny przy równoległych zapisach.
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

func newLogger(w io.Writer) *slog.Logger { return slog.New(slog.NewTextHandler(w, nil)) }

// setupDir tworzy komplet poprawnych plików w katalogu tymczasowym.
func setupDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":  `<p>panel {{.Name}}</p>`,
		"login.html":  `<p>logowanie</p>`,
		"script.js":   `console.log(1);`,
		"style.css":   `body{}`,
		"favicon.svg": `<svg/>`,
	}
	for name, content := range files {
		write(t, dir, name, content, time.Now().Add(-time.Hour))
	}
	return dir
}

// write zapisuje plik i ustawia czas modyfikacji, żeby zmiana była
// wykrywalna niezależnie od rozdzielczości zegara systemu plików.
func write(t *testing.T, dir, name, content string, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func render(t *testing.T, f *Files, name string) string {
	t.Helper()
	tmpl, err := f.Template(name)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, map[string]string{"Name": "YarnPower"}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestLoadFilesOK(t *testing.T) {
	f, err := LoadFiles(setupDir(t), newLogger(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	if got := render(t, f, "index.html"); got != "<p>panel YarnPower</p>" {
		t.Errorf("render = %q", got)
	}
	s, err := f.Static("script.js")
	if err != nil {
		t.Fatal(err)
	}
	if string(s.Content) != "console.log(1);" || s.ETag == "" {
		t.Errorf("static = %q etag %q", s.Content, s.ETag)
	}
}

func TestLoadFilesMissingEachFile(t *testing.T) {
	for _, name := range append(append([]string{}, TemplateNames...), StaticNames...) {
		t.Run(name, func(t *testing.T) {
			dir := setupDir(t)
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			_, err := LoadFiles(dir, newLogger(io.Discard))
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("oczekiwano błędu z nazwą %s, jest %v", name, err)
			}
		})
	}
}

func TestLoadFilesReportsAllMissing(t *testing.T) {
	dir := setupDir(t)
	os.Remove(filepath.Join(dir, "login.html"))
	os.Remove(filepath.Join(dir, "style.css"))
	_, err := LoadFiles(dir, newLogger(io.Discard))
	if err == nil || !strings.Contains(err.Error(), "login.html") || !strings.Contains(err.Error(), "style.css") {
		t.Fatalf("oczekiwano obu nazw w błędzie, jest %v", err)
	}
}

func TestLoadFilesTemplateSyntaxError(t *testing.T) {
	dir := setupDir(t)
	write(t, dir, "login.html", "{{if}}", time.Now())
	_, err := LoadFiles(dir, newLogger(io.Discard))
	if err == nil || !strings.Contains(err.Error(), "login.html") {
		t.Fatalf("oczekiwano błędu składni z nazwą pliku, jest %v", err)
	}
}

func TestLoadFilesBadDir(t *testing.T) {
	if _, err := LoadFiles(filepath.Join(t.TempDir(), "brak"), newLogger(io.Discard)); err == nil {
		t.Error("brak katalogu powinien dać błąd")
	}
	file := filepath.Join(t.TempDir(), "plik")
	os.WriteFile(file, nil, 0o644)
	if _, err := LoadFiles(file, newLogger(io.Discard)); err == nil {
		t.Error("plik zamiast katalogu powinien dać błąd")
	}
}

func TestTemplateReloadOnChange(t *testing.T) {
	dir := setupDir(t)
	f, err := LoadFiles(dir, newLogger(io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "index.html", "<p>nowy {{.Name}}</p>", time.Now())
	if got := render(t, f, "index.html"); got != "<p>nowy YarnPower</p>" {
		t.Errorf("po zmianie render = %q", got)
	}
}

func TestTemplateBrokenKeepsPrevious(t *testing.T) {
	dir := setupDir(t)
	var logs syncBuffer
	f, err := LoadFiles(dir, newLogger(&logs))
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "index.html", "{{if}}", time.Now())
	if got := render(t, f, "index.html"); got != "<p>panel YarnPower</p>" {
		t.Errorf("po błędnej zmianie render = %q, oczekiwano poprzedniej wersji", got)
	}
	if !strings.Contains(logs.String(), "index.html") {
		t.Errorf("brak wpisu w logu: %s", logs.String())
	}

	// Błędny plik nie jest parsowany przy każdym żądaniu.
	before := strings.Count(logs.String(), "błąd w nowej wersji")
	render(t, f, "index.html")
	if after := strings.Count(logs.String(), "błąd w nowej wersji"); after != before {
		t.Errorf("błędny szablon parsowany ponownie bez zmiany pliku (%d → %d)", before, after)
	}

	// Poprawka pliku jest wczytywana.
	write(t, dir, "index.html", "<p>poprawiony</p>", time.Now().Add(time.Second))
	if got := render(t, f, "index.html"); got != "<p>poprawiony</p>" {
		t.Errorf("po poprawce render = %q", got)
	}
}

func TestTemplateDeletedKeepsPrevious(t *testing.T) {
	dir := setupDir(t)
	f, _ := LoadFiles(dir, newLogger(io.Discard))
	os.Remove(filepath.Join(dir, "index.html"))
	if got := render(t, f, "index.html"); got != "<p>panel YarnPower</p>" {
		t.Errorf("po usunięciu pliku render = %q", got)
	}
}

func TestStaticReloadAndETag(t *testing.T) {
	dir := setupDir(t)
	f, _ := LoadFiles(dir, newLogger(io.Discard))
	old, _ := f.Static("style.css")
	write(t, dir, "style.css", "body{color:red}", time.Now())
	s, err := f.Static("style.css")
	if err != nil {
		t.Fatal(err)
	}
	if string(s.Content) != "body{color:red}" {
		t.Errorf("content = %q", s.Content)
	}
	if s.ETag == old.ETag {
		t.Error("ETag nie zmienił się po zmianie pliku")
	}
}

func TestUnknownNames(t *testing.T) {
	f, _ := LoadFiles(setupDir(t), newLogger(io.Discard))
	if _, err := f.Template("../config.json"); err == nil {
		t.Error("nieznany szablon powinien dać błąd")
	}
	if _, err := f.Static("../config.json"); err == nil {
		t.Error("nieznany plik powinien dać błąd")
	}
}

func TestConcurrentReload(t *testing.T) {
	dir := setupDir(t)
	f, _ := LoadFiles(dir, newLogger(io.Discard))
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				tmpl, err := f.Template("index.html")
				if err != nil {
					t.Error(err)
					return
				}
				if err := tmpl.Execute(io.Discard, map[string]string{"Name": "x"}); err != nil {
					t.Error(err)
					return
				}
				if _, err := f.Static("script.js"); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	base := time.Now()
	for i := range 50 {
		write(t, dir, "index.html", "<p>{{.Name}} wersja</p>", base.Add(time.Duration(i)*time.Second))
		write(t, dir, "script.js", "// wersja", base.Add(time.Duration(i)*time.Second))
	}
	close(stop)
	wg.Wait()
}

func TestRealWebDirLoads(t *testing.T) {
	if _, err := LoadFiles("../../web", newLogger(io.Discard)); err != nil {
		t.Fatalf("pliki w web/ nie wczytują się: %v", err)
	}
}
