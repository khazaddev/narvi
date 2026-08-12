//go:build integration

// Integration tests for Step 65's ("review: automatic re-review on new
// commits", §24.1) new `pull_request`/`synchronize` ingress lane -- posted
// through the FULL, real HTTP handler (NewHandler, package github_test,
// mirroring handler_integration_test.go's own established convention),
// not called directly, so signature verification, claim/release dedupe,
// and the eventType/action dispatch gate ahead of this lane are all
// exercised exactly as a real GitHub webhook delivery would hit them.
package github_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	githubingress "github.com/khazaddev/narvi/internal/adapters/inbound/github"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
)

// pullRequestSynchronizeBody builds a synthetic, real-shaped `pull_request`
// webhook payload with action="synchronize" -- §24.1's own new ingress
// lane's trigger shape (repository.full_name, pull_request.number,
// pull_request.head.sha -- exactly the three fields that lane needs).
func pullRequestSynchronizeBody(repoFullName string, prNumber int, headSHA string) []byte {
	body, err := json.Marshal(map[string]any{
		"action": "synchronize",
		"pull_request": map[string]any{
			"number": prNumber,
			"head":   map[string]any{"sha": headSHA},
		},
		"repository": map[string]any{"full_name": repoFullName},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// pullRequestOpenedBody builds an ordinary, unrelated `pull_request` event
// (action="opened") carrying the SAME head sha shape a synchronize event
// would -- used to prove the synchronize lane's own action gate never
// fires for a different action.
func pullRequestOpenedBody(repoFullName string, prNumber int, headSHA string) []byte {
	body, err := json.Marshal(map[string]any{
		"action": "opened",
		"pull_request": map[string]any{
			"number": prNumber,
			"head":   map[string]any{"sha": headSHA},
		},
		"repository": map[string]any{"full_name": repoFullName},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// withTimers wires cfg.Timers -- the nil-safe field that gates this whole
// lane (handler.go's own "cfg.Timers != nil" dispatch check). newTestPool
// returns this test binary's own single, shared pool (sharedpool_
// integration_test.go), so the *postgres.TimerStore built here targets
// the SAME database newTestRig's own internal pool will use.
func withTimers(timers *narvipg.TimerStore) func(*githubingress.Config) {
	return func(cfg *githubingress.Config) { cfg.Timers = timers }
}

func getReviewRetriggerDebounceTimer(ctx context.Context, t *testing.T, pool *narvipg.TimerStore, sessionID pgtype.UUID) (sqlcgen.SessionTimer, bool) {
	t.Helper()
	row, err := pool.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: sessionactor.TimerReviewRetriggerDebounce})
	if err == nil {
		return row, true
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return sqlcgen.SessionTimer{}, false
	}
	t.Fatalf("get review_retrigger_debounce timer: %v", err)
	return sqlcgen.SessionTimer{}, false
}

// TestGitHubIntegration_PullRequestSynchronize_NoReviewSession_Acknowledges200NoWrite
// covers §24.1's own "no row, or a row with session_id still NULL" no-op:
// both cases must acknowledge (200) without arming anything, exactly like
// today's "no mention" case for a comment event.
func TestGitHubIntegration_PullRequestSynchronize_NoReviewSession_Acknowledges200NoWrite(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	timers := narvipg.NewTimerStore(pool)
	prSessions := narvipg.NewGitHubPRSessionStore(pool)

	t.Run("no github_pr_sessions row at all", func(t *testing.T) {
		rig := newTestRig(t, withTimers(timers))
		repoFullName := "acme/no-row-at-all"

		status := postWebhookEventType(t, rig, pullRequestSynchronizeBody(repoFullName, 1, "sha-1"), "delivery-no-row", "pull_request")
		if status != 200 {
			t.Fatalf("status = %d, want 200", status)
		}

		if _, err := prSessions.GetBySessionID(ctx, pgtype.UUID{}); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("sanity: GetBySessionID(zero uuid) = %v, want pgx.ErrNoRows", err)
		}
	})

	t.Run("row exists but session_id still NULL", func(t *testing.T) {
		rig := newTestRig(t, withTimers(timers))
		repoFullName := "acme/null-session-id"
		if err := prSessions.EnsureRow(ctx, repoFullName, 2); err != nil {
			t.Fatalf("ensure github pr session row: %v", err)
		}

		status := postWebhookEventType(t, rig, pullRequestSynchronizeBody(repoFullName, 2, "sha-2"), "delivery-null-session", "pull_request")
		if status != 200 {
			t.Fatalf("status = %d, want 200", status)
		}
		// UpsertPendingRetriggerHeadSHA's own guard (session_id IS NOT
		// NULL) must have made this a no-op -- there is no session_id to
		// look this row back up by, so the only thing left to assert is
		// that the handler didn't panic/500 and returned 200 above.
	})
}

// TestGitHubIntegration_PullRequestSynchronize_ExistingSession_UpsertsPendingHeadSHAAndArmsTimer
// covers the full §24.1/§24.2 happy path: an existing review session for
// this PR gets pending_retrigger_head_sha upserted to the event's own
// head sha, and the review_retrigger_debounce named timer is armed to
// fire in the future, atomically.
func TestGitHubIntegration_PullRequestSynchronize_ExistingSession_UpsertsPendingHeadSHAAndArmsTimer(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	timers := narvipg.NewTimerStore(pool)
	prSessions := narvipg.NewGitHubPRSessionStore(pool)
	sessions := narvipg.NewSessionStore(pool)

	rig := newTestRig(t, withTimers(timers))

	sessionRow, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	repoFullName := "acme/widgets"
	const prNumber = 42
	if err := prSessions.EnsureRow(ctx, repoFullName, prNumber); err != nil {
		t.Fatalf("ensure github pr session row: %v", err)
	}
	if err := prSessions.SetSessionID(ctx, repoFullName, prNumber, sessionRow.ID); err != nil {
		t.Fatalf("set github pr session id: %v", err)
	}

	before := time.Now()
	status := postWebhookEventType(t, rig, pullRequestSynchronizeBody(repoFullName, prNumber, "sha-happy-path"), "delivery-happy-path", "pull_request")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}

	got, err := prSessions.GetBySessionID(ctx, sessionRow.ID)
	if err != nil {
		t.Fatalf("get github pr session: %v", err)
	}
	if got.PendingRetriggerHeadSha == nil || *got.PendingRetriggerHeadSha != "sha-happy-path" {
		t.Errorf("pending_retrigger_head_sha = %v, want %q", got.PendingRetriggerHeadSha, "sha-happy-path")
	}

	timer, ok := getReviewRetriggerDebounceTimer(ctx, t, timers, sessionRow.ID)
	if !ok {
		t.Fatal("review_retrigger_debounce timer was not armed")
	}
	if !timer.FiresAt.Time.After(before) {
		t.Errorf("timer fires_at = %v, want strictly after %v (armed to now()+ReviewRetriggerDebounce)", timer.FiresAt.Time, before)
	}
}

// TestGitHubIntegration_PullRequestSynchronize_SecondPush_OverwritesPendingHeadSHA
// covers §24.2's own "upserted (overwritten, not appended) on every
// event" rule directly: two synchronize events for the same PR leave
// pending_retrigger_head_sha holding only the SECOND (latest) sha, never
// both/an array/the first.
func TestGitHubIntegration_PullRequestSynchronize_SecondPush_OverwritesPendingHeadSHA(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	timers := narvipg.NewTimerStore(pool)
	prSessions := narvipg.NewGitHubPRSessionStore(pool)
	sessions := narvipg.NewSessionStore(pool)

	rig := newTestRig(t, withTimers(timers))

	sessionRow, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	repoFullName := "acme/burst-of-pushes"
	const prNumber = 7
	if err := prSessions.EnsureRow(ctx, repoFullName, prNumber); err != nil {
		t.Fatalf("ensure github pr session row: %v", err)
	}
	if err := prSessions.SetSessionID(ctx, repoFullName, prNumber, sessionRow.ID); err != nil {
		t.Fatalf("set github pr session id: %v", err)
	}

	if status := postWebhookEventType(t, rig, pullRequestSynchronizeBody(repoFullName, prNumber, "sha-first-push"), "delivery-burst-1", "pull_request"); status != 200 {
		t.Fatalf("first push status = %d, want 200", status)
	}
	if status := postWebhookEventType(t, rig, pullRequestSynchronizeBody(repoFullName, prNumber, "sha-second-push"), "delivery-burst-2", "pull_request"); status != 200 {
		t.Fatalf("second push status = %d, want 200", status)
	}

	got, err := prSessions.GetBySessionID(ctx, sessionRow.ID)
	if err != nil {
		t.Fatalf("get github pr session: %v", err)
	}
	if got.PendingRetriggerHeadSha == nil || *got.PendingRetriggerHeadSha != "sha-second-push" {
		t.Errorf("pending_retrigger_head_sha = %v, want the LATEST push %q, never the first", got.PendingRetriggerHeadSha, "sha-second-push")
	}
}

// TestGitHubIntegration_PullRequestSynchronize_OtherAction_NeverArmsAnything
// proves the action gate in handler.go (readPullRequestEventAction(body)
// == "synchronize") correctly excludes every OTHER pull_request action --
// here, "opened" -- from this lane: no pending_retrigger_head_sha write
// happens even though the payload carries the identical head-sha shape.
func TestGitHubIntegration_PullRequestSynchronize_OtherAction_NeverArmsAnything(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	timers := narvipg.NewTimerStore(pool)
	prSessions := narvipg.NewGitHubPRSessionStore(pool)
	sessions := narvipg.NewSessionStore(pool)

	rig := newTestRig(t, withTimers(timers))

	sessionRow, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	repoFullName := "acme/opened-not-synchronize"
	const prNumber = 9
	if err := prSessions.EnsureRow(ctx, repoFullName, prNumber); err != nil {
		t.Fatalf("ensure github pr session row: %v", err)
	}
	if err := prSessions.SetSessionID(ctx, repoFullName, prNumber, sessionRow.ID); err != nil {
		t.Fatalf("set github pr session id: %v", err)
	}

	status := postWebhookEventType(t, rig, pullRequestOpenedBody(repoFullName, prNumber, "sha-opened"), "delivery-opened", "pull_request")
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}

	got, err := prSessions.GetBySessionID(ctx, sessionRow.ID)
	if err != nil {
		t.Fatalf("get github pr session: %v", err)
	}
	if got.PendingRetriggerHeadSha != nil {
		t.Errorf("pending_retrigger_head_sha = %v, want nil (an 'opened' action must never reach the synchronize lane)", got.PendingRetriggerHeadSha)
	}
	if _, ok := getReviewRetriggerDebounceTimer(ctx, t, timers, sessionRow.ID); ok {
		t.Error("review_retrigger_debounce timer was armed for a non-synchronize action")
	}
}

// malformedSynchronizeBody carries a genuinely valid action="synchronize"
// (so handler.go's own cheap readPullRequestEventAction peek -- which
// only ever unmarshals a bare {"action": "..."} envelope -- succeeds and
// routes this INTO handlePullRequestSynchronize, never falling through to
// the ordinary parseMention pipeline's own, DIFFERENT error path), but a
// pull_request field of the wrong JSON type (a string, not an object),
// which fails specifically when handlePullRequestSynchronize's own full
// json.Unmarshal(body, &pullRequestPayload{}) call runs.
func malformedSynchronizeBody() []byte {
	return []byte(`{"action":"synchronize","pull_request":"not-an-object","repository":{"full_name":"acme/malformed"}}`)
}

// TestGitHubIntegration_PullRequestSynchronize_MalformedPayload_ReleasesClaimAndReturns400
// covers §24.1's own explicit instruction to reuse the existing claim/
// release handling: a malformed body is claimed (first delivery) but
// fails to process (400), and the SAME delivery id must be re-postable
// afterward (the claim was released, not left permanently consumed) --
// proven by re-posting the SAME delivery id with a valid body next and
// observing it is processed (200), not silently swallowed as a
// "duplicate delivery" 200 with no effect.
func TestGitHubIntegration_PullRequestSynchronize_MalformedPayload_ReleasesClaimAndReturns400(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	timers := narvipg.NewTimerStore(pool)
	prSessions := narvipg.NewGitHubPRSessionStore(pool)
	sessions := narvipg.NewSessionStore(pool)

	rig := newTestRig(t, withTimers(timers))

	sessionRow, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	repoFullName := "acme/malformed-then-retry"
	const prNumber = 11
	if err := prSessions.EnsureRow(ctx, repoFullName, prNumber); err != nil {
		t.Fatalf("ensure github pr session row: %v", err)
	}
	if err := prSessions.SetSessionID(ctx, repoFullName, prNumber, sessionRow.ID); err != nil {
		t.Fatalf("set github pr session id: %v", err)
	}

	const deliveryID = "delivery-malformed-retry"
	status := postWebhookEventType(t, rig, malformedSynchronizeBody(), deliveryID, "pull_request")
	if status != 400 {
		t.Fatalf("malformed body status = %d, want 400", status)
	}

	// Re-post the SAME delivery id, now with a valid body -- if the claim
	// had NOT been released, this would come back 200 as a silently
	// ignored "duplicate delivery" with pending_retrigger_head_sha left
	// unset; if it WAS released (the fix under test), this is processed
	// as a genuine first delivery.
	status = postWebhookEventType(t, rig, pullRequestSynchronizeBody(repoFullName, prNumber, "sha-after-retry"), deliveryID, "pull_request")
	if status != 200 {
		t.Fatalf("retried valid body status = %d, want 200", status)
	}

	got, err := prSessions.GetBySessionID(ctx, sessionRow.ID)
	if err != nil {
		t.Fatalf("get github pr session: %v", err)
	}
	if got.PendingRetriggerHeadSha == nil || *got.PendingRetriggerHeadSha != "sha-after-retry" {
		t.Errorf("pending_retrigger_head_sha = %v, want %q (the released claim must have been genuinely reprocessed)", got.PendingRetriggerHeadSha, "sha-after-retry")
	}
}

// TestGitHubIntegration_PullRequestSynchronize_TimersNilConfig_FallsThroughNoOp
// covers the nil-safe contract handler.go's own doc comment on cfg.Timers
// promises: a deployment/test wiring that never sets it (this package's
// own default newTestRig, with no withTimers mutate) must never panic --
// every `pull_request`/synchronize event simply falls through to the
// ordinary pipeline, acknowledged as a no-op exactly like today.
func TestGitHubIntegration_PullRequestSynchronize_TimersNilConfig_FallsThroughNoOp(t *testing.T) {
	rig := newTestRig(t) // no withTimers mutate -- cfg.Timers stays nil

	status := postWebhookEventType(t, rig, pullRequestSynchronizeBody("acme/nil-timers", 5, "sha-nil-timers"), "delivery-nil-timers", "pull_request")
	if status != 200 {
		t.Fatalf("status = %d, want 200 (nil Timers must degrade to a harmless no-op, never a panic/500)", status)
	}
}
