//go:build integration

// Integration tests for the two small, EXPORTED bot-ingress wrappers in
// bot.go (Step 32, "GitHub ingress") -- CreateSessionForBot and
// CreateTurnForBot. Lives in package httpapi (not httpapi_test), mirroring
// createcore_integration_test.go's own precedent, since it exercises
// createSessionCore indirectly through CreateSessionForBot. Builds its
// own minimal testcontainers-Postgres rig rather than reusing
// httpapi_test's own newTestPool/newTestRig, for the SAME reason
// createcore_integration_test.go's own doc comment already gives: an
// external test package's unexported helpers are not reachable from this
// internal one.
package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// newBotTestPool is this file's own copy of the testcontainers-Postgres-
// plus-embedded-migrations helper createcore_integration_test.go's own
// newCoreTestPool already implements.
func newBotTestPool(t *testing.T) *pgxpool.Pool {
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

// TestCreateSessionForBot_CreatesNullCreatorSession proves the exported
// wrapper forwards to createSessionCore with a genuine NULL creator and
// surfaces its result as a plain error (not *createSessionError) on
// success.
func TestCreateSessionForBot_CreatesNullCreatorSession(t *testing.T) {
	ctx := context.Background()
	pool := newBotTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	prompt := "review this PR please"
	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Prompt:      restdtos.CreateSessionRequestPrompt(&prompt),
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "widgets", Url: "https://github.com/acme/widgets.git"},
		},
	}

	created, err := CreateSessionForBot(ctx, pool, sessions, turns, environments, auditLog, registry, req)
	if err != nil {
		t.Fatalf("CreateSessionForBot: %v", err)
	}
	if created.CreatedBy.Valid {
		t.Error("CreatedBy.Valid = true, want false (NULL) for a bot-created session")
	}
	if created.SpawnSource != sqlcgen.SessionSpawnSourceGithub {
		t.Errorf("SpawnSource = %q, want %q", created.SpawnSource, sqlcgen.SessionSpawnSourceGithub)
	}

	turnRows, err := turns.ListForSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turnRows) != 1 {
		t.Fatalf("len(turns) = %d, want 1", len(turnRows))
	}
}

// TestCreateSessionForBot_ValidationFailureSurfacesAsError proves an
// invalid repo (rejected by internal/domain/reposource before any
// Postgres write, exactly like createSessionCore's own doc comment
// describes) surfaces as a plain, non-nil error -- CreateSessionForBot
// flattens *createSessionError into the error interface, so a caller in
// another package (which cannot reference the unexported type itself)
// still gets a usable, non-nil error to log/act on.
func TestCreateSessionForBot_ValidationFailureSurfacesAsError(t *testing.T) {
	ctx := context.Background()
	pool := newBotTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Repos:       []restdtos.CreateSessionRequestReposElem{}, // empty -- rejected before any Postgres write.
	}

	if _, err := CreateSessionForBot(ctx, pool, sessions, turns, environments, auditLog, registry, req); err == nil {
		t.Fatal("CreateSessionForBot() error = nil, want non-nil for an empty repos list")
	}
}

// TestCreateTurnForBot_EnqueuesTurnOnExistingSession proves
// CreateTurnForBot enqueues a new Pending turn on an EXISTING session,
// and that it does NOT apply CreateTurn's own hasOpenTurn 409 gate -- a
// SECOND call while the first turn is still Pending must still succeed
// (see bot.go's own doc comment for why: this is exactly the coalesced-
// backlog behavior Step 32's own per-PR reuse needs).
func TestCreateTurnForBot_EnqueuesTurnOnExistingSession(t *testing.T) {
	ctx := context.Background()
	pool := newBotTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	created, err := CreateSessionForBot(ctx, pool, sessions, turns, environments, auditLog, registry, restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "widgets", Url: "https://github.com/acme/widgets.git"},
		},
	})
	if err != nil {
		t.Fatalf("CreateSessionForBot (setup): %v", err)
	}

	first, err := CreateTurnForBot(ctx, pool, sessions, turns, registry, created.ID, "first mention", nil, false)
	if err != nil {
		t.Fatalf("CreateTurnForBot (first): %v", err)
	}

	second, err := CreateTurnForBot(ctx, pool, sessions, turns, registry, created.ID, "second concurrent mention", nil, false)
	if err != nil {
		t.Fatalf("CreateTurnForBot (second, while first still pending): %v", err)
	}
	if second.ID == first.ID {
		t.Error("second turn has the same id as the first, want a distinct new turn")
	}

	turnRows, err := turns.ListForSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turnRows) != 2 {
		t.Fatalf("len(turns) = %d, want 2 (both enqueued despite neither having reached a terminal state)", len(turnRows))
	}
}
