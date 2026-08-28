//go:build integration

package sessionactor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file proves §30.8's own "the push/PR pair resolves its mode ONCE
// per turn": completeProcessingTurn resolves and persists the decision
// exactly once, and createPRBestEffort honors the PERSISTED value rather
// than ever re-deriving it -- both directions (a persisted shadow
// decision is never overridden by a later-observed live mode, and a
// persisted live decision is never suppressed by a later-observed shadow
// mode), plus §30.4's own demotion-cancellation escape hatch.

// createUserWithGitHubIdentity is a small extraction of the user+identity
// seeding TestHandleSandboxEvent_PushComplete_CreatesPRArtifact already
// duplicates inline -- this file's own tests need the identical fixture
// repeatedly.
func createUserWithGitHubIdentity(ctx context.Context, t *testing.T, pool *pgxpool.Pool) (sqlcgen.User, string) {
	t.Helper()
	userStore := narvipg.NewUserStore(pool)
	identityStore := narvipg.NewIdentityStore(pool)

	user, err := userStore.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("push-shadow-test-%d@example.com", time.Now().UnixNano()),
		DisplayName:  "Push Shadow Test User",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	const plaintextToken = "gh-fake-oauth-token"
	encrypted, err := platform.EncryptToken(testTokenEncryptionKey, []byte(plaintextToken))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	email := user.PrimaryEmail
	if _, err := identityStore.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:               user.ID,
		Provider:             sqlcgen.IdentityProviderGithub,
		ExternalID:           fmt.Sprintf("push-shadow-test-external-%d", time.Now().UnixNano()),
		Email:                &email,
		EmailVerified:        true,
		LinkedVia:            sqlcgen.IdentityLinkedViaAdmin,
		AccessTokenEncrypted: encrypted,
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}
	return user, plaintextToken
}

// TestCompleteProcessingTurn_PersistsPushEgressModeDecision proves the
// resolve-once-and-persist half: a turn completing for a session naming a
// repo with no repo_settings row (egressmode.Resolve's own fail-closed
// shadow default) persists pending_push_suppressed_in_shadow = true on
// the sandbox row, in the SAME transact that completes the turn --
// BEFORE sendPushBestEffort or createPRBestEffort ever run.
func TestCompleteProcessingTurn_PersistsPushEgressModeDecision(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{}, "repo", "https://github.com/acme/persist-shadow.git", "main")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	turnStore := narvipg.NewTurnStore(pool)
	processing := createProcessingTurn(ctx, t, turnStore, sessionID)

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete", Gen: 1,
		Raw: executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})

	waitUntil(t, 5*time.Second, func() bool {
		row, err := turnStore.Get(ctx, processing.ID)
		return err == nil && row.Status == sqlcgen.TurnStatusCompleted
	})

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.PendingPushSuppressedInShadow == nil {
		t.Fatal("pending_push_suppressed_in_shadow is nil, want a real persisted decision")
	}
	if !*row.PendingPushSuppressedInShadow {
		t.Error("pending_push_suppressed_in_shadow = false, want true (this repo has no repo_settings row, so its effective mode is shadow)")
	}
}

// TestHandleSandboxEvent_PushComplete_PersistedShadowDecision_NeverReReadLive
// proves the bug this Step's own Part 3 exists to prevent, in the
// direction that would otherwise produce "a live CreatePR on a
// never-pushed branch": the push cycle's own persisted decision is
// shadow, and the repo is then PROMOTED to live before push_complete
// arrives. createPRBestEffort must still skip PR creation -- honoring the
// persisted decision, never re-resolving against the now-live mode.
func TestHandleSandboxEvent_PushComplete_PersistedShadowDecision_NeverReReadLive(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	user, _ := createUserWithGitHubIdentity(ctx, t, pool)

	sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID, "repo1", "https://github.com/acme/never-reread-live.git", "feature-x")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	suppressed := true
	if _, err := sandboxStore.SetPendingPush(ctx, sqlcgen.SetSandboxPendingPushParams{
		SessionID: sessionID, PendingPushSuppressedInShadow: &suppressed,
	}); err != nil {
		t.Fatalf("seed persisted push decision: %v", err)
	}

	// The repo is promoted to LIVE only AFTER the push decision above was
	// already persisted as shadow -- exactly the race this test proves is
	// handled correctly.
	if _, err := narvipg.NewRepoSettingsStore(pool).UpsertLiveEgressEnabled(ctx, "acme/never-reread-live", true); err != nil {
		t.Fatalf("promote repo to live egress: %v", err)
	}

	sourceControl := &fakeSourceControl{nextRef: ports.PRRef{Number: 1, URL: "https://github.com/acme/never-reread-live/pull/1"}}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete", Gen: 1,
		Raw: pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-x", "abc123"),
	})

	// Nothing to positively wait for -- a fixed sleep, matching this
	// package's own "asserting something did NOT happen" precedent
	// (dispatch_integration_test.go's circuit-breaker test, snapshot_
	// test.go's mint-failure test).
	time.Sleep(500 * time.Millisecond)

	if got := sourceControl.callCount(); got != 0 {
		t.Errorf("CreatePR called %d times, want 0 (the persisted decision was shadow at push-send time; the repo's later promotion must not resurrect a live create)", got)
	}
	// Suppressed is NOT the same as skipped, and asserting only "CreatePR
	// was not called" cannot tell them apart -- doing nothing at all
	// passes that assertion too. §30.6 makes the difference a contract:
	// a suppressed effect that leaves no ledger row is a violation, and
	// SuppressCreatePR is the path that writes one.
	if got := sourceControl.suppressCallCount(); got != 1 {
		t.Errorf("SuppressCreatePR called %d times, want 1: a shadow-stamped cycle must be suppressed AND RECORDED, never silently skipped", got)
	}

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.PendingPushSuppressedInShadow != nil {
		t.Error("pending_push_suppressed_in_shadow still set after createPRBestEffort consumed it, want cleared back to nil")
	}
}

