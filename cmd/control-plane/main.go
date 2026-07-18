// Command control-plane is the Narvi control plane binary: config, wiring,
// migrations, HTTP+WS server. Config loading + validation landed in PR-02
// (§5.4); structured logging + OTel bootstrap landed in PR-03 (§5.3); PR-06
// adds the real dev-loop server: a Postgres pool + boot-time migrations, a
// chi router with a real /health, and errgroup-managed graceful shutdown
// (§5.2, §10-P0). The full REST/WS API lands in later PRs.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver, used only for the golang-migrate handle below
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/adapters/inbound/wshub"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// This is intentionally a bare-bones dispatch, not a flag-parsing library:
// there is exactly one subcommand today ("serve"). Anything else prints a
// one-line usage message to stderr and exits non-zero.
func main() {
	if len(os.Args) < 2 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: control-plane serve")
		os.Exit(1)
	}

	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// serve loads config, wires logging/OTel (unchanged from PR-02/PR-03),
// opens the Postgres pool and applies embedded migrations, then runs the
// chi-routed HTTP server until SIGINT/SIGTERM, shutting down gracefully
// within Timeouts.ShutdownGracePeriod. The listen goroutine and the
// shutdown-watcher goroutine are both launched via errgroup.Group.Go —
// never a bare `go` statement (§11: no naked goroutines).
func serve() error {
	cfg, err := platform.Load()
	if err != nil {
		return err
	}

	logger := platform.NewLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownOTel, err := platform.SetupOTel(ctx, "narvi-control-plane")
	if err != nil {
		return err
	}
	defer func() {
		// Deliberately a fresh background context, not ctx: by the time
		// this deferred call runs, ctx may already be canceled (that's
		// exactly what triggers shutdown below), and a canceled context
		// would make the flush itself fail immediately.
		if err := shutdownOTel(context.Background()); err != nil {
			slog.Error("otel shutdown failed", "error", err)
		}
	}()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open postgres pool: %w", err)
	}
	defer pool.Close()

	if err := applyMigrations(cfg.DatabaseURL); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	// Registry/wshub are wired into the real binary for the first time in
	// this Step (Step 18) -- an intended, natural consequence of that: the
	// timer pump (already built in Step 11) becomes genuinely live here for
	// the first time too, run via the errgroup below.
	registry := sessionactor.NewRegistry(ctx, pool, cfg.Timeouts)
	sandboxStore := postgres.NewSandboxStore(pool)

	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.Use(platform.CorrelationIDMiddleware)
	// Deliberately NOT chi's own middleware.Logger/RequestID: PR-03 already
	// built our own correlation-id + platform.Logger(ctx) convention above,
	// and stacking chi's competing convention on top would give every
	// request two different request-identity mechanisms.
	router.Get("/health", healthHandler(pool, cfg.Timeouts))
	router.Get("/sessions/{sessionID}/ws", wshub.NewSandboxHandler(registry, sandboxStore, cfg.Timeouts))

	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: router,
	}

	group, groupCtx := errgroup.WithContext(ctx)

	group.Go(func() error {
		slog.Info("narvi control-plane: listening", "addr", cfg.HTTPAddr, "stage", cfg.Stage)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		if err := registry.RunTimerPump(groupCtx); err != nil && !errors.Is(err, context.Canceled) {
			// RunTimerPump returns ctx.Err() (context.Canceled) on normal
			// shutdown -- that must NOT be treated as a fatal error the way
			// http.ErrServerClosed is already specially unwrapped for the
			// listener goroutine above; only a genuinely different error is
			// surfaced here.
			return fmt.Errorf("timer pump: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		<-groupCtx.Done()
		slog.Info("narvi control-plane: shutting down", "grace_period", cfg.Timeouts.ShutdownGracePeriod.String())

		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Timeouts.ShutdownGracePeriod)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("http server shutdown: %w", err)
		}

		// Registry.Shutdown cancels every live actor's run loop and waits
		// for all of them, plus the timer-pump goroutine above, to finish.
		// Its own errgroup.Wait() will very likely surface context.Canceled
		// from every actor whose run loop was still alive at shutdown time
		// -- expected/benign, not a real failure, so it gets the exact same
		// context.Canceled carve-out as the timer pump above; anything else
		// is a genuine shutdown failure.
		if err := registry.Shutdown(); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("session actor registry shutdown: %w", err)
		}
		return nil
	})

	return group.Wait()
}

// applyMigrations runs the embedded migrations (migrations.FS) up against
// dsn, using the exact same iofs-source + golang-migrate/database/postgres
// pattern already proven in
// internal/adapters/outbound/postgres/postgres_integration_test.go.
// golang-migrate's Postgres driver takes its own internal advisory lock, so
// this is safe to call on every boot regardless of replica count — no extra
// locking needed here.
func applyMigrations(dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open migration db handle: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			slog.Error("close migration db handle failed", "error", closeErr)
		}
	}()

	dbDriver, err := migratepg.WithInstance(db, &migratepg.Config{})
	if err != nil {
		return fmt.Errorf("migrate postgres driver: %w", err)
	}

	srcDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("migrate iofs source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", srcDriver, "pgx", dbDriver)
	if err != nil {
		return fmt.Errorf("migrate.NewWithInstance: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}

// healthResponse is the /health JSON body: {"status":"ok"} on success, or
// {"status":"unhealthy"} on failure. The underlying error is logged
// server-side only — pool.Ping's error text can include the DB user,
// database name, and host:port, which an unauthenticated caller has no
// business learning.
type healthResponse struct {
	Status string `json:"status"`
}

// healthHandler backs /health with a real pool.Ping bounded by
// timeouts.HealthCheckTimeout, so a stuck DB reports 503 within that bound
// rather than hanging the handler indefinitely — never panics (Recoverer
// is also mounted above it as a second line of defense), never hangs past
// the timeout.
func healthHandler(pool *pgxpool.Pool, timeouts platform.Timeouts) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeouts.HealthCheckTimeout)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")

		if err := pool.Ping(ctx); err != nil {
			slog.Error("health handler: db ping failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			if encErr := json.NewEncoder(w).Encode(healthResponse{Status: "unhealthy"}); encErr != nil {
				slog.Error("health handler: encode unhealthy response", "error", encErr)
			}
			return
		}

		w.WriteHeader(http.StatusOK)
		if encErr := json.NewEncoder(w).Encode(healthResponse{Status: "ok"}); encErr != nil {
			slog.Error("health handler: encode ok response", "error", encErr)
		}
	}
}
