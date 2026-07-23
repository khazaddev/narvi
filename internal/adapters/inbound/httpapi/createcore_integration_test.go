//go:build integration

// Integration tests for the UNEXPORTED createSessionCore function
// itself (Step 31, "webhook toolkit" extraction) -- deliberately in
// package httpapi (not httpapi_test, unlike this package's other
// integration tests) since createSessionCore is intentionally
// unexported: it is CreateSession's own internal implementation detail,
// not yet a second public entry point (Steps 32-34 gain their own
// webhook-specific callers later; nothing outside this package calls it
// today). This file builds its own minimal testcontainers-Postgres rig
// rather than reusing httpapi_test's own newTestRig/newTestPool -- an
// external test package's unexported helpers are not reachable from an
// internal one, matching this codebase's own existing precedent that
// each DB-touching test file is free to set up what it needs directly
// (see e.g. sandbox_upsertforspawn_integration_test.go, which builds its
// own session directly via raw stores rather than a shared REST rig).
package httpapi

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

// newCoreTestPool is this file's own copy of the testcontainers-Postgres-
// plus-embedded-migrations helper httpapi_test's own newTestPool already
// implements -- necessarily duplicated (not shared) because this file
// lives in the internal httpapi package while that one lives in the
// external httpapi_test package.
func newCoreTestPool(t *testing.T) *pgxpool.Pool {
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

// TestCreateSessionCore_NilCreator_StoresNullCreatedBy proves the
// extracted core function's own new capability: a NIL/invalid creator
// (pgtype.UUID{}, Valid == false -- exactly what a future webhook/bot
// ingress caller with no cookie-authenticated human passes) is accepted
// and stored as a genuine SQL NULL sessions.created_by, never rejected
// and never coerced into some fake placeholder id. This path is never
// exercised by httpapi_test's own HTTP-only tests, which all go through
// CreateSession's own hard-required authenticatedUserID.
func TestCreateSessionCore_NilCreator_StoresNullCreatedBy(t *testing.T) {
	ctx := context.Background()
	pool := newCoreTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "narvi", Url: "https://github.com/khazaddev/narvi"},
		},
	}

	var nilCreator pgtype.UUID // Valid == false: the explicit "no human caller" case.

	created, cerr := createSessionCore(ctx, pool, sessions, turns, environments, registry, req, nilCreator)
	if cerr != nil {
		t.Fatalf("createSessionCore: status=%d message=%q", cerr.status, cerr.message)
	}

	if created.CreatedBy.Valid {
		t.Errorf("CreatedBy.Valid = true, want false (NULL) for a nil creator")
	}
	if created.SpawnSource != sqlcgen.SessionSpawnSourceGithub {
		t.Errorf("SpawnSource = %q, want %q", created.SpawnSource, sqlcgen.SessionSpawnSourceGithub)
	}

	// Confirm directly against Postgres too, not just the returned row --
	// proves the NULL genuinely round-tripped through the actual INSERT,
	// not merely reflected back from an in-memory struct.
	var createdByIsNull bool
	if err := pool.QueryRow(ctx,
		`SELECT created_by IS NULL FROM sessions WHERE id = $1`, created.ID,
	).Scan(&createdByIsNull); err != nil {
		t.Fatalf("query created_by: %v", err)
	}
	if !createdByIsNull {
		t.Error("sessions.created_by is NOT NULL in Postgres, want NULL for a nil creator")
	}
}

// TestCreateSessionCore_NilCreator_WithPromptDispatches proves a nil
// creator does not disturb the rest of createSessionCore's own existing
// behavior: a non-nil prompt still creates a pending turn AND still
// triggers the post-commit GetOrSpawn+EnsureDispatched path exactly like
// today's authenticated-human path already does (indirectly proven here
// by a successful, error-free call -- dispatch_integration_test.go in
// app/sessionactor is what exhaustively covers the decision tree itself;
// this test only proves createSessionCore's own wiring into it is
// unaffected by createdBy being NULL).
func TestCreateSessionCore_NilCreator_WithPromptDispatches(t *testing.T) {
	ctx := context.Background()
	pool := newCoreTestPool(t)

	sessions := narvipg.NewSessionStore(pool)
	turns := narvipg.NewTurnStore(pool)
	environments := narvipg.NewEnvironmentStore(pool)
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	prompt := "fix the failing check"
	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
		Prompt:      restdtos.CreateSessionRequestPrompt(&prompt),
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "narvi", Url: "https://github.com/khazaddev/narvi"},
		},
	}

	var nilCreator pgtype.UUID

	created, cerr := createSessionCore(ctx, pool, sessions, turns, environments, registry, req, nilCreator)
	if cerr != nil {
		t.Fatalf("createSessionCore: status=%d message=%q", cerr.status, cerr.message)
	}

	turnRows, err := turns.ListForSession(ctx, created.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turnRows) != 1 {
		t.Fatalf("len(turns) = %d, want 1 (prompt was non-nil)", len(turnRows))
	}
	if turnRows[0].Prompt == nil || *turnRows[0].Prompt != prompt {
		t.Errorf("turn prompt = %v, want %q", turnRows[0].Prompt, prompt)
	}
}