// TestHandleSandboxEvent_PushComplete_PersistedLiveDecision_NeverReReadShadow
// proves the OTHER direction, the one that would otherwise produce "an
// orphan branch in a customer repo": the push cycle's own persisted
// decision is live, and the repo has since become shadow (e.g. it was
// never actually promoted -- repo_settings absent, resolving shadow by
// egressmode.Resolve's own fail-closed default -- simulating ANY
// after-the-fact demotion/never-armed state). createPRBestEffort must
// still create the PR for real -- the branch really was pushed live, and
// the persisted decision says so.
func TestHandleSandboxEvent_PushComplete_PersistedLiveDecision_NeverReReadShadow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	user, _ := createUserWithGitHubIdentity(ctx, t, pool)

	sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID, "repo1", "https://github.com/acme/never-reread-shadow.git", "feature-x")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	suppressed := false
	if _, err := sandboxStore.SetPendingPush(ctx, sqlcgen.SetSandboxPendingPushParams{
		SessionID: sessionID, PendingPushSuppressedInShadow: &suppressed,
	}); err != nil {
		t.Fatalf("seed persisted push decision: %v", err)
	}
	// Deliberately NO repo_settings row: this repo's CURRENT effective
	// mode resolves shadow (egressmode.Resolve's own fail-closed default)
	// even though the push decision was persisted live.

	sourceControl := &fakeSourceControl{nextRef: ports.PRRef{Number: 7, URL: "https://github.com/acme/never-reread-shadow/pull/7"}}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete", Gen: 1,
		Raw: pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-x", "abc123"),
	})

	waitUntil(t, 5*time.Second, func() bool {
		return sourceControl.callCount() == 1
	})

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.PendingPushSuppressedInShadow != nil {
		t.Error("pending_push_suppressed_in_shadow still set after createPRBestEffort consumed it, want cleared back to nil")
	}
}

// TestHandleSandboxEvent_PushComplete_CancelledPersistedPush_SkipsPRCreation
// proves §30.4's own demotion-cancellation escape hatch: a push cycle
// persisted live but subsequently CANCELLED by a repo-demotion sweep
// (internal/app/seed) must not create a PR, regardless of the persisted
// suppressedInShadow value.
func TestHandleSandboxEvent_PushComplete_CancelledPersistedPush_SkipsPRCreation(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	user, _ := createUserWithGitHubIdentity(ctx, t, pool)

	sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID, "repo1", "https://github.com/acme/cancelled-push.git", "feature-x")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	suppressed := false
	if _, err := sandboxStore.SetPendingPush(ctx, sqlcgen.SetSandboxPendingPushParams{
		SessionID: sessionID, PendingPushSuppressedInShadow: &suppressed,
	}); err != nil {
		t.Fatalf("seed persisted push decision: %v", err)
	}
	if _, err := sandboxStore.CancelPendingPush(ctx, sessionID); err != nil {
		t.Fatalf("cancel persisted push decision: %v", err)
	}

	sourceControl := &fakeSourceControl{nextRef: ports.PRRef{Number: 9, URL: "https://github.com/acme/cancelled-push/pull/9"}}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "", nil, false)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete", Gen: 1,
		Raw: pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-x", "abc123"),
	})

	time.Sleep(500 * time.Millisecond)

	if got := sourceControl.callCount(); got != 0 {
		t.Errorf("CreatePR called %d times, want 0 (a repo-demotion sweep cancelled this in-flight push signal)", got)
	}
}
