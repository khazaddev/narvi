//go:build integration

// Full HTTP-level integration tests for POST /webhooks/automations/
// {automationID} ("automations: triggers & extras", §8.4) --
// mirrors internal/adapters/inbound/identitylink's own single-file,
// per-test testcontainers-Postgres convention exactly (this package's own
// integration suite is small enough that a shared, cross-test container --
// the pattern internal/app/automation and internal/adapters/inbound/
// {github,linear,httpapi} each use for their own much larger suites --
// would be needless complexity here).
package automationwebhook_test

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/adapters/inbound/automationwebhook"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	domainautomation "github.com/khazaddev/narvi/internal/domain/automation"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool mirrors internal/adapters/inbound/identitylink's own
// newTestPool (handler_integration_test.go) exactly -- one throwaway
// testcontainers Postgres per test, embedded migrations applied, no
// shared-container optimization (this package's own suite is small).
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	const containerStartWatchdog = 2*time.Minute + 15*time.Second
	type containerStartResult struct {
		container *tcpostgres.PostgresContainer
		err       error
	}
	startCh := make(chan containerStartResult, 1)
	var startGroup errgroup.Group
	startGroup.Go(func() error {
		container, err := tcpostgres.Run(startCtx, "postgres:17-alpine",
			tcpostgres.WithDatabase("narvi_test"),
			tcpostgres.WithUsername("narvi"),
			tcpostgres.WithPassword("narvi"),
			tcpostgres.BasicWaitStrategies(),
		)
		startCh <- containerStartResult{container: container, err: err}
		return nil
	})

	var container *tcpostgres.PostgresContainer
	var err error
	select {
	case res := <-startCh:
		container, err = res.container, res.err
		if err != nil {
			t.Fatalf("start postgres container: %v", err)
		}
	case <-time.After(containerStartWatchdog):
		t.Fatalf("start postgres container: tcpostgres.Run did not return within %s", containerStartWatchdog)
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
	pool        *pgxpool.Pool
	automations *narvipg.AutomationStore
	invocations *narvipg.AutomationInvocationStore
	server      *httptest.Server
}

func newTestRig(t *testing.T) *testRig {
	t.Helper()
	pool := newTestPool(t)

	automations := narvipg.NewAutomationStore(pool)
	invocations := narvipg.NewAutomationInvocationStore(pool)

	router := chi.NewRouter()
	router.Post("/webhooks/automations/{automationID}", automationwebhook.NewHandler(automations, invocations))

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &testRig{pool: pool, automations: automations, invocations: invocations, server: server}
}

// createWebhookAutomation inserts an automation with TriggerTypeWebhook
// and one target, returning the row and the PLAINTEXT bearer token
// (mirrors the create flow httpapi.CreateAutomation drives, minus the
// HTTP layer -- this package's own tests only need the resulting Postgres
// state, not that endpoint's own request/response wire format).
func (r *testRig) createWebhookAutomation(t *testing.T, status sqlcgen.AutomationStatus) (sqlcgen.Automation, string) {
	t.Helper()
	ctx := context.Background()

	targets := []domainautomation.Target{{Name: "widgets", URL: "https://github.com/acme/widgets"}}
	reposJSON, err := json.Marshal(targets)
	if err != nil {
		t.Fatalf("marshal repos: %v", err)
	}

	token, err := platform.GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	hash := platform.HashToken(token)

	row, err := r.automations.Create(ctx, sqlcgen.CreateAutomationParams{
		Name: "webhook automation", Repos: reposJSON, CreatedBy: pgtype.UUID{},
		TriggerType: sqlcgen.AutomationTriggerTypeWebhook, TriggerConfig: []byte("{}"), EnvVars: []byte("[]"),
		WebhookTokenHash: &hash,
	})
	if err != nil {
		t.Fatalf("create webhook automation: %v", err)
	}

	if status == sqlcgen.AutomationStatusPaused {
		if _, err := r.pool.Exec(ctx, "UPDATE automations SET status = 'paused' WHERE id = $1", row.ID); err != nil {
			t.Fatalf("pause automation: %v", err)
		}
		row.Status = sqlcgen.AutomationStatusPaused
	}

	return row, token
}

func (r *testRig) countInvocationsForAutomation(t *testing.T, automationID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := r.pool.QueryRow(context.Background(), "SELECT count(*) FROM automation_invocations WHERE automation_id = $1", automationID).Scan(&count); err != nil {
		t.Fatalf("count invocations: %v", err)
	}
	return count
}

