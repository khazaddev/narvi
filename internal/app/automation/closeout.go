package automation

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	domainautomation "github.com/narvidev/narvi/internal/domain/automation"
	"github.com/narvidev/narvi/internal/platform"
)

// terminalizeRun applies internal/domain/automation.RunTransition's own
// verdict for run reaching newStatus (RunStatusSucceeded or
// RunStatusFailed), via TerminalizeAutomationRun's own CAS guard ("AND
// status IN ('starting', 'running')") -- shared by BOTH callers that can
// ever terminalize a run: reconcile.go's own reconcileRun (a genuine turn
// outcome) and sweep.go's own sweepStarting/sweepRunning (an orphan
// timeout), so the "terminalize, then cascade to this invocation's own
// close-out" sequence is written exactly once.
//
// pgx.ErrNoRows (this run is already terminal -- a concurrent reconcile
// tick and sweep tick racing on the SAME run, or a defensive re-run) is a
// silent no-op, never logged as an error: exactly the same "lost the
// race, harmless" outcome every other CAS-guarded write in this codebase
// treats identically (e.g. outboxworker.attempt's own RenewClaim no-op
// branch).
func (e *Engine) terminalizeRun(ctx context.Context, logger *slog.Logger, run sqlcgen.AutomationRun, newStatus domainautomation.RunStatus) {
	_, err := e.runs.Terminalize(ctx, sqlcgen.TerminalizeAutomationRunParams{
		ID:     run.ID,
		Status: sqlcgen.AutomationRunStatus(newStatus),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		logger.Error("automation: terminalize run failed", "error", err, "automation_run_id", run.ID.String())
		return
	}

	e.maybeCloseInvocation(ctx, logger, run.InvocationID, run.AutomationID)
}

// maybeCloseInvocation checks whether invocationID's own runs are now
// ALL terminal (CountTerminalForInvocation against its own persisted
// total_runs, internal/domain/automation.EvaluateInvocationOutcome) and,
// if so, closes it. Called after EVERY run terminalization (a genuine
// outcome, an orphan timeout, or the defensive create-failed path) --
// harmless to call when the invocation is not yet ready (EvaluateInvocationOutcome
// simply reports Ready=false and this returns) or already closed
// (closeInvocation's own CAS guard below absorbs that).
func (e *Engine) maybeCloseInvocation(ctx context.Context, logger *slog.Logger, invocationID, automationID pgtype.UUID) {
	counts, err := e.runs.CountTerminalForInvocation(ctx, invocationID)
	if err != nil {
		logger.Error("automation: count terminal runs for invocation failed", "error", err, "automation_invocation_id", invocationID.String())
		return
	}

	inv, err := e.invocations.Get(ctx, invocationID)
	if err != nil {
		logger.Error("automation: get invocation failed", "error", err, "automation_invocation_id", invocationID.String())
		return
	}

	outcome := domainautomation.EvaluateInvocationOutcome(int(inv.TotalRuns), int(counts.TerminalRuns), int(counts.FailedRuns))
	if !outcome.Ready {
		return
	}

	e.closeInvocation(ctx, logger, invocationID, automationID, outcome.Failed)
}

// closeInvocation applies internal/domain/automation.InvocationTransition's
// own verdict via CloseAutomationInvocation's own CAS guard ("AND status =
// 'pending'") -- at most one caller ever wins this for a given invocation,
// no matter how many concurrent callers observe "ready to close" at
// roughly the same moment (two runs finishing within the same reconcile
// tick, or a reconcile tick and a sweep tick racing on the SAME
// invocation's last remaining run). The loser sees pgx.ErrNoRows and
// returns silently -- exactly the SAME "lost the race, harmless" outcome
// terminalizeRun's own doc comment describes.
//
// Only the WINNER proceeds to the failure-strike consequence (failed
// true) or the streak reset (failed false) -- never both callers, and
// never twice for the same invocation.
func (e *Engine) closeInvocation(ctx context.Context, logger *slog.Logger, invocationID, automationID pgtype.UUID, failed bool) {
	toStatus := sqlcgen.AutomationInvocationStatusSucceeded
	if failed {
		toStatus = sqlcgen.AutomationInvocationStatusFailed
	}

	closedInv, err := e.invocations.Close(ctx, sqlcgen.CloseAutomationInvocationParams{ID: invocationID, Status: toStatus})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		logger.Error("automation: close invocation failed", "error", err, "automation_invocation_id", invocationID.String())
		return
	}

	// §8.4 ("automations: triggers & extras"): "last_run + artifact_summary
	// populated" -- only the WINNER of the CAS above (the caller that just
	// actually closed this invocation, never a loser that saw
	// pgx.ErrNoRows above) ever records this, so a concurrent
	// reconcile-tick/sweep-tick race on the SAME invocation can never write
	// this twice. Runs on a best-effort basis: a failure here is logged but
	// never un-does the invocation close-out itself (the SAME "this is
	// enrichment, not the source of truth" posture ResetConsecutiveFailures'
	// own error handling immediately below already establishes).
	e.recordLastRun(ctx, logger, closedInv, toStatus)

	if !failed {
		if _, err := e.automations.ResetConsecutiveFailures(ctx, automationID); err != nil {
			logger.Error("automation: reset consecutive failures failed", "error", err, "automation_id", automationID.String())
		}
		return
	}

	e.applyFailureStrike(ctx, logger, automationID, invocationID)
}

