//go:build integration

// Integration tests for Step 46's ("review sessions", §8.2) own manual
// re-trigger-via-BUTTON REST endpoint (reviewretrigger.go), against a real
// Postgres instance -- gated behind the "integration" build tag, sharing
// this package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// fakeReviewContextFetcher is a test-only reviewcontext.Fetcher -- no real
// HTTP round trip, mirroring internal/adapters/inbound/github's own
// identically-named fixture (handler_integration_test.go) exactly.
// diffOwner/diffRepo/diffNumber/diffToken (audit fix, test-coverage
// finding) record GetPullRequestDiff's own last call args: this endpoint's
// own diffFetcher call site (reviewretrigger.go) previously had NO
// integration coverage at all with a non-nil diffFetcher wired in (this
// rig's own default leaves it nil) -- an owner/repo swap there would have
// been invisible to every test in the repo, unit or integration.
type fakeReviewContextFetcher struct {
	diff          string
	diffTruncated bool

	diffOwner  string
	diffRepo   string
	diffNumber int32
	diffToken  string
}

func (f *fakeReviewContextFetcher) GetPullRequest(_ context.Context, _, _ string, _ int32, _ string) (githubapi.PullRequest, error) {
	return githubapi.PullRequest{}, nil
}

func (f *fakeReviewContextFetcher) GetPullRequestDiff(_ context.Context, owner, repo string, number int32, token string) (string, bool, error) {
	f.diffOwner, f.diffRepo, f.diffNumber, f.diffToken = owner, repo, number, token
	return f.diff, f.diffTruncated, nil
}

// createOwnedGitHubReviewSession creates a session (CreatedBy = owner) plus
// a github_pr_sessions row pointing back at it -- exactly the fixture shape
// coalesce.go's own WINNER path produces in production, built directly via
// the stores here rather than round-tripping through the real webhook
// handler (a different package's own concern, already covered by
// internal/adapters/inbound/github's own integration tests).
func (r testRig) createOwnedGitHubReviewSession(ctx context.Context, t *testing.T, ownerID pgtype.UUID, repoFullName string, prNumber int32) sqlcgen.Session {
	t.Helper()

	session, err := r.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub, CreatedBy: ownerID})
	if err != nil {
		t.Fatalf("create test github review session: %v", err)
	}
	if err := r.prSessions.EnsureRow(ctx, repoFullName, prNumber); err != nil {
		t.Fatalf("ensure github_pr_sessions row: %v", err)
	}
	if err := r.prSessions.SetSessionID(ctx, repoFullName, prNumber, session.ID); err != nil {
		t.Fatalf("set github_pr_sessions session id: %v", err)
	}
	return session
}

// TestRetriggerReview_NotFound proves a nonexistent session id is a plain
// 404, exactly like every other session-scoped route in this package.
func TestRetriggerReview_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/00000000-0000-0000-0000-000000000000/review/retrigger", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestRetriggerReview_NoGitHubPRMapping_BadRequest proves an ordinary,
// non-GitHub session (no github_pr_sessions row at all) is rejected 400 --
// this action is meaningless for a session with no PR to review. The
// caller must be authorized for authz.ActionRetriggerReview (maintainer)
// to reach that 400 at all -- the authz gate runs BEFORE the PR-mapping
// check.
func TestRetriggerReview_NoGitHubPRMapping_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	session, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb, CreatedBy: user.ID})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (no github_pr_sessions row for this session)", status, http.StatusBadRequest)
	}
}

