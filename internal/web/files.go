// Package web obsługuje pliki interfejsu i handlery HTTP.
package web

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Pliki wymagane w katalogu web_dir.
var (
	TemplateNames = []string{"index.html", "login.html"}
	StaticNames   = []string{"script.js", "style.css", "favicon.svg"}
)

// maxFileSize ogranicza rozmiar pojedynczego pliku interfejsu.
const maxFileSize = 1 << 20

// fileStamp identyfikuje wersję pliku na dysku.
type fileStamp struct {
	modTime time.Time
	size    int64
}

type templateEntry struct {
	stamp fileStamp
	tmpl  *template.Template
}

// StaticFile to wczytana zawartość pliku statycznego.
type StaticFile struct {
	Name    string
	Content []byte
	ModTime time.Time
	ETag    string
	stamp   fileStamp
}

// Files trzyma szablony i pliki statyczne z web_dir. Przy każdym
// odczycie porównuje czas modyfikacji i rozmiar pliku i wczytuje go
// ponownie po zmianie. Błędna nowa wersja nie zastępuje poprzedniej.
type Files struct {
	dir string
	log *slog.Logger

	mu        sync.RWMutex
	templates map[string]*templateEntry
	static    map[string]*StaticFile
}

// LoadFiles wczytuje wszystkie wymagane pliki. Brak lub błąd któregokolwiek
// przerywa start z komunikatem wskazującym plik.
func LoadFiles(dir string, log *slog.Logger) (*Files, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("katalog web_dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("web_dir %s nie jest katalogiem", dir)
	}

	f := &Files{
		dir:       dir,
		log:       log,
		templates: make(map[string]*templateEntry, len(TemplateNames)),
		static:    make(map[string]*StaticFile, len(StaticNames)),
	}
	var errs []error
	for _, name := range TemplateNames {
		e, err := f.loadTemplate(name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		f.templates[name] = e
	}
	for _, name := range StaticNames {
		s, err := f.loadStatic(name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		f.static[name] = s
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return f, nil
}

// Template zwraca aktualny szablon o podanej nazwie.
func (f *Files) Template(name string) (*template.Template, error) {
	f.mu.RLock()
	e, ok := f.templates[name]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("nieznany szablon %s", name)
	}

	stamp, err := f.stat(name)
	if err != nil || stamp == e.stamp {
		if err != nil {
			f.log.Error("nie można sprawdzić szablonu, używam poprzedniej wersji", "file", name, "err", err)
		}
		return e.tmpl, nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if cur := f.templates[name]; cur.stamp != e.stamp {
		// Inne żądanie już przeładowało plik.
		return cur.tmpl, nil
	}
	ne, err := f.loadTemplate(name)
	if err != nil {
		f.log.Error("błąd w nowej wersji szablonu, używam poprzedniej", "file", name, "err", err)
		// Zapamiętaj wersję, żeby nie parsować błędnego pliku przy każdym żądaniu.
		f.templates[name] = &templateEntry{stamp: stamp, tmpl: e.tmpl}
		return e.tmpl, nil
	}
	f.log.Info("przeładowano szablon", "file", name)
	f.templates[name] = ne
	return ne.tmpl, nil
}

// Static zwraca aktualną zawartość pliku statycznego.
func (f *Files) Static(name string) (*StaticFile, error) {
	f.mu.RLock()
	s, ok := f.static[name]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("nieznany plik %s", name)
	}

	stamp, err := f.stat(name)
	if err != nil || stamp == s.stamp {
		if err != nil {
			f.log.Error("nie można sprawdzić pliku, używam poprzedniej wersji", "file", name, "err", err)
		}
		return s, nil
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if cur := f.static[name]; cur.stamp != s.stamp {
		return cur, nil
	}
	ns, err := f.loadStatic(name)
	if err != nil {
		f.log.Error("nie można wczytać nowej wersji pliku, używam poprzedniej", "file", name, "err", err)
		kept := *s
		kept.stamp = stamp
		f.static[name] = &kept
		return &kept, nil
	}
	f.log.Info("przeładowano plik", "file", name)
	f.static[name] = ns
	return ns, nil
}

func (f *Files) stat(name string) (fileStamp, error) {
	info, err := os.Stat(filepath.Join(f.dir, name))
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{modTime: info.ModTime(), size: info.Size()}, nil
}

func (f *Files) read(name string) ([]byte, fileStamp, error) {
	path := filepath.Join(f.dir, name)
	fh, err := os.Open(path)
	if err != nil {
		return nil, fileStamp{}, fmt.Errorf("%s: %w", name, err)
	}
	defer fh.Close()
	info, err := fh.Stat()
	if err != nil {
		return nil, fileStamp{}, fmt.Errorf("%s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fileStamp{}, fmt.Errorf("%s: nie jest zwykłym plikiem", name)
	}
	data, err := io.ReadAll(io.LimitReader(fh, maxFileSize+1))
	if err != nil {
		return nil, fileStamp{}, fmt.Errorf("%s: %w", name, err)
	}
	if len(data) > maxFileSize {
		return nil, fileStamp{}, fmt.Errorf("%s: plik większy niż %d KiB", name, maxFileSize>>10)
	}
	return data, fileStamp{modTime: info.ModTime(), size: info.Size()}, nil
}

func (f *Files) loadTemplate(name string) (*templateEntry, error) {
	data, stamp, err := f.read(name)
	if err != nil {
		return nil, err
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &templateEntry{stamp: stamp, tmpl: t}, nil
}

func (f *Files) loadStatic(name string) (*StaticFile, error) {
	data, stamp, err := f.read(name)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &StaticFile{
		Name:    name,
		Content: data,
		ModTime: stamp.modTime,
		ETag:    `"` + hex.EncodeToString(sum[:8]) + `"`,
		stamp:   stamp,
	}, nil
}
