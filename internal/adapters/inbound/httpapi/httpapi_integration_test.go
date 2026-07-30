//go:build integration

// Integration tests for internal/adapters/inbound/httpapi's 5 REST
// endpoints (§6.3), against a real Postgres instance -- gated behind the
// "integration" build tag, matching internal/adapters/inbound/wshub's own
// testcontainers-Postgres-plus-embedded-migrations convention exactly
// (each DB-touching package builds its own copy of this small helper
// rather than sharing one across package boundaries, per that package's
// own precedent). Run via `make test-integration`.
//
// As of Step 20 ("auth v1"), every route in this file is mounted behind
// internal/adapters/inbound/auth.Middleware, exactly like cmd/
// control-plane/main.go's own real wiring -- every request below now goes
// through a REAL session (createAuthenticatedUser constructs one directly
// via the stores: users.Create + identities.Create + userSessions.Create +
// attaching the resulting cookie, mirroring exactly how Step 19's own
// createSession helper already bypasses REST for test setup) rather than
// mocking auth away.
package httpapi_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/auth"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every
// embedded migration up, and returns a ready *pgxpool.Pool. t.Cleanup
// tears down both the pool and the container.
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

// testRig bundles a fresh pool + every store + an httptest.Server
// mounting all 5 REST routes exactly as cmd/control-plane/main.go does --
// including, as of Step 20, auth.Middleware gating the whole /api/sessions
// group.
type testRig struct {
	pool         *pgxpool.Pool
	sessions     *narvipg.SessionStore
	turns        *narvipg.TurnStore
	sandboxes    *narvipg.SandboxStore
	events       *narvipg.EventStore
	artifacts    *narvipg.ArtifactStore
	wsTokens     *narvipg.WSTokenStore
	environments *narvipg.EnvironmentStore
	users        *narvipg.UserStore
	identities   *narvipg.IdentityStore
	userSessions *narvipg.UserSessionStore
	registry     *sessionactor.Registry
	server       *httptest.Server

	// plans/participants are Step 37's ("plan mode, web", §8.1) own
	// additions, backing this rig's own approve/reject plan routes below
	// (planapprove_integration_test.go).
	plans        *narvipg.PlanStore
	participants *narvipg.ParticipantStore

	// outbox/linearAgentSessions are Step 38's ("plan mode, cross-channel",
	// §8.1/§13.3) own additions -- DecidePlanOnTx's own cross-channel-notify
	// dependencies (decideplan.go), now threaded through the approve/reject
	// routes below exactly like production wiring (cmd/control-plane/
	// main.go).
	outbox              *narvipg.OutboxStore
	linearAgentSessions *narvipg.LinearAgentSessionStore

	// auditLog is Step 39's ("identities + full RBAC", §13.3) own
	// addition -- threaded through to CreateSession/CreateTurn/
	// ApprovePlan/RejectPlan below exactly like production wiring
	// (cmd/control-plane/main.go).
	auditLog *narvipg.AuditLogStore

	// linkPrompts backs the members API's own GET /api/members (below) --
	// ListMembers's own "pending-link state" half (Step 39, §13.2).
	linkPrompts *narvipg.IdentityLinkPromptStore

	// promptTemplates backs POST /api/intent-templates(/preview) below
	// (audit finding M5, completeness) -- the first-ever caller of
	// postgres.PromptTemplateStore's own Upsert method.
	promptTemplates *narvipg.PromptTemplateStore

	// tokenEncryptionKey is a fixed, valid 32-byte AES-256-GCM key used by
	// this rig's own scm-credentials tests (real EncryptToken/DecryptToken
	// round trip, matching the SAME real flow Step 20's own OAuth callback
	// uses -- not a shortcut).
	tokenEncryptionKey []byte

	// provider is Step 22's ("snapshots & restore") own addition -- a
	// *fakeSnapshotProvider (snapshotmint_integration_test.go), configured
	// per-test via its own exported fields, backing this rig's own
	// snapshot-mint route below.
	provider *fakeSnapshotProvider
}

