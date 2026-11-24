package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

// Session lifetimes from the requirements, counted from login.
const (
	PersistentTTL = 90 * 24 * time.Hour
	BrowserTTL    = 12 * time.Hour
)

const (
	fileVersion        = 1
	defaultMaxSessions = 100
	maxStoreFileSize   = 1 << 20
)

// ErrLocked means that the session file is in use by another process
// (usually the running service).
var ErrLocked = errors.New("plik sesji jest zablokowany przez inny proces (czy usługa działa?)")

// Session is the session data. The identifier itself is not stored.
type Session struct {
	Created    time.Time
	Expires    time.Time
	Persistent bool
}

type fileFormat struct {
	Version  int           `json:"version"`
	Sessions []fileSession `json:"sessions"`
}

type fileSession struct {
	IDHash     string    `json:"id_hash"`
	Created    time.Time `json:"created"`
	Expires    time.Time `json:"expires"`
	Persistent bool      `json:"persistent"`
}

// StoreOptions configures the store. Zero fields get default values.
type StoreOptions struct {
	MaxSessions int
	Now         func() time.Time
	Log         *slog.Logger
}

// Store keeps sessions in memory and in a JSON file. Every change writes the
// whole file atomically; the in-memory state changes only after a successful write.
// For its whole lifetime Store holds a flock lock on the file <path>.lock.
type Store struct {
	path string
	opt  StoreOptions
	lock *os.File

	mu       sync.RWMutex
	sessions map[string]fileSession // key: hash of the identifier
}

// OpenStore locks and reads the session file. A missing file means no sessions.
// A corrupt or oversized file is moved aside with a .corrupt-… suffix,
// and the store starts empty (logging in again is enough).
func OpenStore(path string, o StoreOptions) (*Store, error) {
	if o.MaxSessions == 0 {
		o.MaxSessions = defaultMaxSessions
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}

	lock, err := acquireLock(path + ".lock")
	if err != nil {
		return nil, err
	}
	s := &Store{path: path, opt: o, lock: lock}

	sessions, err := s.load()
	if err != nil {
		s.Close()
		return nil, err
	}
	s.sessions = sessions
	return s, nil
}

func acquireLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("plik blokady sesji: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("blokada pliku sesji: %w", err)
	}
	return f, nil
}

// Close releases the file lock.
func (s *Store) Close() error {
	if s.lock == nil {
		return nil
	}
	syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	err := s.lock.Close()
	s.lock = nil
	return err
}

func (s *Store) load() (map[string]fileSession, error) {
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]fileSession{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("odczyt pliku sesji: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxStoreFileSize+1))
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("odczyt pliku sesji: %w", err)
	}

	sessions, perr := parseStore(data)
	if perr != nil {
		bad := fmt.Sprintf("%s.corrupt-%s", s.path, s.opt.Now().UTC().Format("20060102T150405Z"))
		if err := os.Rename(s.path, bad); err != nil {
			return nil, fmt.Errorf("plik sesji jest uszkodzony (%v) i nie da się go przenieść: %w", perr, err)
		}
		s.opt.Log.Error("plik sesji uszkodzony, przeniesiono go i rozpoczęto bez sesji",
			"err", perr, "moved_to", filepath.Base(bad))
		return map[string]fileSession{}, nil
	}

	now := s.opt.Now()
	for k, v := range sessions {
		if !v.Expires.After(now) {
			delete(sessions, k)
		}
	}
	return sessions, nil
}

func parseStore(data []byte) (map[string]fileSession, error) {
	if len(data) > maxStoreFileSize {
		return nil, fmt.Errorf("plik większy niż %d KiB", maxStoreFileSize>>10)
	}
	var ff fileFormat
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ff); err != nil {
		return nil, errors.New("niepoprawny JSON")
	}
	if ff.Version != fileVersion {
		return nil, fmt.Errorf("nieobsługiwana wersja %d", ff.Version)
	}
	out := make(map[string]fileSession, len(ff.Sessions))
	for _, fs := range ff.Sessions {
		if len(fs.IDHash) != 64 || fs.Expires.IsZero() {
			return nil, errors.New("niepoprawny wpis sesji")
		}
		out[fs.IDHash] = fs
	}
	return out, nil
}