// TestRetriggerReview_MemberDenied_EvenAsSessionOwner is this endpoint's
// own regression test for a confirmed privilege-escalation audit finding:
// this handler used to authorize against authz.ActionPromptSession (the
// SAME own/joined-aware check CreateTurn's REST endpoint applies), which
// let a plain member re-trigger a review on any session THEY THEMSELVES
// created. §13.3 row 5 ("edit review verdicts; re-trigger reviews;
// auto-approval eligibility config") is admin/maintainer only, with NO
// member own/joined carve-out -- so even the session's own owning member
// must be denied here, unlike CreateTurn's own ordinary prompt gate.
func TestRetriggerReview_MemberDenied_EvenAsSessionOwner(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, ownerToken := rig.createAuthenticatedUser(ctx, t) // default role: member (httpapi_integration_test.go's own createAuthenticatedUser).

	session := rig.createOwnedGitHubReviewSession(ctx, t, owner.ID, "acme/member-owner-repo", 1)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, ownerToken)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (a member must be denied even on a session they themselves own -- §13.3 row 5 has no member carve-out)", status, http.StatusForbidden)
	}

	var turnCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM turns WHERE session_id = $1`, session.ID).Scan(&turnCount); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turnCount != 0 {
		t.Errorf("turn count = %d, want 0 (the denied call must never queue a turn)", turnCount)
	}
}

// TestRetriggerReview_MaintainerAllowed_EvenAsNonOwner is this endpoint's
// own regression test for the OTHER half of the same §13.3 row 5 rule:
// unlike ActionPromptSession's own own/joined carve-out, ActionRetriggerReview
// has NO ownership concept at all -- a maintainer who neither created nor
// joined the target session is still allowed, admin/maintainer bypassing
// ownership entirely.
func TestRetriggerReview_MaintainerAllowed_EvenAsNonOwner(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t) // some other, unrelated member owns the session.
	_, maintainerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	session := rig.createOwnedGitHubReviewSession(ctx, t, owner.ID, "acme/maintainer-nonowner-repo", 2)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, maintainerToken)
	if status != http.StatusCreated {
		t.Errorf("status = %d, want %d (a maintainer must be allowed regardless of ownership/participation)", status, http.StatusCreated)
	}
}

// TestRetriggerReview_Success proves the happy path: a maintainer
// re-triggering an (unrelated) GitHub review session gets a new, queued
// turn, carrying this endpoint's own fixed, deterministic
// manualRetriggerPromptText (diffFetcher is nil in this rig's own default
// wiring, so no pre-fetched context is folded in -- the resulting prompt
// is exactly the fixed constant, unmodified). The caller is a maintainer,
// not the session's own owner -- authz.ActionRetriggerReview (§13.3 row 5)
// is admin/maintainer only, with no member own/joined carve-out at all
// (see TestRetriggerReview_MemberDenied_EvenAsSessionOwner above).
func TestRetriggerReview_Success(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	session := rig.createOwnedGitHubReviewSession(ctx, t, owner.ID, "acme/retrigger-repo", 55)

	var resp restdtos.CreateTurnResponse
	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, &resp, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if resp.Id == "" {
		t.Error("response Id is empty, want a real turn id")
	}

	var turnCount int
	var prompt string
	if err := rig.pool.QueryRow(ctx, `SELECT count(*), max(prompt) FROM turns WHERE session_id = $1`, session.ID).Scan(&turnCount, &prompt); err != nil {
		t.Fatalf("query turns: %v", err)
	}
	if turnCount != 1 {
		t.Fatalf("turn count = %d, want 1", turnCount)
	}
	if prompt != "Manual re-review requested via the web review button." {
		t.Errorf("turn prompt = %q, want the fixed manualRetriggerPromptText constant", prompt)
	}
}

// TestRetriggerReview_PreFetchesReviewContext_CorrectOwnerRepoArgs (audit
// fix, test-coverage finding) proves this endpoint's OWN diffFetcher call
// site (reviewretrigger.go, distinct code from internal/adapters/inbound/
// github's own identical-looking call) reaches reviewcontext.Fetch with
// the correct owner/repo/number/token split from prSession.RepoFullName --
// this rig's own default wiring leaves diffFetcher nil (this file's own
// testRig doc comment), so before this test, an owner/repo swap at THIS
// call site specifically would have been invisible to every test in the
// repo.
func TestRetriggerReview_PreFetchesReviewContext_CorrectOwnerRepoArgs(t *testing.T) {
	fetcher := &fakeReviewContextFetcher{diff: "diff --git a/x b/x\n+hello\n"}
	rig := newTestRig(t, func(r *testRig) {
		r.diffFetcher = fetcher
		r.botToken = "test-bot-token"
	})
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	session := rig.createOwnedGitHubReviewSession(ctx, t, owner.ID, "acme/prefetch-retrigger-repo", 42)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	// owner ("acme") and repo ("prefetch-retrigger-repo") are deliberately
	// distinguishable strings -- a swapped-argument regression at this
	// call site would fail this assertion.
	if fetcher.diffOwner != "acme" || fetcher.diffRepo != "prefetch-retrigger-repo" || fetcher.diffNumber != 42 || fetcher.diffToken != "test-bot-token" {
		t.Errorf("GetPullRequestDiff args = (%q, %q, %d, %q), want (%q, %q, %d, %q)",
			fetcher.diffOwner, fetcher.diffRepo, fetcher.diffNumber, fetcher.diffToken, "acme", "prefetch-retrigger-repo", 42, "test-bot-token")
	}

	var prompt string
	if err := rig.pool.QueryRow(ctx, `SELECT prompt FROM turns WHERE session_id = $1`, session.ID).Scan(&prompt); err != nil {
		t.Fatalf("query turn prompt: %v", err)
	}
	if !strings.Contains(prompt, "diff --git a/x b/x") {
		t.Errorf("prompt = %q, want it to contain the pre-fetched diff", prompt)
	}
}

// TestRetriggerReview_AlwaysQueue_NeverRejectsAnAlreadyOpenTurn proves this
// endpoint's own AlwaysQueue policy (reviewretrigger.go's own top doc
// comment): unlike the ordinary REST CreateTurn endpoint's RejectIfOpen
// 409, a manual re-review click on a session that ALREADY has a Pending
// turn still succeeds, queuing behind it.
func TestRetriggerReview_AlwaysQueue_NeverRejectsAnAlreadyOpenTurn(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	session := rig.createOwnedGitHubReviewSession(ctx, t, owner.ID, "acme/queue-repo", 7)

	// First click.
	first := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, token)
	if first != http.StatusCreated {
		t.Fatalf("first click status = %d, want %d", first, http.StatusCreated)
	}
	// Second click, immediately -- the first turn is still Pending
	// (nothing dispatches it in this test's own minimal registry wiring,
	// httpapi_integration_test.go's own doc comment). An ordinary
	// CreateTurn call here would 409; this endpoint must not.
	second := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, token)
	if second != http.StatusCreated {
		t.Fatalf("second click status = %d, want %d (AlwaysQueue must never reject an already-open turn)", second, http.StatusCreated)
	}

	var turnCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM turns WHERE session_id = $1`, session.ID).Scan(&turnCount); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turnCount != 2 {
		t.Errorf("turn count = %d, want 2 (both clicks must queue)", turnCount)
	}
}