func newTestRig(t *testing.T) testRig {
	t.Helper()
	ctx := context.Background()
	pool := newTestPool(t)

	// nil provider/commander: this rig's own tests only assert that
	// EnsureDispatched is correctly TRIGGERED by CreateSession, not what
	// the full spawn/dispatch decision tree then does with it --
	// internal/app/sessionactor's own dispatch_integration_test.go covers
	// that decision tree exhaustively.
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	rig := testRig{
		pool:                pool,
		sessions:            narvipg.NewSessionStore(pool),
		turns:               narvipg.NewTurnStore(pool),
		sandboxes:           narvipg.NewSandboxStore(pool),
		events:              narvipg.NewEventStore(pool),
		artifacts:           narvipg.NewArtifactStore(pool),
		wsTokens:            narvipg.NewWSTokenStore(pool),
		environments:        narvipg.NewEnvironmentStore(pool),
		users:               narvipg.NewUserStore(pool),
		identities:          narvipg.NewIdentityStore(pool),
		userSessions:        narvipg.NewUserSessionStore(pool),
		registry:            registry,
		tokenEncryptionKey:  []byte("01234567890123456789012345678901"), // exactly 32 bytes
		provider:            &fakeSnapshotProvider{},
		plans:               narvipg.NewPlanStore(pool),
		participants:        narvipg.NewParticipantStore(pool),
		outbox:              narvipg.NewOutboxStore(pool),
		linearAgentSessions: narvipg.NewLinearAgentSessionStore(pool),
		auditLog:            narvipg.NewAuditLogStore(pool),
		linkPrompts:         narvipg.NewIdentityLinkPromptStore(pool),
		promptTemplates:     narvipg.NewPromptTemplateStore(pool),
	}
	t.Cleanup(func() { _ = rig.registry.Shutdown() })

	router := chi.NewRouter()
	router.Route("/api/sessions", func(r chi.Router) {
		r.Use(auth.Middleware(rig.userSessions, rig.users))
		r.Post("/", httpapi.CreateSession(rig.pool, rig.sessions, rig.turns, rig.environments, rig.auditLog, rig.registry, nil))
		r.Get("/{sessionID}", httpapi.GetSession(rig.sessions))
		r.Get("/{sessionID}/events", httpapi.ListEvents(rig.sessions, rig.events))
		r.Get("/{sessionID}/artifacts", httpapi.ListArtifacts(rig.sessions, rig.artifacts))
		r.Post("/{sessionID}/ws-token", httpapi.MintWSToken(rig.sessions, rig.wsTokens, platform.DefaultTimeouts()))
		r.Post("/{sessionID}/turns", httpapi.CreateTurn(rig.pool, rig.sessions, rig.turns, rig.plans, rig.participants, rig.auditLog, rig.registry))
		r.Post("/{sessionID}/plans/{planId}/approve", httpapi.ApprovePlan(rig.pool, rig.sessions, rig.turns, rig.plans, rig.participants, rig.outbox, rig.linearAgentSessions, rig.auditLog, rig.registry))
		r.Post("/{sessionID}/plans/{planId}/reject", httpapi.RejectPlan(rig.pool, rig.sessions, rig.turns, rig.plans, rig.participants, rig.outbox, rig.linearAgentSessions, rig.auditLog))
		// Audit-fix batch (completeness/discoverability, M3) -- see
		// httpapi/plans.go's own doc comment.
		r.Get("/{sessionID}/plans", httpapi.ListPlans(rig.sessions, rig.plans))
	})
	// /api/members, /api/audit-log (Step 39, "identities + full RBAC",
	// §13.2/§13.3) -- mounted exactly like cmd/control-plane/main.go's own
	// wiring (this file's own doc comment on that file's own precedent):
	// gated behind auth.Middleware only, with each handler rendering its
	// own admin-only authz.Authorize verdict.
	router.Route("/api/members", func(r chi.Router) {
		r.Use(auth.Middleware(rig.userSessions, rig.users))
		r.Get("/", httpapi.ListMembers(rig.users, rig.identities, rig.linkPrompts))
		r.Patch("/{userID}/role", httpapi.UpdateMemberRole(rig.pool, rig.users, rig.identities, rig.auditLog))
		r.Post("/{userID}/identities", httpapi.LinkMemberIdentity(rig.pool, rig.users, rig.identities, rig.auditLog))
		r.Delete("/{userID}/identities/{identityID}", httpapi.UnlinkMemberIdentity(rig.pool, rig.identities, rig.auditLog))
	})
	router.Route("/api/audit-log", func(r chi.Router) {
		r.Use(auth.Middleware(rig.userSessions, rig.users))
		r.Get("/", httpapi.ListAuditLog(rig.auditLog))
	})
	// /api/intent-templates, /api/intent-templates/preview (audit finding
	// M5, completeness) -- mounted exactly like cmd/control-plane/main.go's
	// own wiring (see classifiertemplates.go's own doc comment).
	router.Route("/api/intent-templates", func(r chi.Router) {
		r.Use(auth.Middleware(rig.userSessions, rig.users))
		r.Post("/preview", httpapi.PreviewIntentTemplate())
		r.Post("/", httpapi.UpsertIntentTemplate(rig.pool, rig.promptTemplates, rig.auditLog))
	})
	// scm-credentials is deliberately mounted OUTSIDE /api/sessions and
	// outside auth.Middleware entirely -- see scmcredentials.go's own doc
	// comment.
	router.Post("/sessions/{sessionID}/scm-credentials",
		httpapi.ScmCredentials(rig.sessions, rig.sandboxes, rig.identities, rig.users, rig.tokenEncryptionKey, platform.DefaultTimeouts()))
	// snapshot-mint (Step 22, "snapshots & restore") is mounted the SAME
	// way -- see snapshotmint.go's own doc comment.
	router.Post("/sessions/{sessionID}/snapshot",
		httpapi.SnapshotMint(rig.sandboxes, rig.provider))

	rig.server = httptest.NewServer(router)
	t.Cleanup(rig.server.Close)

	return rig
}

func (r testRig) createSession(ctx context.Context, t *testing.T) sqlcgen.Session {
	t.Helper()
	row, err := r.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	return row
}

// createAuthenticatedUser builds a real user + linked GitHub identity + a
// user_sessions row directly via the stores (bypassing the OAuth flow
// entirely -- internal/adapters/inbound/auth's own package is what
// integration-tests that flow; this package only needs a REAL, valid
// session to attach as a cookie), and returns the created user row plus
// the PLAINTEXT session token.
func (r testRig) createAuthenticatedUser(ctx context.Context, t *testing.T) (sqlcgen.User, string) {
	t.Helper()

	externalID := fmt.Sprintf("test-github-id-%d", time.Now().UnixNano())
	email := externalID + "@example.com"

	user, err := r.users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: email,
		DisplayName:  "Test User",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create test user: %v", err)
	}

	if _, err := r.identities.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:        user.ID,
		Provider:      sqlcgen.IdentityProviderGithub,
		ExternalID:    externalID,
		Email:         &email,
		EmailVerified: true,
		LinkedVia:     sqlcgen.IdentityLinkedViaAdmin,
	}); err != nil {
		t.Fatalf("create test identity: %v", err)
	}

	token, err := platform.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if _, err := r.userSessions.Create(ctx, sqlcgen.CreateUserSessionParams{
		UserID:    user.ID,
		TokenHash: platform.HashToken(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(platform.DefaultTimeouts().UserSessionTTL), Valid: true},
	}); err != nil {
		t.Fatalf("create test user session: %v", err)
	}

	return user, token
}

