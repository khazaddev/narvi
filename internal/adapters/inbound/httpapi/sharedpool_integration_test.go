//go:build integration

// This package's integration suite is, by a wide margin, the largest in
// the repo (~170 test functions across every *_integration_test.go file
// here, vs. a handful in most sibling packages) -- and, before this file,
// every one of those ~170 tests spun up its OWN brand-new throwaway
// Postgres container via testcontainers-go, ran every embedded migration,
// opened a pool, and tore the whole thing down again, via one of FOUR
// independent, byte-for-byte-identical copies of that same container-
// start-plus-migrate helper duplicated across this package's own files:
// httpapi_test's own newTestPool (httpapi_integration_test.go), and
// package httpapi's own newBotTestPool (bot_integration_test.go),
// newCoreTestPool/newCoreTestPoolAndConnStr (createcore_integration_test.
// go). Even at a healthy ~1-3s per container start (confirmed elsewhere
// in this repo's own CI history when Docker isn't stalled), that is
// 170-510s of pure container-churn overhead alone, before any actual test
// logic runs -- and this package's own integration job is the slowest leg
// of the whole CI matrix (~9m24s) specifically because of it.
//
// Nothing here or in git history suggests per-TEST container isolation
// was ever a deliberate correctness requirement for THIS package
// specifically -- neither plan document says anything about it, and
// git blame traces the pattern back to this
// repo's very first testcontainers-Postgres helper (internal/adapters/
// outbound/postgres's own original), copied forward mechanically into
// every new DB-touching test file/package ever since (see e.g. this
// file's own sibling packages' identical newTestPool doc comments). The
// FOUR-WAY duplication specifically WITHIN this one package (rather than
// one shared helper) IS a deliberate, documented, and still entirely
// valid choice, just for an unrelated reason: package httpapi's own test
// files (bot_integration_test.go, createcore_integration_test.go -- both
// need white-box access to unexported helpers like createSessionCore)
// cannot import package httpapi_test's unexported newTestPool/newTestRig
// at all -- that would be a reverse import of a test-only package, which
// Go does not allow. This file resolves that the other way around:
// instead of one container per test, exactly one container for the
// WHOLE test binary, exposed via an EXPORTED accessor
// (IntegrationTestPool/IntegrationTestPoolAndConnStr, below) that
// package httpapi_test's own files can reach perfectly well -- Go's test
// tooling links package httpapi_test against the TEST-AUGMENTED variant
// of package httpapi (i.e. including this file's own exported
// declarations), not the production-only one, precisely because
// httpapi_test itself imports "github.com/.../httpapi" to reach this
// package's real, non-test exports already. Every one of the four
// existing helpers above now simply delegates here (kept under their own
// original names/signatures, so no other file in this package needed to
// change).
//
// Container lifecycle is now owned by TestMain (Go allows exactly one
// per test binary, regardless of how many internal/external test
// packages contribute files to it -- see internal/app/imagebuild's own
// builder_whitebox_integration_test.go doc comment for this exact
// constraint, already navigated once elsewhere in this repo): start one
// container, run every migration once, hand out one shared pool, and
// tear both down only after every test in the binary has finished.
//
// # Why this is safe against this package's own async Actor background work
//
// httpapi.TriggerDispatch (create.go) fires a deliberately fire-and-
// forget Registry.GetOrSpawn + Actor.Send(EnsureDispatched{}) after
// almost every session/turn-creating request in this package's own
// tests -- the spawned Actor keeps running real, separate Postgres
// transactions (dispatch.go's own planDispatch) on its OWN background
// goroutine (started via errgroup.Group.Go, registry.go) well after the
// HTTP response the test asserted on has already returned. This exact
// class of bug -- a test's own assertion racing that SAME still-running
// background goroutine -- caused a real, -race-caught failure elsewhere
// in this repo (see this branch's own commit 557a4fa,
// internal/adapters/inbound/linear): a bare bytes.Buffer captured
// slog's default logger, and the actor's own dispatch-decision Warn log
// line raced the test's own logBuf.String() read.
//
// Reusing ONE container/pool across ~170 tests does NOT reopen that
// class of bug here, for a reason specific to this package's own
// existing structure, true before this file and unchanged by it: every
// rig this package's tests build (testRig.newTestRig, turnCoreTestRig.
// newTurnCoreTestRig, bot_integration_test.go's/createcore_integration_
// test.go's own standalone rigs) already constructs its own FRESH
// sessionactor.Registry per test and already registers t.Cleanup(func()
// { _ = registry.Shutdown() }) -- and Registry.Shutdown (registry.go) is
// a synchronous, fully-draining wait: it cancels every live actor's own
// context and then blocks on r.group.Wait() until every one of those
// actor goroutines has actually returned, not merely been asked to stop.
// Go's own t.Cleanup contract runs cleanups in LIFO order, and every
// call site in this package obtains its pool (this file's own
// IntegrationTestPool/IntegrationTestPoolAndConnStr, below -- which
// registers THIS file's own truncate-and-restore cleanup) strictly
// BEFORE it ever constructs a Registry (which registers ITS OWN
// Shutdown cleanup afterward). That ordering means, for every single
// test, without exception: registry.Shutdown() (draining every actor
// goroutine this test spawned) always finishes BEFORE this file's own
// truncate-and-restore cleanup below ever runs -- so no in-flight actor
// transaction can ever be caught mid-write by a TRUNCATE, and the next
// test never observes a partially-drained previous test's state. (This
// package also never calls t.Parallel() -- verified directly -- so at
// most one test's rig, and therefore at most one test's worth of actor
// goroutines, is ever alive at a time regardless.)
//
// # Why a full TRUNCATE, not per-test unique IDs alone
//
// The standard alternative -- give every test's own rows fully unique,
// collision-proof identifiers and never truncate at all -- was
// considered first and rejected for THIS package specifically: a real
// audit of its own ~170 tests turned up multiple tests that assert on
// UNSCOPED, whole-table state, not merely their own rows' state, e.g.
// TestCreateSession_NeitherPathScopeNorMockConfig_NoEnvironmentRow's own
// `SELECT count(*) FROM environments` expecting exactly 0,
// TestUpdateMemberRole_ConcurrentDemoteBothLastAdmins_ExactlyOneSucceeds
// (members_integration_test.go) expecting `count(*) FROM users WHERE
// role = 'admin' AND disabled = false` to be exactly 1, and
// turnCoreTestRig.totalTurnCreateAuditRows (turncore_integration_test.
// go), used by several tests to assert an EXACT total count of
// turn.create audit_log rows across the whole database. None of these
// would still be correct if 169 OTHER tests' own leftover rows were
// still sitting in the same tables. Rewriting every one of these to a
// scoped/delta-based assertion instead was judged a larger, riskier
// change than reproducing exactly what a fresh container already
// guaranteed -- a byte-for-byte-empty, freshly-migrated database at the
// start of every test -- via TRUNCATE, which is what this file does.
//
// One wrinkle: the database is not actually empty immediately after
// migrating -- migrations/000033_intent_classifier.up.sql seeds exactly
// one real prompt_templates row ("intent_classifier_system"), which
// classifiertemplates_integration_test.go's own tests depend on existing
// at the start of every one of them. A blind TRUNCATE of every table
// would silently and permanently delete that seed row the first time ANY
// test's cleanup ran, breaking every classifiertemplates test after it.
// prepareDatabaseReset (below) handles this generically, not just for
// this one known case: immediately after migrating, it snapshots (via a
// literal `CREATE TABLE ... AS TABLE ...` copy) every table that already
// has at least one row, and the per-test reset restores those snapshots
// right after truncating -- so this keeps working unchanged even if some
// FUTURE migration seeds a different/additional table.
package httpapi

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

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/migrations"
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
// comment for the full container-reuse story. Only one TestMain is
// allowed per test binary (Go's own constraint, already navigated once
// elsewhere in this repo -- see internal/app/imagebuild's own
// builder_whitebox_integration_test.go), so this is now the ONE place in
// this whole package (spanning both package httpapi's and package
// httpapi_test's own test files) that starts/stops Postgres.
func TestMain(m *testing.M) {
	ctx := context.Background()

	container, connStr, err := startSharedTestContainer(ctx)
	if err != nil {
		log.Fatalf("httpapi: start shared integration-test container: %v", err)
	}

	if err := runMigrations(connStr); err != nil {
		log.Fatalf("httpapi: run migrations against shared integration-test container: %v", err)
	}

	pool, err := narvipg.NewPool(ctx, connStr)
	if err != nil {
		log.Fatalf("httpapi: open shared integration-test pool: %v", err)
	}

	if err := prepareDatabaseReset(ctx, pool); err != nil {
		log.Fatalf("httpapi: prepare shared integration-test database reset: %v", err)
	}

	sharedPool = pool
	sharedConnStr = connStr

	code := m.Run()

	pool.Close()
	if err := testcontainers.TerminateContainer(container); err != nil {
		log.Printf("httpapi: terminate shared integration-test container: %v", err)
	}
	os.Exit(code)
}

