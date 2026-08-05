//go:build integration

package workflowengine_test

import (
	"context"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/workflowengine"
	"github.com/khazaddev/narvi/internal/domain/turn"
)

// TestOnTurnCompleted_SingleStepLane_OkOutcome_CompletesRun proves the
// common-case path for review/request (§25.8: single step, no edges): a
// real execution_complete (turn.TriggerComplete) derives StepOutcomeOK,
// finishes the step-run 'completed', and -- since NextStep has no further
// step to advance to -- completes the owning run.
func TestOnTurnCompleted_SingleStepLane_OkOutcome_CompletesRun(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessions := postgres.NewSessionStore(pool)
	turns := postgres.NewTurnStore(pool)
	workflows := postgres.NewWorkflowStore(pool)

	session := newSession(t, ctx, sessions)
	row, _ := startRunAndAttachRealTurn(t, ctx, sessions, turns, workflows, session, "do the thing", nil, false)

	workflowengine.OnTurnCompleted(ctx, workflows, row.turnID, turn.TriggerComplete)

	var status string
	var outcome *string
	var finishedAt *string
	if err := pool.QueryRow(ctx, `SELECT status, outcome_status, finished_at::text FROM workflow_step_runs WHERE id = $1`, row.stepRunID.String()).
		Scan(&status, &outcome, &finishedAt); err != nil {
		t.Fatalf("query step-run: %v", err)
	}
	if status != "completed" {
		t.Errorf("step-run status = %q, want completed", status)
	}
	if outcome == nil || *outcome != "ok" {
		t.Errorf("step-run outcome_status = %v, want ok", outcome)
	}
	if finishedAt == nil {
		t.Error("step-run finished_at is NULL, want set (this attempt is terminal)")
	}

	var runStatus string
	var runFinishedAt *string
	if err := pool.QueryRow(ctx, `SELECT status, finished_at::text FROM workflow_runs WHERE id = $1`, row.runID.String()).
		Scan(&runStatus, &runFinishedAt); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "completed" {
		t.Errorf("run status = %q, want completed (single-step lane, no further step)", runStatus)
	}
	if runFinishedAt == nil {
		t.Error("run finished_at is NULL, want set")
	}
}

// TestOnTurnCompleted_SingleStepLane_FailTrigger_EscalatesRun proves the
// fail-conservative default (§25.4): a turn ending in TriggerFail (no
// posted outcome) derives StepOutcomeBlocked; with no edge wired for
// "blocked" (true of every §25.8 built-in), NextStep escalates the run to
// needs_review -- non-terminal, finished_at stays NULL (migration 000057's
// own "one notice ... stop", never "hold the session hostage" comment).
func TestOnTurnCompleted_SingleStepLane_FailTrigger_EscalatesRun(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessions := postgres.NewSessionStore(pool)
	turns := postgres.NewTurnStore(pool)
	workflows := postgres.NewWorkflowStore(pool)

	session := newSession(t, ctx, sessions)
	row, _ := startRunAndAttachRealTurn(t, ctx, sessions, turns, workflows, session, "do the thing", nil, false)

	workflowengine.OnTurnCompleted(ctx, workflows, row.turnID, turn.TriggerFail)

	var status string
	var outcome *string
	if err := pool.QueryRow(ctx, `SELECT status, outcome_status FROM workflow_step_runs WHERE id = $1`, row.stepRunID.String()).
		Scan(&status, &outcome); err != nil {
		t.Fatalf("query step-run: %v", err)
	}
	if status != "failed" {
		t.Errorf("step-run status = %q, want failed", status)
	}
	if outcome == nil || *outcome != "blocked" {
		t.Errorf("step-run outcome_status = %v, want blocked", outcome)
	}

	var runStatus string
	var runFinishedAt *string
	if err := pool.QueryRow(ctx, `SELECT status, finished_at::text FROM workflow_runs WHERE id = $1`, row.runID.String()).
		Scan(&runStatus, &runFinishedAt); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "needs_review" {
		t.Errorf("run status = %q, want needs_review (escalated, fail-conservative default)", runStatus)
	}
	if runFinishedAt != nil {
		t.Error("run finished_at is set, want NULL (needs_review is non-terminal, must not freeze the session)")
	}
}

