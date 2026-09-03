// Command billycore runs the BillyCore daemon.
//
// One binary, one SQLite file, one bearer token, bound to loopback.
// See ARCHITECTURE.md §2 and SECURITY.md §4.
package main

import (
	"context"
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

	"github.com/alejandroayalad/billycore/internal/adapter/api"
	"github.com/alejandroayalad/billycore/internal/adapter/config"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/profile"
	"github.com/alejandroayalad/billycore/internal/adapter/source/gmail"
	"github.com/alejandroayalad/billycore/internal/adapter/store/sqlite"
	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// Secrets arrive through the environment, never through flags: a flag value is
// visible in `ps` to every user on the machine. None of these are ever logged
// or echoed in an error (SECURITY.md §10).
const (
	tokenEnv        = "BILLYCORE_TOKEN"
	clientIDEnv     = "BILLYCORE_GMAIL_CLIENT_ID"
	clientSecretEnv = "BILLYCORE_GMAIL_CLIENT_SECRET"
)

// databaseFile is the one file the user backs up. D22 fixes where it lives; D15
// and D22 both insist the backup gesture stays "copy billy.db" and never "copy
// ~/.billy", because credentials.json is in that directory.
const databaseFile = "billy.db"

// shutdownGrace bounds how long a sync in flight may finish in.
const shutdownGrace = 30 * time.Second

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
		case "peek":
			return runPeek(args)
		case "tx":
			return runTx(args)
		case "serve":
			return runServe(args)
		default:
			return fmt.Errorf("unknown command %q (commands: serve, auth, peek, tx)", cmd)
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
	dir := fs.String("dir", defaultDir, "data directory holding billy.db, credentials.json, and sources.json")
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

	db, err := sqlite.Open(filepath.Join(*dir, databaseFile))
	if err != nil {
		return err
	}
	defer db.Close()

	sources, household, err := config.LoadFile(*dir)
	if err != nil {
		return err
	}
	targets, err := syncTargets(sources, *dir)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		// Not a startup failure: BillyCore serves, and every sync answers 404
		// until a Source is configured (D26).
		slog.Warn("no Source is configured: every sync will answer 404",
			"file", filepath.Join(*dir, config.SourcesFile))
	}

	evidence := sqlite.NewEvidenceRepository(db)

	// The pipeline: one background worker draining extraction and then
	// Transaction construction, woken by whoever records Evidence (D45).
	// One queue object serving both stages. They are separate ports because they
	// are consumed by separate use cases (D6), not because two things move rows
	// through one table.
	queue := sqlite.NewEvidenceQueue(db)
	extractor := app.NewExtractor(queue, sqlite.NewClaimRepository(db), profile.New())
	transactions := sqlite.NewTransactionRepository(db).WithHousehold(household)
	reconciler := app.NewReconciler(queue, transactions)
	matcher := app.NewMatcher(sqlite.NewReconciliationRepository(db))
	pipelineWake, wakePipeline := newWakeSignal()
	worker := newPipelineWorker(extractor, reconciler, matcher, pipelineWake)

	server := api.NewServer(app.NewIngestor(evidence), evidence, transactions, configuredSources(sources), targets, db, wakePipeline)

	// The viewing page is served at the bind address root (D32, D73).
	slog.Info("billycore starting", "addr", *addr, "dir", *dir, "sources", len(sources),
		"transactions", "http://"+*addr+"/")

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(token),
		ReadHeaderTimeout: 5 * time.Second,
	}

	signalled, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The worker runs under a context this function can end, not only one a
	// signal can: a listener that dies must take the pipeline down with it
	// rather than leaving a worker draining a database nobody is serving from.
	ctx, cancel := context.WithCancel(signalled)
	defer cancel()

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		worker.Run(ctx)
	}()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var serveErr error
	select {
	case serveErr = <-errCh:
	case <-ctx.Done():
		slog.Info("shutting down")
	}

	// Cancelling first is what lets the worker stop cleanly: Extractor and
	// Reconciler release rows they claimed and had not yet attempted, so a
	// restart finds them claimable at once instead of waiting out a lease.
	cancel()

	// A sync in flight may be most of the way through a mailbox. Give it room
	// to finish rather than abandoning work that is already fetched. The same
	// grace bounds the wait for the worker: one budget for the shutdown, not
	// one per goroutine.
	shutdownCtx, done := context.WithTimeout(context.Background(), shutdownGrace)
	defer done()
	shutdownErr := srv.Shutdown(shutdownCtx)

	select {
	case <-workerDone:
	case <-shutdownCtx.Done():
		// It is mid-batch and holding leases. They lapse on their own within a
		// minute, which is the whole reason the lock is a timestamp (D39).
		slog.Warn("the pipeline worker did not stop within the shutdown grace")
	}

	return errors.Join(serveErr, shutdownErr)
}

