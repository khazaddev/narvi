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
	"strconv"
	"strings"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	githubingress "github.com/khazaddev/narvi/internal/adapters/inbound/github"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
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
	pool        *pgxpool.Pool
	turns       *narvipg.TurnStore
	plans       *narvipg.PlanStore
	users       *narvipg.UserStore
	identities  *narvipg.IdentityStore
	linkNotices *narvipg.GitHubActorLinkNoticeStore
	server      *httptest.Server
}

// newTestRig builds the default rig (no PullRequestResolver wired -- every
// pre-existing test in this file exercises the pre-H5-fix-compatible
// path). mutate (variadic so every EXISTING newTestRig(t) call site keeps
// compiling unchanged) lets a caller override githubingress.Config fields
// -- see this file's own H5 fix coverage below (TestGitHubIntegration_
// IssueCommentResolvesRealHeadBranch / ...APIFailureFallsBack).
func newTestRig(t *testing.T, mutate ...func(*githubingress.Config)) testRig {
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
		pool:        pool,
		turns:       narvipg.NewTurnStore(pool),
		plans:       narvipg.NewPlanStore(pool),
		users:       narvipg.NewUserStore(pool),
		identities:  narvipg.NewIdentityStore(pool),
		linkNotices: narvipg.NewGitHubActorLinkNoticeStore(pool),
	}

	coalescer := &githubingress.SessionCoalescer{
		Pool:         pool,
		PRSessions:   narvipg.NewGitHubPRSessionStore(pool),
		Sessions:     narvipg.NewSessionStore(pool),
		Turns:        rig.turns,
		Environments: narvipg.NewEnvironmentStore(pool),
		Registry:     registry,
		AuditLog:     narvipg.NewAuditLogStore(pool),
		// Plans (Step 37/38 follow-up fix, Finding 1): wired unconditionally
		// for every test in this file, mirroring cmd/control-plane/main.go's
		// own production wiring -- harmless for every EXISTING test here
		// (none of them ever seed a plan row, so ListSummariesForSession
		// always comes back empty and the awaiting-plan gate never
		// triggers); required for this file's own new awaiting-plan
		// coverage below.
		Plans: rig.plans,
		// Identities/Users/Participants (M14 audit-fix batch addition;
		// EVERY test in this file now needs a genuinely LINKED commenter --
		// batch fix/deny-unlinked-github-actors denies an unresolved one
		// outright, so the pre-existing "bot attribution, commenterID == 0"
		// shortcut this comment used to describe no longer produces a
		// created session/turn at all). rig.users/rig.identities are the
		// SAME instances threaded through here, never a second,
		// independently-constructed copy -- see createLinkedGitHubUser
		// below, this file's own shared fixture helper.
		Identities:   rig.identities,
		Users:        rig.users,
		Participants: narvipg.NewParticipantStore(pool),
	}
	deliveries := narvipg.NewWebhookDeliveryStore(pool)

	cfg := githubingress.Config{
		WebhookSecret: testWebhookSecret,
		BotHandle:     testBotHandleIntegration,
		// LinkNotices (batch fix/deny-unlinked-github-actors): wired by
		// DEFAULT for every test in this file, mirroring cmd/control-
		// plane/main.go's own unconditional production wiring -- harmless
		// for every pre-existing test (none of them ever exercise the
		// unlinked-actor deny path, so this store is simply never
		// touched); a mutate func below can still override it (e.g. to
		// nil) for a test that specifically wants to prove the nil-safe
		// "dedupe unavailable" fallback.
		LinkNotices: rig.linkNotices,
	}
	for _, m := range mutate {
		m(&cfg)
	}

	handler := githubingress.NewHandler(coalescer, deliveries, cfg)

	mux := http.NewServeMux()
	mux.Handle("/webhooks/github", handler)
	rig.server = httptest.NewServer(mux)
	t.Cleanup(rig.server.Close)

	return rig
}

// fakePullRequestResolver is a test-only githubingress.PullRequestResolver
// -- no real HTTP round trip to GitHub, exactly the point of that
// interface being narrow and locally defined in the github package.
type fakePullRequestResolver struct {
	pr  githubapi.PullRequest
	err error
}

