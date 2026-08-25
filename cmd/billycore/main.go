// Command billycore runs the BillyCore daemon.
//
// One binary, one SQLite file, one bearer token, bound to loopback.
// See ARCHITECTURE.md §2 and SECURITY.md §4.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/alejandroayalad/billycore/internal/adapter/source/gmail"
)

// Secrets arrive through the environment, never through flags: a flag value is
// visible in `ps` to every user on the machine. None of these are ever logged
// or echoed in an error (SECURITY.md §10).
const (
	tokenEnv        = "BILLYCORE_TOKEN"
	clientIDEnv     = "BILLYCORE_GMAIL_CLIENT_ID"
	clientSecretEnv = "BILLYCORE_GMAIL_CLIENT_SECRET"
)

func main() {
	if err := run(); err != nil {
		slog.Error("billycore stopped", "error", err)
		os.Exit(1)
	}
}

// run dispatches to a subcommand, or serves when none is given.
//
// `auth` is separate from the daemon on purpose: the OAuth grant needs a human
// and a browser exactly once, and SECURITY.md §6 refuses to turn BillyCore into
// a binary you babysit through every restart.
func run() error {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		cmd, args := os.Args[1], os.Args[2:]
		switch cmd {
		case "auth":
			return runAuth(args)
		case "serve":
			return runServe(args)
		default:
			return fmt.Errorf("unknown command %q (commands: serve, auth)", cmd)
		}
	}
	return runServe(os.Args[1:])
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	defaultDir, err := defaultDataDir()
	if err != nil {
		return err
	}
	addr := fs.String("addr", "127.0.0.1:8787", "address to bind; loopback unless you mean otherwise")
	dir := fs.String("dir", defaultDir, "data directory holding billy.db and credentials.json")
	if err := fs.Parse(args); err != nil {
		return err
	}

	token := os.Getenv(tokenEnv)
	if token == "" {
		// A default-open financial API is not an acceptable failure mode.
		return fmt.Errorf("%s is not set: refusing to start without a bearer token", tokenEnv)
	}
	if !isLoopback(*addr) {
		// The token is weak authentication and is only adequate because the
		// listener is not reachable off-host. Say so loudly. SECURITY.md §4.
		slog.Warn("binding to a non-loopback address: the bearer token is now the only control",
			"addr", *addr)
	}

	if err := ensureDataDir(*dir); err != nil {
		return err
	}
	slog.Info("billycore starting", "addr", *addr, "dir", *dir)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// handleHealthz reports whether the process can serve requests. Unauthenticated
// and deliberately outside /v1 (API.md §11). It will need a database check the
// moment there is a database to check.
func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":      "ok",
		"api_version": "v1",
	})
}

// defaultDataDir is ~/.billy — one predictable location the user can name, back
// up, and delete. DECISIONS.md D22.
func defaultDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".billy"), nil
}

// ensureDataDir creates the data directory at 0700 and refuses a wider one.
// The 0600 files inside it are only as private as the directory holding them.
func ensureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cannot create data directory %s: %w", dir, err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("data directory %s is mode %04o: refusing to use a directory others can read (want 0700)", dir, perm)
	}
	return nil
}

// isLoopback reports whether the bind address is unreachable off-host. This is
// a security control, not a formatting check: SECURITY.md §4 treats loopback as
// the primary mitigation and the token as the second line.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	// An empty host is NOT loopback. net.SplitHostPort(":8787") yields host ""
	// with no error, and http.Server binds that to every interface — so ":8787",
	// the most common way to expose a Go server, is precisely the case that must
	// warn rather than the case that is exempt.
	return host == "127.0.0.1" || host == "::1" || host == "localhost"
}

// runAuth performs the one interactive OAuth grant and stores the refresh token.
//
// It writes credentials.json and nothing else: no database is opened, and no
// Source configuration is recorded, because where that belongs is still an open
// question (CONTEXT.md §5 item 3).
func runAuth(args []string) error {
	fs := flag.NewFlagSet("auth", flag.ExitOnError)
	defaultDir, err := defaultDataDir()
	if err != nil {
		return err
	}
	dir := fs.String("dir", defaultDir, "data directory holding billy.db and credentials.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureDataDir(*dir); err != nil {
		return err
	}

	creds, err := gmail.LoadCredentials(*dir)
	if err != nil {
		return err
	}

	clientID, clientSecret := os.Getenv(clientIDEnv), os.Getenv(clientSecretEnv)
	if clientID == "" || clientSecret == "" {
		// Fall back to what a previous grant stored, so re-authorizing does not
		// require digging the client credentials out again.
		if creds.Gmail != nil {
			clientID, clientSecret = creds.Gmail.ClientID, creds.Gmail.ClientSecret
		}
	}
	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("%s and %s must be set: create an OAuth 2.0 Client ID of type \"Desktop app\" in the Google Cloud console, with the Gmail API enabled", clientIDEnv, clientSecretEnv)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	g, err := gmail.Authorize(ctx, clientID, clientSecret)
	if err != nil {
		return err
	}
	creds.Gmail = g
	if err := creds.Save(*dir); err != nil {
		return err
	}
	// The token itself is never printed. SECURITY.md §10.
	slog.Info("gmail authorized", "scope", gmail.ScopeReadonly, "credentials", filepath.Join(*dir, gmail.CredentialsFile))
	return nil
}