// TestRetriggerReview_ConcurrentClicks_AllSucceedNoDeadlock is this
// endpoint's own real-concurrency proof: N concurrent manual re-review
// clicks against the SAME already-existing session all succeed, producing
// exactly N turns on that ONE session -- never a race, a deadlock, or a
// lost turn. Driven with real concurrent HTTP requests against the real
// handler/real Postgres, matching internal/adapters/inbound/github's own
// TestGitHubIntegration_ConcurrentMentionsCoalesceToOneSessionManyTurns
// style exactly (real goroutines, not sequential calls).
func TestRetriggerReview_ConcurrentClicks_AllSucceedNoDeadlock(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	session := rig.createOwnedGitHubReviewSession(ctx, t, owner.ID, "acme/concurrent-retrigger-repo", 303)

	const n = 8
	start := make(chan struct{})
	statuses := make([]int, n)

	var g errgroup.Group
	for i := 0; i < n; i++ {
		idx := i
		g.Go(func() error {
			<-start
			statuses[idx] = rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, token)
			return nil
		})
	}
	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent retrigger clicks: %v", err)
	}

	for i, status := range statuses {
		if status != http.StatusCreated {
			t.Errorf("statuses[%d] = %d, want %d", i, status, http.StatusCreated)
		}
	}

	var sessionCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE id = $1`, session.ID).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Fatalf("session count = %d, want exactly 1 (this endpoint never creates a session)", sessionCount)
	}

	var turnCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM turns WHERE session_id = $1`, session.ID).Scan(&turnCount); err != nil {
		t.Fatalf("count turns: %v", err)
	}
	if turnCount != n {
		t.Errorf("turn count = %d, want exactly %d (one turn per concurrent click, none lost/raced away)", turnCount, n)
	}
}

// TestRetriggerReview_AlreadyAnsweredFacts_PrependedNeverReplacingProse is
// Step 48's own explicitly required test (§22.1): an already-open
// review_findings row for this PR renders as a deterministic "already
// answered" fact block PREPENDED to -- never replacing -- the manual
// re-trigger's own fixed prose text (manualRetriggerPromptText,
// reviewretrigger.go).
func TestRetriggerReview_AlreadyAnsweredFacts_PrependedNeverReplacingProse(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	repoFullName := "acme/already-answered-repo"
	const prNumber = int32(55)
	session := rig.createOwnedGitHubReviewSession(ctx, t, owner.ID, repoFullName, prNumber)

	const wantDescription = "Missing test coverage for the timeout path."
	if _, err := rig.reviewFindings.Upsert(ctx, sqlcgen.UpsertReviewFindingParams{
		RepoFullName: repoFullName,
		PrNumber:     prNumber,
		IdentityHash: "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Severity:     "medium",
		FilePath:     "internal/foo/bar.go",
		Description:  wantDescription,
	}); err != nil {
		t.Fatalf("upsert review finding: %v", err)
	}

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	var prompt string
	if err := rig.pool.QueryRow(ctx, `SELECT prompt FROM turns WHERE session_id = $1`, session.ID).Scan(&prompt); err != nil {
		t.Fatalf("query turn prompt: %v", err)
	}

	if !strings.Contains(prompt, wantDescription) {
		t.Fatalf("prompt = %q, want it to contain the already-answered finding's own description %q", prompt, wantDescription)
	}
	// The exact literal manualRetriggerPromptText renders (reviewretrigger.go)
	// -- unexported, so named here as a literal rather than imported (this
	// is package httpapi_test, mirroring every other black-box test in
	// this file).
	const wantProseFallback = "Manual re-review requested via the web review button."
	if !strings.Contains(prompt, wantProseFallback) {
		t.Fatalf("prompt = %q, want it to STILL contain the manual re-trigger's own prose fallback (prepended TO, never REPLACING it)", prompt)
	}

	factsIdx := strings.Index(prompt, wantDescription)
	proseIdx := strings.Index(prompt, wantProseFallback)
	if factsIdx >= proseIdx {
		t.Errorf("already-answered facts appear at index %d, prose fallback at index %d -- want facts PREPENDED (before), not appended after", factsIdx, proseIdx)
	}
}