func (f *fakePullRequestResolver) GetPullRequest(_ context.Context, _, _ string, _ int32, _ string) (githubapi.PullRequest, error) {
	return f.pr, f.err
}

// postedComment records one fakeCommentPoster.PostIssueComment call --
// Finding 1's own regression coverage below asserts against this shape.
type postedComment struct {
	owner, repo string
	prNumber    int
	token, body string
}

// fakeCommentPoster is a test-only githubingress.CommentPoster -- no real
// HTTP round trip to GitHub, exactly the point of that interface being
// narrow and locally defined in the github package (mirrors
// fakePullRequestResolver's own identical precedent immediately above).
type fakeCommentPoster struct {
	calls []postedComment
	err   error
}

func (f *fakeCommentPoster) PostIssueComment(_ context.Context, owner, repo string, prNumber int, token, body string) error {
	f.calls = append(f.calls, postedComment{owner: owner, repo: repo, prNumber: prNumber, token: token, body: body})
	return f.err
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

// createLinkedGitHubUser creates a Narvi user with role, and a matching
// "github" identities row for commenterID -- the direct (provider,
// external_id) link a real GitHub OAuth sign-in would have already
// produced (Step 20), which is exactly what resolveCommenterActor
// (identity.go) looks up. Batch fix/deny-unlinked-github-actors means
// EVERY test in this package now needs one of these for its own mention
// to be processed at all (an unresolved commenter's mention is now
// denied outright, not bot-attributed) -- a free function over the
// stores directly (rather than a method on this file's own testRig)
// so identity_integration_test.go's own distinct identityTestRig type can
// share it too, instead of hand-duplicating the identical insert twice.
func createLinkedGitHubUser(ctx context.Context, t *testing.T, users *narvipg.UserStore, identities *narvipg.IdentityStore, commenterID int64, role sqlcgen.UserRole) sqlcgen.User {
	t.Helper()

	user, err := users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("github-commenter-%d@example.com", commenterID),
		DisplayName:  "GitHub Commenter",
		Role:         role,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}

	email := user.PrimaryEmail
	if _, err := identities.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:        user.ID,
		Provider:      sqlcgen.IdentityProviderGithub,
		ExternalID:    strconv.FormatInt(commenterID, 10),
		Email:         &email,
		EmailVerified: true,
		LinkedVia:     sqlcgen.IdentityLinkedViaAutoEmail,
	}); err != nil {
		t.Fatalf("create fixture github identity: %v", err)
	}

	return user
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
// "issue_comment" payload mentioning the bot, from an already-LINKED
// commenter, POSTed to the real handler, results in a real session + turn
// in Postgres, attributed to that commenter (batch fix/deny-unlinked-
// github-actors: an UNLINKED commenter's mention is now denied outright --
// see TestGitHubIntegration_UnlinkedCommenter_DeniedOnUntrackedPR below
// for that path's own coverage instead).
func TestGitHubIntegration_FullHTTPFlow_CreatesSessionAndTurn(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)

	const commenterID = 80000101
	user := createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := issueCommentBodyWithCommenter("acme/widgets", "widgets", "https://github.com/acme/widgets.git", 101, "full-flow", commenterID, "full-flow-user")

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

	var createdByText string
	if err := rig.pool.QueryRow(ctx, `SELECT coalesce(created_by::text, '') FROM sessions WHERE id = $1`, sessionID).Scan(&createdByText); err != nil {
		t.Fatalf("query created_by: %v", err)
	}
	if createdByText != user.ID.String() {
		t.Errorf("created_by = %q, want %q (the linked commenter's own user id -- batch fix/deny-unlinked-github-actors means this path is never bot-attributed/NULL anymore)", createdByText, user.ID.String())
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

	const commenterID = 80000202
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := issueCommentBodyWithCommenter("acme/dedupe-repo", "dedupe-repo", "https://github.com/acme/dedupe-repo.git", 202, "dedupe", commenterID, "dedupe-user")
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
	// SAME delivery id, this time a genuine, well-formed mention payload
	// from an already-linked commenter (batch fix/deny-unlinked-github-
	// actors: an unlinked one would now be denied, which is a DIFFERENT
	// outcome this test isn't about). It must be processed, not skipped as
	// an already-claimed duplicate.
	const commenterID = 80000404
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)
	validBody := issueCommentBodyWithCommenter("acme/retry-repo", "retry-repo", "https://github.com/acme/retry-repo.git", 404, "retry-after-failure", commenterID, "retry-user")
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
	const commenterID = 80000303

	// A single already-linked maintainer mentions the bot N times
	// concurrently -- batch fix/deny-unlinked-github-actors means an
	// unlinked commenter would be denied on both the WINNER and REUSE
	// gates, which would collapse this test's own "N turns" assertion
	// below into "at most 1" (every loser after the first denied WINNER
	// gets its own fresh, independent, and ALSO-denied verdict, per
	// coalesce.go's own claim-row-safety doc comment) -- a different
	// property than the one this test exists to prove. Created BEFORE the
	// concurrent goroutines start, never inside one of them, so there is
	// no fixture-creation race alongside the real concurrency this test
	// means to exercise.
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	start := make(chan struct{})
	statuses := make([]int, n)

	var g errgroup.Group
	for i := 0; i < n; i++ {
		idx := i
		g.Go(func() error {
			<-start
			body := issueCommentBodyWithCommenter(repoFullName, "concurrent-repo", "https://github.com/acme/concurrent-repo.git", prNumber, fmt.Sprintf("mention-%d", idx), commenterID, "concurrent-user")
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

// TestGitHubIntegration_IssueCommentResolvesRealHeadBranch is the H5 audit
// fix's own headline end-to-end proof: a mention arriving via GitHub's
// "issue_comment" webhook event (the PR "Conversation" tab, the most
// common way the bot gets mentioned) on a PR whose real head branch
// differs from the base repo's own default branch resolves to that REAL
// head branch being cloned -- never left nil/falling back to the default,
// which is exactly what the pre-fix behavior did.
func TestGitHubIntegration_IssueCommentResolvesRealHeadBranch(t *testing.T) {
	ctx := context.Background()

	resolver := &fakePullRequestResolver{
		pr: githubapi.PullRequest{
			HeadRef:          "feature-real-branch",
			HeadRepoName:     "widgets",
			HeadRepoCloneURL: "https://github.com/acme/widgets.git",
		},
	}
	rig := newTestRig(t, func(cfg *githubingress.Config) {
		cfg.PullRequests = resolver
		cfg.BotToken = "test-bot-token"
		cfg.Timeouts = platform.DefaultTimeouts()
	})

	const commenterID = 80000555
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := issueCommentBodyWithCommenter("acme/widgets", "widgets", "https://github.com/acme/widgets.git", 555, "real-head-branch", commenterID, "head-branch-user")
	status := postWebhook(t, rig, body, "delivery-real-head-branch-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	var branch, repoName string
	if err := rig.pool.QueryRow(ctx,
		`SELECT repos->0->>'branch', repos->0->>'name' FROM sessions WHERE spawn_source = 'github' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&branch, &repoName); err != nil {
		t.Fatalf("query session repos: %v", err)
	}

	if branch != "feature-real-branch" {
		t.Errorf("stored branch = %q, want %q (the PR's REAL head branch, not nil/default)", branch, "feature-real-branch")
	}
	if repoName != "widgets" {
		t.Errorf("stored repo name = %q, want %q", repoName, "widgets")
	}
}

// TestGitHubIntegration_IssueCommentGetPullRequestFailureFallsBack proves
// the OTHER half of the H5 fix: when the outbound GetPullRequest call
// itself fails (a GitHub API outage, a network error), the mention is
// NOT dropped and the webhook delivery does NOT fail -- it falls back to
// today's pre-fix behavior (a session still gets created, with HeadBranch
// left nil / the base repo's own default branch), exactly like
// createPRBestEffort's own log-and-continue convention for a failed
// outbound GitHub API call elsewhere in this codebase.
func TestGitHubIntegration_IssueCommentGetPullRequestFailureFallsBack(t *testing.T) {
	ctx := context.Background()

	resolver := &fakePullRequestResolver{err: errors.New("simulated GitHub API outage")}
	rig := newTestRig(t, func(cfg *githubingress.Config) {
		cfg.PullRequests = resolver
		cfg.BotToken = "test-bot-token"
		cfg.Timeouts = platform.DefaultTimeouts()
	})

	const commenterID = 80000556
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := issueCommentBodyWithCommenter("acme/fallback-repo", "fallback-repo", "https://github.com/acme/fallback-repo.git", 556, "api-failure-fallback", commenterID, "fallback-user")
	status := postWebhook(t, rig, body, "delivery-api-failure-fallback-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (a failed GetPullRequest must never fail the whole webhook delivery)", status, http.StatusOK)
	}

	var branchIsNull bool
	var repoName string
	if err := rig.pool.QueryRow(ctx,
		// repos->0->>'branch' (the ->> TEXT-extraction operator), NOT
		// repos->0->'branch' (-> alone): Postgres's -> operator returns a
		// JSONB null SCALAR for a stored `"branch":null`, which is NOT SQL
		// NULL and would make `IS NULL` always false here -- ->> is what
		// actually converts a JSON null into a genuine SQL NULL (exactly
		// like the OTHER test's own successful repos->0->>'branch' text
		// read above, TestGitHubIntegration_IssueCommentResolvesRealHeadBranch).
		`SELECT repos->0->>'branch' IS NULL, repos->0->>'name' FROM sessions WHERE spawn_source = 'github' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&branchIsNull, &repoName); err != nil {
		t.Fatalf("query session repos: %v", err)
	}

	if !branchIsNull {
		t.Error("stored branch is NOT null, want null (GetPullRequest failed -- fall back to the base repo's own default branch)")
	}
	if repoName != "fallback-repo" {
		t.Errorf("stored repo name = %q, want %q (base repo, unchanged)", repoName, "fallback-repo")
	}
}

// TestGitHubIntegration_AwaitingPlanBlocksReuseTurn_HonestReplyNoRelease is
// Finding 1's own end-to-end regression test (Step 37/38 follow-up fix): a
// second mention landing on a PR whose review session already has a plan
// in StatusAwaitingApproval hits the SAME awaiting-plan gate Slack/Linear
// ingress hit (httpapi/turn.go's createTurnLocked, reached here via
// coalesce.go's REUSE path -> httpapi.CreateTurnForBot).
//
// BEFORE this fix, the sentinel (httpapi.ErrPlanAwaitingApproval) never
// survived CreateTurnForBot's own re-wrap (a "%s" verb discarded the error
// chain), so this fell into handler.go's generic-error branch: released
// the webhook delivery claim (inviting a pointless GitHub redelivery
// retry storm, since the awaiting-plan condition persists until a human
// decides elsewhere) and left the PR thread with no reply at all, unlike
// Slack/Linear. This proves all three symptoms are fixed at once: 200
// (never 500), the delivery claim is NOT released (a redelivery would
// only ever reproduce this exact outcome again), and an honest reply is
// posted back to the PR thread.
func TestGitHubIntegration_AwaitingPlanBlocksReuseTurn_HonestReplyNoRelease(t *testing.T) {
	ctx := context.Background()

	poster := &fakeCommentPoster{}
	rig := newTestRig(t, func(cfg *githubingress.Config) {
		cfg.Comments = poster
		cfg.BotToken = "test-bot-token"
	})

	const repoFullName = "acme/awaiting-plan-repo"
	const cloneURL = "https://github.com/acme/awaiting-plan-repo.git"
	const prNumber = 707
	const commenterID = 80000707

	// Both mentions come from the SAME already-linked maintainer -- batch
	// fix/deny-unlinked-github-actors means an unlinked commenter would now
	// be denied on both the WINNER and REUSE gates before ever reaching the
	// awaiting-plan gate this test exists to cover, which is a different
	// property than the one under test here.
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	// First mention: the WINNER path, creates the review session.
	first := postWebhook(t, rig, issueCommentBodyWithCommenter(repoFullName, "awaiting-plan-repo", cloneURL, prNumber, "first-mention", commenterID, "awaiting-plan-user"), "delivery-awaiting-plan-1")
	if first != http.StatusOK {
		t.Fatalf("first delivery status = %d, want %d", first, http.StatusOK)
	}

	var sessionID pgtype.UUID
	if err := rig.pool.QueryRow(ctx,
		`SELECT session_id FROM github_pr_sessions WHERE repo_full_name = $1 AND pr_number = $2`,
		repoFullName, prNumber,
	).Scan(&sessionID); err != nil {
		t.Fatalf("query claim row session id: %v", err)
	}

	// Seed a producing turn (Completed, plan_mode true) and an
	// awaiting_approval plans row atop the session the first mention just
	// created -- mirrors httpapi's own seedAwaitingApprovalPlan precedent
	// (turncore_integration_test.go).
	producingTurn, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: sessionID, Status: sqlcgen.TurnStatusCompleted, PlanMode: true})
	if err != nil {
		t.Fatalf("seed producing turn: %v", err)
	}
	if _, err := rig.plans.Create(ctx, sqlcgen.CreatePlanParams{SessionID: sessionID, TurnID: producingTurn.ID, Version: 1, Status: sqlcgen.PlanStatusAwaitingApproval}); err != nil {
		t.Fatalf("seed awaiting_approval plan: %v", err)
	}

	// Second mention on the SAME PR: takes the REUSE path (coalesce.go),
	// which now hits the awaiting-plan gate instead of enqueuing an
	// ordinary build turn.
	const secondDeliveryID = "delivery-awaiting-plan-2"
	second := postWebhook(t, rig, issueCommentBodyWithCommenter(repoFullName, "awaiting-plan-repo", cloneURL, prNumber, "second-mention-during-awaiting-plan", commenterID, "awaiting-plan-user"), secondDeliveryID)
	if second != http.StatusOK {
		t.Fatalf("second (awaiting-plan) delivery status = %d, want %d (a deterministic, expected business state -- never a 500)", second, http.StatusOK)
	}

	// The claim must NOT have been released -- a GitHub redelivery of this
	// SAME delivery id would only ever reproduce this exact outcome again.
	var deliveryRowCount int
	if err := rig.pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = 'github' AND delivery_id = $1`, secondDeliveryID,
	).Scan(&deliveryRowCount); err != nil {
		t.Fatalf("count webhook_deliveries rows: %v", err)
	}
	if deliveryRowCount != 1 {
		t.Errorf("webhook_deliveries row count = %d, want exactly 1 (claim must NOT be released for this deterministic business state)", deliveryRowCount)
	}

	// No new turn must have been enqueued by the second mention -- only the
	// first mention's own turn (the WINNER path always creates one) plus
	// the seeded producing turn.
	var turnCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM turns WHERE session_id = $1`, sessionID).Scan(&turnCount); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turnCount != 2 {
		t.Errorf("turn count = %d, want exactly 2 (the first mention's own turn + the seeded producing turn -- the gate must block the second mention's own ordinary turn)", turnCount)
	}

	// An honest reply must have been posted back to the PR thread -- the
	// PRE-fix behavior posted nothing at all.
	if len(poster.calls) != 1 {
		t.Fatalf("len(poster.calls) = %d, want exactly 1", len(poster.calls))
	}
	got := poster.calls[0]
	if got.owner != "acme" || got.repo != "awaiting-plan-repo" {
		t.Errorf("posted comment repo = %s/%s, want acme/awaiting-plan-repo", got.owner, got.repo)
	}
	if got.prNumber != prNumber {
		t.Errorf("posted comment prNumber = %d, want %d", got.prNumber, prNumber)
	}
	if got.token != "test-bot-token" {
		t.Errorf("posted comment token = %q, want %q", got.token, "test-bot-token")
	}
	if !strings.Contains(got.body, "awaiting approval") {
		t.Errorf("posted comment body = %q, want it to mention the plan is awaiting approval", got.body)
	}
}
