//go:build integration

// This file proves ReconcileDemotions (§30.4): the process-wide half that
// actually terminates a real cloud resource once internal/app/
// repodemotion.Sweep has flagged a sandbox's own demotion_terminate_
// requested_at column -- mirrors reconciler_integration_test.go's own
// conventions exactly (same fakeReconcileProvider, same createTestSession/
// createLiveSandbox precedent, same shared testcontainers pool).
package reconciler_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/reconciler"
	"github.com/narvidev/narvi/internal/platform"
)

// createLiveSandboxForSession mirrors createLiveSandbox exactly but
// returns the session id, which this file's own tests need to flag
// demotion termination against a specific row.
func createLiveSandboxForSession(ctx context.Context, t *testing.T, pool *pgxpool.Pool, providerID string, status sqlcgen.SandboxStatus) pgtype.UUID {
	t.Helper()
	sessionID := createTestSession(ctx, t, pool)
	store := narvipg.NewSandboxStore(pool)

	if _, err := store.Create(ctx, sessionID); err != nil {
		t.Fatalf("create test sandbox: %v", err)
	}
	if _, err := store.UpdateProviderID(ctx, sqlcgen.UpdateSandboxProviderIDParams{
		SessionID:  sessionID,
		ProviderID: &providerID,
	}); err != nil {
		t.Fatalf("set test sandbox provider id: %v", err)
	}
	if _, err := store.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID,
		Status:    status,
	}); err != nil {
		t.Fatalf("move test sandbox to %s: %v", status, err)
	}
	return sessionID
}

// TestReconcileDemotions_TerminatesFlaggedSandboxAndClearsRequest proves
// the core round trip: a sandbox flagged for demotion termination gets a
// real StopSandbox call, and the flag is cleared afterward -- a SECOND
// tick must never call StopSandbox for it again.
func TestReconcileDemotions_TerminatesFlaggedSandboxAndClearsRequest(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sandboxes := narvipg.NewSandboxStore(pool)

	demotedSessionID := createLiveSandboxForSession(ctx, t, pool, "demoted-1", sqlcgen.SandboxStatusReady)
	untouchedSessionID := createLiveSandboxForSession(ctx, t, pool, "untouched-1", sqlcgen.SandboxStatusReady)

	if _, err := sandboxes.MarkDemotionTerminationRequested(ctx, demotedSessionID); err != nil {
		t.Fatalf("flag demoted-1 for termination: %v", err)
	}

	provider := &fakeReconcileProvider{}
	r, err := reconciler.NewReconciler(sandboxes, narvipg.NewRepoSettingsStore(pool), provider, platform.DefaultTimeouts())
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}

	if err := r.ReconcileDemotions(ctx); err != nil {
		t.Fatalf("ReconcileDemotions (tick 1): %v", err)
	}

	if got := provider.stopCallCount(); got != 1 {
		t.Fatalf("StopSandbox call count after tick 1 = %d, want 1 (stoppedProviderIDs=%v)", got, provider.stoppedProviderIDs())
	}
	stopped := provider.stoppedProviderIDs()
	if stopped[0] != "demoted-1" {
		t.Errorf("StopSandbox was called for %q, want %q", stopped[0], "demoted-1")
	}

	row, err := sandboxes.Get(ctx, demotedSessionID)
	if err != nil {
		t.Fatalf("get demoted sandbox: %v", err)
	}
	if row.DemotionTerminateRequestedAt.Valid {
		t.Error("demotion_terminate_requested_at still set after a successful StopSandbox, want cleared")
	}

	untouchedRow, err := sandboxes.Get(ctx, untouchedSessionID)
	if err != nil {
		t.Fatalf("get untouched sandbox: %v", err)
	}
	if untouchedRow.DemotionTerminateRequestedAt.Valid {
		t.Error("demotion_terminate_requested_at set on a sandbox nothing ever flagged")
	}

	// A second tick must find nothing left to do.
	if err := r.ReconcileDemotions(ctx); err != nil {
		t.Fatalf("ReconcileDemotions (tick 2): %v", err)
	}
	if got := provider.stopCallCount(); got != 1 {
		t.Errorf("StopSandbox call count after tick 2 = %d, want still 1 (the cleared flag must not be re-actioned)", got)
	}
}

