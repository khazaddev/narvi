//go:build integration

// Integration tests for internal/adapters/inbound/httpapi's 5 REST
// endpoints (§6.3), against a real Postgres instance -- gated behind the
// "integration" build tag, matching internal/adapters/inbound/wshub's own
// testcontainers-Postgres-plus-embedded-migrations convention exactly
// (each DB-touching package builds its own copy of this small helper
// rather than sharing one across package boundaries, per that package's
// own precedent). Run via `make test-integration`.
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
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
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
// mounting all 5 REST routes exactly as cmd/control-plane/main.go does.
type testRig struct {
	pool      *pgxpool.Pool
	sessions  *narvipg.SessionStore
	events    *narvipg.EventStore
	artifacts *narvipg.ArtifactStore
	wsTokens  *narvipg.WSTokenStore
	server    *httptest.Server
}

func newTestRig(t *testing.T) testRig {
	t.Helper()
	pool := newTestPool(t)

	rig := testRig{
		pool:      pool,
		sessions:  narvipg.NewSessionStore(pool),
		events:    narvipg.NewEventStore(pool),
		artifacts: narvipg.NewArtifactStore(pool),
		wsTokens:  narvipg.NewWSTokenStore(pool),
	}

	router := chi.NewRouter()
	router.Post("/api/sessions", httpapi.CreateSession(rig.sessions))
	router.Get("/api/sessions/{sessionID}", httpapi.GetSession(rig.sessions))
	router.Get("/api/sessions/{sessionID}/events", httpapi.ListEvents(rig.sessions, rig.events))
	router.Get("/api/sessions/{sessionID}/artifacts", httpapi.ListArtifacts(rig.sessions, rig.artifacts))
	router.Post("/api/sessions/{sessionID}/ws-token", httpapi.MintWSToken(rig.sessions, rig.wsTokens, platform.DefaultTimeouts()))

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

// doJSON issues method against r.server.URL+path with an optional body,
// decoding the response body into v (if non-nil) and returning the
// status code.
func (r testRig) doJSON(t *testing.T, method, path string, body []byte, v any) int {
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

// --- CreateSession ---

func TestCreateSession_HappyPath(t *testing.T) {
	rig := newTestRig(t)

	body := []byte(`{
		"spawnSource": "web",
		"title": "my session",
		"prompt": "do the thing",
		"repos": [{"name": "narvi", "url": "https://github.com/khazaddev/narvi", "branch": null}],
		"modelId": null,
		"planMode": false
	}`)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, &got)
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
	if got.CreatedBy != nil {
		t.Errorf("CreatedBy = %v, want nil (no auth mechanism exists yet)", *got.CreatedBy)
	}
	if got.Title == nil || *got.Title != "my session" {
		t.Errorf("Title = %v, want \"my session\"", got.Title)
	}
	if got.Archived {
		t.Error("Archived = true, want false for a freshly created session")
	}
}

func TestCreateSession_EmptyRepos(t *testing.T) {
	rig := newTestRig(t)

	body := []byte(`{"spawnSource":"web","title":null,"prompt":null,"repos":[],"modelId":null,"planMode":false}`)
	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestCreateSession_MalformedBody(t *testing.T) {
	rig := newTestRig(t)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions", []byte("not json"), nil)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestCreateSession_OversizedBody(t *testing.T) {
	rig := newTestRig(t)

	// A single "repos[0].name" value well beyond the 1 MiB request-body
	// cap -- json.Decoder will hit http.MaxBytesReader's own limit before
	// ever producing a complete value.
	huge := strings.Repeat("a", 2<<20) // 2 MiB
	body := []byte(fmt.Sprintf(`{"spawnSource":"web","title":null,"prompt":null,"repos":[{"name":%q,"url":"https://example.com","branch":null}],"modelId":null,"planMode":false}`, huge))

	status := rig.doJSON(t, http.MethodPost, "/api/sessions", body, nil)
	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", status, http.StatusRequestEntityTooLarge)
	}
}

// --- GetSession ---

func TestGetSession_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)

	var got restdtos.Session
	status := rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String(), nil, &got)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Id != session.ID.String() {
		t.Errorf("Id = %q, want %q", got.Id, session.ID.String())
	}
}

func TestGetSession_MalformedID(t *testing.T) {
	rig := newTestRig(t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/not-a-uuid", nil, nil)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

func TestGetSession_NotFound(t *testing.T) {
	rig := newTestRig(t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/11111111-1111-1111-1111-111111111111", nil, nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// --- ListEvents ---

func TestListEvents_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)

	const total = 3
	for i := 0; i < total; i++ {
		if _, err := rig.events.Create(ctx, sqlcgen.CreateEventParams{
			SessionID: session.ID,
			Type:      "token",
			Payload:   []byte(fmt.Sprintf(`{"n":%d}`, i)),
		}); err != nil {
			t.Fatalf("create event %d: %v", i, err)
		}
	}

	var got restdtos.EventsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String()+"/events?limit=2", nil, &got)
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
	status = rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String()+"/events?cursor="+*got.NextCursor+"&limit=2", nil, &page2)
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

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/11111111-1111-1111-1111-111111111111/events", nil, nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestListEvents_MalformedSessionID(t *testing.T) {
	rig := newTestRig(t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/not-a-uuid/events", nil, nil)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// --- ListArtifacts ---

func TestListArtifacts_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)

	if _, err := rig.pool.Exec(ctx,
		`INSERT INTO artifacts (session_id, type, url) VALUES ($1, 'pr', 'https://example.com/pr/1')`,
		session.ID,
	); err != nil {
		t.Fatalf("insert test artifact: %v", err)
	}

	var got restdtos.ArtifactsResponse
	status := rig.doJSON(t, http.MethodGet, "/api/sessions/"+session.ID.String()+"/artifacts", nil, &got)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Artifacts) != 1 {
		t.Fatalf("len(Artifacts) = %d, want 1", len(got.Artifacts))
	}
}

func TestListArtifacts_SessionNotFound(t *testing.T) {
	rig := newTestRig(t)

	status := rig.doJSON(t, http.MethodGet, "/api/sessions/11111111-1111-1111-1111-111111111111/artifacts", nil, nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// --- MintWSToken ---

func TestMintWSToken_HappyPath(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSession(ctx, t)

	before := time.Now()
	var got restdtos.WSTokenResponse
	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/ws-token", []byte{}, &got)
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

	// The stored row holds only the HASH, never the plaintext, and is
	// scoped to this session with a NULL user_id (no auth mechanism
	// exists yet).
	stored, err := rig.wsTokens.GetByHash(ctx, platform.HashToken(got.Token))
	if err != nil {
		t.Fatalf("GetByHash: %v", err)
	}
	if stored.SessionID != session.ID {
		t.Errorf("stored.SessionID = %v, want %v", stored.SessionID, session.ID)
	}
	if stored.UserID.Valid {
		t.Error("stored.UserID.Valid = true, want false (NULL)")
	}
}

func TestMintWSToken_SessionNotFound(t *testing.T) {
	rig := newTestRig(t)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/11111111-1111-1111-1111-111111111111/ws-token", []byte{}, nil)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestMintWSToken_MalformedSessionID(t *testing.T) {
	rig := newTestRig(t)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/not-a-uuid/ws-token", []byte{}, nil)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}
