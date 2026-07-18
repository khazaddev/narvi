//go:build integration

// Shared fixtures for this package's own integration tests (sandbox_test.go,
// dispatch_test.go) -- gated behind the "integration" build tag (needs
// Docker), matching internal/app/sessionactor/integration_helpers_test.go's
// own testcontainers-Postgres-plus-embedded-migrations convention exactly
// (each DB-touching package builds its own copy of this small helper rather
// than sharing one across package boundaries, per that same file's own
// precedent).
package wshub_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/khazaddev/narvi/internal/adapters/inbound/wshub"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every embedded
// migration up (proving migration 000015's token_hash column lands
// correctly), and returns a ready *pgxpool.Pool. t.Cleanup tears down both
// the pool and the container.
func newTestPool(t *testing.T) *pgxpool.Pool {
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

// createTestSandbox inserts a sandbox row for sessionID (gen 1, Pending by
// default per migrations/000006_sandboxes.up.sql) and returns it.
func createTestSandbox(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID) sqlcgen.Sandbox {
	t.Helper()

	created, err := narvipg.NewSandboxStore(pool).Create(ctx, sessionID)
	if err != nil {
		t.Fatalf("create test sandbox: %v", err)
	}
	return created
}

// moveSandboxStatus sets sessionID's sandbox row to status via a plain
// UpdateStatus call (no last_seen_at bump -- the zero pgtype.Timestamptz
// COALESCEs to "leave unchanged", matching UpdateSandboxStatus's own doc
// comment).
func moveSandboxStatus(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, status sqlcgen.SandboxStatus) {
	t.Helper()

	if _, err := narvipg.NewSandboxStore(pool).UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID,
		Status:    status,
	}); err != nil {
		t.Fatalf("move sandbox to %s: %v", status, err)
	}
}

// setSandboxTokenHash writes sessionID's sandbox row's token_hash directly
// via raw SQL -- no sqlc query exists for this (real token MINTING is
// Step 21+'s own job, see migrations/000015_sandbox_token_hash.up.sql's own
// doc comment); this is test-fixture setup only, proving verifySandboxToken
// actually enforces a REAL stored hash when one exists.
func setSandboxTokenHash(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, hash string) {
	t.Helper()

	if _, err := pool.Exec(ctx, `UPDATE sandboxes SET token_hash = $1 WHERE session_id = $2`, hash, sessionID); err != nil {
		t.Fatalf("set sandbox token_hash: %v", err)
	}
}

// getSandbox re-reads sessionID's current sandbox row.
func getSandbox(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID) sqlcgen.Sandbox {
	t.Helper()

	got, err := narvipg.NewSandboxStore(pool).Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	return got
}

// countEvents returns how many rows exist in events for (sessionID,
// eventType).
func countEvents(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, eventType string) int {
	t.Helper()

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE session_id = $1 AND type = $2`,
		sessionID, eventType,
	).Scan(&n); err != nil {
		t.Fatalf("count %s events: %v", eventType, err)
	}
	return n
}

// waitUntil polls cond every 20ms until it reports true, or fails the test
// once timeout elapses -- used to observe the eventual effect of the
// session actor's own asynchronous mailbox processing without coupling the
// test to internal timing. Mirrors internal/app/sessionactor/
// integration_helpers_test.go's own identical helper.
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

// newTestServer builds a chi router mounting NewSandboxHandler at the real
// route path, wraps it in an httptest.Server, and returns both the server
// (for Close) and the ws:// URL prefix to dial against (session id + query
// string are the caller's own job to append).
func newTestServer(registry *sessionactor.Registry, sandboxes *narvipg.SandboxStore, timeouts platform.Timeouts) (*httptest.Server, string) {
	router := chi.NewRouter()
	router.Get("/sessions/{sessionID}/ws", wshub.NewSandboxHandler(registry, sandboxes, timeouts))
	server := httptest.NewServer(router)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	return server, wsURL
}
