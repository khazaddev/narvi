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
	users        *narvipg.UserStore
	identities   *narvipg.IdentityStore
	userSessions *narvipg.UserSessionStore
	registry     *sessionactor.Registry
	server       *httptest.Server

	// tokenEncryptionKey is a fixed, valid 32-byte AES-256-GCM key used by
	// this rig's own scm-credentials tests (real EncryptToken/DecryptToken
	// round trip, matching the SAME real flow Step 20's own OAuth callback
	// uses -- not a shortcut).
	tokenEncryptionKey []byte
}

func newTestRig(t *testing.T) testRig {
	t.Helper()
	ctx := context.Background()
	pool := newTestPool(t)

	rig := testRig{
		pool:         pool,
		sessions:     narvipg.NewSessionStore(pool),
		turns:        narvipg.NewTurnStore(pool),
		sandboxes:    narvipg.NewSandboxStore(pool),
		events:       narvipg.NewEventStore(pool),
		artifacts:    narvipg.NewArtifactStore(pool),
		wsTokens:     narvipg.NewWSTokenStore(pool),
		users:        narvipg.NewUserStore(pool),
		identities:   narvipg.NewIdentityStore(pool),
		userSessions: narvipg.NewUserSessionStore(pool),
		// nil provider/commander: this rig's own tests only assert that
		// EnsureDispatched is correctly TRIGGERED by CreateSession, not
		// what the full spawn/dispatch decision tree then does with it --
		// internal/app/sessionactor's own dispatch_integration_test.go
		// covers that decision tree exhaustively.
		registry:           sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil),
		tokenEncryptionKey: []byte("01234567890123456789012345678901"), // exactly 32 bytes
	}
	t.Cleanup(func() { _ = rig.registry.Shutdown() })

	router := chi.NewRouter()
	router.Route("/api/sessions", func(r chi.Router) {
		r.Use(auth.Middleware(rig.userSessions, rig.users))
		r.Post("/", httpapi.CreateSession(rig.pool, rig.sessions, rig.turns, rig.registry))
		r.Get("/{sessionID}", httpapi.GetSession(rig.sessions))
		r.Get("/{sessionID}/events", httpapi.ListEvents(rig.sessions, rig.events))
		r.Get("/{sessionID}/artifacts", httpapi.ListArtifacts(rig.sessions, rig.artifacts))
		r.Post("/{sessionID}/ws-token", httpapi.MintWSToken(rig.sessions, rig.wsTokens, platform.DefaultTimeouts()))
	})
	// scm-credentials is deliberately mounted OUTSIDE /api/sessions and
	// outside auth.Middleware entirely -- see scmcredentials.go's own doc
	// comment.
	router.Post("/sessions/{sessionID}/scm-credentials",
		httpapi.ScmCredentials(rig.sessions, rig.sandboxes, rig.identities, rig.tokenEncryptionKey, platform.DefaultTimeouts()))

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
		{name: "MintWSToken", method: http.MethodPost, path: "/api/sessions/" + session.ID.String() + "/ws-token"},
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
