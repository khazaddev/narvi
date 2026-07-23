//go:build integration

// Integration tests for the GitHub webhook ingress adapter (Step 32,
// "GitHub ingress", §8.2), against a real Postgres instance -- gated
// behind the "integration" build tag, matching internal/adapters/inbound/
// httpapi's own testcontainers-Postgres-plus-embedded-migrations
// convention exactly (each DB-touching package builds its own copy of
// this small helper rather than sharing one across package boundaries).
// Run via `make test-integration`.
package github_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	githubingress "github.com/khazaddev/narvi/internal/adapters/inbound/github"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

const testWebhookSecret = "test-github-webhook-secret"
const testBotHandleIntegration = "narvi-bot"

// newTestPool spins up a throwaway Postgres container, runs every
// embedded migration up, and returns a ready *pgxpool.Pool. t.Cleanup
// tears down both the pool and the container. A duplicate of httpapi's
// own newTestPool -- necessarily so, since it lives in a different
// package (see that file's own doc comment for this precedent).
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
// mounting the real POST /webhooks/github handler exactly as cmd/
// control-plane/main.go does.
type testRig struct {
	pool   *pgxpool.Pool
	turns  *narvipg.TurnStore
	server *httptest.Server
}

func newTestRig(t *testing.T) testRig {
	t.Helper()
	ctx := context.Background()
	pool := newTestPool(t)

	// nil provider/commander: these tests only assert that a session/turn
	// is genuinely CREATED and EnsureDispatched is triggered, not what the
	// full spawn/dispatch decision tree then does with it --
	// internal/app/sessionactor's own dispatch_integration_test.go covers
	// that decision tree exhaustively. Mirrors httpapi's own testRig
	// precedent exactly.
	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	rig := testRig{
		pool:  pool,
		turns: narvipg.NewTurnStore(pool),
	}

	coalescer := &githubingress.SessionCoalescer{
		Pool:         pool,
		PRSessions:   narvipg.NewGitHubPRSessionStore(pool),
		Sessions:     narvipg.NewSessionStore(pool),
		Turns:        rig.turns,
		Environments: narvipg.NewEnvironmentStore(pool),
		Registry:     registry,
	}
	deliveries := narvipg.NewWebhookDeliveryStore(pool)

	handler := githubingress.NewHandler(coalescer, deliveries, githubingress.Config{
		WebhookSecret: testWebhookSecret,
		BotHandle:     testBotHandleIntegration,
	})

	mux := http.NewServeMux()
	mux.Handle("/webhooks/github", handler)
	rig.server = httptest.NewServer(mux)
	t.Cleanup(rig.server.Close)

	return rig
}

