//go:build integration

// Integration tests for Step 46's ("review sessions", §8.2) own manual
// re-trigger-via-BUTTON REST endpoint (reviewretrigger.go), against a real
// Postgres instance -- gated behind the "integration" build tag, sharing
// this package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

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
// this action is meaningless for a session with no PR to review.
func TestRetriggerReview_NoGitHubPRMapping_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	user, token := rig.createAuthenticatedUser(ctx, t)

	session, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb, CreatedBy: user.ID})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (no github_pr_sessions row for this session)", status, http.StatusBadRequest)
	}
}

// TestRetriggerReview_NotOwnedOrJoined_Forbidden proves the SAME
// ActionPromptSession/ownedOrJoined gate CreateTurn's own REST endpoint
// already enforces (turn.go) applies here too: a member with no
// ownership/participation in the target session is denied.
func TestRetriggerReview_NotOwnedOrJoined_Forbidden(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, _ := rig.createAuthenticatedUser(ctx, t)
	_, strangerToken := rig.createAuthenticatedUser(ctx, t)

	session := rig.createOwnedGitHubReviewSession(ctx, t, owner.ID, "acme/forbidden-repo", 1)

	status := rig.doJSON(t, http.MethodPost, "/api/sessions/"+session.ID.String()+"/review/retrigger", nil, nil, strangerToken)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (a non-owning, non-participant member must be denied)", status, http.StatusForbidden)
	}
}

// TestRetriggerReview_Success proves the happy path: an owned GitHub review
// session gets a new, queued turn, carrying this endpoint's own fixed,
// deterministic manualRetriggerPromptText (diffFetcher is nil in this
// rig, per its own doc comment, so no pre-fetched context is folded in --
// the resulting prompt is exactly the fixed constant, unmodified).
func TestRetriggerReview_Success(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, token := rig.createAuthenticatedUser(ctx, t)

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

// TestRetriggerReview_AlwaysQueue_NeverRejectsAnAlreadyOpenTurn proves this
// endpoint's own AlwaysQueue policy (reviewretrigger.go's own top doc
// comment): unlike the ordinary REST CreateTurn endpoint's RejectIfOpen
// 409, a manual re-review click on a session that ALREADY has a Pending
// turn still succeeds, queuing behind it.
func TestRetriggerReview_AlwaysQueue_NeverRejectsAnAlreadyOpenTurn(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	owner, token := rig.createAuthenticatedUser(ctx, t)

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
	owner, token := rig.createAuthenticatedUser(ctx, t)

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