// sessionCountForUser returns how many rows exist in sessions with
// created_by = userID -- used by this file's own repo-validation rejection
// tests to prove a 400 happens strictly BEFORE sessions.WithTx(tx).Create,
// not merely that the handler returns the right status code.
func (r testRig) sessionCountForUser(ctx context.Context, t *testing.T, userID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE created_by = $1`, userID).Scan(&count); err != nil {
		t.Fatalf("count sessions for user: %v", err)
	}
	return count
}

// doJSON issues method against r.server.URL+path with an optional body,
// decoding the response body into v (if non-nil) and returning the status
// code. If token is non-empty, it is attached as the narvi_auth_session
// cookie -- pass "" to exercise the no-auth-at-all case.
func (r testRig) doJSON(t *testing.T, method, path string, body []byte, v any, token string) int {
	t.Helper()

	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, r.server.URL+path, reqBody)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: platform.AuthSessionCookieName, Value: token})
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode response body: %v", err)
		}
	}
	return resp.StatusCode
}

// --- Auth gate itself ---

// TestRoutes_RequireAuth proves every one of the 5 routes rejects a request
// with NO narvi_auth_session cookie at all with 401 -- the concrete proof
// Step 19's old open-access behavior is gone.
func TestRoutes_RequireAuth(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "CreateSession", method: http.MethodPost, path: "/api/sessions"},
		{name: "GetSession", method: http.MethodGet, path: "/api/sessions/" + session.ID.String()},
		{name: "ListEvents", method: http.MethodGet, path: "/api/sessions/" + session.ID.String() + "/events"},
		{name: "ListArtifacts", method: http.MethodGet, path: "/api/sessions/" + session.ID.String() + "/artifacts"},
		{name: "ListPlans", method: http.MethodGet, path: "/api/sessions/" + session.ID.String() + "/plans"},
		{name: "MintWSToken", method: http.MethodPost, path: "/api/sessions/" + session.ID.String() + "/ws-token"},
		{name: "CreateTurn", method: http.MethodPost, path: "/api/sessions/" + session.ID.String() + "/turns"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status := rig.doJSON(t, tc.method, tc.path, []byte{}, nil, "" /* no cookie */)
			if status != http.StatusUnauthorized {
				t.Errorf("status = %d, want %d (no auth cookie presented)", status, http.StatusUnauthorized)
			}
		})
	}
}

// --- CreateSession ---

func TestCreateSession_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{
		"spawnSource": "web",
		"title": "my session",
		"prompt": "do the thing",
		"repos": [{"name": "narvi", "url": "https://github.com/khazaddev/narvi", "branch": null}],
		"modelId": null,
		"planMode": false
	}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.Id == "" {
		t.Error("Id is empty")
	}
	if got.Status != restdtos.SessionStatusCreated {
		t.Errorf("Status = %q, want %q", got.Status, restdtos.SessionStatusCreated)
	}
	if got.SpawnSource != restdtos.SessionSpawnSourceWeb {
		t.Errorf("SpawnSource = %q, want %q", got.SpawnSource, restdtos.SessionSpawnSourceWeb)
	}
	// The concrete proof Step 19's own "created_by always NULL" gap is
	// closed: it now matches the REAL authenticated caller's id.
	if got.CreatedBy == nil {
		t.Fatal("CreatedBy = nil, want the authenticated user's id")
	}
	if *got.CreatedBy != user.ID.String() {
		t.Errorf("CreatedBy = %q, want %q", *got.CreatedBy, user.ID.String())
	}
	if got.Title == nil || *got.Title != "my session" {
		t.Errorf("Title = %v, want \"my session\"", got.Title)
	}
	if got.Archived {
		t.Error("Archived = true, want false for a freshly created session")
	}

	// Step 21 ("e2e happy path"): repos is now actually persisted, and a
	// pending turn was created carrying the prompt/planMode -- the
	// concrete proof create.go's own doc comment describes.
	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}
	var reposJSON []byte
	if err := rig.pool.QueryRow(ctx, `SELECT repos FROM sessions WHERE id = $1`, sessionID).Scan(&reposJSON); err != nil {
		t.Fatalf("query persisted repos: %v", err)
	}
	var repos []map[string]any
	if err := json.Unmarshal(reposJSON, &repos); err != nil {
		t.Fatalf("unmarshal persisted repos: %v", err)
	}
	if len(repos) != 1 || repos[0]["name"] != "narvi" {
		t.Errorf("persisted repos = %s, want one entry named %q", reposJSON, "narvi")
	}

	turns, err := rig.turns.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1 (prompt was non-nil)", len(turns))
	}
	if turns[0].Status != sqlcgen.TurnStatusPending {
		t.Errorf("turn status = %s, want %s", turns[0].Status, sqlcgen.TurnStatusPending)
	}
	if turns[0].Prompt == nil || *turns[0].Prompt != "do the thing" {
		t.Errorf("turn prompt = %v, want %q", turns[0].Prompt, "do the thing")
	}
}

// TestCreateSession_HappyPath_WritesSessionCreateAuditRow proves the
// audit-fix batch's own M9 fix: unlike plan.approve/plan.reject (already
// fully covered, existence AND detail-JSON shape, by
// decideplan_integration_test.go's own TestDecidePlanOnTx_Approve_
// AuditDetailCarriesTurnID/_Reject_AuditDetailHasNoTurnID), session.create
// (create.go's own CreateSessionOnTx, "session.create" written right after
// the session insert) had ZERO test coverage of any kind before this fix
// -- no existence check, nothing. This proves a real session.create row
// exists after a successful POST /api/sessions, with the correct action
// string/actor, AND that its own detail_json actually carries
// {"spawn_source": ...} -- mirrors internal/app/identitylink/
// service_integration_test.go's own TestResolve_ExactlyOneMatchAutoLinks
// AuditLog.List(ctx, 10, 0) idiom, extended (that precedent itself never
// does) to also decode entries[i].DetailJson.
func TestCreateSession_HappyPath_WritesSessionCreateAuditRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)
	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	entries, err := rig.auditLog.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("AuditLog.List: %v", err)
	}
	var entry *sqlcgen.AuditLog
	for i := range entries {
		if entries[i].Action == "session.create" && entries[i].ResourceID == got.Id {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		t.Fatalf("no session.create audit_log row found for session %s among %d entries", got.Id, len(entries))
	}
	if entry.ResourceType != "session" {
		t.Errorf("ResourceType = %q, want %q", entry.ResourceType, "session")
	}
	if !entry.ActorUserID.Valid || entry.ActorUserID != user.ID {
		t.Errorf("ActorUserID = %v, want %v (the authenticated caller)", entry.ActorUserID, user.ID)
	}

	var detail map[string]any
	if err := json.Unmarshal(entry.DetailJson, &detail); err != nil {
		t.Fatalf("unmarshal detail_json: %v", err)
	}
	if detail["spawn_source"] != "web" {
		t.Errorf("detail_json[spawn_source] = %v, want %q", detail["spawn_source"], "web")
	}
}

// TestCreateSession_NoPrompt_NoTurnCreated proves a nil prompt creates the
// session with NO turn row at all -- CreateSessionRequest.Prompt being
// nil means "create the session without dispatching a first turn".
func TestCreateSession_NoPrompt_NoTurnCreated(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}
	turns, err := rig.turns.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("len(turns) = %d, want 0 (prompt was nil)", len(turns))
	}
}

func TestCreateSession_NoAuth(t *testing.T) {
	rig := newTestRig(t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, "" /* no cookie */)
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestCreateSession_EmptyRepos(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[],"modelId":null,"planMode":false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestCreateSession_InvalidRepoURL_Rejected proves every one of
// reposource.ValidateRepoURL's own rejection reasons is checked at this
// handler's own trust boundary (before any Postgres write), not only much
// later at actual git-invocation time deep inside the sandbox agent: a
// non-https scheme, a URL that fails net/url.Parse outright, and a URL
// that parses but has no host.
func TestCreateSession_InvalidRepoURL_Rejected(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{name: "non-https scheme", url: "http://example.com"},
		{name: "git scheme", url: "git://example.com/repo.git"},
		{name: "fails to parse", url: "https://%zz"},
		{name: "no host", url: "https://"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t)
			ctx := context.Background()
			user, token := rig.createAuthenticatedUser(ctx, t)

			body := []byte(fmt.Sprintf(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":%q,"branch":null}],"modelId":null,"planMode":false}`, tc.url))
			status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
			}
			if count := rig.sessionCountForUser(ctx, t, user.ID); count != 0 {
				t.Errorf("sessions for user = %d, want 0 (rejected before any Postgres write)", count)
			}
		})
	}
}