// TestReconcileDemotions_FailedStopSandbox_LeavesFlaggedForRetry mirrors
// ReconcileOnce's own per-orphan error-isolation precedent
// (TestReconcileOnce_OneStopSandboxFailureDoesNotAbortBatch): a failed
// StopSandbox call must leave the sandbox flagged (so the next tick
// retries), never silently drop it, and must not increment the
// demotions_terminated counter.
func TestReconcileDemotions_FailedStopSandbox_LeavesFlaggedForRetry(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sandboxes := narvipg.NewSandboxStore(pool)

	sessionID := createLiveSandboxForSession(ctx, t, pool, "will-fail-1", sqlcgen.SandboxStatusReady)
	if _, err := sandboxes.MarkDemotionTerminationRequested(ctx, sessionID); err != nil {
		t.Fatalf("flag for termination: %v", err)
	}

	provider := &fakeReconcileProvider{
		stopErrFor: map[string]error{"will-fail-1": errors.New("provider: stop failed")},
	}
	r, err := reconciler.NewReconciler(sandboxes, narvipg.NewRepoSettingsStore(pool), provider, platform.DefaultTimeouts())
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}

	if err := r.ReconcileDemotions(ctx); err != nil {
		t.Fatalf("ReconcileDemotions: %v", err)
	}
	if got := provider.stopCallCount(); got != 1 {
		t.Fatalf("StopSandbox call count = %d, want 1 (attempted once, even though it failed)", got)
	}

	row, err := sandboxes.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if !row.DemotionTerminateRequestedAt.Valid {
		t.Error("demotion_terminate_requested_at cleared despite a FAILED StopSandbox call, want it left set for the next tick to retry")
	}

	// A retry tick must attempt StopSandbox again for the still-flagged row.
	if err := r.ReconcileDemotions(ctx); err != nil {
		t.Fatalf("ReconcileDemotions (retry): %v", err)
	}
	if got := provider.stopCallCount(); got != 2 {
		t.Errorf("StopSandbox call count after retry tick = %d, want 2 (the still-flagged row must be retried)", got)
	}
}

// TestReconcileDemotions_NoProviderIDYet_LeavesFlaggedWithoutCallingStop
// proves the documented in-flight-spawn race: a live-status sandbox with
// no provider_id yet (a spawn attempt genuinely in progress) must be
// left flagged for a later tick, never call StopSandbox with an empty ref.
func TestReconcileDemotions_NoProviderIDYet_LeavesFlaggedWithoutCallingStop(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sandboxes := narvipg.NewSandboxStore(pool)

	sessionID := createTestSession(ctx, t, pool)
	if _, err := sandboxes.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	// Deliberately no UpdateProviderID call -- status defaults to
	// 'spawning' (a live status) via Create, matching a genuinely
	// in-flight spawn's own real shape.
	if _, err := sandboxes.MarkDemotionTerminationRequested(ctx, sessionID); err != nil {
		t.Fatalf("flag for termination: %v", err)
	}

	provider := &fakeReconcileProvider{}
	r, err := reconciler.NewReconciler(sandboxes, narvipg.NewRepoSettingsStore(pool), provider, platform.DefaultTimeouts())
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}

	if err := r.ReconcileDemotions(ctx); err != nil {
		t.Fatalf("ReconcileDemotions: %v", err)
	}
	if got := provider.stopCallCount(); got != 0 {
		t.Errorf("StopSandbox call count = %d, want 0 (no provider_id yet -- nothing real to stop)", got)
	}

	row, err := sandboxes.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if !row.DemotionTerminateRequestedAt.Valid {
		t.Error("demotion_terminate_requested_at cleared with no provider_id ever stopped, want it left set for a later tick")
	}
}