// syncTargets turns configuration into something the API can sync.
//
// The fetcher is a function rather than a value because building it is where an
// access token gets refreshed: a client built once at startup would stop working
// an hour into the daemon's life (D26).
func syncTargets(sources []config.Source, dir string) (map[string]api.SyncTarget, error) {
	targets := make(map[string]api.SyncTarget, len(sources))
	for _, source := range sources {
		switch source.Type {
		case domain.SourceGmail:
			query := source.Query
			targets[source.ID] = api.SyncTarget{
				Source: app.Source{ID: source.ID, Type: source.Type, Profile: source.ExtractionProfile},
				Fetcher: func(ctx context.Context) (app.SourceFetcher, error) {
					client, err := gmail.NewClient(ctx, dir)
					if err != nil {
						return nil, err
					}
					return gmail.NewFetcher(client, query), nil
				},
			}
		case domain.SourceBankStatement, domain.SourceManual:
			// Direct Sources are configured so POST /v1/evidence can resolve
			// their type and profile. They have no fetcher (D53).
			continue
		default:
			// Configuration that names a Source kind BillyCore cannot fetch is
			// a startup error, not a 404 discovered later.
			return nil, fmt.Errorf("source %q is of type %s, which has no fetcher yet", source.ID, source.Type)
		}
	}
	return targets, nil
}

func configuredSources(sources []config.Source) map[string]app.Source {
	configured := make(map[string]app.Source, len(sources))
	for _, source := range sources {
		configured[source.ID] = app.Source{
			ID: source.ID, Type: source.Type, Profile: source.ExtractionProfile,
		}
	}
	return configured
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
	manual := fs.Bool("manual", false, "paste the callback URL instead of listening for it; use when the browser is on another machine")
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

	g, err := gmail.Authorize(ctx, clientID, clientSecret, *manual)
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

// runPeek lists messages from a Source without recording anything.
//
// A diagnostic, not a pipeline stage: it opens no database and creates no
// Evidence. It exists because CONTEXT.md §3 step 5 — look at real bank email
// before designing a parser — is the step the whole milestone is for.
//
// The query is a flag rather than configuration, because where Source
// configuration belongs is still open (CONTEXT.md §5 item 3) and D21 forbids
// answering that in passing.
func runPeek(args []string) error {
	fs := flag.NewFlagSet("peek", flag.ExitOnError)
	defaultDir, err := defaultDataDir()
	if err != nil {
		return err
	}
	dir := fs.String("dir", defaultDir, "data directory holding billy.db and credentials.json")
	query := fs.String("query", "from:nu@nu.com.mx", "Gmail search query")
	limit := fs.Int("limit", 25, "maximum messages to list")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := gmail.NewClient(ctx, *dir)
	if err != nil {
		return err
	}
	ids, err := client.ListIDs(ctx, *query, *limit)
	if err != nil {
		return err
	}
	fmt.Printf("%d message(s) matching %q\n\n", len(ids), *query)

	for _, id := range ids {
		m, err := client.GetMetadata(ctx, id)
		if err != nil {
			// One bad message must not stop the listing (SECURITY.md §7).
			fmt.Printf("%-18s  ERROR: %v\n", id, err)
			continue
		}
		fmt.Printf("%-18s  %s  %7d  %s\n",
			m.ID, m.InternalDate.Format("2006-01-02 15:04"), m.SizeEstimate, truncate(m.Subject, 60))
	}
	return nil
}

// truncate keeps the listing one line per message on a normal terminal.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
