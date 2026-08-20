//go:build integration

// Integration tests for ListSessions (the session-list sidebar, §12.2
// item 1) against a REAL Postgres instance -- gated behind the
// "integration" build tag. A self-contained router, mirroring
// decisioninbox_integration_test.go's own precedent (this file's own top
// doc comment there): this handler needs nothing from the package's
// large shared testRig beyond the same Postgres pool + auth.Middleware.
package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/auth"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

type listSessionsTestRig struct {
	pool         *pgxpool.Pool
	users        *narvipg.UserStore
	userSessions *narvipg.UserSessionStore
	sessions     *narvipg.SessionStore
	sandboxes    *narvipg.SandboxStore
	participants *narvipg.ParticipantStore
	server       *httptest.Server
}

func newListSessionsTestRig(t *testing.T) *listSessionsTestRig {
	t.Helper()
	pool := newTestPool(t)

	users := narvipg.NewUserStore(pool)
	userSessions := narvipg.NewUserSessionStore(pool)
	sessions := narvipg.NewSessionStore(pool)

	router := chi.NewRouter()
	router.Route("/api/sessions", func(r chi.Router) {
		r.Use(auth.Middleware(userSessions, users))
		r.Get("/", httpapi.ListSessions(sessions))
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &listSessionsTestRig{
		pool: pool, users: users, userSessions: userSessions, sessions: sessions,
		sandboxes: narvipg.NewSandboxStore(pool), participants: narvipg.NewParticipantStore(pool), server: server,
	}
}

// createAuthenticatedUser mirrors decisionInboxTestRig's own identical
// helper (decisioninbox_integration_test.go) -- duplicated for the same
// reason that file duplicates it from testRig.
func (rig *listSessionsTestRig) createAuthenticatedUser(ctx context.Context, t *testing.T) (sqlcgen.User, string) {
	t.Helper()
	unique := time.Now().UnixNano()
	user, err := rig.users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("listsessions-test-%d@example.com", unique), DisplayName: "Test User", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token, err := platform.GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if _, err := rig.userSessions.Create(ctx, sqlcgen.CreateUserSessionParams{
		UserID:    user.ID,
		TokenHash: platform.HashToken(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(platform.DefaultTimeouts().UserSessionTTL), Valid: true},
	}); err != nil {
		t.Fatalf("create user session: %v", err)
	}
	return user, token
}

func (rig *listSessionsTestRig) createSession(ctx context.Context, t *testing.T, createdBy pgtype.UUID, title string) sqlcgen.Session {
	t.Helper()
	titlePtr := &title
	s, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{
		Title: titlePtr, SpawnSource: sqlcgen.SessionSpawnSourceWeb, CreatedBy: createdBy,
		Repos: []byte(`[{"name":"acme/example","url":"https://example.invalid/acme/example.git","branch":null}]`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return s
}

func (rig *listSessionsTestRig) doJSON(t *testing.T, path string, v any, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, rig.server.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
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
			t.Fatalf("decode response: %v", err)
		}
	}
	return resp.StatusCode
}

func TestListSessions_RequiresAuth(t *testing.T) {
	rig := newListSessionsTestRig(t)

	status := rig.doJSON(t, "/api/sessions", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestListSessions_MineDefaultsToCreatedOrJoined(t *testing.T) {
	rig := newListSessionsTestRig(t)
	ctx := context.Background()

	me, meToken := rig.createAuthenticatedUser(ctx, t)
	other, _ := rig.createAuthenticatedUser(ctx, t)

	mine := rig.createSession(ctx, t, me.ID, "mine, created")
	joined := rig.createSession(ctx, t, other.ID, "not mine, but joined")
	if _, err := rig.participants.Create(ctx, joined.ID, me.ID); err != nil {
		t.Fatalf("create participant: %v", err)
	}
	rig.createSession(ctx, t, other.ID, "neither created nor joined")

	var resp restdtos.ListSessionsResponse
	status := rig.doJSON(t, "/api/sessions", &resp, meToken)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	got := map[string]bool{}
	for _, s := range resp.Sessions {
		got[s.Id] = true
	}
	if !got[mine.ID.String()] {
		t.Errorf("expected created session %s in response, got %+v", mine.ID.String(), resp.Sessions)
	}
	if !got[joined.ID.String()] {
		t.Errorf("expected joined session %s in response, got %+v", joined.ID.String(), resp.Sessions)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("len(sessions) = %d, want 2 (mine defaults to created-or-joined only)", len(resp.Sessions))
	}
}

func TestListSessions_AllReturnsEverySessionRegardlessOfOwnership(t *testing.T) {
	rig := newListSessionsTestRig(t)
	ctx := context.Background()

	me, meToken := rig.createAuthenticatedUser(ctx, t)
	other, _ := rig.createAuthenticatedUser(ctx, t)

	rig.createSession(ctx, t, me.ID, "mine")
	rig.createSession(ctx, t, other.ID, "not mine, not joined")

	var resp restdtos.ListSessionsResponse
	status := rig.doJSON(t, "/api/sessions?filter=all", &resp, meToken)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(resp.Sessions) < 2 {
		t.Errorf("len(sessions) = %d, want at least 2 (filter=all is system-wide)", len(resp.Sessions))
	}
}

func TestListSessions_RejectsUnknownFilter(t *testing.T) {
	rig := newListSessionsTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, "/api/sessions?filter=bogus", nil, token)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestListSessions_ReposAndSandboxStatus proves the two fields this Step
// adds to restdtos.Session round-trip honestly: repos matches exactly
// what was persisted at creation time, and sandboxStatus is null until a
// sandbox row exists, then reflects it -- never a fabricated default (see
// Session.sandboxStatus's own schema doc comment,
// contracts/rest/v1/dtos.schema.json).
func TestListSessions_ReposAndSandboxStatus(t *testing.T) {
	rig := newListSessionsTestRig(t)
	ctx := context.Background()
	me, token := rig.createAuthenticatedUser(ctx, t)

	s := rig.createSession(ctx, t, me.ID, "repos and sandbox status")

	var resp restdtos.ListSessionsResponse
	status := rig.doJSON(t, "/api/sessions", &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	row := findSession(t, resp.Sessions, s.ID.String())
	if len(row.Repos) != 1 || row.Repos[0].Name != "acme/example" {
		t.Fatalf("repos = %+v, want one acme/example entry", row.Repos)
	}
	if row.SandboxStatus != nil {
		t.Fatalf("sandboxStatus = %v, want nil before any sandbox row exists", row.SandboxStatus)
	}

	if _, err := rig.sandboxes.Create(ctx, s.ID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := rig.sandboxes.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: s.ID, Status: sqlcgen.SandboxStatusBooting,
	}); err != nil {
		t.Fatalf("update sandbox status: %v", err)
	}

	resp = restdtos.ListSessionsResponse{}
	status = rig.doJSON(t, "/api/sessions", &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	row = findSession(t, resp.Sessions, s.ID.String())
	if row.SandboxStatus == nil || row.SandboxStatus.Value != "booting" {
		t.Fatalf("sandboxStatus = %+v, want \"booting\"", row.SandboxStatus)
	}
}

func findSession(t *testing.T, sessions []restdtos.Session, id string) restdtos.Session {
	t.Helper()
	for _, s := range sessions {
		if s.Id == id {
			return s
		}
	}
	t.Fatalf("session %s not found in %+v", id, sessions)
	return restdtos.Session{}
}