// TestReconcileDemotions_RecoversAnObligationNoSweepCleared is the
// recoverability §30.4's demotion requirement was missing.
//
// The obligation used to live ONLY in the difference between a
// pre-transaction read and the declared value — and the commit destroyed
// it. A sweep that failed on its third sandbox abandoned the rest with
// nothing recording they were owed a termination, and the operator's
// obvious response (re-run the manifest) found false→false, reported
// success, and swept nothing. Those sandboxes kept a usable write
// credential for the full TTL window.
//
// This drives the state that failure leaves behind: the flip stamped the
// obligation, no sweep cleared it. A tick must pick it up, flag the
// repo's live sandbox, and only then clear.
func TestReconcileDemotions_RecoversAnObligationNoSweepCleared(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sandboxes := narvipg.NewSandboxStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	const repoFullName = "acme/abandoned-sweep"
	sessionID := createLiveSandboxForSession(ctx, t, pool, "abandoned-1", sqlcgen.SandboxStatusReady)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET repos = $2 WHERE id = $1`, sessionID,
		[]byte(`[{"name":"repo","url":"https://github.com/`+repoFullName+`.git","branch":"main"}]`)); err != nil {
		t.Fatalf("set session repos: %v", err)
	}

	// A genuine live -> shadow transition: the second call is the
	// demotion, and its own statement stamps the obligation.
	if _, err := repoSettings.UpsertLiveEgressEnabled(ctx, repoFullName, true); err != nil {
		t.Fatalf("promote repo: %v", err)
	}
	if _, err := repoSettings.UpsertLiveEgressEnabled(ctx, repoFullName, false); err != nil {
		t.Fatalf("demote repo: %v", err)
	}

	owed, err := repoSettings.ListOwedDemotionSweep(ctx, 10)
	if err != nil {
		t.Fatalf("list owed: %v", err)
	}
	if len(owed) != 1 {
		t.Fatalf("repos owed a demotion sweep = %d, want 1: the flip itself must record the obligation, or a failed sweep is unrecoverable", len(owed))
	}

	provider := &fakeReconcileProvider{}
	r, err := reconciler.NewReconciler(sandboxes, repoSettings, provider, platform.DefaultTimeouts())
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}
	if err := r.ReconcileDemotions(ctx); err != nil {
		t.Fatalf("ReconcileDemotions: %v", err)
	}

	if got := provider.stopCallCount(); got != 1 {
		t.Errorf("StopSandbox call count = %d, want 1: the recovered sweep must flag this repo's live sandbox and the same tick must terminate it", got)
	}
	after, err := repoSettings.ListOwedDemotionSweep(ctx, 10)
	if err != nil {
		t.Fatalf("list owed after: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("repos still owed a sweep = %d, want 0 after a clean sweep", len(after))
	}
}

// TestUpsertLiveEgressEnabled_NonTransitionsOweNoSweep pins the other
// half: only a genuine true->false transition owes a sweep. A repo whose
// first-ever write is false has never been live, so no sandbox of it ever
// held more than a read-only credential, and stamping an obligation there
// would make every fresh onboarding look like a demotion.
func TestUpsertLiveEgressEnabled_NonTransitionsOweNoSweep(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	if _, err := repoSettings.UpsertLiveEgressEnabled(ctx, "acme/born-shadow", false); err != nil {
		t.Fatalf("first write false: %v", err)
	}
	if _, err := repoSettings.UpsertLiveEgressEnabled(ctx, "acme/born-shadow", false); err != nil {
		t.Fatalf("re-affirm false: %v", err)
	}
	if _, err := repoSettings.UpsertLiveEgressEnabled(ctx, "acme/stays-live", true); err != nil {
		t.Fatalf("first write true: %v", err)
	}
	if _, err := repoSettings.UpsertLiveEgressEnabled(ctx, "acme/stays-live", true); err != nil {
		t.Fatalf("re-affirm true: %v", err)
	}

	owed, err := repoSettings.ListOwedDemotionSweep(ctx, 10)
	if err != nil {
		t.Fatalf("list owed: %v", err)
	}
	if len(owed) != 0 {
		t.Errorf("repos owed a demotion sweep = %d, want 0: no true->false transition happened", len(owed))
	}
}