// TestOnTurnCompleted_HITLAfterStep_MarksAwaitingDecision_RunStaysRunning
// proves §25.9's HITL gate for the ONE built-in step that carries it
// (plan lane's step 1): OnTurnCompleted never calls workflow.NextStep at
// all here -- the step-run lands in awaiting_decision (not completed), and
// the RUN's own status is completely untouched (still running), exactly
// where Step 56's own decide endpoint is meant to pick it up.
func TestOnTurnCompleted_HITLAfterStep_MarksAwaitingDecision_RunStaysRunning(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessions := postgres.NewSessionStore(pool)
	turns := postgres.NewTurnStore(pool)
	workflows := postgres.NewWorkflowStore(pool)

	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := sessions.UpdateIntentDecisionIfNull(ctx, session.ID, []byte(`{"target":"request","mode":"plan"}`)); err != nil {
		t.Fatalf("seed intent_decision: %v", err)
	}
	session, err = sessions.Get(ctx, session.ID)
	if err != nil {
		t.Fatalf("re-fetch session: %v", err)
	}

	row, res := startRunAndAttachRealTurn(t, ctx, sessions, turns, workflows, session, "draft a plan", nil, true)
	if row.stepDefID.String() != builtInPlanStep1ID {
		t.Fatalf("step-run step id = %s, want the built-in plan step 1 %s (test setup assumption)", row.stepDefID.String(), builtInPlanStep1ID)
	}
	if res.Prompt != "draft a plan" {
		t.Fatalf("res.Prompt = %q, want unchanged (test setup assumption)", res.Prompt)
	}

	workflowengine.OnTurnCompleted(ctx, workflows, row.turnID, turn.TriggerComplete)

	var status string
	var outcome *string
	var finishedAt *string
	if err := pool.QueryRow(ctx, `SELECT status, outcome_status, finished_at::text FROM workflow_step_runs WHERE id = $1`, row.stepRunID.String()).
		Scan(&status, &outcome, &finishedAt); err != nil {
		t.Fatalf("query step-run: %v", err)
	}
	if status != "awaiting_decision" {
		t.Errorf("step-run status = %q, want awaiting_decision", status)
	}
	if outcome == nil || *outcome != "ok" {
		t.Errorf("step-run outcome_status = %v, want ok (the turn itself completed normally)", outcome)
	}
	if finishedAt != nil {
		t.Error("step-run finished_at is set, want NULL (awaiting_decision is still 'live', migration 000057's own doc comment)")
	}

	var runStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow_runs WHERE id = $1`, row.runID.String()).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "running" {
		t.Errorf("run status = %q, want running (untouched -- NextStep is never consulted for a HITLAfter-gated step)", runStatus)
	}
}

// TestOnTurnCompleted_PostedOutcomeTakesPrecedenceOverImplicitDerivation
// proves the generic step-outcome-posting tool's own write
// (WorkflowStore.SetStepRunOutcome) is never clobbered by OnTurnCompleted's
// own implicit-from-trigger fallback: an outcome already posted DURING the
// turn (needs_fix, here) survives a subsequent TriggerComplete completion
// unchanged -- the COALESCE discipline both WorkflowStore.
// MarkAwaitingDecision and FinishStepRun share (workflows.sql's own doc
// comment).
func TestOnTurnCompleted_PostedOutcomeTakesPrecedenceOverImplicitDerivation(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessions := postgres.NewSessionStore(pool)
	turns := postgres.NewTurnStore(pool)
	workflows := postgres.NewWorkflowStore(pool)

	session := newSession(t, ctx, sessions)
	row, _ := startRunAndAttachRealTurn(t, ctx, sessions, turns, workflows, session, "do the thing", nil, false)

	// Simulate the posting tool having already recorded an outcome mid-turn
	// (a hypothetical non-built-in step's own agent call -- no built-in
	// does this in Step 55, but the persisted-precedence contract must
	// hold regardless of who posted it).
	rowsAffected, err := workflows.SetStepRunOutcome(ctx, row.stepRunID, "needs_fix", "found a fixable issue", nil)
	if err != nil {
		t.Fatalf("SetStepRunOutcome: %v", err)
	}
	if rowsAffected != 1 {
		t.Fatalf("SetStepRunOutcome rowsAffected = %d, want 1", rowsAffected)
	}

	// The turn itself now completes normally -- a naive implementation
	// would derive StepOutcomeOK and overwrite the already-posted
	// needs_fix; the COALESCE in FinishStepRun must prevent that.
	workflowengine.OnTurnCompleted(ctx, workflows, row.turnID, turn.TriggerComplete)

	var status string
	var outcome *string
	var summary *string
	if err := pool.QueryRow(ctx, `SELECT status, outcome_status, outcome_summary FROM workflow_step_runs WHERE id = $1`, row.stepRunID.String()).
		Scan(&status, &outcome, &summary); err != nil {
		t.Fatalf("query step-run: %v", err)
	}
	if status != "completed" {
		t.Errorf("step-run status = %q, want completed", status)
	}
	if outcome == nil || *outcome != "needs_fix" {
		t.Errorf("step-run outcome_status = %v, want needs_fix (the POSTED value, never overwritten by the implicit ok derivation)", outcome)
	}
	if summary == nil || *summary != "found a fixable issue" {
		t.Errorf("step-run outcome_summary = %v, want the posted summary preserved", summary)
	}

	// needs_fix with no wired edge (true of every §25.8 built-in) escalates
	// -- proving NextStep really did consult the POSTED outcome, not a
	// silently-re-derived "ok".
	var runStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM workflow_runs WHERE id = $1`, row.runID.String()).Scan(&runStatus); err != nil {
		t.Fatalf("query run: %v", err)
	}
	if runStatus != "needs_review" {
		t.Errorf("run status = %q, want needs_review (needs_fix with no wired edge escalates)", runStatus)
	}
}