// TestCreateSession_InvalidRepoName_PathTraversal_Rejected proves a repo
// name shaped like a path-traversal payload is rejected here -- repo.Name
// later reaches filepath.Join(workspaceDir, repo.Name) inside gitclone, so
// it is exactly as much a risk as an unvalidated Url/Branch, even though
// the audit finding's own summary names only url/branch explicitly.
func TestCreateSession_InvalidRepoName_PathTraversal_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"../escaped","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if count := rig.sessionCountForUser(ctx, t, user.ID); count != 0 {
		t.Errorf("sessions for user = %d, want 0 (rejected before any Postgres write)", count)
	}
}

// TestCreateSession_InvalidRepoBranch_DashPrefix_Rejected proves a branch
// beginning with "-" -- the argument-injection-shaped payload
// internal/domain/reposource's own tests already use as their canonical
// example -- is rejected here too.
func TestCreateSession_InvalidRepoBranch_DashPrefix_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":"--upload-pack=evil"}],"modelId":null,"planMode":false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if count := rig.sessionCountForUser(ctx, t, user.ID); count != 0 {
		t.Errorf("sessions for user = %d, want 0 (rejected before any Postgres write)", count)
	}
}

// TestCreateSession_NilBranch_Succeeds proves a nil branch on an otherwise
// -valid repo is never accidentally rejected -- nil means "use the repo's
// own default branch" and must never reach reposource.ValidateBranch at
// all.
func TestCreateSession_NilBranch_Succeeds(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusCreated {
		t.Errorf("status = %d, want %d", status, http.StatusCreated)
	}
}

// TestCreateSession_MultipleRepos_SecondInvalid_Rejected proves the repo
// validation loop actually inspects EVERY repo, in order -- a valid first
// repo must never cause an invalid second repo to be skipped.
func TestCreateSession_MultipleRepos_SecondInvalid_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null},{"name":"../escaped","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if count := rig.sessionCountForUser(ctx, t, user.ID); count != 0 {
		t.Errorf("sessions for user = %d, want 0 (rejected before any Postgres write)", count)
	}
}

// --- CreateSession: spawnSource forced to "web" (audit remediation,
// wire-contract/security-adjacent lens) ---

// TestCreateSession_NonWebSpawnSource_Rejected proves this REST handler --
// the ONLY authenticated REST session-creation path -- rejects a request
// that explicitly claims a non-"web" spawnSource, rather than persisting
// it verbatim. Before this fix, an authenticated web caller could POST
// spawnSource: "slack" (or linear/github) and have it land on the new
// session row as-is, forging provenance in the UI's own source icons/
// filters and audit rows, and steering app/sessionactor/outboxenqueue.go's
// own turn-completion outbox routing (which genuinely branches on
// sessions.spawn_source) down a channel this session was never actually
// created through. Table-driven over the three non-"web" enum values;
// each is rejected 400 BEFORE any Postgres write, matching the established
// repo/pathScope/mockConfig rejection-test precedent above exactly
// (asserting zero session rows for the calling user, not merely the status
// code).
func TestCreateSession_NonWebSpawnSource_Rejected(t *testing.T) {
	tests := []struct {
		name        string
		spawnSource string
	}{
		{name: "slack", spawnSource: "slack"},
		{name: "linear", spawnSource: "linear"},
		{name: "github", spawnSource: "github"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t)
			ctx := context.Background()
			user, token := rig.createAuthenticatedUser(ctx, t)

			body := []byte(fmt.Sprintf(`{"spawnSource":%q,"title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`, tc.spawnSource))
			var errBody map[string]string
			status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &errBody, token)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
			}
			if errBody["error"] == "" {
				t.Error(`error body["error"] is empty, want a clear validation error naming spawnSource`)
			}
			if count := rig.sessionCountForUser(ctx, t, user.ID); count != 0 {
				t.Errorf("sessions for user = %d, want 0 (rejected before any Postgres write, and no forged spawn_source ever reached Postgres)", count)
			}
		})
	}
}

// TestCreateSession_WebSpawnSource_PersistsAsWeb is the companion positive
// case to TestCreateSession_NonWebSpawnSource_Rejected above: an explicit
// "web" spawnSource -- the one legitimate value on this endpoint -- is
// still accepted and persists exactly as sent, both in the response DTO
// and (queried directly, not merely trusting the handler's own echo) in
// the sessions.spawn_source column itself.
func TestCreateSession_WebSpawnSource_PersistsAsWeb(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)
	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.SpawnSource != restdtos.SessionSpawnSourceWeb {
		t.Errorf("SpawnSource = %q, want %q", got.SpawnSource, restdtos.SessionSpawnSourceWeb)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}
	var persisted string
	if err := rig.pool.QueryRow(ctx, `SELECT spawn_source FROM sessions WHERE id = $1`, sessionID).Scan(&persisted); err != nil {
		t.Fatalf("query persisted spawn_source: %v", err)
	}
	if persisted != "web" {
		t.Errorf("persisted spawn_source = %q, want %q", persisted, "web")
	}
}

