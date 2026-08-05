//go:build integration

// This package's integration suite has ~28 test functions across TWO
// files -- builder_integration_test.go (package imagebuild_test,
// black-box) and builder_whitebox_integration_test.go (package
// imagebuild, white-box -- needs unexported access to attemptRefresh's
// own early-return branches that RefreshOnce's public entry point can
// never reach) -- and before this file, every one of those spun up its
// OWN brand-new throwaway Postgres container via testcontainers-go, via
// TWO independent, byte-for-byte-identical copies of that same
// container-start-plus-migrate helper: builder_integration_test.go's own
// newTestPool and builder_whitebox_integration_test.go's own
// newWhiteboxTestPool. Even at a healthy ~1-3s per container start, that
// is real container-churn overhead across every test run, before any
// actual test logic runs.
//
// This mirrors, package-for-package, internal/adapters/inbound/httpapi's
// own perf/httpapi-integration-container-reuse fix (see that package's
// own sharedpool_integration_test.go for the original, more heavily
// documented precedent this file follows, and internal/adapters/inbound/
// github's own identical white-box/black-box follow-up): exactly one
// container/pool for the WHOLE test binary instead of one per test,
// exposed via IntegrationTestPool/IntegrationTestPoolAndConnStr (below),
// with per-test isolation preserved via a full TRUNCATE + seed-data
// restore registered as t.Cleanup rather than relying on unique IDs
// alone.
//
// The TWO-WAY duplication specifically WITHIN this one package is a
// deliberate, still-valid choice, for the SAME reason httpapi's own
// four-way duplication was, and the reason builder_whitebox_integration_
// test.go's own doc comment already gives: package imagebuild's white-box
// test file cannot import package imagebuild_test's own unexported
// newTestPool -- a reverse import Go does not allow. This file resolves
// that the other way around, exactly like httpapi's/github's own
// versions: exactly one container for the WHOLE test binary, exposed via
// an EXPORTED accessor (IntegrationTestPool/IntegrationTestPoolAndConnStr,
// below) that package imagebuild_test's own files can reach perfectly
// well -- Go's test tooling links package imagebuild_test against the
// test-augmented variant of package imagebuild (i.e. including this
// file's own exported declarations), not the production-only one.
//
// builder_integration_test.go ALSO already had its own TestMain (wiring
// a single global OTel MeterProvider for this whole binary's
// metric-delta assertions, e.g. readFailureStreak/readPermanentlyFailed/
// readRefreshClaimReclaimed) -- that TestMain now lives here too, merged
// with this file's own container startup (Go allows exactly one TestMain
// per test binary). Since it has to live in package imagebuild (not
// imagebuild_test) for the white-box/black-box reason above, the
// ManualReader it wires is now exported too (IntegrationOtelReader,
// below) so builder_integration_test.go's own call sites can still reach
// it, qualified.
//
// # Why this is safe against async Actor background work
//
// Neither test file in this package ever constructs a sessionactor.
// Registry or touches TriggerDispatch/EnsureDispatched at all (verified:
// Builder.RefreshOnce/PumpOnce/Run and attemptRefresh are called
// directly) -- so condition #1 from httpapi's own doc comment (pool-
// before-registry ordering) simply does not apply here. A few tests use
// a naked `go func(){ done <- ... }()` (TestRefreshOnce_
// OldRefStaysServableDuringRefresh, TestBuilderRun_
// RefreshPumpGoroutineStarts, and one more), but every one is
// synchronously joined via a channel read before the test function
// itself returns, so no Registry-style drain-ordering hazard exists
// here. This package also never calls t.Parallel() anywhere (verified).
//
// # Why a full TRUNCATE, not per-test unique IDs alone
//
// Same rationale as httpapi's own version: an audit of this package's
// own tests found the same class of assertion that depends on a fully
// reset table, not merely unique IDs -- TestRefreshOnce_BatchCap_
// BoundsRowsPerTickButPicksUpRemainderLater and TestRefreshOnce_
// StarvationFreedom_GenuinelyStaleRowNotStarvedByStaticFrontCohort both
// assert exact bounded counts from ListReady's global `ORDER BY
// updated_at LIMIT` query, which would break if another test's leftover
// 'ready'/stale rows persisted. Reproducing exactly what a fresh
// container already guaranteed (a byte-for-byte-empty, freshly-migrated
// database at the start of every test) via TRUNCATE was judged the
// safer, smaller change here too, matching httpapi's own documented
// tradeoff exactly. prepareDatabaseReset (below) also restores any
// migration-seeded row (e.g. prompt_templates' one seed row,
// migrations/000033_intent_classifier.up.sql), so this keeps working
// unchanged even if a future migration seeds a different/additional
// table.
package imagebuild

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
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

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

