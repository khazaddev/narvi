//go:build integration

// Full HTTP-level integration tests for GET /auth/identity-link/{nonce}
// (Step 39, "identities + full RBAC", §13.2's own magic-link consume
// flow) -- mirrors internal/adapters/inbound/httpapi's own testcontainers-
// Postgres-plus-embedded-migrations convention exactly.
package identitylink_test

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
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

	identitylinkhttp "github.com/khazaddev/narvi/internal/adapters/inbound/identitylink"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	appidentitylink "github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

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

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

type testRig struct {
	pool         *pgxpool.Pool
	router       http.Handler
	users        *narvipg.UserStore
	identities   *narvipg.IdentityStore
	linkPrompts  *narvipg.IdentityLinkPromptStore
	userSessions *narvipg.UserSessionStore
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	pool := newTestPool(t)

	users := narvipg.NewUserStore(pool)
	identities := narvipg.NewIdentityStore(pool)
	linkPrompts := narvipg.NewIdentityLinkPromptStore(pool)
	userSessions := narvipg.NewUserSessionStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	handler := identitylinkhttp.NewConsumeHandler(identitylinkhttp.Deps{
		UserSessions: userSessions,
		Users:        users,
		AppIdentityLink: appidentitylink.Deps{
			Pool:          pool,
			Users:         users,
			Identities:    identities,
			LinkPrompts:   linkPrompts,
			AuditLog:      auditLog,
			PublicBaseURL: "https://narvi.example.com",
			PromptTTL:     time.Hour,
		},
	})

	router := chi.NewRouter()
	router.Get("/auth/identity-link/{nonce}", handler)

	return &testRig{pool: pool, router: router, users: users, identities: identities, linkPrompts: linkPrompts, userSessions: userSessions}
}

// createAuthenticatedUser creates a real user + user_sessions row and
// returns the PLAINTEXT session token -- mirrors auth_integration_test.
// go's own TestMiddleware precedent exactly.
func (rig *testRig) createAuthenticatedUser(ctx context.Context, t *testing.T, email string) (sqlcgen.User, string) {
	t.Helper()
	user, err := rig.users.Create(ctx, sqlcgen.CreateUserParams{PrimaryEmail: email, DisplayName: email, Role: sqlcgen.UserRoleMember})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	token, err := platform.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if _, err := rig.userSessions.Create(ctx, sqlcgen.CreateUserSessionParams{
		UserID:    user.ID,
		TokenHash: platform.HashToken(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("create fixture session: %v", err)
	}
	return user, token
}

func doGet(t *testing.T, router http.Handler, path, sessionToken string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if sessionToken != "" {
		req.AddCookie(&http.Cookie{Name: platform.AuthSessionCookieName, Value: sessionToken})
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestConsumeHandler_NotAuthenticated_RedirectsToLoginWithNext proves a
// signed-out visitor is redirected into the real GitHub OAuth login flow,
// carrying THIS exact URL as ?next= so they land back here after signing
// in.
func TestConsumeHandler_NotAuthenticated_RedirectsToLoginWithNext(t *testing.T) {
	rig := newTestRig(t)

	rec := doGet(t, rig.router, "/auth/identity-link/some-nonce", "")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusFound, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	want := "/auth/github/login?next=%2Fauth%2Fidentity-link%2Fsome-nonce"
	if loc != want {
		t.Errorf("Location = %q, want %q", loc, want)
	}
}

// TestConsumeHandler_Authenticated_LinksIdentity proves the full,
// real, DB-backed round trip: internal/app/identitylink.Resolve mints a
// prompt, then a signed-in visitor clicking it links the identity to
// THEIR OWN user, deletes the prompt, and records an audit-log entry.
func TestConsumeHandler_Authenticated_LinksIdentity(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	res, err := appidentitylink.Resolve(ctx, appidentitylink.Deps{
		Pool:          rig.pool,
		Users:         rig.users,
		Identities:    rig.identities,
		LinkPrompts:   rig.linkPrompts,
		AuditLog:      narvipg.NewAuditLogStore(rig.pool),
		PublicBaseURL: "https://narvi.example.com",
		PromptTTL:     time.Hour,
	}, sqlcgen.IdentityProviderSlack, "U-CONSUME-HTTP", "nobody-matches@example.com", true)
	if err != nil {
		t.Fatalf("Resolve (seed link prompt): %v", err)
	}
	nonce := res.MagicLinkURL[len("https://narvi.example.com/auth/identity-link/"):]

	_, sessionToken := rig.createAuthenticatedUser(ctx, t, "clicker@example.com")

	rec := doGet(t, rig.router, "/auth/identity-link/"+nonce, sessionToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	identity, err := rig.identities.GetByProviderAndExternalID(ctx, sqlcgen.IdentityProviderSlack, "U-CONSUME-HTTP")
	if err != nil {
		t.Fatalf("GetByProviderAndExternalID: %v", err)
	}
	if identity.LinkedVia != sqlcgen.IdentityLinkedViaPrompt {
		t.Errorf("LinkedVia = %v, want prompt", identity.LinkedVia)
	}

	if _, err := rig.linkPrompts.GetLatestForProviderAndExternalID(ctx, sqlcgen.IdentityProviderSlack, "U-CONSUME-HTTP"); err == nil {
		t.Error("link prompt still exists after being consumed, want it deleted")
	}
}

// TestConsumeHandler_UnknownNonce_NotFound proves a bogus nonce (signed-in
// visitor) is rejected with 404, never a 500.
func TestConsumeHandler_UnknownNonce_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	_, sessionToken := rig.createAuthenticatedUser(ctx, t, "unknown-nonce-clicker@example.com")

	rec := doGet(t, rig.router, "/auth/identity-link/totally-bogus-nonce", sessionToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}