// --- CreateSession: pathScope / Environment (row 10, "domain: Environment
// scoping", §14.1) ---

// TestCreateSession_InvalidPathScope_Rejected proves a pathScope pattern
// containing a ".." segment -- internal/domain/environment.
// ValidatePathScope's own ErrPathTraversal case -- is rejected 400 BEFORE
// any Postgres write, matching the established repo-validation precedent
// exactly (assert zero session rows for the calling user).
func TestCreateSession_InvalidPathScope_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{
		"spawnSource": "web",
		"title": null,
		"prompt": null,
		"repos": [{"name": "narvi", "url": "https://example.com", "branch": null}],
		"modelId": null,
		"planMode": false,
		"pathScope": ["apps/../etc"]
	}`)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if count := rig.sessionCountForUser(ctx, t, user.ID); count != 0 {
		t.Errorf("sessions for user = %d, want 0 (rejected before any Postgres write)", count)
	}
}

// TestCreateSession_ValidPathScope_CreatesEnvironment proves a valid,
// non-empty pathScope creates a real environments row (with the exact
// pattern list persisted), sets the new session's environment_id to that
// row's id, and sets provenance_tag to CreateSession's own chosen
// scopedEnvironmentProvenanceTag value.
func TestCreateSession_ValidPathScope_CreatesEnvironment(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{
		"spawnSource": "web",
		"title": null,
		"prompt": null,
		"repos": [{"name": "narvi", "url": "https://example.com", "branch": null}],
		"modelId": null,
		"planMode": false,
		"pathScope": ["/apps/web/*", "/apps/api/*"]
	}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}

	var environmentID pgtype.UUID
	var provenanceTag *string
	if err := rig.pool.QueryRow(ctx,
		`SELECT environment_id, provenance_tag FROM sessions WHERE id = $1`, sessionID,
	).Scan(&environmentID, &provenanceTag); err != nil {
		t.Fatalf("query persisted session: %v", err)
	}
	if !environmentID.Valid {
		t.Fatal("environment_id = NULL, want a real environments row id")
	}
	if provenanceTag == nil || *provenanceTag == "" {
		t.Fatal("provenance_tag = nil/empty, want a non-empty value")
	}

	var pathScopeJSON []byte
	var mockConfigured bool
	if err := rig.pool.QueryRow(ctx,
		`SELECT path_scope, mock_configured FROM environments WHERE id = $1`, environmentID,
	).Scan(&pathScopeJSON, &mockConfigured); err != nil {
		t.Fatalf("query persisted environment: %v", err)
	}
	var pathScope []string
	if err := json.Unmarshal(pathScopeJSON, &pathScope); err != nil {
		t.Fatalf("unmarshal persisted path_scope: %v", err)
	}
	want := []string{"/apps/web/*", "/apps/api/*"}
	if len(pathScope) != len(want) || pathScope[0] != want[0] || pathScope[1] != want[1] {
		t.Errorf("persisted path_scope = %v, want %v", pathScope, want)
	}
	if mockConfigured {
		t.Error("mock_configured = true, want false (nothing in this call path sets it)")
	}
}

// TestCreateSession_NilPathScope_LeavesEnvironmentUnset proves an
// absent/nil pathScope leaves both environment_id and provenance_tag NULL,
// exactly matching pre-existing (pre-this-batch) behavior -- no
// environments row is created at all.
func TestCreateSession_NilPathScope_LeavesEnvironmentUnset(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false,"pathScope":null}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}

	var environmentID pgtype.UUID
	var provenanceTag *string
	if err := rig.pool.QueryRow(ctx,
		`SELECT environment_id, provenance_tag FROM sessions WHERE id = $1`, sessionID,
	).Scan(&environmentID, &provenanceTag); err != nil {
		t.Fatalf("query persisted session: %v", err)
	}
	if environmentID.Valid {
		t.Errorf("environment_id = %v, want NULL (pathScope was null)", environmentID)
	}
	if provenanceTag != nil {
		t.Errorf("provenance_tag = %q, want nil (pathScope was null)", *provenanceTag)
	}

	var environmentCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM environments`).Scan(&environmentCount); err != nil {
		t.Fatalf("count environments: %v", err)
	}
	if environmentCount != 0 {
		t.Errorf("environments row count = %d, want 0 (no pathScope was supplied)", environmentCount)
	}
}

// TestCreateSession_AbsentPathScope_LeavesEnvironmentUnset proves the
// pathScope key being entirely ABSENT from the request body (not merely
// present-and-null) behaves identically to TestCreateSession_
// NilPathScope_LeavesEnvironmentUnset -- pathScope is genuinely optional,
// not just nullable (unlike every other field on this DTO).
func TestCreateSession_AbsentPathScope_LeavesEnvironmentUnset(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	// No "pathScope" key at all -- this is TestCreateSession_HappyPath's own
	// exact request body, re-run unmodified to confirm this batch changed
	// nothing about it.
	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}

	var environmentID pgtype.UUID
	var provenanceTag *string
	if err := rig.pool.QueryRow(ctx,
		`SELECT environment_id, provenance_tag FROM sessions WHERE id = $1`, sessionID,
	).Scan(&environmentID, &provenanceTag); err != nil {
		t.Fatalf("query persisted session: %v", err)
	}
	if environmentID.Valid {
		t.Errorf("environment_id = %v, want NULL (pathScope was absent)", environmentID)
	}
	if provenanceTag != nil {
		t.Errorf("provenance_tag = %q, want nil (pathScope was absent)", *provenanceTag)
	}
}