// recordLastRun computes and persists §8.4's own "last_run +
// artifact_summary" onto closedInv's own parent automation, the moment
// closeInvocation's own CAS is won for closedInv. Reads back every run of
// closedInv (small, ≤ domainautomation.MaxFanOutTargets rows) to name which
// specific targets failed (domainautomation.BuildArtifactSummary) -- a
// second, cheap query, but the ONLY way to get the actual target NAMES
// this summary names: CountTerminalForInvocation (maybeCloseInvocation,
// above) only ever returns counts, never which targets they belong to.
func (e *Engine) recordLastRun(ctx context.Context, logger *slog.Logger, closedInv sqlcgen.AutomationInvocation, status sqlcgen.AutomationInvocationStatus) {
	runs, err := e.runs.ListForInvocation(ctx, closedInv.ID)
	if err != nil {
		logger.Error("automation: list runs for invocation failed; last_run/artifact_summary not updated", "error", err, "automation_invocation_id", closedInv.ID.String())
		return
	}

	total := len(runs)
	failedCount := 0
	var failedNames []string
	for _, run := range runs {
		if run.Status != sqlcgen.AutomationRunStatusFailed {
			continue
		}
		failedCount++
		targets, terr := UnmarshalTargets(run.Target)
		if terr != nil || len(targets) == 0 {
			logger.Warn("automation: decode failed run's own target failed; artifact_summary will name it as unknown", "error", terr, "automation_run_id", run.ID.String())
			continue
		}
		failedNames = append(failedNames, targets[0].Name)
	}

	summary := domainautomation.BuildArtifactSummary(total, failedCount, failedNames)

	if _, err := e.automations.UpdateLastRun(ctx, sqlcgen.UpdateAutomationLastRunParams{
		ID:              closedInv.AutomationID,
		LastRunAt:       closedInv.ClosedAt,
		LastRunStatus:   &status,
		ArtifactSummary: &summary,
	}); err != nil {
		logger.Error("automation: update automation last_run failed", "error", err, "automation_id", closedInv.AutomationID.String())
	}
}

