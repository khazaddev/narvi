//go:build integration

// Package resilience_test is Step 30's ("resilience suite", §9.3) own
// harness: a real, reusable "mini control plane" that assembles the same
// components cmd/control-plane/main.go wires in production -- a real
// Postgres (via testcontainers, embedded migrations applied), the real
// Postgres store types, a real sessionactor.Registry, and a real
// wshub.Hub -- using ONLY exported APIs from the packages it wires
// together, exactly like main.go itself does. This is deliberately a
// MORE realistic, less white-box style of test than the existing
// internal-package integration tests (e.g. internal/app/sessionactor's
// own resilience_killpod_integration_test.go): appropriate for scenarios
// that genuinely span multiple packages, not one package's own internals
// in isolation.
//
// Kept intentionally minimal, driven by this PR's own scenario #12's real
// needs (test/resilience/scenario12_rolling_restart_test.go) -- a second
// PR is expected to extend this harness further once its own scenarios'
// (§9.3 #3 slow boot, #5's plain-SPAWN race variant, #7 WS-drop ack
// redelivery) real needs are known, per test/resilience/README.md's own
// scenario-by-scenario index.
package resilience_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver, used only for the migrate handle below
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/khazaddev/narvi/internal/adapters/inbound/wshub"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// Harness is the shared, real multi-component rig every test/resilience
// scenario builds on: one throwaway Postgres container (migrated up
// once), the real store types wrapping it, and a real wshub.Hub -- all
// exposed so a scenario test can seed state directly via the stores (the
// same way internal/app/sessionactor's own resilience tests seed
// Processing turns/Ready sandboxes directly, bypassing the live
// dispatch path) and can construct one or more real Registry "pods"
// against the SAME underlying database via NewRegistry.
//
// Unlike resilience_killpod_integration_test.go's own newTestPoolPair
// (which deliberately hands back TWO INDEPENDENT *pgxpool.Pool instances,
// because that scenario's whole point is severing exactly one pod's own
// connections without touching the other's), this harness exposes a
// SINGLE shared pool: scenario #12 (graceful rolling restart) needs
// multiple Registry "pods" racing for the same session's advisory lock
// over time, never two pods with truly independent connection pools torn
// down independently mid-test -- see scenario12_rolling_restart_test.go's
// own doc comment for why a shared pool is the faithful shape here.
type Harness struct {
	Pool     *pgxpool.Pool
	Timeouts platform.Timeouts
	Hub      *wshub.Hub

	Sessions  *narvipg.SessionStore
	Turns     *narvipg.TurnStore
	Sandboxes *narvipg.SandboxStore
	Timers    *narvipg.TimerStore
}

// newHarness spins up one throwaway Postgres container, applies every
// embedded migration, and returns a ready Harness. t.Cleanup tears down
// the pool and the container. Mirrors internal/app/sessionactor's and
// internal/adapters/inbound/httpapi's own newTestPool convention exactly
// (same testcontainers image/options, same golang-migrate iofs source) --
// this package builds its own copy rather than importing either of
// theirs, matching that same established precedent of each DB-touching
// package/test-package owning a small copy of this helper rather than
// sharing one across package boundaries.
func newHarness(t *testing.T) *Harness {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
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
	defer func() { _ = migrateDB.Close() }()

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

	return &Harness{
		Pool:      pool,
		Timeouts:  platform.DefaultTimeouts(),
		Hub:       wshub.NewHub(),
		Sessions:  narvipg.NewSessionStore(pool),
		Turns:     narvipg.NewTurnStore(pool),
		Sandboxes: narvipg.NewSandboxStore(pool),
		Timers:    narvipg.NewTimerStore(pool),
	}
}

// NewRegistry constructs a fresh *sessionactor.Registry sharing this
// harness's Pool/Timeouts/Hub -- mirroring cmd/control-plane/main.go's own
// real wiring order (pool -> hub -> sessionactor.NewRegistry(...,
// broadcaster: hub, ...)) exactly, but with commander/provider/
// sourceControl left nil: scenario #12 (this PR) and a future #3 (slow
// boot) never exercise the spawn/dispatch/push-PR path, only hydration +
// the advisory lock + the timer/shutdown machinery -- the SAME "nil is
// fine" precedent internal/app/sessionactor's own resilience tests and
// internal/adapters/inbound/httpapi's own newTestRig already establish
// for exactly this reason.
//
// A future #5 (plain-SPAWN race variant) or #7 (WS-drop ack redelivery)
// scenario will NOT be able to reuse this nil wiring as-is: #5 needs a
// real/fake ports.SandboxProvider (mirroring
// dispatch_integration_test.go's own fakeSpawnProvider precedent) and #7
// needs a live wshub-backed ports.SandboxCommander plus the WS handler
// pipeline. Extending for either should add a sibling constructor here
// (or call sessionactor.NewRegistry directly, still reusing h.Pool/
// h.Timeouts/h.Hub) rather than parameterizing this method, so #12's own
// simplest case stays simple.
//
// Call this once per simulated "pod": each call is a fully independent
// Registry value with its own actors map and its own lifecycleCtx/cancel
// pair, so a scenario simulating a rolling restart (pod A shuts down,
// pod B picks the session back up) calls this twice, against the SAME
// underlying database, exactly like two real replicas of this binary
// would.
func (h *Harness) NewRegistry(ctx context.Context, t *testing.T) *sessionactor.Registry {
	t.Helper()

	r, err := sessionactor.NewRegistry(ctx, h.Pool, h.Timeouts, h.Hub, nil, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("sessionactor.NewRegistry: %v", err)
	}
	return r
}

// CreateSession inserts a minimal session row and returns its id --
// mirrors internal/app/sessionactor's own createTestSession helper
// exactly (the shape every resilience scenario in this package needs to
// start from).
func (h *Harness) CreateSession(ctx context.Context, t *testing.T) pgtype.UUID {
	t.Helper()

	created, err := h.Sessions.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
	})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	return created.ID
}
