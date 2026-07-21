//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file proves Step 24's ("two-phase terminalization") own recovery
// half: §3.2's "any liveness signal during grace returns to previous
// state" rule, wired into handleSandboxEvent (sandboxevent.go) -- see that
// file's own top comment for the full mechanics this exercises.

// seedSuspectSandboxWithPreSuspectStatus creates a sandbox row and moves it
// directly to Suspect carrying a real pre_suspect_status -- the exact
// (status, pre_suspect_status) precondition transitionSandboxToSuspect
// (timerfired.go) leaves behind, mirroring seedStoppedSandboxWithSnapshot's
// own shape (dispatch_integration_test.go) for THIS Step's own precondition
// instead of a Stopped+snapshot one. Returns the store so callers can
// re-Get afterward.
func seedSuspectSandboxWithPreSuspectStatus(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, preSuspectStatus sqlcgen.SandboxStatus) *narvipg.SandboxStore {
	t.Helper()
	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatusToSuspect(ctx, sqlcgen.UpdateSandboxStatusToSuspectParams{
		SessionID:        sessionID,
		PreSuspectStatus: &preSuspectStatus,
	}); err != nil {
		t.Fatalf("move sandbox to suspect with pre_suspect_status=%s: %v", preSuspectStatus, err)
	}
	return sandboxStore
}

// heartbeatRaw builds a minimal, schema-valid heartbeat wire payload with a
// nil lastBootPhase -- used throughout this file as "any recognized
// inbound event" (§3.2's own wording), deliberately never execution_complete
// (see TestHandleSandboxEvent_LateExecutionComplete_RecoversSandboxTurnAndSession's
// own doc comment for why that one test alone needs execution_complete,
// and why every OTHER test here avoids it).
func heartbeatRaw(messageID string) json.RawMessage {
	return json.RawMessage(`{"type":"heartbeat","messageId":"` + messageID + `","sessionId":"s","gen":1,"conversationId":null,"lastBootPhase":null}`)
}

// TestHandleSandboxEvent_SuspectRecovery_ReturnsToPreSuspectStatus proves
// the core Step 24 mechanism in isolation: a Suspect sandbox seeded with a
// real pre_suspect_status (Ready) receiving ANY recognized inbound event
// (a plain heartbeat) recovers to that exact pre-suspect status,
// pre_suspect_status is cleared back to NULL, TimerTerminalGrace is
// deleted (confirmed directly via the timer store, not inferred), and
// last_seen_at is bumped -- the event itself IS the liveness signal §3.2
// names.
func TestHandleSandboxEvent_SuspectRecovery_ReturnsToPreSuspectStatus(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := seedSuspectSandboxWithPreSuspectStatus(ctx, t, pool, sessionID, sqlcgen.SandboxStatusReady)

	timerStore := narvipg.NewTimerStore(pool)
	if _, err := timerStore.Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: sessionID, Name: TimerTerminalGrace,
		FiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true},
	}); err != nil {
		t.Fatalf("arm terminal_grace: %v", err)
	}

	before, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if before.PreSuspectStatus == nil || *before.PreSuspectStatus != sqlcgen.SandboxStatusReady {
		t.Fatalf("precondition: pre_suspect_status = %v, want %s", before.PreSuspectStatus, sqlcgen.SandboxStatusReady)
	}

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })
	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	outcome := sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "heartbeat", Gen: int(before.Gen), MessageID: "hb-recover-1", Raw: heartbeatRaw("hb-recover-1"),
	})
	if !outcome.Persisted {
		t.Error("outcome.Persisted = false, want true")
	}

	got, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.Status != sqlcgen.SandboxStatusReady {
		t.Errorf("sandbox status = %s, want %s (recovered to its pre-suspect status)", got.Status, sqlcgen.SandboxStatusReady)
	}
	if got.PreSuspectStatus != nil {
		t.Errorf("pre_suspect_status = %v, want nil (cleared on recovery)", *got.PreSuspectStatus)
	}
	if !got.LastSeenAt.Valid {
		t.Error("last_seen_at not set after the recovering event (it IS the liveness signal)")
	}

	if _, err := timerStore.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: TimerTerminalGrace}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("terminal_grace timer get = %v, want pgx.ErrNoRows (recovery must delete it)", err)
	}
}

