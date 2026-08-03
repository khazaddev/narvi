//go:build integration

// Integration tests for AuthorizeResolvedActor/OwnedOrJoined against a
// REAL Postgres instance -- gated behind the "integration" build tag,
// mirroring internal/app/identitylink's own testcontainers-Postgres-plus-
// embedded-migrations convention exactly (each DB-touching package builds
// its own copy of this small helper rather than sharing one across package
// boundaries). Run via `make test-integration`.
//
// These cases are moved/adapted from internal/adapters/inbound/{slack,
// linear}'s own pre-extraction coverage (identity_integration_test.go in
// each package, which exercised authorizeResolvedActor/ownedOrJoined only
// indirectly, through a full webhook/interactivity request) -- proving
// the extraction into this package changed no behavior: same role matrix
// verdicts, same disabled-user-fails-closed check, same unresolved-actor
// short-circuit, same own/joined resolution.
package actorauthz_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/khazaddev/narvi/internal/app/actorauthz"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every embedded
// migration up, and returns a ready *pgxpool.Pool -- a duplicate of
// identitylink's own newTestPool, necessarily so (see that file's own doc
// comment for this codebase's established per-package precedent).
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

	pool, err := narvipg.NewPool(ctx, connStr)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestAuthorizeResolvedActor_UnresolvedActorAllowedWithNoLookup proves
// §13.2's own "unlinked actors get bot attribution ... the action
// proceeds" precedent: an invalid actorUserID short-circuits to
// allowed=true with NO users lookup at all -- proven here by passing a
// nil *postgres.UserStore, which would panic on any actual dereference.
func TestAuthorizeResolvedActor_UnresolvedActorAllowedWithNoLookup(t *testing.T) {
	ctx := context.Background()
	logger := discardLogger()

	got := actorauthz.AuthorizeResolvedActor(ctx, logger, "test", nil, pgtype.UUID{}, authz.ActionCreateSession, authz.Resource{})
	if !got {
		t.Error("AuthorizeResolvedActor() = false, want true for an unresolved (invalid) actor")
	}
}

// TestAuthorizeLinkedActor_UnresolvedActorDeniedWithNoLookup mirrors
// TestAuthorizeResolvedActor_UnresolvedActorAllowedWithNoLookup above --
// same shape, opposite verdict: AuthorizeLinkedActor is the audit-hardening
// counterpart that DENIES (rather than allows) an unresolved actor, and
// does so with NO users lookup at all, proven here identically by passing
// a nil *postgres.UserStore (which would panic on any actual dereference).
func TestAuthorizeLinkedActor_UnresolvedActorDeniedWithNoLookup(t *testing.T) {
	ctx := context.Background()
	logger := discardLogger()

	got := actorauthz.AuthorizeLinkedActor(ctx, logger, "test", nil, pgtype.UUID{}, authz.ActionCreateSession, authz.Resource{})
	if got {
		t.Error("AuthorizeLinkedActor() = true, want false for an unresolved (invalid) actor")
	}
}

// TestAuthorizeResolvedActor_UnknownUserFailsClosed proves a role-lookup
// failure (here: a syntactically valid but nonexistent user id) denies
// rather than silently proceeding -- "should be unreachable in practice"
// per this function's own doc comment, but still fails closed if it ever
// happens.
func TestAuthorizeResolvedActor_UnknownUserFailsClosed(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	users := narvipg.NewUserStore(pool)
	logger := discardLogger()

	var nonexistent pgtype.UUID
	if err := nonexistent.Scan("00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatalf("scan uuid: %v", err)
	}

	got := actorauthz.AuthorizeResolvedActor(ctx, logger, "test", users, nonexistent, authz.ActionCreateSession, authz.Resource{})
	if got {
		t.Error("AuthorizeResolvedActor() = true, want false (fail closed) for a user id with no matching row")
	}
}

// TestAuthorizeResolvedActor_RoleMatrix is table-driven over the §13.3 role
// matrix verdicts this function renders once an actor IS resolved --
// mirrors slack/linear's own pre-extraction integration coverage (a
// viewer denied ActionCreateSession, a member denied ActionPromptSession
// without ownership but allowed with it, an admin/maintainer allowed
// regardless of ownership).
func TestAuthorizeResolvedActor_RoleMatrix(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	users := narvipg.NewUserStore(pool)
	logger := discardLogger()

	tests := []struct {
		name     string
		role     sqlcgen.UserRole
		action   authz.Action
		resource authz.Resource
		want     bool
	}{
		{name: "viewer denied create session", role: sqlcgen.UserRoleViewer, action: authz.ActionCreateSession, resource: authz.Resource{}, want: false},
		{name: "member allowed create session (no ownership concept)", role: sqlcgen.UserRoleMember, action: authz.ActionCreateSession, resource: authz.Resource{}, want: true},
		{name: "member denied prompt without ownership", role: sqlcgen.UserRoleMember, action: authz.ActionPromptSession, resource: authz.Resource{OwnedOrJoined: false}, want: false},
		{name: "member allowed prompt with ownership", role: sqlcgen.UserRoleMember, action: authz.ActionPromptSession, resource: authz.Resource{OwnedOrJoined: true}, want: true},
		{name: "viewer denied prompt even with ownership", role: sqlcgen.UserRoleViewer, action: authz.ActionPromptSession, resource: authz.Resource{OwnedOrJoined: true}, want: false},
		{name: "maintainer allowed prompt without ownership", role: sqlcgen.UserRoleMaintainer, action: authz.ActionPromptSession, resource: authz.Resource{OwnedOrJoined: false}, want: true},
		{name: "admin allowed approve plan without ownership", role: sqlcgen.UserRoleAdmin, action: authz.ActionApprovePlan, resource: authz.Resource{OwnedOrJoined: false}, want: true},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			user, err := users.Create(ctx, sqlcgen.CreateUserParams{
				PrimaryEmail: fmt.Sprintf("actorauthz-matrix-%d@example.com", i),
				DisplayName:  "Matrix Test User",
				Role:         tc.role,
			})
			if err != nil {
				t.Fatalf("create fixture user: %v", err)
			}

			got := actorauthz.AuthorizeResolvedActor(ctx, logger, "test", users, user.ID, tc.action, tc.resource)
			if got != tc.want {
				t.Errorf("AuthorizeResolvedActor(role=%s, action=%s, resource=%+v) = %v, want %v", tc.role, tc.action, tc.resource, got, tc.want)
			}
		})
	}
}