// TestCreateSession_EmptyPathScope_LeavesEnvironmentUnset proves an empty
// (present, non-null, zero-length) pathScope array is treated the same as
// nil/absent -- "unscoped" -- never creating an environments row nor
// calling ValidatePathScope (which would trivially accept an empty slice
// anyway, but this proves CreateSession's own hasPathScope gate, not just
// ValidatePathScope's tolerance of it).
func TestCreateSession_EmptyPathScope_LeavesEnvironmentUnset(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false,"pathScope":[]}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}

	var environmentID pgtype.UUID
	if err := rig.pool.QueryRow(ctx,
		`SELECT environment_id FROM sessions WHERE id = $1`, sessionID,
	).Scan(&environmentID); err != nil {
		t.Fatalf("query persisted session: %v", err)
	}
	if environmentID.Valid {
		t.Errorf("environment_id = %v, want NULL (pathScope was empty)", environmentID)
	}
}

// TestCreateSession_MockConfigPresent_ContractsPathOmitted_DefaultsAndCreatesEnvironment
// proves row 27's ("mocking + contract drift", §14.3) own core semantics:
// a present "mockConfig" key (even as {}, contractsPath absent) creates an
// environments row with mock_configured=true and contracts_path defaulting
// to the literal "contracts/api".
func TestCreateSession_MockConfigPresent_ContractsPathOmitted_DefaultsAndCreatesEnvironment(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{
		"spawnSource": "web",
		"title": null,
		"prompt": null,
		"repos": [{"name": "narvi", "url": "https://example.com", "branch": null}],
		"modelId": null,
		"planMode": false,
		"mockConfig": {}
	}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}

	var environmentID pgtype.UUID
	if err := rig.pool.QueryRow(ctx,
		`SELECT environment_id FROM sessions WHERE id = $1`, sessionID,
	).Scan(&environmentID); err != nil {
		t.Fatalf("query persisted session: %v", err)
	}
	if !environmentID.Valid {
		t.Fatal("environment_id = NULL, want a real environments row id")
	}

	var mockConfigured bool
	var contractsPath *string
	if err := rig.pool.QueryRow(ctx,
		`SELECT mock_configured, contracts_path FROM environments WHERE id = $1`, environmentID,
	).Scan(&mockConfigured, &contractsPath); err != nil {
		t.Fatalf("query persisted environment: %v", err)
	}
	if !mockConfigured {
		t.Error("mock_configured = false, want true")
	}
	if contractsPath == nil || *contractsPath != "contracts/api" {
		t.Errorf("contracts_path = %v, want %q", contractsPath, "contracts/api")
	}
}

// TestCreateSession_MockConfigPresent_ContractsPathSet_StoredVerbatim
// proves an explicit mockConfig.contractsPath is stored verbatim, not
// defaulted.
func TestCreateSession_MockConfigPresent_ContractsPathSet_StoredVerbatim(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{
		"spawnSource": "web",
		"title": null,
		"prompt": null,
		"repos": [{"name": "narvi", "url": "https://example.com", "branch": null}],
		"modelId": null,
		"planMode": false,
		"mockConfig": {"contractsPath": "services/mock-api/contracts"}
	}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}

	var environmentID pgtype.UUID
	if err := rig.pool.QueryRow(ctx,
		`SELECT environment_id FROM sessions WHERE id = $1`, sessionID,
	).Scan(&environmentID); err != nil {
		t.Fatalf("query persisted session: %v", err)
	}
	if !environmentID.Valid {
		t.Fatal("environment_id = NULL, want a real environments row id")
	}

	var mockConfigured bool
	var contractsPath *string
	if err := rig.pool.QueryRow(ctx,
		`SELECT mock_configured, contracts_path FROM environments WHERE id = $1`, environmentID,
	).Scan(&mockConfigured, &contractsPath); err != nil {
		t.Fatalf("query persisted environment: %v", err)
	}
	if !mockConfigured {
		t.Error("mock_configured = false, want true")
	}
	if contractsPath == nil || *contractsPath != "services/mock-api/contracts" {
		t.Errorf("contracts_path = %v, want %q", contractsPath, "services/mock-api/contracts")
	}
}

// TestCreateSession_MockConfigPresent_PathScopeAbsent_StillCreatesEnvironment
// proves the "either" gate (row 27's own doc comment on CreateSession):
// mockConfig ALONE, with pathScope entirely absent, is sufficient to
// create a new, session-scoped Environment row -- pathScope is NOT
// required.
func TestCreateSession_MockConfigPresent_PathScopeAbsent_StillCreatesEnvironment(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false,"mockConfig":{}}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}

	var environmentID pgtype.UUID
	var provenanceTag *string
	if err := rig.pool.QueryRow(ctx,
		`SELECT environment_id, provenance_tag FROM sessions WHERE id = $1`, sessionID,
	).Scan(&environmentID, &provenanceTag); err != nil {
		t.Fatalf("query persisted session: %v", err)
	}
	if !environmentID.Valid {
		t.Fatal("environment_id = NULL, want a real environments row id (mockConfig alone must be sufficient)")
	}
	// RequiresProvenanceTag depends only on PathScope (environment.
	// RequiresProvenanceTag's own doc comment) -- a mockConfig-only
	// Environment must NOT cause a provenance tag to be set.
	if provenanceTag != nil {
		t.Errorf("provenance_tag = %q, want nil (mockConfig alone does not require a provenance tag)", *provenanceTag)
	}

	var pathScope []byte
	if err := rig.pool.QueryRow(ctx,
		`SELECT path_scope FROM environments WHERE id = $1`, environmentID,
	).Scan(&pathScope); err != nil {
		t.Fatalf("query persisted environment: %v", err)
	}
	if pathScope != nil {
		t.Errorf("path_scope = %s, want NULL (pathScope was absent)", pathScope)
	}
}

// TestCreateSession_NeitherPathScopeNorMockConfig_NoEnvironmentRow is a
// regression guard: a request carrying NEITHER pathScope nor mockConfig
// behaves exactly as before this batch -- no environments row at all.
func TestCreateSession_NeitherPathScopeNorMockConfig_NoEnvironmentRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":"narvi","url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(got.Id); err != nil {
		t.Fatalf("scan session id: %v", err)
	}

	var environmentID pgtype.UUID
	if err := rig.pool.QueryRow(ctx,
		`SELECT environment_id FROM sessions WHERE id = $1`, sessionID,
	).Scan(&environmentID); err != nil {
		t.Fatalf("query persisted session: %v", err)
	}
	if environmentID.Valid {
		t.Errorf("environment_id = %v, want NULL (neither pathScope nor mockConfig was supplied)", environmentID)
	}
}

