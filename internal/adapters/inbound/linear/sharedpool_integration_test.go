//go:build integration

// This package's integration suite has ~33 test functions across 9
// *_integration_test.go files (all package linear_test -- no white-box
// split needed here), and before this file, every one of those spun up
// its OWN brand-new throwaway Postgres container via testcontainers-go
// (webhook_integration_test.go's own newTestPool, reused by every other
// file). Even at a healthy ~1-3s per container start, that is real
// container-churn overhead across every test run, before any actual test
// logic runs.
//
// This mirrors, package-for-package, internal/adapters/inbound/httpapi's
// own perf/httpapi-integration-container-reuse fix (see that package's
// own sharedpool_integration_test.go for the original, more heavily
// documented precedent this file follows): exactly one container/pool
// for the WHOLE test binary instead of one per test, exposed via
// IntegrationTestPool/IntegrationTestPoolAndConnStr (below), with
// per-test isolation preserved via a full TRUNCATE + seed-data restore
// registered as t.Cleanup rather than relying on unique IDs alone.
//
// # Why this is safe against this package's own async Actor background work
//
// This package's tests construct a real sessionactor.Registry via one
// shared helper, newHandlerDeps (webhook_integration_test.go), which
// every one of this package's own rig constructors calls -- every one of
// its ~14 call sites across 8 files is preceded by `pool :=
// newTestPool(t)` in the same function, so `t.Cleanup(func() { _ =
// registry.Shutdown() })` always registers after the pool's own cleanup.
// Go's LIFO t.Cleanup ordering then guarantees registry.Shutdown() (a
// synchronous, fully-draining wait) always finishes before this file's
// own truncate-and-restore cleanup ever runs -- including
// TestWebhookHandler_FailedFirstAttemptReleasesBothClaimsForRedelivery's
// own two-registries-one-pool case, and turnconsolidation_integration_
// test.go's own TestWebhookHandler_Prompted_ConcurrentReplies_L2_
// OnlyOneSucceeds (8 concurrent webhook POSTs via errgroup, eg.Wait()
// blocking for all HTTP responses before the test proceeds, so the
// correctly-ordered registry.Shutdown() cleanup fully drains any
// resulting actor activity before that test's own truncate ever runs).
// callback_integration_test.go never touches sessionactor at all
// (OAuth-callback only), so this concern doesn't apply there. This
// package also never calls t.Parallel() anywhere (verified).
//
// Separately, this branch's own commit 557a4fa ("fix(linear): stop the
// log-buffer assertion racing the async actor spawn") already fixed an
// unrelated but structurally similar bug in this exact package: 4 slog-
// capture sites (reviseemptyfeedback_integration_test.go x2,
// setsessionid_retry_integration_test.go, turnconsolidation_integration_
// test.go) used a bare, unsynchronized bytes.Buffer/strings.Builder that
// raced the actor's own background log write under -race. That fix
// (already on this branch, unrelated to and unaffected by this change)
// made those 4 sites mutex-safe; this file's own container-reuse change
// does not reintroduce or depend on anything about that fix.
//
// # Why a full TRUNCATE, not per-test unique IDs alone
//
// Same rationale as httpapi's own version: an audit of this package's
// own tests found one genuine whole-table/unscoped assertion that would
// break under any leftover cross-test state -- identity_integration_
// test.go's own TestWebhookHandler_Prompted_UnknownUserCreatesLinkPrompt
// AndIsDenied asserts `SELECT count(*) FROM turns` (no WHERE at all)
// expecting exactly 1. Reproducing exactly what a fresh container
// already guaranteed (a byte-for-byte-empty, freshly-migrated database
// at the start of every test) via TRUNCATE was judged the safer, smaller
// change here too, matching httpapi's own documented tradeoff exactly.
// prepareDatabaseReset (below) also restores any migration-seeded row
// (e.g. prompt_templates' one seed row, migrations/000033_intent_
// classifier.up.sql), so this keeps working unchanged even if a future
// migration seeds a different/additional table.
package linear_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"golang.org/x/sync/errgroup"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/migrations"
)

// sharedPool/sharedConnStr are set exactly once, by TestMain below,
// before any test in this binary runs, and never mutated again.
// truncateStatement/restoreStatements are computed once in the same
// place -- see prepareDatabaseReset's own doc comment.
var (
	sharedPool        *pgxpool.Pool
	sharedConnStr     string
	truncateStatement string
	restoreStatements []string
)

// TestMain replaces this package's old per-test container lifecycle with
// a single one for the whole binary -- see this file's own top doc
// comment for the full container-reuse story. This package had no
// existing TestMain before this change (verified), so there is no merge
// concern here.
func TestMain(m *testing.M) {
	ctx := context.Background()

	container, connStr, err := startSharedTestContainer(ctx)
	if err != nil {
		log.Fatalf("linear: start shared integration-test container: %v", err)
	}

	if err := runMigrations(connStr); err != nil {
		log.Fatalf("linear: run migrations against shared integration-test container: %v", err)
	}

	pool, err := narvipg.NewPool(ctx, connStr)
	if err != nil {
		log.Fatalf("linear: open shared integration-test pool: %v", err)
	}

	if err := prepareDatabaseReset(ctx, pool); err != nil {
		log.Fatalf("linear: prepare shared integration-test database reset: %v", err)
	}

	sharedPool = pool
	sharedConnStr = connStr

	code := m.Run()

	pool.Close()
	if err := testcontainers.TerminateContainer(container); err != nil {
		log.Printf("linear: terminate shared integration-test container: %v", err)
	}
	os.Exit(code)
}