func TestNewHandler_MissingBearerToken_Returns401(t *testing.T) {
	rig := newTestRig(t)
	auto, _ := rig.createWebhookAutomation(t, sqlcgen.AutomationStatusActive)

	req, err := http.NewRequest(http.MethodPost, rig.server.URL+"/webhooks/automations/"+auto.ID.String(), nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if got := rig.countInvocationsForAutomation(t, auto.ID); got != 0 {
		t.Fatalf("invocations = %d, want 0 for an unauthenticated call", got)
	}
}

func TestNewHandler_WrongToken_Returns401(t *testing.T) {
	rig := newTestRig(t)
	auto, _ := rig.createWebhookAutomation(t, sqlcgen.AutomationStatusActive)

	status := rig.doPost(t, auto.ID.String(), "not-the-real-token")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
	if got := rig.countInvocationsForAutomation(t, auto.ID); got != 0 {
		t.Fatalf("invocations = %d, want 0 for a wrong token", got)
	}
}

func TestNewHandler_MalformedAutomationID_Returns400(t *testing.T) {
	rig := newTestRig(t)
	_, token := rig.createWebhookAutomation(t, sqlcgen.AutomationStatusActive)

	status := rig.doPost(t, "not-a-uuid", token)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestNewHandler_TokenBelongsToADifferentAutomation_Returns401 proves the
// path's own {automationID} must match the automation the presented token
// actually hashes to -- a real token for automation A, presented against
// automation B's own URL, is rejected exactly like a wrong token.
func TestNewHandler_TokenBelongsToADifferentAutomation_Returns401(t *testing.T) {
	rig := newTestRig(t)
	autoA, tokenA := rig.createWebhookAutomation(t, sqlcgen.AutomationStatusActive)
	autoB, _ := rig.createWebhookAutomation(t, sqlcgen.AutomationStatusActive)
	_ = autoA

	status := rig.doPost(t, autoB.ID.String(), tokenA)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
	if got := rig.countInvocationsForAutomation(t, autoB.ID); got != 0 {
		t.Fatalf("invocations for B = %d, want 0", got)
	}
	if got := rig.countInvocationsForAutomation(t, autoA.ID); got != 0 {
		t.Fatalf("invocations for A = %d, want 0 (A's own token was presented against B's own URL, never A's)", got)
	}
}

func TestNewHandler_PausedAutomation_Returns409(t *testing.T) {
	rig := newTestRig(t)
	auto, token := rig.createWebhookAutomation(t, sqlcgen.AutomationStatusPaused)

	status := rig.doPost(t, auto.ID.String(), token)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d", status, http.StatusConflict)
	}
	if got := rig.countInvocationsForAutomation(t, auto.ID); got != 0 {
		t.Fatalf("invocations = %d, want 0 for a paused automation", got)
	}
}

// TestNewHandler_NonWebhookTriggerType_Returns401 exercises the defensive
// "matched automation's own trigger_type is not 'webhook'" check --
// unreachable via the normal create flow (a non-webhook automation never
// gets a webhook_token_hash at all), so this drives it directly via raw
// SQL, the same "defensive, should-be-unreachable path" precedent this
// package's own NewHandler doc comment names.
func TestNewHandler_NonWebhookTriggerType_Returns401(t *testing.T) {
	rig := newTestRig(t)
	auto, token := rig.createWebhookAutomation(t, sqlcgen.AutomationStatusActive)

	if _, err := rig.pool.Exec(context.Background(), "UPDATE automations SET trigger_type = 'manual' WHERE id = $1", auto.ID); err != nil {
		t.Fatalf("force trigger_type to manual: %v", err)
	}

	status := rig.doPost(t, auto.ID.String(), token)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// TestNewHandler_Success_CreatesInvocationTargetingCurrentRepos is the
// happy path: a correctly authenticated call creates exactly one new
// automation_invocations row, snapshotting the automation's own CURRENT
// repos as targets.
func TestNewHandler_Success_CreatesInvocationTargetingCurrentRepos(t *testing.T) {
	rig := newTestRig(t)
	auto, token := rig.createWebhookAutomation(t, sqlcgen.AutomationStatusActive)

	status := rig.doPost(t, auto.ID.String(), token)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", status, http.StatusAccepted)
	}
	if got := rig.countInvocationsForAutomation(t, auto.ID); got != 1 {
		t.Fatalf("invocations = %d, want exactly 1", got)
	}

	var targetsJSON []byte
	if err := rig.pool.QueryRow(context.Background(), "SELECT targets FROM automation_invocations WHERE automation_id = $1", auto.ID).Scan(&targetsJSON); err != nil {
		t.Fatalf("query invocation targets: %v", err)
	}
	targets, err := unmarshalTargetsForTest(targetsJSON)
	if err != nil {
		t.Fatalf("unmarshal targets: %v", err)
	}
	if len(targets) != 1 || targets[0].Name != "widgets" {
		t.Fatalf("targets = %+v, want the automation's own single 'widgets' repo", targets)
	}

	// A second call fires a SECOND, independent invocation -- this
	// endpoint's own "condition" is authentication, evaluated fresh every
	// call, never a one-shot latch.
	status = rig.doPost(t, auto.ID.String(), token)
	if status != http.StatusAccepted {
		t.Fatalf("second call status = %d, want %d", status, http.StatusAccepted)
	}
	if got := rig.countInvocationsForAutomation(t, auto.ID); got != 2 {
		t.Fatalf("invocations after a second call = %d, want 2", got)
	}
}

// unmarshalTargetsForTest is a tiny, local, test-only
// decode of the SAME {name,url,branch} wire shape internal/app/automation's
// own (exported) UnmarshalTargets uses -- this test file deliberately
// avoids importing internal/app/automation ONLY to keep this package's own
// test dependency graph minimal (production code, handler.go, already
// imports it for the real decode); duplicating this ~10-line shape here is
// cheap and avoids no actual constraint (unlike target.go's own MarshalTargets/
// UnmarshalTargets export, which exists because of a REAL import-cycle
// constraint, not a style preference).
func unmarshalTargetsForTest(raw []byte) ([]domainautomation.Target, error) {
	var wire []struct {
		Name   string  `json:"name"`
		URL    string  `json:"url"`
		Branch *string `json:"branch,omitempty"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}
	targets := make([]domainautomation.Target, len(wire))
	for i, w := range wire {
		t := domainautomation.Target{Name: w.Name, URL: w.URL}
		if w.Branch != nil {
			t.Branch = *w.Branch
		}
		targets[i] = t
	}
	return targets, nil
}

// doPost issues an authenticated (or not, if token == "") POST against
// automationID's own webhook endpoint and returns the status code.
func (r *testRig) doPost(t *testing.T, automationID, token string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/webhooks/automations/"+automationID, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}