// TestAuthorizeResolvedActor_DisabledUserDeniedEvenWithPermittingRole
// proves user.Disabled is checked BEFORE domain/authz.Authorize -- a
// disabled member (whose role would otherwise permit ActionCreateSession
// unconditionally) is still denied, mirroring slack/linear's own identical
// pre-extraction regression coverage exactly.
func TestAuthorizeResolvedActor_DisabledUserDeniedEvenWithPermittingRole(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	users := narvipg.NewUserStore(pool)
	logger := discardLogger()

	user, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "actorauthz-disabled@example.com",
		DisplayName:  "Disabled Test User",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET disabled = true WHERE id = $1`, user.ID); err != nil {
		t.Fatalf("disable fixture user: %v", err)
	}

	got := actorauthz.AuthorizeResolvedActor(ctx, logger, "test", users, user.ID, authz.ActionCreateSession, authz.Resource{})
	if got {
		t.Error("AuthorizeResolvedActor() = true, want false for a disabled user even with an otherwise-permitting role")
	}
}

// TestOwnedOrJoined_TrueWhenCreator proves the "created it" half of the
// §13.3 row 2 own/joined carve-out, with no participants row at all.
func TestOwnedOrJoined_TrueWhenCreator(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	users := narvipg.NewUserStore(pool)
	sessions := narvipg.NewSessionStore(pool)
	participants := narvipg.NewParticipantStore(pool)

	creator, err := users.Create(ctx, sqlcgen.CreateUserParams{PrimaryEmail: "actorauthz-owner@example.com", DisplayName: "Owner", Role: sqlcgen.UserRoleMember})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub, CreatedBy: creator.ID})
	if err != nil {
		t.Fatalf("create fixture session: %v", err)
	}

	joined, err := actorauthz.OwnedOrJoined(ctx, participants, session, creator.ID)
	if err != nil {
		t.Fatalf("OwnedOrJoined: %v", err)
	}
	if !joined {
		t.Error("OwnedOrJoined() = false, want true for the session's own creator")
	}
}

// TestOwnedOrJoined_TrueWhenParticipant proves the "joined it" half: a
// user who did NOT create the session, but has a participants row for it,
// still counts as owned/joined.
func TestOwnedOrJoined_TrueWhenParticipant(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	users := narvipg.NewUserStore(pool)
	sessions := narvipg.NewSessionStore(pool)
	participants := narvipg.NewParticipantStore(pool)

	creator, err := users.Create(ctx, sqlcgen.CreateUserParams{PrimaryEmail: "actorauthz-creator@example.com", DisplayName: "Creator", Role: sqlcgen.UserRoleMember})
	if err != nil {
		t.Fatalf("create fixture creator: %v", err)
	}
	joiner, err := users.Create(ctx, sqlcgen.CreateUserParams{PrimaryEmail: "actorauthz-joiner@example.com", DisplayName: "Joiner", Role: sqlcgen.UserRoleMember})
	if err != nil {
		t.Fatalf("create fixture joiner: %v", err)
	}
	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub, CreatedBy: creator.ID})
	if err != nil {
		t.Fatalf("create fixture session: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO participants (session_id, user_id) VALUES ($1, $2)`, session.ID, joiner.ID); err != nil {
		t.Fatalf("insert participants row: %v", err)
	}

	joined, err := actorauthz.OwnedOrJoined(ctx, participants, session, joiner.ID)
	if err != nil {
		t.Fatalf("OwnedOrJoined: %v", err)
	}
	if !joined {
		t.Error("OwnedOrJoined() = false, want true for a joined (non-creator) participant")
	}
}

// TestOwnedOrJoined_FalseWhenNeitherCreatorNorParticipant proves the
// negative case: neither creator nor participant.
func TestOwnedOrJoined_FalseWhenNeitherCreatorNorParticipant(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	users := narvipg.NewUserStore(pool)
	sessions := narvipg.NewSessionStore(pool)
	participants := narvipg.NewParticipantStore(pool)

	creator, err := users.Create(ctx, sqlcgen.CreateUserParams{PrimaryEmail: "actorauthz-creator2@example.com", DisplayName: "Creator", Role: sqlcgen.UserRoleMember})
	if err != nil {
		t.Fatalf("create fixture creator: %v", err)
	}
	stranger, err := users.Create(ctx, sqlcgen.CreateUserParams{PrimaryEmail: "actorauthz-stranger@example.com", DisplayName: "Stranger", Role: sqlcgen.UserRoleMember})
	if err != nil {
		t.Fatalf("create fixture stranger: %v", err)
	}
	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub, CreatedBy: creator.ID})
	if err != nil {
		t.Fatalf("create fixture session: %v", err)
	}

	joined, err := actorauthz.OwnedOrJoined(ctx, participants, session, stranger.ID)
	if err != nil {
		t.Fatalf("OwnedOrJoined: %v", err)
	}
	if joined {
		t.Error("OwnedOrJoined() = true, want false for a user who neither created nor joined")
	}
}
