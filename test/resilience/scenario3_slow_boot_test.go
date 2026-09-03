//go:build integration

// Resilience scenario #3 (§9.3, docs/IMPLEMENTATION_PLAN.md row 30):
// "Slow boot (inject 5-min delay in deps install) -> boot_progress keeps
// session alive; no false kill." See test/resilience/README.md's own #3
// entry for why this needed its own end-to-end proof: only narrow,
// domain-decision-level coverage existed before this file
// (TestEvaluateConnectingTimeout, internal/domain/sandbox/liveness_test.go;
// TestConnectingDeadlineHandoff_ToLivenessCheck, internal/app/sessionactor/
// timerfired_integration_test.go) -- neither drives a REAL sustained
// slow-boot survival across repeated pings.
//
// No genuine multi-package orchestration is required here (the whole
// mechanism -- handleSandboxEvent bumping last_seen_at on every recognized
// event, handleConnectingDeadlineTimer's own self-re-arming poll -- lives
// entirely inside internal/app/sessionactor, already real, already-working
// code), but this fits naturally in test/resilience since the harness
// already exists and all this scenario needs on top of it is a SHORT
// platform.Timeouts override, applied to h.Timeouts before constructing the
// registry for this test only (Harness.Timeouts is a plain exported field;
// sessionactor.NewRegistry never calls Timeouts.Validate(), so a short
// struct-literal override needs no cross-field-invariant massaging) --
// newHarness's own defaults are untouched for every other scenario in this
// package.
//
// Sequencing (mirrors TestConnectingDeadlineHandoff_ToLivenessCheck's own
// setup/assertion precedent throughout, read that test first):
//  1. Seed a session + a sandbox row in Booting, and arm
//     TimerConnectingDeadline at now+FirstConnectBudget directly via
//     h.Timers.Upsert -- mirroring tryPlanSpawn's own arm-at-spawn-time
//     write (dispatch.go) without needing to drive a real spawn.
//  2. Drive at least 4 rounds of: sleep ~half of SteadyHeartbeatBudget,
//     send a real boot_progress SandboxEvent directly to the actor, then
//     send a real TimerFired{Name: TimerConnectingDeadline} the same way
//     (mirroring TestConnectingDeadlineHandoff_ToLivenessCheck's own
//     a.Send(ctx, SandboxEvent{...}) pattern) -- forcing a real, immediate
//     re-evaluation rather than waiting on a real timer pump. After each
//     round, confirm from Postgres that the sandbox is STILL Booting (never
//     Suspect/Failed) and that connecting_deadline is still armed, with a
//     genuinely NEW fires_at (proving handleConnectingDeadlineTimer's own
//     real !result.IsTimedOut re-arm branch actually ran that round, not
//     merely that the row was never touched).
//  3. Assert the CUMULATIVE elapsed real wall-clock time across all rounds
//     genuinely exceeds FirstConnectBudget -- proving this test really
//     exercises "kept alive by repeated pings", not merely "happened to
//     finish within the first budget window regardless".
//  4. Finally, let the sandbox reach Ready via a real heartbeat (the exact
//     Booting->Ready trigger TestConnectingDeadlineHandoff_ToLivenessCheck
//     proves in isolation) and confirm connecting_deadline hands off
//     cleanly (deletes itself) while liveness_check takes over -- re-using
//     that same precedent's own tail assertions, so this test also proves
//     the slow boot eventually finishing is not itself broken by having
//     survived so many repeated pings first.
package resilience_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/sessionactor"
)