// sign mirrors GitHub's own "X-Hub-Signature-256: sha256=<hex>" scheme.
func sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// issueCommentBody builds a synthetic, real-shaped "issue_comment"
// webhook payload mentioning testBotHandleIntegration on repo/prNumber,
// with a unique comment body derived from label so concurrent/duplicate
// requests in the same test are distinguishable in turns.prompt.
func issueCommentBody(repoFullName, repoName, cloneURL string, prNumber int, label string) []byte {
	body, err := json.Marshal(map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       prNumber,
			"pull_request": map[string]any{"url": fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repoFullName, prNumber)},
		},
		"comment": map[string]any{
			"body": fmt.Sprintf("@%s please review (%s)", testBotHandleIntegration, label),
		},
		"repository": map[string]any{
			"full_name": repoFullName,
			"name":      repoName,
			"clone_url": cloneURL,
		},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func postWebhook(t *testing.T, rig testRig, body []byte, deliveryID string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, rig.server.URL+"/webhooks/github", strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("X-Hub-Signature-256", sign([]byte(testWebhookSecret), body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", deliveryID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// TestGitHubIntegration_FullHTTPFlow_CreatesSessionAndTurn proves the
// full stack end to end: a synthetic, correctly-signed GitHub
// "issue_comment" payload mentioning the bot, POSTed to the real handler,
// results in a real session + turn in Postgres.
func TestGitHubIntegration_FullHTTPFlow_CreatesSessionAndTurn(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)

	body := issueCommentBody("acme/widgets", "widgets", "https://github.com/acme/widgets.git", 101, "full-flow")

	status := postWebhook(t, rig, body, "delivery-full-flow-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	var sessionID string
	var spawnSource, repos string
	if err := rig.pool.QueryRow(ctx,
		`SELECT id, spawn_source, repos::text FROM sessions WHERE spawn_source = 'github' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&sessionID, &spawnSource, &repos); err != nil {
		t.Fatalf("query session: %v", err)
	}
	if spawnSource != "github" {
		t.Errorf("spawn_source = %q, want %q", spawnSource, "github")
	}
	if !strings.Contains(repos, "acme/widgets.git") {
		t.Errorf("repos = %q, want it to reference the mentioned repo's clone url", repos)
	}

	var turnCount int
	var prompt string
	if err := rig.pool.QueryRow(ctx,
		`SELECT count(*), max(prompt) FROM turns WHERE session_id = $1`, sessionID,
	).Scan(&turnCount, &prompt); err != nil {
		t.Fatalf("query turns: %v", err)
	}
	if turnCount != 1 {
		t.Fatalf("turn count = %d, want 1", turnCount)
	}
	if !strings.Contains(prompt, "full-flow") {
		t.Errorf("turn prompt = %q, want it to contain the mention comment's own body", prompt)
	}

	var createdByNull bool
	if err := rig.pool.QueryRow(ctx, `SELECT created_by IS NULL FROM sessions WHERE id = $1`, sessionID).Scan(&createdByNull); err != nil {
		t.Fatalf("query created_by: %v", err)
	}
	if !createdByNull {
		t.Error("sessions.created_by is NOT NULL, want NULL for a bot-created session")
	}
}

// TestGitHubIntegration_DedupeSameDeliveryNotDoubleProcessed proves a
// redelivered X-GitHub-Delivery (GitHub retries on timeout/5xx) is
// detected and NOT processed a second time -- exactly one session, even
// though the same signed payload is POSTed twice with the identical
// delivery id.
func TestGitHubIntegration_DedupeSameDeliveryNotDoubleProcessed(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)

	body := issueCommentBody("acme/dedupe-repo", "dedupe-repo", "https://github.com/acme/dedupe-repo.git", 202, "dedupe")
	const deliveryID = "delivery-dedupe-1"

	first := postWebhook(t, rig, body, deliveryID)
	if first != http.StatusOK {
		t.Fatalf("first delivery status = %d, want %d", first, http.StatusOK)
	}
	second := postWebhook(t, rig, body, deliveryID)
	if second != http.StatusOK {
		t.Fatalf("redelivered status = %d, want %d (acknowledged, not reprocessed)", second, http.StatusOK)
	}

	var sessionCount int
	if err := rig.pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE spawn_source = 'github'`,
	).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Errorf("session count = %d, want exactly 1 (redelivery must not double-process)", sessionCount)
	}

	var deliveryRowCount int
	if err := rig.pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = 'github' AND delivery_id = $1`, deliveryID,
	).Scan(&deliveryRowCount); err != nil {
		t.Fatalf("count webhook_deliveries rows: %v", err)
	}
	if deliveryRowCount != 1 {
		t.Errorf("webhook_deliveries row count = %d, want exactly 1", deliveryRowCount)
	}
}

// TestGitHubIntegration_FailedFirstAttemptReleasesClaimForRedelivery
// proves the delivery-dedupe claim does NOT permanently poison a
// (provider, delivery_id) when the first attempt fails AFTER the claim
// succeeds but BEFORE the mention is actually processed (payload parse
// error here; a transient DB error downstream of the claim is the same
// code path) -- GitHub always redelivers on a non-2xx response, so the
// SAME delivery id must be reprocessable, not silently swallowed as an
// "already claimed" duplicate forever.
func TestGitHubIntegration_FailedFirstAttemptReleasesClaimForRedelivery(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)

	const deliveryID = "delivery-retry-after-failure-1"

	// First attempt: correctly signed, but the body is not valid JSON --
	// parseMention fails after the claim already succeeded.
	malformedBody := []byte("not valid json")
	first := postWebhook(t, rig, malformedBody, deliveryID)
	if first != http.StatusBadRequest {
		t.Fatalf("first (malformed) delivery status = %d, want %d", first, http.StatusBadRequest)
	}

	// The claim row must have been released by the failure path, not left
	// behind poisoning this delivery id.
	var deliveryRowCountAfterFailure int
	if err := rig.pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = 'github' AND delivery_id = $1`, deliveryID,
	).Scan(&deliveryRowCountAfterFailure); err != nil {
		t.Fatalf("count webhook_deliveries rows after failure: %v", err)
	}
	if deliveryRowCountAfterFailure != 0 {
		t.Fatalf("webhook_deliveries row count after failed attempt = %d, want 0 (claim must be released on failure)", deliveryRowCountAfterFailure)
	}

	// Redelivery: GitHub's real retry behavior on a non-2xx response --
	// SAME delivery id, this time a genuine, well-formed mention payload.
	// It must be processed, not skipped as an already-claimed duplicate.
	validBody := issueCommentBody("acme/retry-repo", "retry-repo", "https://github.com/acme/retry-repo.git", 404, "retry-after-failure")
	second := postWebhook(t, rig, validBody, deliveryID)
	if second != http.StatusOK {
		t.Fatalf("redelivered (valid) status = %d, want %d", second, http.StatusOK)
	}

	var sessionCount int
	if err := rig.pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE spawn_source = 'github'`,
	).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Errorf("session count = %d, want exactly 1 (the redelivered valid payload must actually be processed)", sessionCount)
	}
}

// TestGitHubIntegration_ConcurrentMentionsCoalesceToOneSessionManyTurns
// is this Step's own headline concurrency proof: N concurrent, distinctly
// -delivered @mentions on the SAME PR must result in exactly ONE session
// and N turns -- never N sessions. Driven with real concurrent HTTP
// requests against the real handler/real Postgres, matching Step 31's
// own ClaimWebhookDelivery concurrency-test style (real goroutines, not
// sequential calls).
func TestGitHubIntegration_ConcurrentMentionsCoalesceToOneSessionManyTurns(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)

	const n = 8
	const repoFullName = "acme/concurrent-repo"
	const prNumber = 303

	start := make(chan struct{})
	statuses := make([]int, n)

	var g errgroup.Group
	for i := 0; i < n; i++ {
		idx := i
		g.Go(func() error {
			<-start
			body := issueCommentBody(repoFullName, "concurrent-repo", "https://github.com/acme/concurrent-repo.git", prNumber, fmt.Sprintf("mention-%d", idx))
			statuses[idx] = postWebhook(t, rig, body, fmt.Sprintf("delivery-concurrent-%d", idx))
			return nil
		})
	}
	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent webhook posts: %v", err)
	}

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Errorf("statuses[%d] = %d, want %d", i, status, http.StatusOK)
		}
	}

	var sessionCount int
	if err := rig.pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE spawn_source = 'github'`,
	).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("session count = %d, want exactly 1 (all %d concurrent mentions on the SAME PR must coalesce)", sessionCount, n)
	}

	var claimSessionID string
	if err := rig.pool.QueryRow(ctx,
		`SELECT session_id::text FROM github_pr_sessions WHERE repo_full_name = $1 AND pr_number = $2`,
		repoFullName, prNumber,
	).Scan(&claimSessionID); err != nil {
		t.Fatalf("query claim row: %v", err)
	}

	var turnCount int
	if err := rig.pool.QueryRow(ctx,
		`SELECT count(*) FROM turns WHERE session_id = $1`, claimSessionID,
	).Scan(&turnCount); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turnCount != n {
		t.Errorf("turn count = %d, want exactly %d (one turn per concurrent mention, all on the SAME session)", turnCount, n)
	}
}