// TestHandleSandboxEvent_LateExecutionComplete_RecoversSandboxTurnAndSession
// is THE scenario Step 24 ("two-phase terminalization") exists for,
// matching §9.3 scenario #4's own literal description ("execution_complete
// arrives AFTER terminalization -> state reconciled"): a Processing turn +
// a sandbox already driven to Suspect (directly seeded with a real
// pre_suspect_status, mirroring seedStoppedSandboxWithSnapshot's own
// precedent of a surgical precondition seed rather than driving the full
// watchdog machinery) receives a genuinely late execution_complete for the
// CURRENT gen while still Suspect -- in ONE event/one commit: the sandbox
// recovers (Suspect -> its pre-suspect status), the turn completes
// (Processing -> Completed), AND the session's own derived status
// re-computes to Completed. All three are proved by querying Postgres
// directly for each, not by trusting any single signal.
//
// pre_suspect_status is deliberately Connecting here, NOT Ready --
// recovering to Ready is already proved as the recovery target by
// TestHandleSandboxEvent_SuspectRecovery_ReturnsToPreSuspectStatus above,
// using a plain heartbeat. Using Ready HERE, combined with a REAL
// execution_complete, would ALSO satisfy triggerSnapshotBestEffort's own
// (Step 22, "snapshots & restore") independent "status == Ready" post-
// turn-snapshot precondition -- a real, correct, but entirely SEPARATE
// feature this test is not about -- and its own asynchronous follow-up
// writes to the same sandboxes row (fired from handleSandboxEvent's own
// post-commit block, AFTER this event's own outcome reply is already
// sent, per that function's own doc comment on reply-before-side-effects
// ordering) would then race this test's own post-send assertions on that
// same row. Choosing a non-Ready pre-suspect status keeps
// triggerSnapshotBestEffort's own guard a deterministic no-op regardless
// of timing, isolating Step 24's own recovery+reconciliation behavior from
// Step 22's.
func TestHandleSandboxEvent_LateExecutionComplete_RecoversSandboxTurnAndSession(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := seedSuspectSandboxWithPreSuspectStatus(ctx, t, pool, sessionID, sqlcgen.SandboxStatusConnecting)

	timerStore := narvipg.NewTimerStore(pool)
	if _, err := timerStore.Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: sessionID, Name: TimerTerminalGrace,
		FiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Minute), Valid: true},
	}); err != nil {
		t.Fatalf("arm terminal_grace: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)
	processingTurn := createProcessingTurn(ctx, t, turnStore, sessionID)
	// Arm turn_deadline exactly like a real dispatch would have, so this
	// test also proves it gets cleaned up on real completion -- mirrors
	// TestHandleSandboxEvent_ExecutionCompleteCompleted_CompletesTurnAndSendsPush's
	// own precedent (pushpr_integration_test.go).
	if _, err := timerStore.Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: sessionID, Name: TimerTurnDeadline,
		FiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("arm turn_deadline: %v", err)
	}

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })
	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	outcome := sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})
	if !outcome.Persisted {
		t.Error("outcome.Persisted = false, want true")
	}
	if outcome.AckID == "" {
		t.Error("outcome.AckID is empty, want a real ack for this critical event")
	}

	// --- (1) the sandbox recovered: Suspect -> its pre-suspect status. ---
	gotSandbox, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if gotSandbox.Status != sqlcgen.SandboxStatusConnecting {
		t.Errorf("sandbox status = %s, want %s (recovered to its pre-suspect status)", gotSandbox.Status, sqlcgen.SandboxStatusConnecting)
	}
	if gotSandbox.PreSuspectStatus != nil {
		t.Errorf("pre_suspect_status = %v, want nil (cleared on recovery)", *gotSandbox.PreSuspectStatus)
	}
	if _, err := timerStore.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: TimerTerminalGrace}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("terminal_grace timer get = %v, want pgx.ErrNoRows (recovery must delete it)", err)
	}

	// --- (2) the turn completed, Processing -> Completed, in the SAME
	// event. ---
	gotTurn, err := turnStore.Get(ctx, processingTurn.ID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if gotTurn.Status != sqlcgen.TurnStatusCompleted {
		t.Errorf("turn status = %s, want %s", gotTurn.Status, sqlcgen.TurnStatusCompleted)
	}
	if !gotTurn.CompletedAt.Valid {
		t.Error("turn completed_at not set")
	}
	if _, err := timerStore.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: TimerTurnDeadline}); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("turn_deadline timer get = %v, want pgx.ErrNoRows (deleted on real completion)", err)
	}

	// --- (3) the session's own derived status re-computed to Completed. ---
	sessionStore := narvipg.NewSessionStore(pool)
	gotSession, err := sessionStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if gotSession.Status != sqlcgen.SessionStatusCompleted {
		t.Errorf("session status = %s, want %s", gotSession.Status, sqlcgen.SessionStatusCompleted)
	}
}