func TestResilienceScenario3_SlowBoot_SurvivesRepeatedBootProgressPings_NeverFalselyKilled(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)

	// Short, test-scale overrides -- only these three fields matter for
	// this code path (confirmed by direct reading of
	// handleConnectingDeadlineTimer/EvaluateConnectingTimeout before this
	// test was written): FirstConnectBudget governs the FIRST evaluation
	// (before any sign of life), SteadyHeartbeatBudget governs every
	// evaluation after the first boot_progress bumps last_seen_at, and
	// InactivityMinCheckInterval is what handleConnectingDeadlineTimer's
	// own "not yet timed out" branch re-arms with (a short recheck poll,
	// not the budget itself). Assigned before constructing the registry
	// below, for this test only -- newHarness's own defaults are untouched
	// for every other scenario in this package.
	h.Timeouts.FirstConnectBudget = 500 * time.Millisecond
	h.Timeouts.SteadyHeartbeatBudget = 300 * time.Millisecond
	h.Timeouts.InactivityMinCheckInterval = 50 * time.Millisecond

	sessionID := h.CreateSession(ctx, t)

	if _, err := h.Sandboxes.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := h.Sandboxes.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusBooting,
	}); err != nil {
		t.Fatalf("move sandbox to booting: %v", err)
	}

	// Mirrors tryPlanSpawn's own arm-at-spawn-time write (dispatch.go),
	// seeded directly here rather than via a real spawn -- provider/
	// commander are irrelevant to what this scenario proves, exactly like
	// TestConnectingDeadlineHandoff_ToLivenessCheck's own identical choice.
	if _, err := h.Timers.Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: sessionID,
		Name:      sessionactor.TimerConnectingDeadline,
		FiresAt:   pgtype.Timestamptz{Time: time.Now().Add(h.Timeouts.FirstConnectBudget), Valid: true},
	}); err != nil {
		t.Fatalf("arm connecting_deadline timer: %v", err)
	}

	registry := h.NewRegistry(ctx, t)
	t.Cleanup(func() { _ = registry.Shutdown() })

	a, err := registry.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	const rounds = 4
	sleepPerRound := h.Timeouts.SteadyHeartbeatBudget / 2
	start := time.Now()

	for i := 0; i < rounds; i++ {
		time.Sleep(sleepPerRound)

		before, err := h.Timers.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: sessionactor.TimerConnectingDeadline})
		if err != nil {
			t.Fatalf("round %d: get connecting_deadline timer before: %v", i, err)
		}

		msgID := fmt.Sprintf("boot-progress-%d", i)
		raw := []byte(fmt.Sprintf(
			`{"type":"boot_progress","messageId":%q,"sessionId":%q,"gen":1,"phase":"web:booting","timestamp":%q}`,
			msgID, sessionID.String(), time.Now().Format(time.RFC3339Nano),
		))
		reply := make(chan sessionactor.SandboxEventOutcome, 1)
		if err := a.Send(ctx, sessionactor.SandboxEvent{Type: "boot_progress", Gen: 1, MessageID: msgID, Raw: raw, Reply: reply}); err != nil {
			t.Fatalf("round %d: send boot_progress: %v", i, err)
		}
		select {
		case outcome := <-reply:
			if !outcome.Persisted {
				t.Fatalf("round %d: boot_progress Persisted = false, want true", i)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: timed out waiting for boot_progress outcome", i)
		}

		if err := a.Send(ctx, sessionactor.TimerFired{Name: sessionactor.TimerConnectingDeadline}); err != nil {
			t.Fatalf("round %d: send TimerFired connecting_deadline: %v", i, err)
		}

		// Poll for a genuinely NEW fires_at -- the positive signal that
		// handleConnectingDeadlineTimer's own transact (triggered by the
		// TimerFired just sent, processed asynchronously on the actor's
		// own mailbox goroutine) has actually run and taken its
		// re-arm-not-kill branch this round.
		waitUntil(t, 5*time.Second, func() bool {
			got, err := h.Timers.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: sessionactor.TimerConnectingDeadline})
			return err == nil && !got.FiresAt.Time.Equal(before.FiresAt.Time)
		})

		gotSandbox, err := h.Sandboxes.Get(ctx, sessionID)
		if err != nil {
			t.Fatalf("round %d: get sandbox: %v", i, err)
		}
		if gotSandbox.Status != sqlcgen.SandboxStatusBooting {
			t.Fatalf("round %d: sandbox status = %s, want still %s (a repeated slow-boot ping must never falsely kill it)",
				i, gotSandbox.Status, sqlcgen.SandboxStatusBooting)
		}
	}

	elapsed := time.Since(start)
	if elapsed <= h.Timeouts.FirstConnectBudget {
		t.Fatalf("test's own cumulative elapsed time = %v, want > FirstConnectBudget (%v) -- this scenario's whole point is proving survival PAST what a naive single fixed deadline would have allowed",
			elapsed, h.Timeouts.FirstConnectBudget)
	}

	// --- Finally: let the sandbox reach Ready via a real heartbeat (the
	// SAME Booting->Ready trigger TestConnectingDeadlineHandoff_ToLivenessCheck
	// proves in isolation), confirming connecting_deadline hands off
	// cleanly once it is no longer relevant. ---
	hbRaw := []byte(fmt.Sprintf(`{"type":"heartbeat","messageId":"hb-final","sessionId":%q,"gen":1,"conversationId":null,"lastBootPhase":null}`, sessionID.String()))
	hbReply := make(chan sessionactor.SandboxEventOutcome, 1)
	if err := a.Send(ctx, sessionactor.SandboxEvent{Type: "heartbeat", Gen: 1, MessageID: "hb-final", Raw: hbRaw, Reply: hbReply}); err != nil {
		t.Fatalf("send final heartbeat: %v", err)
	}
	select {
	case outcome := <-hbReply:
		if !outcome.Persisted {
			t.Fatal("final heartbeat Persisted = false, want true")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for final heartbeat outcome")
	}

	gotReady, err := h.Sandboxes.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if gotReady.Status != sqlcgen.SandboxStatusReady {
		t.Fatalf("status after final heartbeat = %s, want %s", gotReady.Status, sqlcgen.SandboxStatusReady)
	}

	if err := a.Send(ctx, sessionactor.TimerFired{Name: sessionactor.TimerConnectingDeadline}); err != nil {
		t.Fatalf("send final TimerFired connecting_deadline: %v", err)
	}
	waitUntil(t, 5*time.Second, func() bool {
		_, err := h.Timers.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: sessionactor.TimerConnectingDeadline})
		return errors.Is(err, pgx.ErrNoRows)
	})

	if _, err := h.Timers.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: sessionactor.TimerLivenessCheck}); err != nil {
		t.Errorf("get liveness_check timer after handoff: %v (must be armed once Ready)", err)
	}
}