// startSharedTestContainer is this package's own former newTestPool/
// newBotTestPool/newCoreTestPool(AndConnStr)'s identical container-start
// logic, run exactly once now instead of once per test -- including the
// SAME hardening (an independent watchdog raced against the startup call
// itself, not relying solely on context cancellation) those four copies
// each already carried, for the exact reasons documented at each of
// their own original call sites (CI runs 30831633470/30834918806: a
// stalled CI-runner Docker daemon that did not honor context
// cancellation even one layer inside testcontainers-go's own wait
// strategy).
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
// migrations/000033, plus §25.4's FK-dependent workflow seed rows,
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
// -- see this file's own top doc comment for why this is safe against
// this package's own async sessionactor.Actor background work, and why a
// full reset (rather than relying on unique IDs alone) is the right
// tradeoff for this specific package's own existing tests. Exported
// (unlike the rest of this file) specifically so package httpapi_test's
// own files can reach it -- see this file's own top doc comment for why
// that is possible at all.
func IntegrationTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, _ := IntegrationTestPoolAndConnStr(t)
	return pool
}

// IntegrationTestPoolAndConnStr is IntegrationTestPool plus the shared
// container's raw connection string, for the rare test that needs to
// open its OWN differently-configured pool (e.g. a MaxConns:1 one)
// against the same database rather than reuse the shared-default pool.
func IntegrationTestPoolAndConnStr(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	if sharedPool == nil {
		t.Fatal("httpapi: IntegrationTestPoolAndConnStr called before TestMain finished setting up the shared pool")
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
		t.Fatalf("httpapi: reset shared integration-test database (truncate): %v", err)
	}
	for _, stmt := range restoreStatements {
		if _, err := sharedPool.Exec(ctx, stmt); err != nil {
			t.Fatalf("httpapi: reset shared integration-test database (restore seed data): %v", err)
		}
	}
}
