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

	// startCtx bounds ONLY the container-startup call below (image pull +
	// Docker daemon round trip + Postgres's own internal ready-wait) --
	// an unbounded context.Background() here can hang for Go's own full
	// 10-minute test-binary panic timeout if the CI runner's Docker daemon
	// stalls (CONFIRMED: CI run 30831633470's own goroutine dump showed
	// exactly this, blocked in moby/moby client.ContainerStart via
	// net/http.(*persistConn).roundTrip, panicking the whole test binary
	// after 10m0s and burning that binary's entire remaining test budget).
	// A healthy container start normally takes single-digit seconds; 2
	// minutes is generous margin for a slow image pull on a cold runner
	// cache while still failing fast, with an honest error, well short of
	// that 10-minute ceiling. ctx itself (unbounded) is still used for
	// everything else below, unchanged.
	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(startCtx, "postgres:17-alpine",
		tcpostgres.WithDatabase("narvi_test"),
		tcpostgres.WithUsername("narvi"),
		tcpostgres.WithPassword("narvi"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
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
