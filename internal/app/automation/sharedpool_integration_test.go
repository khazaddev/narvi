//go:build integration

// This package's integration suite spans TWO files -- automation_
// integration_test.go (package automation_test, black-box, exercising
// Engine's exported PumpOnce/ReconcileOnce/SweepOnce/CreateInvocation) and
// closeout_whitebox_integration_test.go (package automation, white-box --
// needs unexported access to closeInvocation/applyFailureStrike to stress
// the §3.5 failure-strike CAS directly, the way builder_whitebox_
// integration_test.go needs unexported access to attemptRefresh in
// internal/app/imagebuild) -- and, mirroring that package's own
// sharedpool_integration_test.go (the direct precedent this file follows,
// package-for-package), exactly ONE shared Postgres container/pool for the
// WHOLE test binary instead of one per test, exposed via
// IntegrationTestPool/IntegrationTestPoolAndConnStr below, with per-test
// isolation preserved via a full TRUNCATE + seed-data restore registered as
// t.Cleanup.
//
// This file lives in package automation (not automation_test) for the
// SAME reason imagebuild's own version does: closeout_whitebox_integration_
// test.go cannot import package automation_test's own unexported
// declarations (a reverse import Go does not allow), so the shared
// container/pool lives here instead, exposed via an EXPORTED accessor
// (IntegrationTestPool/IntegrationTestPoolAndConnStr) that package
// automation_test's own files can reach perfectly well -- Go's test
// tooling links package automation_test against the test-augmented variant
// of package automation (i.e. including this file's own exported
// declarations), not the production-only one.
//
// # Why a full TRUNCATE, not per-test unique IDs alone
//
// Same rationale as imagebuild's own version: this package's own tests
// assert exact counts from queries with no natural per-invocation/
// per-automation scoping in their own WHERE clause (ListInFlightRuns,
// ListOrphanedStartingRuns/ListOrphanedRunningRuns), which would break if
// another test's leftover rows persisted. A byte-for-byte-empty, freshly-
// migrated database at the start of every test is the safer, smaller
// change here too.
package automation

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

// sharedPool/sharedConnStr are set exactly once, by TestMain below, before
// any test in this binary runs, and never mutated again.
// truncateStatement/restoreStatements are computed once in the same place
// -- see prepareDatabaseReset's own doc comment.
var (
	sharedPool        *pgxpool.Pool
	sharedConnStr     string
	truncateStatement string
	restoreStatements []string
)

// TestMain starts exactly one shared Postgres container/pool for this
// whole test binary -- mirrors app/imagebuild's own identical TestMain
// exactly (minus the OTel MeterProvider wiring: this package constructs no
// custom OTel instruments of its own).
func TestMain(m *testing.M) {
	ctx := context.Background()

	container, connStr, err := startSharedTestContainer(ctx)
	if err != nil {
		log.Fatalf("automation: start shared integration-test container: %v", err)
	}

	if err := runMigrations(connStr); err != nil {
		log.Fatalf("automation: run migrations against shared integration-test container: %v", err)
	}

	// MaxConns is set explicitly to 20, well above pgxpool.ParseConfig's own
	// default of max(4, runtime.NumCPU()) -- mirroring internal/app/
	// sessionactor's own identical fix (TestMain, sharedpool_integration_
	// test.go there) for the EXACT same root cause, reproduced here: CI run
	// 30954282160 hung the full Go test-binary panic-timeout inside THIS
	// package's own TestPumpOnce_FansOutOneRunPerTarget, goroutine-dumped
	// stuck in sessionactor.(*Actor).transact -> pgxpool.(*Pool).Acquire.
	// Every fanned-out target this engine dispatches calls httpapi.
	// TriggerDispatch (fanout.go's own createRunAndSession) ->
	// registry.GetOrSpawn, and Registry.hydrateAndAcquire (sessionactor/
	// hydrate.go) pins ONE pool connection per live Actor for that Actor's
	// entire lifetime (holding the session's own Postgres advisory lock) --
	// never released until the Actor evicts or this test's own t.Cleanup
	// calls Registry.Shutdown. §3.5's own fan-out cap
	// (domainautomation.MaxFanOutTargets = 10, see TestPumpOnce_
	// RespectsMaxFanOutOfTen) means a single PumpOnce tick in this package's
	// own tests can pin up to 10 Actor connections simultaneously, on top of
	// whatever transient connections concurrent hydration/store queries
	// need at the same moment -- on a low-core-count GitHub Actions runner
	// this pool's previous unset MaxConns silently defaulted to as few as 4,
	// and hydrateAndAcquire's own r.pool.Acquire(ctx) has no timeout (every
	// caller here passes context.Background()), so once genuinely out of
	// connections it blocks forever instead of failing fast. 20 is
	// comfortably above this package's own worst case (10), independent of
	// the host's core count.
	pool, err := narvipg.NewPoolWithMaxConns(ctx, connStr, 20)
	if err != nil {
		log.Fatalf("automation: open shared integration-test pool: %v", err)
	}

	if err := prepareDatabaseReset(ctx, pool); err != nil {
		log.Fatalf("automation: prepare shared integration-test database reset: %v", err)
	}

	sharedPool = pool
	sharedConnStr = connStr

	code := m.Run()

	pool.Close()
	if err := testcontainers.TerminateContainer(container); err != nil {
		log.Printf("automation: terminate shared integration-test container: %v", err)
	}
	os.Exit(code)
}

// startSharedTestContainer mirrors app/imagebuild's own identical
// container-start logic (including its own independent watchdog raced
// against the startup call itself, for the exact CI-runner Docker-daemon
// stall reasons documented at that function's own original call site).
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

// runMigrations runs every migration against the shared container above,
// exactly once.
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

// prepareDatabaseReset runs once, immediately after migrating -- mirrors
// app/imagebuild's own identical helper exactly.
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

// IntegrationTestPool returns this whole test binary's ONE shared Postgres
// pool (started once by TestMain above) and registers a t.Cleanup that
// resets the database back to a pristine, freshly-migrated state.
// Exported (unlike the rest of this file) specifically so package
// automation_test's own files can reach it.
func IntegrationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, _ := IntegrationTestPoolAndConnStr(t)
	return pool
}

// IntegrationTestPoolAndConnStr is IntegrationTestPool plus the shared
// container's raw connection string, for the rare test that needs to open
// its own differently-configured pool against the same database.
func IntegrationTestPoolAndConnStr(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	if sharedPool == nil {
		t.Fatal("automation: IntegrationTestPoolAndConnStr called before TestMain finished setting up the shared pool")
	}
	t.Cleanup(func() { resetSharedTestDatabase(t) })
	return sharedPool, sharedConnStr
}

// resetSharedTestDatabase truncates every real table and restores whatever
// pristine seed data prepareDatabaseReset snapshotted.
func resetSharedTestDatabase(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := sharedPool.Exec(ctx, truncateStatement); err != nil {
		t.Fatalf("automation: reset shared integration-test database (truncate): %v", err)
	}
	for _, stmt := range restoreStatements {
		if _, err := sharedPool.Exec(ctx, stmt); err != nil {
			t.Fatalf("automation: reset shared integration-test database (restore seed data): %v", err)
		}
	}
}
