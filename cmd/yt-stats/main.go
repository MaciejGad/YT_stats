// Command yt-stats runs a private statistics panel for a YouTube channel.
//
// Usage:
//
//	yt-stats [-config path]                 runs the server
//	yt-stats hash-password                  creates an Argon2id password hash
//	yt-stats revoke-sessions [-config path] removes all sessions
//	yt-stats check-config [-config path]    checks the configuration and files
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "time/tzdata" // time zones independent of the system

	"yt-stats/internal/auth"
	"yt-stats/internal/config"
	"yt-stats/internal/stats"
	"yt-stats/internal/web"
	"yt-stats/internal/youtube"
)

// version is set by the linker: -ldflags "-X main.version=...".
var version = "dev"

const defaultConfigPath = "/etc/yt-stats/config.json"

// shutdownTimeout is how long in-flight requests get to finish on
// shutdown. A variable so that a test can shorten it.
var shutdownTimeout = 10 * time.Second

// env holds process dependencies that tests replace.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	// listen opens the listening socket; net.Listen in production.
	listen func(network, address string) (net.Listener, error)
}

func main() {
	// The context ends on SIGINT or SIGTERM (e.g. systemctl stop).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], env{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
		listen: net.Listen,
	})
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, e env) int {
	if len(args) > 0 {
		switch args[0] {
		case "hash-password":
			return cmdHashPassword(args[1:], e.stdin, e.stdout, e.stderr)
		case "revoke-sessions":
			return cmdRevokeSessions(args[1:], e)
		case "check-config":
			return cmdCheckConfig(args[1:], e)
		case "version":
			fmt.Fprintln(e.stdout, version)
			return 0
		case "help", "-h", "--help":
			usage(e.stdout)
			return 0
		}
	}
	return cmdServe(ctx, args, e)
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `yt-stats %s

Użycie:
  yt-stats [-config ścieżka]   uruchamia serwer (domyślnie %s)
  yt-stats hash-password       tworzy hash Argon2id hasła do pola password_hash
  yt-stats revoke-sessions [-config ścieżka]
                               usuwa wszystkie sesje (przy zatrzymanej usłudze)
  yt-stats check-config [-config ścieżka]
                               sprawdza konfigurację i pliki interfejsu
  yt-stats version             wypisuje wersję
`, version, defaultConfigPath)
}

// cmdServe loads the configuration and interface files, opens the socket
// and serves requests until ctx is done.
func cmdServe(ctx context.Context, args []string, e env) int {
	fs := flag.NewFlagSet("yt-stats", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	configPath := fs.String("config", defaultConfigPath, "ścieżka pliku konfiguracji")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "nieznane polecenie %q\n", fs.Arg(0))
		usage(e.stderr)
		return 2
	}

	log := slog.New(slog.NewTextHandler(e.stderr, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(e.stderr, "%s: %v\n", *configPath, err)
		return 1
	}
	files, err := web.LoadFiles(cfg.WebDir, log)
	if err != nil {
		fmt.Fprintf(e.stderr, "pliki interfejsu: %v\n", err)
		return 1
	}

	hash, err := auth.ParseHash(cfg.PasswordHash)
	if err != nil { // config validation has already checked this
		fmt.Fprintf(e.stderr, "password_hash: %v\n", err)
		return 1
	}
	authenticator, err := auth.NewAuthenticator(cfg.Username, hash, auth.AuthOptions{})
	if err != nil {
		fmt.Fprintf(e.stderr, "weryfikacja hasła: %v\n", err)
		return 1
	}

	yt, err := youtube.New(youtube.Options{
		ChannelID:      cfg.ChannelID,
		APIKey:         cfg.APIKey,
		APIKeyFallback: cfg.APIKeyFallback,
		Log:            log,
	})
	if err != nil {
		fmt.Fprintf(e.stderr, "klient YouTube: %v\n", err)
		return 1
	}

	sessions, err := auth.OpenStore(cfg.SessionFile, auth.StoreOptions{Log: log})
	if err != nil {
		fmt.Fprintf(e.stderr, "plik sesji %s: %v\n", cfg.SessionFile, err)
		return 1
	}
	defer sessions.Close()

	ln, err := e.listen("tcp", cfg.Listen)
	if err != nil {
		fmt.Fprintf(e.stderr, "nasłuch na %s: %v\n", cfg.Listen, err)
		return 1
	}

	h := web.NewHandler(web.Deps{
		Files:    files,
		Log:      log,
		Sessions: sessions,
		PreLogin: auth.NewPreLogin(0, 0, nil),
		Auth:     authenticator,
		Limiter:  auth.NewLimiter(auth.LimiterOptions{}),
		Stats:    stats.New(yt, stats.Options{Log: log}),
	})
	log.Info("wczytano sesje", "active", sessions.Len())
	if err := serve(ctx, ln, h, log); err != nil {
		return 1
	}
	return 0
}

// cmdRevokeSessions removes all sessions. It works only while the service
// is stopped, because the service holds the session file lock.
func cmdRevokeSessions(args []string, e env) int {
	fs := flag.NewFlagSet("revoke-sessions", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	configPath := fs.String("config", defaultConfigPath, "ścieżka pliku konfiguracji")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "revoke-sessions: nieoczekiwany argument %q\n", fs.Arg(0))
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(e.stderr, "%s: %v\n", *configPath, err)
		return 1
	}
	n, err := auth.RevokeAll(cfg.SessionFile, auth.StoreOptions{})
	if errors.Is(err, auth.ErrLocked) {
		fmt.Fprintln(e.stderr, "revoke-sessions: usługa działa i trzyma plik sesji; zatrzymaj ją (systemctl stop yt-stats) i spróbuj ponownie")
		return 1
	}
	if err != nil {
		fmt.Fprintf(e.stderr, "revoke-sessions: %v\n", err)
		return 1
	}
	fmt.Fprintf(e.stdout, "Usunięto sesje: %d\n", n)
	return 0
}

// newServer creates the HTTP server with the limits from the requirements.
func newServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      40 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// serve handles requests on ln until ctx is done, then shuts the server down,
// waiting up to shutdownTimeout for in-flight requests. It closes ln.
func serve(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {
	srv := newServer(h, log)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("serwer uruchomiony", "version", version, "listen", ln.Addr().String())

	select {
	case err := <-errc:
		// Serve returns on its own only on a socket error.
		log.Error("serwer zakończył działanie", "err", err)
		return err
	case <-ctx.Done():
	}

	log.Info("zatrzymywanie serwera")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("zatrzymanie serwera", "err", err)
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		log.Error("serwer zakończył działanie", "err", err)
		return err
	}
	log.Info("serwer zatrzymany")
	return nil
}

// cmdCheckConfig checks the configuration and interface files without starting
// the server or locking the session file, so it works next to a running service.
func cmdCheckConfig(args []string, e env) int {
	fs := flag.NewFlagSet("check-config", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	configPath := fs.String("config", defaultConfigPath, "ścieżka pliku konfiguracji")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(e.stderr, "check-config: nieoczekiwany argument %q\n", fs.Arg(0))
		return 2
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(e.stderr, "%s: %v\n", *configPath, err)
		return 1
	}
	if _, err := web.LoadFiles(cfg.WebDir, slog.New(slog.DiscardHandler)); err != nil {
		fmt.Fprintf(e.stderr, "pliki interfejsu: %v\n", err)
		return 1
	}
	fmt.Fprintf(e.stdout, "Konfiguracja %s jest poprawna.\n", *configPath)
	return 0
}
