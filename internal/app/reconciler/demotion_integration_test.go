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

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/reconciler"
	"github.com/khazaddev/narvi/internal/platform"
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
	r, err := reconciler.NewReconciler(sandboxes, provider, platform.DefaultTimeouts())
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
	r, err := reconciler.NewReconciler(sandboxes, provider, platform.DefaultTimeouts())
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
	r, err := reconciler.NewReconciler(sandboxes, provider, platform.DefaultTimeouts())
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