// --- CreateSession: mockConfig.contractsPath validation (audit
// remediation: security-crosscutting lens) ---

// TestCreateSession_ContractsPathTraversal_Rejected proves a
// mockConfig.contractsPath containing a ".." segment -- environment.
// ValidateContractsPath's own ErrContractsPathTraversal case -- is
// rejected 400 BEFORE any Postgres write, mirroring
// TestCreateSession_InvalidPathScope_Rejected's own identical precedent
// exactly (assert zero session rows for the calling user).
func TestCreateSession_ContractsPathTraversal_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{
		"spawnSource": "web",
		"title": null,
		"prompt": null,
		"repos": [{"name": "narvi", "url": "https://example.com", "branch": null}],
		"modelId": null,
		"planMode": false,
		"mockConfig": {"contractsPath": "contracts/../etc"}
	}`)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if count := rig.sessionCountForUser(ctx, t, user.ID); count != 0 {
		t.Errorf("sessions for user = %d, want 0 (rejected before any Postgres write)", count)
	}
}

// TestCreateSession_ContractsPathQueryChar_Rejected proves a
// mockConfig.contractsPath containing a "?" -- the exact query-injection
// shape the githubapi adapter's own audit remediation independently
// guards against too -- is rejected 400 BEFORE any Postgres write.
func TestCreateSession_ContractsPathQueryChar_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{
		"spawnSource": "web",
		"title": null,
		"prompt": null,
		"repos": [{"name": "narvi", "url": "https://example.com", "branch": null}],
		"modelId": null,
		"planMode": false,
		"mockConfig": {"contractsPath": "contracts/api?ref=attacker"}
	}`)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if count := rig.sessionCountForUser(ctx, t, user.ID); count != 0 {
		t.Errorf("sessions for user = %d, want 0 (rejected before any Postgres write)", count)
	}
}

// TestCreateSession_ContractsPathFragmentChar_Rejected proves a
// mockConfig.contractsPath containing a "#" is ALSO rejected 400 BEFORE
// any Postgres write -- the fragment-truncation counterpart to the "?"
// case above.
func TestCreateSession_ContractsPathFragmentChar_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	body := []byte(`{
		"spawnSource": "web",
		"title": null,
		"prompt": null,
		"repos": [{"name": "narvi", "url": "https://example.com", "branch": null}],
		"modelId": null,
		"planMode": false,
		"mockConfig": {"contractsPath": "contracts/api#evil"}
	}`)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
	if count := rig.sessionCountForUser(ctx, t, user.ID); count != 0 {
		t.Errorf("sessions for user = %d, want 0 (rejected before any Postgres write)", count)
	}
}