// startSharedTestContainer is this package's own former newTestPool's
// identical container-start logic, run exactly once now instead of once
// per test -- including the SAME hardening (an independent watchdog
// raced against the startup call itself, not relying solely on context
// cancellation) that copy already carried, for the exact reasons
// documented at its own original call site (CI runs 30831633470/
// 30834918806: a stalled CI-runner Docker daemon that did not honor
// context cancellation even one layer inside testcontainers-go's own
// wait strategy).
func startSharedTestContainer(ctx context.Context) (*tcpostgres.PostgresContainer, string, error) {
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
			return nil, "", fmt.Errorf("start postgres container: %w", err)
		}
	case <-time.After(containerStartWatchdog):
		return nil, "", fmt.Errorf("start postgres container: tcpostgres.Run did not return within %s -- Docker daemon likely "+
			"stalled without honoring context cancellation (see this function's own doc comment)", containerStartWatchdog)
	}

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, "", fmt.Errorf("connection string: %w", err)
	}
	return container, connStr, nil
}

// runMigrations is this package's former per-test migration-running
// logic, now run exactly once against the shared container above.
func runMigrations(connStr string) error {
	migrateDB, err := sql.Open("pgx", connStr)
	if err != nil {
		return fmt.Errorf("sql.Open: %w", err)
	}
	defer func() { _ = migrateDB.Close() }()

	dbDriver, err := migratepg.WithInstance(migrateDB, &migratepg.Config{})
	if err != nil {
		return fmt.Errorf("migratepg.WithInstance: %w", err)
	}
	srcDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("iofs.New: %w", err)
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

// prepareDatabaseReset runs once, immediately after migrating, and
// computes the two pieces every per-test reset (resetSharedTestDatabase,
// below) needs: truncateStatement, a single `TRUNCATE TABLE ...
// RESTART IDENTITY CASCADE` naming every real table migrations created
// (discovered from pg_tables, not hand-maintained, so this never drifts
// out of sync with the schema -- CASCADE lets one statement handle every
// FK relationship among them regardless of order); and restoreStatements,
// one `INSERT INTO t SELECT * FROM __seed_snapshot_t` for every table
// that already had at least one row at this point (today, exactly
// prompt_templates -- see this file's own top doc comment) -- each
// backed by a literal `CREATE TABLE __seed_snapshot_t AS TABLE t` copy
// taken here, once, of that pristine post-migration data. schema_
// migrations (golang-migrate's own bookkeeping table) is deliberately
// excluded from both: it must survive untouched, and is never re-applied
// within this same test binary's lifetime anyway.
func prepareDatabaseReset(ctx context.Context, pool *pgxpool.Pool) error {
	rows, err := pool.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public' AND tablename <> 'schema_migrations'
		ORDER BY tablename`)
	if err != nil {
		return fmt.Errorf("list tables: %w", err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan tablename: %w", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tables: %w", err)
	}
	if len(tables) == 0 {
		return fmt.Errorf("no tables found after migrating -- pg_tables query is likely wrong")
	}

	quoted := make([]string, len(tables))
	for i, name := range tables {
		quoted[i] = pgx.Identifier{name}.Sanitize()
	}
	truncateStatement = fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE", strings.Join(quoted, ", "))

	restoreStatements = nil
	for i, name := range tables {
		var hasRows bool
		if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s LIMIT 1)", quoted[i])).Scan(&hasRows); err != nil {
			return fmt.Errorf("check %s for seed rows: %w", name, err)
		}
		if !hasRows {
			continue
		}
		snapshotName := pgx.Identifier{"__seed_snapshot_" + name}.Sanitize()
		if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s AS TABLE %s", snapshotName, quoted[i])); err != nil {
			return fmt.Errorf("snapshot seed data for %s: %w", name, err)
		}
		restoreStatements = append(restoreStatements, fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", quoted[i], snapshotName))
	}
	return nil
}

// IntegrationTestPool returns this whole test binary's ONE shared
// Postgres pool (started once by TestMain above) and registers a
// t.Cleanup that resets the database back to the exact same pristine,
// freshly-migrated state a brand-new per-test container used to provide
// -- see this file's own top doc comment for why this is safe against
// this package's own async sessionactor.Actor background work, and why a
// full reset (rather than relying on unique IDs alone) is the right
// tradeoff for this specific package's own existing tests.
func IntegrationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, _ := IntegrationTestPoolAndConnStr(t)
	return pool
}

// IntegrationTestPoolAndConnStr is IntegrationTestPool plus the shared
// container's raw connection string, for the rare test that needs to
// open its OWN differently-configured pool against the same database
// rather than reuse the shared-default pool.
func IntegrationTestPoolAndConnStr(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	if sharedPool == nil {
		t.Fatal("linear: IntegrationTestPoolAndConnStr called before TestMain finished setting up the shared pool")
	}
	// Registered before any caller can possibly construct its own
	// sessionactor.Registry (every call site in this package obtains its
	// pool first) -- see this file's own top doc comment for why that
	// ordering, combined with Go's own LIFO t.Cleanup semantics, is what
	// makes this safe.
	t.Cleanup(func() { resetSharedTestDatabase(t) })
	return sharedPool, sharedConnStr
}

// resetSharedTestDatabase truncates every real table and restores
// whatever pristine seed data prepareDatabaseReset snapshotted -- see
// that function's own doc comment.
func resetSharedTestDatabase(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := sharedPool.Exec(ctx, truncateStatement); err != nil {
		t.Fatalf("linear: reset shared integration-test database (truncate): %v", err)
	}
	for _, stmt := range restoreStatements {
		if _, err := sharedPool.Exec(ctx, stmt); err != nil {
			t.Fatalf("linear: reset shared integration-test database (restore seed data): %v", err)
		}
	}
}