// applyFailureStrike is §3.5's own literal CAS idiom in action: "UPDATE
// ... WHERE failure_counted_at IS NULL" (MarkFailureCounted) guards the
// failure-strike CONSEQUENCE against being applied twice for the SAME
// invocation. This guard and its consequence (LockAutomationForUpdate +
// ApplyFailureStrike) now run in ONE shared transaction -- MarkFailureCounted
// is called via e.invocations.WithTx(tx), on the SAME tx opened for the
// lock/apply pair immediately below it, matching the "MUST run inside the
// same transaction" comments on AutomationInvocationStore.WithTx/
// AutomationStore.WithTx and this package's own queries/automations.sql,
// queries/automationinvocations.sql (see internal/domain/automation/doc.go's
// own writeup for why this guard used to commit standalone, ahead of this
// transaction, and why that was a real bug rather than a deliberate design:
// any failure between the old standalone MarkFailureCounted commit and this
// transaction's own commit permanently set the one-way CAS guard with NO
// strike ever recorded, and nothing anywhere retries or reconciles
// failure_counted_at). Only once this call wins that guard does it proceed
// to lock the automation row (LockAutomationForUpdate, serializing against
// any OTHER invocation belonging to the SAME automation that is closing
// failed concurrently) and apply automation.EvaluateFailureStrike's own
// verdict -- and now, either both the guard flip and the strike land
// together, or (any failure before this transaction's own commit) neither
// does, leaving a subsequent retry free to win the guard and apply the
// strike for real.
func (e *Engine) applyFailureStrike(ctx context.Context, logger *slog.Logger, automationID, invocationID pgtype.UUID) {
	conn, err := e.pool.Acquire(ctx)
	if err != nil {
		logger.Error("automation: acquire connection for strike accounting failed", "error", err, "automation_id", automationID.String())
		return
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		logger.Error("automation: begin strike accounting tx failed", "error", err, "automation_id", automationID.String())
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := e.invocations.WithTx(tx).MarkFailureCounted(ctx, invocationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already counted -- a concurrent/retried closer lost this
			// race. Harmless no-op; the deferred rollback above discards
			// this otherwise-empty transaction.
			return
		}
		logger.Error("automation: mark failure counted failed", "error", err, "automation_invocation_id", invocationID.String())
		return
	}

	txAutomations := e.automations.WithTx(tx)

	automationRow, err := txAutomations.LockForUpdate(ctx, automationID)
	if err != nil {
		logger.Error("automation: lock automation for update failed", "error", err, "automation_id", automationID.String())
		return
	}

	decision := domainautomation.EvaluateFailureStrike(int(automationRow.ConsecutiveFailures), true)

	newStatus := automationRow.Status
	if decision.ShouldAutoPause {
		to, terr := domainautomation.Transition(domainautomation.Status(automationRow.Status), domainautomation.TriggerAutoPause)
		if terr != nil {
			// Already Paused (a prior strike already auto-paused this
			// automation; ShouldAutoPause keeps reporting true on every
			// subsequent failure past the threshold, StrikeDecision's own
			// doc comment) -- Transition's own typed rejection is exactly
			// the safe, expected outcome here: leave newStatus (already
			// Paused) unchanged, never treated as an error.
			logger.Debug("automation: auto-pause trigger no-op: automation already paused", "automation_id", automationID.String())
		} else {
			newStatus = sqlcgen.AutomationStatus(to)
		}
	}

	if _, err := txAutomations.ApplyFailureStrike(ctx, sqlcgen.ApplyFailureStrikeParams{
		ID:                  automationID,
		ConsecutiveFailures: int32(decision.NewConsecutiveFailures),
		Status:              newStatus,
	}); err != nil {
		logger.Error("automation: apply failure strike failed", "error", err, "automation_id", automationID.String())
		return
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("automation: commit strike accounting tx failed", "error", err, "automation_id", automationID.String())
		return
	}

	if decision.ShouldAutoPause {
		platform.Logger(ctx).Warn("automation: auto-paused after consecutive invocation failures",
			"automation_id", automationID.String(),
			"consecutive_failures", decision.NewConsecutiveFailures,
			"threshold", domainautomation.AutoPauseThreshold,
		)
	}
}