// TestHandleSandboxEvent_SuspectNoPreSuspectStatus_NoRecoveryAttempted
// proves the defensive fallback named in this Step's own brief: a Suspect
// sandbox with NO pre_suspect_status recorded -- practically unreachable
// in production, since transitionSandboxToSuspect (timerfired.go) always
// sets it in the SAME write that enters Suspect, but seeded directly here
// to prove the fallback is a safe no-op rather than a panic or a spurious
// transition -- receiving a real inbound event stays Suspect: still
// persisted, liveness still bumped, no recovery attempted.
func TestHandleSandboxEvent_SuspectNoPreSuspectStatus_NoRecoveryAttempted(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	// The plain, unmodified UpdateStatus query (not UpdateStatusToSuspect)
	// -- deliberately leaves pre_suspect_status NULL, exactly like every
	// pre-Step-24 caller of it always has.
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID,
		Status:    sqlcgen.SandboxStatusSuspect,
	}); err != nil {
		t.Fatalf("move sandbox to suspect (no pre_suspect_status): %v", err)
	}

	before, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if before.PreSuspectStatus != nil {
		t.Fatalf("precondition: pre_suspect_status = %v, want nil", *before.PreSuspectStatus)
	}

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })
	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	outcome := sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "heartbeat", Gen: int(before.Gen), MessageID: "hb-nopre-1", Raw: heartbeatRaw("hb-nopre-1"),
	})
	if !outcome.Persisted {
		t.Error("outcome.Persisted = false, want true")
	}

	got, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.Status != sqlcgen.SandboxStatusSuspect {
		t.Errorf("sandbox status = %s, want %s (no pre_suspect_status -> no recovery)", got.Status, sqlcgen.SandboxStatusSuspect)
	}
	if !got.LastSeenAt.Valid {
		t.Error("last_seen_at not set (liveness must still bump even without recovery)")
	}
}

// TestHandleSandboxEvent_FailedSandbox_NoRecoveryAttemptedEvenWithStalePreSuspectStatus
// proves handleSandboxEvent's own recovery branch is gated on status ==
// Suspect specifically, not on IsDeadSandboxStatus generally: a sandbox
// already terminalized to Failed -- carrying a pre_suspect_status left
// over from before terminal_grace expired (handleTerminalGraceTimer,
// timerfired.go, deliberately does NOT clear it -- see migrations/000023's
// own doc comment for why that is harmless) -- must never be "recovered"
// back to a live state just because this column happens to still be
// non-NULL. In production, wshub's own 410-on-IsDeadSandboxStatus already
// prevents a stale connection from ever reaching this handler again for a
// Failed row (internal/adapters/inbound/wshub/sandbox.go) -- this test
// proves handleSandboxEvent itself has no SEPARATE bug that would let an
// old, still-open connection (bypassing wshub entirely, exactly like
// every test in this file already does) trigger a recovery anyway.
func TestHandleSandboxEvent_FailedSandbox_NoRecoveryAttemptedEvenWithStalePreSuspectStatus(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := seedSuspectSandboxWithPreSuspectStatus(ctx, t, pool, sessionID, sqlcgen.SandboxStatusReady)
	// Simulate handleTerminalGraceTimer's own real write (timerfired.go):
	// Suspect -> Failed via the plain, unmodified UpdateStatus query, which
	// deliberately leaves pre_suspect_status untouched (a stale, no-longer-
	// meaningful value once terminalized).
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID,
		Status:    sqlcgen.SandboxStatusFailed,
	}); err != nil {
		t.Fatalf("move sandbox to failed: %v", err)
	}

	before, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if before.PreSuspectStatus == nil {
		t.Fatalf("precondition: pre_suspect_status = nil, want a stale non-nil value left over from before terminalization")
	}

	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })
	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	outcome := sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "heartbeat", Gen: int(before.Gen), MessageID: "hb-failed-1", Raw: heartbeatRaw("hb-failed-1"),
	})
	if !outcome.Persisted {
		t.Error("outcome.Persisted = false, want true")
	}

	got, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.Status != sqlcgen.SandboxStatusFailed {
		t.Errorf("sandbox status = %s, want %s (a Failed row must never be recovered, regardless of a stale pre_suspect_status)", got.Status, sqlcgen.SandboxStatusFailed)
	}
}
