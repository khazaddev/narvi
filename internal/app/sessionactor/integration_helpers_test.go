//go:build integration

// Integration tests proving app/sessionactor's locking/fencing/pump
// mechanics against a REAL Postgres instance (§9.1, Step 11's own
// verification requirement) -- gated behind the "integration" build tag,
// matching internal/adapters/outbound/postgres/postgres_integration_test.
// go's own conventions exactly (testcontainers Postgres, embedded
// migrations via golang-migrate's iofs source driver, a real *pgxpool.
// Pool). Run via `make test-integration`.
//
// This file (and its sibling _test.go files under the same build tag) use
// `package sessionactor`, not `sessionactor_test`: TestActorTransact_
// StaleEpochEvictsSelf specifically needs white-box access to Actor's own
// unexported transact method to assert ErrStaleEpoch directly, rather
// than inferring it indirectly through logs.
package sessionactor

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every
// embedded migration up (proving migration 000014's actor_epoch column
// lands correctly alongside the rest), and returns a ready *pgxpool.Pool.
// t.Cleanup tears down both the pool and the container.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	// startCtx bounds the container-startup call below via the ambient
	// context (image pull + Docker daemon round trip + Postgres's own
	// internal ready-wait) -- kept as defense in depth, but NOT solely
	// relied upon any more: CI run 30834918806 showed this exact bound
	// (added after CI run 30831633470's own ContainerStart hang) itself
	// fail to actually cut the call off when the hang recurred one layer
	// deeper, inside testcontainers-go's own wait.(*LogStrategy).
	// WaitUntilReady -- the goroutine dump showed it looping on a 100ms
	// poll for the FULL 10-minute panic window, never once observing
	// ctx.Done(), despite this same context chain being correctly wired
	// all the way through (confirmed directly: reproducing an
	// impossible-to-satisfy wait condition locally against this exact
	// call DOES correctly time out via this same context mechanism, at
	// testcontainers' own hardcoded 60s deadline -- so the mechanism is
	// sound in isolation, but evidently not dependable against whatever a
	// genuinely stalled CI-runner Docker daemon does to it in practice).
	//
	// Rather than keep chasing exactly why context cancellation isn't
	// always honored deep inside a third-party library under conditions
	// this dev machine cannot reproduce, the startup call now ALSO runs on
	// its own goroutine (via errgroup.Group.Go -- no naked `go` statement,
	// §11) raced against an independent, plain time.After watchdog:
	// whichever of "the call returned" or "the watchdog fired" happens
	// first decides the outcome, with no dependency on any context
	// cancellation actually being honored by anything downstream. If the
	// watchdog wins, the goroutine is deliberately abandoned (leaked, not
	// joined) rather than blocking this test's own cleanup on a call that
	// has already demonstrated it can ignore its own cancellation signal.
	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	const containerStartWatchdog = 2*time.Minute + 15*time.Second
	type containerStartResult struct {
		container *tcpostgres.PostgresContainer
		err       error
	}
	startCh := make(chan containerStartResult, 1)
	var startGroup errgroup.Group
	startGroup.Go(func() error {
		container, err := tcpostgres.Run(startCtx, "postgres:17-alpine",
			tcpostgres.WithDatabase("narvi_test"),
			tcpostgres.WithUsername("narvi"),
			tcpostgres.WithPassword("narvi"),
			tcpostgres.BasicWaitStrategies(),
		)
		startCh <- containerStartResult{container: container, err: err}
		return nil
	})

	var container *tcpostgres.PostgresContainer
	var err error
	select {
	case res := <-startCh:
		container, err = res.container, res.err
		if err != nil {
			t.Fatalf("start postgres container: %v", err)
		}
	case <-time.After(containerStartWatchdog):
		t.Fatalf("start postgres container: tcpostgres.Run did not return within %s -- Docker daemon likely "+
			"stalled without honoring context cancellation (see this function's own doc comment)", containerStartWatchdog)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Errorf("terminate container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	migrateDB, err := sql.Open("pgx", connStr)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = migrateDB.Close() })

	dbDriver, err := migratepg.WithInstance(migrateDB, &migratepg.Config{})
	if err != nil {
		t.Fatalf("migratepg.WithInstance: %v", err)
	}
	srcDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("iofs.New: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", srcDriver, "pgx", dbDriver)
	if err != nil {
		t.Fatalf("migrate.NewWithInstance: %v", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}

	// pool_max_conns is set explicitly, generously above this package's own
	// worst-case concurrent need -- VERIFIED root cause of a real,
	// reproducible CI hang: pgxpool.ParseConfig's own default is
	// max(4, runtime.NumCPU()) (pgxpool's own doc comment on Config.MaxConns;
	// confirmed against the vendored v5.10.0 source, pgxpool/pool.go), so on
	// a low-core-count GitHub Actions runner (2-4 vCPUs) this pool defaults
	// to as few as 4 connections, vs. this dev machine's own 12. Registry.
	// hydrateAndAcquire (hydrate.go) pins ONE pool connection per live Actor
	// for that Actor's ENTIRE lifetime (lockConn, holding the session's
	// Postgres advisory lock) -- never released until the Actor itself
	// evicts (idle TTL, default 30m here) or the test's own t.Cleanup calls
	// Registry.Shutdown. A test that spawns several sessions' worth of
	// Actors without shutting the earlier ones down first (e.g.
	// TestRepoAccessGate_RepeatedIndeterminateFailures_CircuitBreakerSkipsFurtherNetworkCalls,
	// repoaccessgate_integration_test.go, which keeps repoAccessCheckBreakerThreshold+3
	// == 6 Actors alive simultaneously) can therefore need MORE permanently-held
	// connections than a 4-conn pool has AT ALL, on top of whatever transient
	// connections concurrent hydration/transact calls need -- and
	// hydrateAndAcquire's own r.pool.Acquire(ctx) has no timeout of its own,
	// so a caller passing the (typical, in this package's own tests)
	// unbounded context.Background() blocks FOREVER once the pool is
	// genuinely out of connections, rather than failing fast. CONFIRMED by
	// forcing this same "pool_max_conns=4" ceiling locally (this dev
	// machine's own 12-core default never reproduces it) and observing
	// TestRepoAccessGate_RepeatedIndeterminateFailures_CircuitBreakerSkipsFurtherNetworkCalls
	// hang indefinitely in pool.Acquire on the 5th of its 6 concurrently-held
	// Actors, exactly matching CI run 30819618607's own goroutine dump
	// (pgxpool.(*Pool).Acquire -> puddle.Pool.acquire -> semaphore.Acquire)
	// -- not a timing-dependent race at all, just a pool sized for this dev
	// machine's own (much higher) core count, silently too small on a
	// low-core-count CI runner. This param is deliberately appended to a
	// SEPARATE connection string, never connStr above: pgx's plain
	// database/sql driver (migrateDB) forwards any query parameter it does
	// not itself recognize straight to Postgres as a server-side GUC --
	// unlike pgxpool.ParseConfig, which intercepts every "pool_*"-prefixed
	// one for itself -- so appending it to connStr made migration itself
	// fail with "unrecognized configuration parameter" (caught while
	// verifying this fix). 20 is comfortably above every current
	// sessionactor integration test's own worst-case concurrent-Actor count
	// (6, the test above), independent of the host's core count.
	poolConnStr, err := container.ConnectionString(ctx, "sslmode=disable", "pool_max_conns=20")
	if err != nil {
		t.Fatalf("pool connection string: %v", err)
	}

	pool, err := narvipg.NewPool(ctx, poolConnStr)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// createTestSession inserts a minimal session row and returns its id.
func createTestSession(ctx context.Context, t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()

	created, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
	})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	return created.ID
}

// waitUntil polls cond every 20ms until it reports true, or fails the
// test once timeout elapses. Used throughout these integration tests to
// observe the eventual effect of an actor's own asynchronous mailbox
// processing (Send only enqueues; the actual write/eviction happens on
// the actor's own goroutine) without coupling the test to internal
// timing.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %v", timeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
