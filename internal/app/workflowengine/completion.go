// This file (completion.go) implements OnTurnCompleted -- the second
// wiring point (§25.6, doc.go), called from THREE places in
// internal/app/sessionactor, every one where a turn genuinely reaches a
// terminal state (Completed/Failed/Cancelled):
//
//   - pushpr.go's completeProcessingTurn -- a real execution_complete wire
//     event (§3.3), the common case.
//   - timerfired.go's handleTurnDeadlineTimer -- turn_deadline expiring
//     with no terminal event ever arriving.
//   - dispatch.go's failDispatchedTurn -- SandboxCommander.SendCommand
//     itself failing, so the prompt never even reached the sandbox.
//
// All three matter: a step's own workflow_step_runs attempt must be
// finalized (or parked awaiting_decision) no matter WHICH of these three
// paths ends its turn, or a bug/outage on the other two would leave that
// session's WorkflowRun stuck 'running' forever -- workflow_runs_
// one_running_per_session (migration 000057) would then block EVERY
// future turn on that session from ever starting a fresh run (see
// dispatch.go's own ResolveStepForNewTurn: it only ever starts a new run
// when none is currently running).
//
// # loopguard is deliberately never consulted here
//
// internal/domain/loopguard (§25.5) is the engine's own job to consult
// "only when a needs_fix edge is about to re-fire" (§25.9) -- verified
// directly against all three built-ins (§25.8): review/request have no
// edges at all (a single step, nothing to loop through), and the plan
// lane's own only wired edge (step 1's needs_fix self-loop, the revise
// loop) sits behind step.HITLAfter below, which returns BEFORE NextStep is
// ever called. So no built-in workflow can reach a needs_fix re-fire
// through any path this Step's own engine actually evaluates -- loopguard
// stays unconsulted, exactly like the task's own brief permits ("don't
// invent a use for it if none of the three built-ins actually loop").
// Step 56's real HITL decide endpoint is what will actually re-fire that
// edge (a human's "revise" verdict) and is the right place for loopguard's
// first real call site.

package workflowengine

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/domain/workflow"
	"github.com/khazaddev/narvi/internal/platform"
)

// implicitOutcome derives a StepOutcomeStatus from a turn's own terminal
// trigger -- used only when no explicit outcome was already posted via the
// generic step-outcome-posting tool during the turn (OnTurnCompleted's own
// COALESCE-never-overwrite discipline, WorkflowStore.MarkAwaitingDecision/
// FinishStepRun: a posted outcome always wins). Pure, no I/O,
// table-driven-testable directly.
//
// turn.TriggerComplete -> StepOutcomeOK: the step's own turn finished
// normally. Every other terminal trigger (TriggerFail, TriggerTimeout,
// TriggerCancel) -> StepOutcomeBlocked: the step could not make progress
// at all -- never StepOutcomeNeedsFix, which is reserved for an agent's
// own explicit, structured report of a FIXABLE problem, never inferred
// from a bare turn-level failure the agent itself never characterized.
func implicitOutcome(trig turn.Trigger) workflow.StepOutcomeStatus {
	if trig == turn.TriggerComplete {
		return workflow.StepOutcomeOK
	}
	return workflow.StepOutcomeBlocked
}

// stepRunTerminalStatus maps a turn's own terminal trigger onto the
// workflow_step_runs status its attempt lands in when the step has no HITL
// gate (see OnTurnCompleted below). Pure, no I/O.
func stepRunTerminalStatus(trig turn.Trigger) string {
	switch trig {
	case turn.TriggerComplete:
		return "completed"
	case turn.TriggerCancel:
		return "cancelled"
	default: // turn.TriggerFail, turn.TriggerTimeout
		return "failed"
	}
}