func TestCreateSession_MalformedBody(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions", []byte("not json"), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestCreateSession_OversizedBody(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	// A single "repos[0].name" value well beyond the 1 MiB request-body
	// cap -- json.Decoder will hit http.MaxBytesReader's own limit before
	// ever producing a complete value.
	huge := strings.Repeat("a", 2<<20) // 2 MiB
	body := []byte(fmt.Sprintf(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":%q,"url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`, huge))

	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil, token)
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", status, http.StatusRequestEntityTooLarge)
	}
}

// --- GetSession ---

func TestGetSession_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)
	_, token := rig.createAuthenticatedUser(ctx, t)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String(), nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Id != session.ID.String() {
		t.Errorf("Id = %q, want %q", got.Id, session.ID.String())
	}
}

func TestGetSession_MalformedID(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/not-a-uuid", nil, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestGetSession_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/11111111-1111-1111-1111-111111111111", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// --- ListEvents ---

func TestListEvents_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)
	_, token := rig.createAuthenticatedUser(ctx, t)

	const total = 3
	for i := 0; i < total; i++ {
		if _, err := rig.events.Create(ctx, sqlcgen.CreateEventParams{
			SessionID: session.ID,
			Type:      "token",
			MessageID: fmt.Sprintf("msg-%d", i),
			Payload:   []byte(fmt.Sprintf(`{"n":%d}`, i)),
		}); err != nil {
			t.Fatalf("create event %d: %v", i, err)
		}
	}

	var got restdtos.EventsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String()+"/events?limit=2", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Events) != 2 {
		t.Fatalf("len(Events) = %d, want 2", len(got.Events))
	}
	if got.NextCursor == nil {
		t.Fatal("NextCursor = nil, want non-nil (1 more event remains)")
	}

	var page2 restdtos.EventsResponse
	status = rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String()+"/events?cursor="+*got.NextCursor+"&limit=2", nil, &page2, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(page2.Events) != 1 {
		t.Fatalf("len(page2.Events) = %d, want 1", len(page2.Events))
	}
	if page2.NextCursor != nil {
		t.Errorf("page2.NextCursor = %v, want nil (exhausted)", *page2.NextCursor)
	}
}

func TestListEvents_SessionNotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/11111111-1111-1111-1111-111111111111/events", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestListEvents_MalformedSessionID(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/not-a-uuid/events", nil, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// --- ListArtifacts ---

func TestListArtifacts_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)
	_, token := rig.createAuthenticatedUser(ctx, t)

	if _, err := rig.pool.Exec(ctx,
		`INSERT INTO artifacts (session_id, type, url) VALUES ($1, 'pr', 'https://example.com/pr/1')`,
		session.ID,
	); err != nil {
		t.Fatalf("insert test artifact: %v", err)
	}

	var got restdtos.ArtifactsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String()+"/artifacts", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Artifacts) != 1 {
		t.Fatalf("len(Artifacts) = %d, want 1", len(got.Artifacts))
	}
}

func TestListArtifacts_SessionNotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/11111111-1111-1111-1111-111111111111/artifacts", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// --- ListPlans (audit finding M3, completeness) ---

// TestListPlans_HappyPath proves GET /api/sessions/:id/plans returns every
// plan VERSION for the session, in version order, with the right shape --
// a superseded v1 that was never decided, an approved v2 with decided_at/
// decided_by set, and a still-awaiting_approval v3 (the version a client
// would actually approve/reject) -- not just whichever version happens to
// be current.
func TestListPlans_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, token := rig.createAuthenticatedUser(ctx, t)
	session := createSessionForUser(ctx, t, rig, owner.ID, nil)

	turn1, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusCompleted, PlanMode: true})
	if err != nil {
		t.Fatalf("create turn1: %v", err)
	}
	turn2, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusCompleted, PlanMode: true})
	if err != nil {
		t.Fatalf("create turn2: %v", err)
	}
	turn3, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusCompleted, PlanMode: true})
	if err != nil {
		t.Fatalf("create turn3: %v", err)
	}

	const planModel = "claude-opus-4-8"
	// v1: superseded by v2's own "request changes" turn, before anyone ever
	// decided it.
	if _, err := rig.pool.Exec(ctx,
		`INSERT INTO plans (session_id, turn_id, version, status, plan_model_id) VALUES ($1, $2, 1, 'superseded', $3)`,
		session.ID, turn1.ID, planModel,
	); err != nil {
		t.Fatalf("seed v1 (superseded): %v", err)
	}
	// v2: approved, decided by the owner.
	if _, err := rig.pool.Exec(ctx,
		`INSERT INTO plans (session_id, turn_id, version, status, plan_model_id, decided_at, decided_by) VALUES ($1, $2, 2, 'approved', $3, now(), $4)`,
		session.ID, turn2.ID, planModel, owner.ID,
	); err != nil {
		t.Fatalf("seed v2 (approved): %v", err)
	}
	// v3: still awaiting_approval.
	if _, err := rig.pool.Exec(ctx,
		`INSERT INTO plans (session_id, turn_id, version, status, plan_model_id) VALUES ($1, $2, 3, 'awaiting_approval', $3)`,
		session.ID, turn3.ID, planModel,
	); err != nil {
		t.Fatalf("seed v3 (awaiting_approval): %v", err)
	}

	var got restdtos.ListPlansResponse
	status := rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String()+"/plans", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Plans) != 3 {
		t.Fatalf("len(Plans) = %d, want 3", len(got.Plans))
	}

	if got.Plans[0].Version != 1 || got.Plans[1].Version != 2 || got.Plans[2].Version != 3 {
		t.Fatalf("versions = [%d, %d, %d], want [1, 2, 3] (ORDER BY version)",
			got.Plans[0].Version, got.Plans[1].Version, got.Plans[2].Version)
	}

	v1 := got.Plans[0]
	if v1.SessionId != session.ID.String() {
		t.Errorf("v1 SessionId = %q, want %q", v1.SessionId, session.ID.String())
	}
	if v1.Status != restdtos.PlanStatusSuperseded {
		t.Errorf("v1 Status = %q, want %q", v1.Status, restdtos.PlanStatusSuperseded)
	}
	if v1.DecidedAt != nil {
		t.Errorf("v1 DecidedAt = %v, want nil (never decided before being superseded)", v1.DecidedAt)
	}
	if v1.DecidedBy != nil {
		t.Errorf("v1 DecidedBy = %v, want nil", v1.DecidedBy)
	}
	if v1.PlanModelId == nil || *v1.PlanModelId != planModel {
		t.Errorf("v1 PlanModelId = %v, want %q", v1.PlanModelId, planModel)
	}

	v2 := got.Plans[1]
	if v2.Status != restdtos.PlanStatusApproved {
		t.Errorf("v2 Status = %q, want %q", v2.Status, restdtos.PlanStatusApproved)
	}
	if v2.DecidedAt == nil {
		t.Error("v2 DecidedAt = nil, want set")
	}
	if v2.DecidedBy == nil || *v2.DecidedBy != owner.ID.String() {
		t.Errorf("v2 DecidedBy = %v, want %q", v2.DecidedBy, owner.ID.String())
	}

	v3 := got.Plans[2]
	if v3.Status != restdtos.PlanStatusAwaitingApproval {
		t.Errorf("v3 Status = %q, want %q", v3.Status, restdtos.PlanStatusAwaitingApproval)
	}
	if v3.DecidedAt != nil {
		t.Errorf("v3 DecidedAt = %v, want nil (still awaiting approval)", v3.DecidedAt)
	}
}

// TestListPlans_SessionNotFound mirrors ListEvents/ListArtifacts's own
// identical precedent above -- every session-scoped GET endpoint 404s on a
// nonexistent session.
func TestListPlans_SessionNotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/11111111-1111-1111-1111-111111111111/plans", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// --- MintWSToken ---

func TestMintWSToken_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)
	user, token := rig.createAuthenticatedUser(ctx, t)

	before := time.Now()
	var got restdtos.WSTokenResponse
	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/ws-token", []byte{}, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Token == "" {
		t.Fatal("Token is empty")
	}

	wantExpiry := before.Add(platform.DefaultTimeouts().WSTokenTTL)
	if got.ExpiresAt.Sub(wantExpiry).Abs() > time.Minute {
		t.Errorf("ExpiresAt = %v, want close to %v", got.ExpiresAt, wantExpiry)
	}

	// The stored row holds only the HASH, never the plaintext, is scoped
	// to this session, and -- the concrete proof Step 19's own
	// "ws_tokens.user_id always NULL" gap is closed -- carries the REAL
	// authenticated caller's id.
	stored, err := rig.wsTokens.GetByHash(ctx, platform.HashToken(got.Token))
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if stored.SessionID != session.ID {
		t.Errorf("stored.SessionID = %v, want %v", stored.SessionID, session.ID)
	}
	if !stored.UserID.Valid {
		t.Fatal("stored.UserID.Valid = false, want true (a real authenticated user)")
	}
	if stored.UserID != user.ID {
		t.Errorf("stored.UserID = %v, want %v", stored.UserID, user.ID)
	}
}

func TestMintWSToken_NoAuth(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/ws-token", []byte{}, nil, "" /* no cookie */)
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestMintWSToken_SessionNotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/11111111-1111-1111-1111-111111111111/ws-token", []byte{}, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestMintWSToken_MalformedSessionID(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/not-a-uuid/ws-token", []byte{}, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}
