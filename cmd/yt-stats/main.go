// Polecenie yt-stats uruchamia prywatny panel statystyk kanału YouTube.
//
// Użycie:
//
//	yt-stats [-config ścieżka]                 uruchamia serwer
//	yt-stats hash-password                     tworzy hash Argon2id hasła
//	yt-stats revoke-sessions [-config ścieżka] usuwa wszystkie sesje
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

	_ "time/tzdata" // strefy czasowe niezależne od systemu

	"yt-stats/internal/auth"
	"yt-stats/internal/config"
	"yt-stats/internal/stats"
	"yt-stats/internal/web"
	"yt-stats/internal/youtube"
)

// version ustawia linker: -ldflags "-X main.version=...".
var version = "dev"

const defaultConfigPath = "/etc/yt-stats/config.json"

// shutdownTimeout to czas na dokończenie trwających żądań przy
// zatrzymaniu. Zmienna, żeby test mógł go skrócić.
var shutdownTimeout = 10 * time.Second

// env to zależności procesu podmieniane w testach.
type env struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	// listen otwiera gniazdo nasłuchu; w produkcji net.Listen.
	listen func(network, address string) (net.Listener, error)
}

func main() {
	// Kontekst kończy się po SIGINT lub SIGTERM (np. systemctl stop).
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
  yt-stats version             wypisuje wersję
`, version, defaultConfigPath)
}

// cmdServe wczytuje konfigurację i pliki interfejsu, otwiera gniazdo
// i obsługuje żądania do zakończenia ctx.
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
	if err != nil { // walidacja konfiguracji już to sprawdziła
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

// cmdRevokeSessions usuwa wszystkie sesje. Działa tylko przy zatrzymanej
// usłudze, bo usługa trzyma blokadę pliku sesji.
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

// newServer tworzy serwer HTTP z limitami z wymagań.
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

// serve obsługuje żądania na ln do zakończenia ctx, potem zamyka serwer,
// czekając do shutdownTimeout na trwające żądania. Zamyka ln.
func serve(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {
	srv := newServer(h, log)

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("serwer uruchomiony", "version", version, "listen", ln.Addr().String())

	select {
	case err := <-errc:
		// Serve kończy się samo tylko przy błędzie gniazda.
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
