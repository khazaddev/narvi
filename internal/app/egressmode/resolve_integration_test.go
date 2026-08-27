//go:build integration

// Integration test for Resolve against a REAL Postgres instance -- §30.8's
// own THIRD pinned case, "on an absent row", kept deliberately separate
// from resolve_test.go's own fake-backed ErrNoRows/arbitrary-error cases.
//
// The distinction matters: resolve_test.go's fakeRepoSettingsReader
// returning a hand-authored pgx.ErrNoRows only proves Resolve behaves
// correctly when TOLD a row is absent. It says nothing about what the
// REAL production path -- *postgres.RepoSettingsStore.Get, backed by the
// real repo_settings table and the real sqlc-generated scan -- actually
// returns for a genuinely missing row, or whether that value is even
// errors.Is-comparable to pgx.ErrNoRows the way Resolve's own branch
// assumes. This codebase has shipped exactly this class of gap before (a
// fixture inventing a shape production never sends); this file closes it
// for Resolve by exercising the concrete store end to end, mirroring
// internal/app/reviewverdict's own newTestPool convention (that package's
// insert_integration_test.go: "each DB-touching package builds its own
// copy of newTestPool rather than sharing one across package
// boundaries"). Run via `make test-integration`.
package egressmode_test

import (
	"context"
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/egressmode"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every
// embedded migration up, and returns a ready *pgxpool.Pool -- a duplicate
// of internal/app/reviewverdict's own newTestPool, necessarily so (this
// codebase's established per-package precedent; see that file's own doc
// comment).
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

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
		t.Fatalf("start postgres container: tcpostgres.Run did not return within %s", containerStartWatchdog)
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

	pool, err := narvipg.NewPool(ctx, connStr)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// TestResolve_AbsentRow_ResolvesShadow is §30.8's own third pinned case,
// against a REAL Postgres: a repo this deployment has never seeded any
// repo_settings row for at all must resolve shadow -- proving the
// production path (*postgres.RepoSettingsStore.Get, wired unmodified as
// egressmode.RepoSettingsReader) really does surface pgx.ErrNoRows the
// way Resolve's own fake-backed unit test assumes, not merely that a
// fake CLAIMING to be pgx.ErrNoRows does.
func TestResolve_AbsentRow_ResolvesShadow(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	const repoFullName = "acme/never-seeded-repo"

	// The sentinel first, because the comment above this test claims the
	// production path really surfaces pgx.ErrNoRows and the shadow
	// assertions below cannot show it: Resolve maps EVERY error to shadow,
	// so they pass identically whatever the store returns. Without this,
	// the comment promised a proof the body did not perform -- and if a
	// future change wrapped the sentinel, the resolver would keep failing
	// closed (harmless) while logging an infrastructure warning on every
	// ordinary never-seeded repo (not harmless, and invisible here).
	// Same assertion the store's own sibling integration tests make.
	if _, err := repoSettings.Get(ctx, repoFullName); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RepoSettingsStore.Get for an absent row returned %v; want an error matching pgx.ErrNoRows -- Resolve's own branch distinguishes them with errors.Is", err)
	}

	got := egressmode.Resolve(ctx, egressmode.Deps{RepoSettings: repoSettings}, repoFullName)

	if got.Live() {
		t.Error("Resolve().Live() = true, want false: a repo with no repo_settings row at all must resolve shadow (§30.8)")
	}
	if !got.Suppressed() {
		t.Error("Resolve().Suppressed() = false, want true")
	}
}

// TestResolve_RealRow_HonorsLiveEgressEnabled is the live-side companion:
// a real row, actually written through the same UpsertLiveEgressEnabled
// path the seed tool uses, must resolve live -- confirming the column
// round-trips through the real sqlc-generated scan (bool, not some
// nullable/pointer shape a hand-rolled fake could get wrong) and that
// GetRepoSettings' own generated SELECT actually includes the new column
// at all.
func TestResolve_RealRow_HonorsLiveEgressEnabled(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	const repoFullName = "acme/promoted-repo"
	if _, err := repoSettings.UpsertLiveEgressEnabled(ctx, repoFullName, true); err != nil {
		t.Fatalf("UpsertLiveEgressEnabled: %v", err)
	}

	got := egressmode.Resolve(ctx, egressmode.Deps{RepoSettings: repoSettings}, repoFullName)

	if !got.Live() {
		t.Error("Resolve().Live() = false, want true: a real row with live_egress_enabled=true must resolve live")
	}
}

// TestResolve_RowExistsForAnUnrelatedReason_StillDefaultsShadow closes a
// gap the two tests above do not cover: a row can exist (any OTHER
// repo_settings column may have been written first -- auto-merge, in
// this test) without live_egress_enabled ever having been touched. That
// is a DIFFERENT case from "no row at all" (pgx.ErrNoRows never fires
// here) and from "explicitly written false" (nothing ever calls
// UpsertLiveEgressEnabled for this repo) -- it is migrations/
// 000101_repo_settings_live_egress_enabled.up.sql's own `DEFAULT false`
// column clause, exercised for real, proving the column's default
// resolves shadow exactly like an absent row does.
func TestResolve_RowExistsForAnUnrelatedReason_StillDefaultsShadow(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	const repoFullName = "acme/auto-merge-only-repo"
	if _, err := repoSettings.UpsertAutoMergeToggle(ctx, repoFullName, true); err != nil {
		t.Fatalf("UpsertAutoMergeToggle: %v", err)
	}

	row, err := repoSettings.Get(ctx, repoFullName)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if row.LiveEgressEnabled {
		t.Fatal("row.LiveEgressEnabled = true immediately after a write that never touched it -- the column's own DEFAULT is wrong at the schema level")
	}

	got := egressmode.Resolve(ctx, egressmode.Deps{RepoSettings: repoSettings}, repoFullName)

	if got.Live() {
		t.Error("Resolve().Live() = true, want false: a row that exists for an unrelated reason must still default shadow (migrations/000101's own DEFAULT false)")
	}
}