// IntegrationOtelReader is the SINGLE ManualReader backing the SINGLE,
// GLOBAL SDK MeterProvider TestMain below registers for this whole test
// binary -- see builder_integration_test.go's own former doc comment
// (moved here) for the full "why exactly once, not once per test"
// reasoning. Exported (unlike this file's other shared state, which
// stays reachable only via IntegrationTestPool below) specifically so
// package imagebuild_test's own call sites (readFailureStreak et al.,
// builder_integration_test.go) can still reach it, now qualified as
// imagebuild.IntegrationOtelReader.
var IntegrationOtelReader *sdkmetric.ManualReader

// TestMain replaces this package's old per-test container lifecycle with
// a single one for the whole binary, AND wires the global OTel
// MeterProvider builder_integration_test.go used to wire in its own
// TestMain (before this package also needed a shared container -- see
// this file's own top doc comment). Only one TestMain is allowed per
// test binary (already navigated once elsewhere in this repo -- this
// package's own builder_whitebox_integration_test.go doc comment is
// httpapi's own cited precedent for that constraint), so this is now the
// ONE place in this whole package (spanning both package imagebuild's
// and package imagebuild_test's own test files) that starts/stops
// Postgres and wires OTel.
func TestMain(m *testing.M) {
	ctx := context.Background()

	container, connStr, err := startSharedTestContainer(ctx)
	if err != nil {
		log.Fatalf("imagebuild: start shared integration-test container: %v", err)
	}

	if err := runMigrations(connStr); err != nil {
		log.Fatalf("imagebuild: run migrations against shared integration-test container: %v", err)
	}

	pool, err := narvipg.NewPool(ctx, connStr)
	if err != nil {
		log.Fatalf("imagebuild: open shared integration-test pool: %v", err)
	}

	if err := prepareDatabaseReset(ctx, pool); err != nil {
		log.Fatalf("imagebuild: prepare shared integration-test database reset: %v", err)
	}

	sharedPool = pool
	sharedConnStr = connStr

	IntegrationOtelReader = sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(IntegrationOtelReader))
	otel.SetMeterProvider(mp)

	code := m.Run()

	_ = mp.Shutdown(context.Background())
	pool.Close()
	if err := testcontainers.TerminateContainer(container); err != nil {
		log.Printf("imagebuild: terminate shared integration-test container: %v", err)
	}
	os.Exit(code)
}

// startSharedTestContainer is this package's own former newTestPool/
// newWhiteboxTestPool's identical container-start logic, run exactly
// once now instead of once per test -- including the SAME hardening (an
// independent watchdog raced against the startup call itself, not
// relying solely on context cancellation) those copies each already
// carried, for the exact reasons documented at each of their own
// original call sites (CI runs 30831633470/30834918806: a stalled
// CI-runner Docker daemon that did not honor context cancellation even
// one layer inside testcontainers-go's own wait strategy).
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
// that already had at least one row at this point (prompt_templates,
// migrations/000033, plus Step 54's FK-dependent workflow seed rows,
// migrations/000057), restored in FK-dependency order
// (orderTablesForSeedRestore, below) -- each
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

	restoreOrder, err := orderTablesForSeedRestore(ctx, pool, tables)
	if err != nil {
		return err
	}

	restoreStatements = nil
	for _, name := range restoreOrder {
		quotedName := pgx.Identifier{name}.Sanitize()
		var hasRows bool
		if err := pool.QueryRow(ctx, fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s LIMIT 1)", quotedName)).Scan(&hasRows); err != nil {
			return fmt.Errorf("check %s for seed rows: %w", name, err)
		}
		if !hasRows {
			continue
		}
		snapshotName := pgx.Identifier{"__seed_snapshot_" + name}.Sanitize()
		if _, err := pool.Exec(ctx, fmt.Sprintf("CREATE TABLE %s AS TABLE %s", snapshotName, quotedName)); err != nil {
			return fmt.Errorf("snapshot seed data for %s: %w", name, err)
		}
		restoreStatements = append(restoreStatements, fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", quotedName, snapshotName))
	}
	return nil
}