// Create creates a session and returns its identifier for the cookie.
func (s *Store) Create(persistent bool) (string, Session, error) {
	token, err := newToken()
	if err != nil {
		return "", Session{}, fmt.Errorf("losowanie identyfikatora sesji: %w", err)
	}
	now := s.opt.Now()
	ttl := BrowserTTL
	if persistent {
		ttl = PersistentTTL
	}
	fs := fileSession{IDHash: hashToken(token), Created: now, Expires: now.Add(ttl), Persistent: persistent}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.liveCopy(now)
	next[fs.IDHash] = fs
	trimOldest(next, s.opt.MaxSessions)
	if err := s.persist(next); err != nil {
		return "", Session{}, err
	}
	s.sessions = next
	return token, toSession(fs), nil
}

// Lookup returns the valid session with the given identifier. It does not extend it.
func (s *Store) Lookup(token string) (Session, bool) {
	if !validToken(token) {
		return Session{}, false
	}
	h := hashToken(token)
	s.mu.RLock()
	fs, ok := s.sessions[h]
	s.mu.RUnlock()
	if !ok || !fs.Expires.After(s.opt.Now()) {
		return Session{}, false
	}
	return toSession(fs), true
}

// Delete invalidates a session. If the write fails, the session stays valid.
func (s *Store) Delete(token string) error {
	if !validToken(token) {
		return nil
	}
	h := hashToken(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[h]; !ok {
		return nil
	}
	next := s.liveCopy(s.opt.Now())
	delete(next, h)
	if err := s.persist(next); err != nil {
		return err
	}
	s.sessions = next
	return nil
}

// Len returns the number of valid sessions.
func (s *Store) Len() int {
	now := s.opt.Now()
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := 0
	for _, v := range s.sessions {
		if v.Expires.After(now) {
			n++
		}
	}
	return n
}

// liveCopy returns a copy of the map without expired sessions. Requires the lock.
func (s *Store) liveCopy(now time.Time) map[string]fileSession {
	out := make(map[string]fileSession, len(s.sessions)+1)
	for k, v := range s.sessions {
		if v.Expires.After(now) {
			out[k] = v
		}
	}
	return out
}

func trimOldest(m map[string]fileSession, max int) {
	if len(m) <= max {
		return
	}
	all := make([]fileSession, 0, len(m))
	for _, v := range m {
		all = append(all, v)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Created.Before(all[j].Created) })
	for _, v := range all[:len(all)-max] {
		delete(m, v.IDHash)
	}
}

// persist writes the file atomically: a temporary file in the same directory,
// fsync, rename, fsync of the directory.
func (s *Store) persist(m map[string]fileSession) error {
	ff := fileFormat{Version: fileVersion, Sessions: make([]fileSession, 0, len(m))}
	for _, v := range m {
		ff.Sessions = append(ff.Sessions, v)
	}
	sort.Slice(ff.Sessions, func(i, j int) bool { return ff.Sessions[i].Created.Before(ff.Sessions[j].Created) })
	data, err := json.MarshalIndent(ff, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("zapis pliku sesji: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("zapis pliku sesji: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("zapis pliku sesji: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("zapis pliku sesji: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("zapis pliku sesji: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("zapis pliku sesji: %w", err)
	}
	ok = true
	if d, err := os.Open(dir); err == nil {
		if err := d.Sync(); err != nil {
			s.opt.Log.Warn("fsync katalogu sesji", "err", err)
		}
		d.Close()
	}
	return nil
}

func toSession(fs fileSession) Session {
	return Session{Created: fs.Created, Expires: fs.Expires, Persistent: fs.Persistent}
}

// RevokeAll removes all sessions. It fails with ErrLocked when the file is
// in use by the running service. It returns the number of valid sessions removed.
func RevokeAll(path string, o StoreOptions) (int, error) {
	s, err := OpenStore(path, o)
	if err != nil {
		return 0, err
	}
	defer s.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.sessions)
	if err := s.persist(map[string]fileSession{}); err != nil {
		return 0, err
	}
	s.sessions = map[string]fileSession{}
	return n, nil
}
