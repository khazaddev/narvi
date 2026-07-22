//go:build integration

// Regression test for audit finding F3 ("two-phase resume's 'no-op guard
// for free' is defeated by stale timestamps"): proves UpsertSandboxForSpawn
// now resets last_seen_at on EVERY call -- including a resume-style claim
// on a box that sat in a terminal status well past
// domain/sandbox.SpawnConfig.SpawningTimeout -- and that
// domain/sandbox.EvaluateSpawnDecision's own Skip guard genuinely covers
// the in-flight window for a concurrent second read as a result. Kept in
// its own file, mirroring event_artifact_wstoken_integration_test.go's own
// precedent of a focused file per fix rather than growing
// postgres_integration_test.go's single pipeline test.
package postgres_test

import (
	"context"
	"testing"
	"time"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestUpsertSandboxForSpawn_ResumeStyleClaim_ResetsLastSeenAt proves the
// query-level half of the F3 fix: a box that has sat Stopped (a terminal
// status) for well longer than SpawningTimeout gets a FRESH last_seen_at
// the moment a resume-style claim (dispatch.go's planResume, which reuses
// this SAME UpsertSandboxForSpawn upsert -- see that query's own generated
// doc comment) runs UpsertSandboxForSpawn against it again, and that this
// fresh timestamp is exactly what lets EvaluateSpawnDecision's own
// Spawning/Connecting/Booting Skip guard genuinely no-op a concurrent
// second actor reading the row right after -- covering the in-flight
// window regardless of how long the box sat terminal beforehand.
func TestUpsertSandboxForSpawn_ResumeStyleClaim_ResetsLastSeenAt(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	store := narvipg.NewSandboxStore(pool)
	tokenHash1 := "token-hash-initial-spawn"

	// Initial spawn: creates the row (gen=1, spawning).
	if _, err := store.UpsertForSpawn(ctx, sqlcgen.UpsertSandboxForSpawnParams{
		SessionID: sessionID, TokenHash: &tokenHash1,
	}); err != nil {
		t.Fatalf("initial UpsertForSpawn: %v", err)
	}

	// Simulate the box running, then genuinely terminalizing and sitting
	// Stopped for well longer than SpawningTimeout (120s default) before
	// anything resumes it -- directly back-dating created_at/last_seen_at
	// via raw SQL, since driving a real 10-minute wall-clock wait in a
	// test is impractical. This is exactly the "box being resumed has by
	// definition sat terminal -- usually > 120s" scenario the audit
	// finding names.
	const longAgo = 10 * time.Minute
	if _, err := pool.Exec(ctx,
		`UPDATE sandboxes SET status = 'stopped',
		    created_at = now() - make_interval(secs => $2),
		    last_seen_at = now() - make_interval(secs => $2)
		 WHERE session_id = $1`,
		sessionID, longAgo.Seconds(),
	); err != nil {
		t.Fatalf("back-date sandbox row: %v", err)
	}

	before, err := store.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox before resume claim: %v", err)
	}
	if time.Since(before.LastSeenAt.Time) < longAgo-time.Second {
		t.Fatalf("sandbox last_seen_at not back-dated as expected: %v", before.LastSeenAt.Time)
	}

	// The resume-style claim: planResume/planFreshSpawn/planRestore all
	// reuse this EXACT SAME upsert (dispatch.go's own doc comments).
	tokenHash2 := "token-hash-resume-claim"
	after, err := store.UpsertForSpawn(ctx, sqlcgen.UpsertSandboxForSpawnParams{
		SessionID: sessionID, TokenHash: &tokenHash2,
	})
	if err != nil {
		t.Fatalf("resume-claim UpsertForSpawn: %v", err)
	}

	if after.Gen != 2 {
		t.Errorf("gen = %d, want 2 (bumped from the back-dated row's own gen 1)", after.Gen)
	}
	if after.Status != sqlcgen.SandboxStatusSpawning {
		t.Errorf("status = %s, want %s", after.Status, sqlcgen.SandboxStatusSpawning)
	}

	// The core F3 assertion: last_seen_at genuinely ADVANCED past the
	// 10-minutes-stale value it carried beforehand -- both timestamps come
	// from the SAME Postgres instance's own now(), so comparing them
	// directly against each other (rather than against this test process's
	// own wall clock, which is not guaranteed to be tightly synced with a
	// containerized Postgres's clock) is a clock-skew-proof way to prove
	// the resume-style claim is itself a fresh sign of life.
	if !after.LastSeenAt.Time.After(before.LastSeenAt.Time) {
		t.Errorf("last_seen_at = %v, want strictly after the pre-claim value %v (the resume-style claim itself must be a fresh sign of life)",
			after.LastSeenAt.Time, before.LastSeenAt.Time)
	}
	// And it's genuinely fresh, not just "later than 10 minutes ago" --
	// comfortably below the 10-minute back-dating window, with a wide
	// margin for any test-runner/Postgres-container clock skew.
	if time.Since(after.LastSeenAt.Time) > longAgo/2 {
		t.Errorf("last_seen_at = %v is not fresh (more than %v old)", after.LastSeenAt.Time, longAgo/2)
	}

	// created_at is NOT reset by this upsert (only ever set on the
	// original INSERT) -- still back-dated, exactly as the audit finding's
	// own "created_at/last_seen_at never reset" description of the bug
	// implies createdAt alone was never going to be enough. Compared
	// against before.CreatedAt (same Postgres instance) rather than this
	// test process's own clock, for the same clock-skew-proofing reason.
	if !after.CreatedAt.Time.Equal(before.CreatedAt.Time) {
		t.Errorf("created_at = %v, want unchanged from the back-dated value %v (never reset by UpsertSandboxForSpawn)",
			after.CreatedAt.Time, before.CreatedAt.Time)
	}

	// Now prove the DOMAIN-level payoff: a concurrent second actor's own
	// EvaluateSpawnDecision, reading this row immediately after the resume
	// claim, genuinely Skips instead of fresh-spawning a duplicate
	// provider sandbox -- because sinceLastSignOfLife is now measured from
	// the fresh last_seen_at just persisted, not from the stale
	// created_at.
	cfg := sandbox.SpawnConfig{
		Cooldown:        30 * time.Second,
		ReadyWait:       60 * time.Second,
		SpawningTimeout: platform.DefaultTimeouts().SpawnStuckTimeout,
	}
	concurrentRead := sandbox.SpawnState{
		Status:     sandbox.State(after.Status),
		CreatedAt:  after.CreatedAt.Time,
		LastSeenAt: after.LastSeenAt.Time,
	}
	action := sandbox.EvaluateSpawnDecision(concurrentRead, cfg, time.Now(), false, false)
	if action.Kind != sandbox.SpawnActionSkip {
		t.Errorf("EvaluateSpawnDecision(post-resume-claim row) = %s, want %s (a concurrent second actor must no-op via the SpawningTimeout guard, not fresh-spawn a duplicate sandbox)",
			action.Kind, sandbox.SpawnActionSkip)
	}

	// Sanity check reproducing the PRE-FIX bug directly: the same
	// post-claim row (Status now 'spawning', per the resume claim that
	// just landed) but with CreatedAt/LastSeenAt left at their ORIGINAL
	// 10-minutes-stale values -- i.e. exactly what UpsertSandboxForSpawn
	// used to leave behind before this fix (it bumped status/gen but never
	// touched last_seen_at/created_at). Against that unpatched shape, the
	// guard genuinely does NOT skip -- proving this test's back-dating
	// setup reproduces the bug's own precondition, not a vacuously-true
	// assertion, and that resetting last_seen_at is what actually closes
	// the gap.
	preFixShapeRead := sandbox.SpawnState{
		Status:     sandbox.State(after.Status),
		CreatedAt:  before.CreatedAt.Time,
		LastSeenAt: before.LastSeenAt.Time,
	}
	preFixAction := sandbox.EvaluateSpawnDecision(preFixShapeRead, cfg, time.Now(), false, false)
	if preFixAction.Kind == sandbox.SpawnActionSkip {
		t.Errorf("EvaluateSpawnDecision(pre-fix-shaped row: spawning status, 10min-stale timestamps) = %s, want != %s (this must reproduce the bug: a concurrent second actor would have fresh-spawned a duplicate sandbox)",
			preFixAction.Kind, sandbox.SpawnActionSkip)
	}
}