// OnTurnCompleted is sessionactor's own turn-completion hook (§25.6): looks
// up whether turnID is a live, engine-tracked attempt; if so, finalizes it
// and -- unless the step is HITLAfter-gated -- consults workflow.NextStep
// to advance, complete, or escalate the owning run. Never returns an error
// (see doc.go's own "fail-open is load-bearing" section): any internal
// failure is logged and this simply does nothing further -- a turn's own
// completion (already durably persisted by the caller before this runs)
// must never be undone or blocked by a bug in this bookkeeping.
func OnTurnCompleted(ctx context.Context, workflows *postgres.WorkflowStore, turnID pgtype.UUID, trig turn.Trigger) {
	logger := platform.Logger(ctx)

	stepRun, err := workflows.GetLiveStepRunByTurnID(ctx, turnID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("workflowengine: get live step run by turn id failed", "turn_id", turnID.String(), "error", err)
		}
		// pgx.ErrNoRows is the expected, common case for a turn this
		// package never tracked (see GetLiveStepRunByTurnID's own doc
		// comment) -- nothing to do either way.
		return
	}

	runRow, err := workflows.GetRun(ctx, stepRun.WorkflowRunID)
	if err != nil {
		logger.Error("workflowengine: get workflow run failed", "run_id", stepRun.WorkflowRunID.String(), "error", err)
		return
	}

	def, err := loadDefinition(ctx, workflows, runRow.WorkflowDefinitionID)
	if err != nil {
		logger.Error("workflowengine: load definition for completed turn failed", "run_id", runRow.ID.String(), "error", err)
		return
	}

	stepID := workflow.ID(stepRun.StepDefinitionID.String())
	step, ok := stepByID(def, stepID)
	if !ok {
		logger.Error("workflowengine: completed step-run names a step not in its own definition",
			"run_id", runRow.ID.String(), "step_definition_id", stepRun.StepDefinitionID.String())
		return
	}

	outcome := workflow.StepOutcomeStatus("")
	if stepRun.OutcomeStatus != nil {
		outcome = workflow.StepOutcomeStatus(*stepRun.OutcomeStatus)
	}
	if !workflow.IsValidStepOutcomeStatus(outcome) {
		// No outcome was posted via the step-outcome tool during this
		// turn (true for every §25.8 built-in -- none of their prompts
		// change to instruct the agent to call it) -- derive one from how
		// the turn itself ended.
		outcome = implicitOutcome(trig)
	}

	if step.HITLAfter {
		// §25.9's HITL gate: this attempt is done, but the RUN does not
		// advance/complete/escalate until a human decides (Step 56's own
		// decide endpoint). NextStep is deliberately never consulted here.
		if _, err := workflows.MarkAwaitingDecision(ctx, stepRun.ID, string(outcome)); err != nil {
			logger.Error("workflowengine: mark step run awaiting decision failed", "step_run_id", stepRun.ID.String(), "error", err)
		}
		return
	}

	if _, err := workflows.FinishStepRun(ctx, stepRun.ID, stepRunTerminalStatus(trig), string(outcome)); err != nil {
		logger.Error("workflowengine: finish step run failed", "step_run_id", stepRun.ID.String(), "error", err)
		return
	}

	next, err := workflow.NextStep(def, stepID, outcome)
	if err != nil {
		// Should be unreachable for a ValidateDefinition-clean definition
		// (ResolveStepForNewTurn already validates on every fresh run) --
		// fail open: the run is left 'running' with its live step-run
		// already finished, rather than guessing at a further write.
		logger.Error("workflowengine: NextStep failed; leaving run running",
			"run_id", runRow.ID.String(), "step_id", string(stepID), "outcome", string(outcome), "error", err)
		return
	}

	switch next.Kind {
	case workflow.NextComplete:
		if _, err := workflows.CompleteRun(ctx, runRow.ID); err != nil {
			logger.Error("workflowengine: complete workflow run failed", "run_id", runRow.ID.String(), "error", err)
		}
	case workflow.NextEscalate:
		if _, err := workflows.EscalateRun(ctx, runRow.ID); err != nil {
			logger.Error("workflowengine: escalate workflow run failed", "run_id", runRow.ID.String(), "error", err)
		}
	case workflow.NextAdvance:
		toID, perr := parseWorkflowID(next.ToStepID)
		if perr != nil {
			logger.Error("workflowengine: parse next step id failed", "error", perr)
			return
		}
		if _, err := workflows.CreateStepRun(ctx, runRow.ID, toID); err != nil {
			logger.Error("workflowengine: create next step run failed",
				"run_id", runRow.ID.String(), "to_step_id", string(next.ToStepID), "error", err)
			return
		}
		// Auto-dispatching this NEW attempt's own turn, with no human
		// trigger, is deliberately not implemented in this Step -- see
		// this file's own top doc comment: no built-in workflow can ever
		// reach this branch (review/request have no further step; the
		// plan lane's only advancing edge sits behind the HITLAfter branch
		// above, never reaching NextStep at all). The next step's own
		// attempt row now exists, correctly modeled, ready for whichever
		// future Step (the audit-fix loop, §25.9) adds the actual
		// auto-continuation dispatch.
		logger.Info("workflowengine: workflow run advanced to a new step; auto-dispatch is not implemented in this Step",
			"run_id", runRow.ID.String(), "to_step_id", string(next.ToStepID))
	}
}