// orderTablesForSeedRestore returns tables reordered parents-first over
// pg_constraint's FOREIGN KEY edges (Kahn's algorithm, with the incoming
// pg_tables name order as the deterministic tie-break), so the
// seed-restore INSERTs above only ever insert a child row after the
// parent rows it references already exist. The plain alphabetical order
// this replaces was only ever correct while prompt_templates (FK-free)
// was the sole migration-seeded table -- migrations/000057_workflows.
// up.sql is the first to seed FK-DEPENDENT rows, and workflow_bindings
// sorts alphabetically BEFORE the workflow_definitions rows it
// references, so an unordered restore would violate that FK on every
// reset. Self-referencing FKs are ignored (a table's own rows restore
// in one statement, whose FK checks run at statement end, so
// intra-table parents are always visible); a genuine cross-table FK
// cycle (none exists in this schema) falls back to name order for
// whatever remains rather than failing the whole suite.
func orderTablesForSeedRestore(ctx context.Context, pool *pgxpool.Pool, tables []string) ([]string, error) {
	rows, err := pool.Query(ctx, `
		SELECT con.conrelid::regclass::text, con.confrelid::regclass::text
		FROM pg_constraint con
		WHERE con.contype = 'f'
		  AND con.connamespace = 'public'::regnamespace
		  AND con.conrelid <> con.confrelid`)
	if err != nil {
		return nil, fmt.Errorf("list foreign-key edges: %w", err)
	}
	defer rows.Close()

	inSet := make(map[string]bool, len(tables))
	for _, name := range tables {
		inSet[name] = true
	}
	parents := make(map[string]map[string]bool, len(tables))
	for rows.Next() {
		var child, parent string
		if err := rows.Scan(&child, &parent); err != nil {
			return nil, fmt.Errorf("scan foreign-key edge: %w", err)
		}
		if !inSet[child] || !inSet[parent] {
			continue
		}
		if parents[child] == nil {
			parents[child] = make(map[string]bool)
		}
		parents[child][parent] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate foreign-key edges: %w", err)
	}

	ordered := make([]string, 0, len(tables))
	placed := make(map[string]bool, len(tables))
	remaining := append([]string(nil), tables...)
	for len(remaining) > 0 {
		var deferred []string
		progressed := false
		for _, name := range remaining {
			ready := true
			for parent := range parents[name] {
				if !placed[parent] {
					ready = false
					break
				}
			}
			if ready {
				ordered = append(ordered, name)
				placed[name] = true
				progressed = true
			} else {
				deferred = append(deferred, name)
			}
		}
		if !progressed {
			ordered = append(ordered, deferred...)
			break
		}
		remaining = deferred
	}
	return ordered, nil
}

// IntegrationTestPool returns this whole test binary's ONE shared
// Postgres pool (started once by TestMain above) and registers a
// t.Cleanup that resets the database back to the exact same pristine,
// freshly-migrated state a brand-new per-test container used to provide
// -- see this file's own top doc comment for why a full reset (rather
// than relying on unique IDs alone) is the right tradeoff for this
// specific package's own existing tests. Exported (unlike the rest of
// this file) specifically so package imagebuild_test's own files can
// reach it -- see this file's own top doc comment for why that is
// possible at all.
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
		t.Fatal("imagebuild: IntegrationTestPoolAndConnStr called before TestMain finished setting up the shared pool")
	}
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
		t.Fatalf("imagebuild: reset shared integration-test database (truncate): %v", err)
	}
	for _, stmt := range restoreStatements {
		if _, err := sharedPool.Exec(ctx, stmt); err != nil {
			t.Fatalf("imagebuild: reset shared integration-test database (restore seed data): %v", err)
		}
	}
}
