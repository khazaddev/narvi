package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	domainautomation "github.com/khazaddev/narvi/internal/domain/automation"
	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/platform"
)

// ReconcileOnce runs exactly one reconcile tick: lists a bounded batch of
// still-non-terminal automation_runs rows and, for each, re-derives its
// current status from its linked session's own turn history
// (domainautomation.DeriveRunStatus) and applies whatever CAS-guarded
// write that implies. Exported (rather than only reachable through Run's
// own loop) so tests can drive exactly one tick deterministically,
// matching PumpOnce/SweepOnce's own precedent.
//
// A failure in the batch-level list step aborts the tick and returns the
// error (Run logs it) -- but once a batch is listed, one run's own
// reconcile failure is isolated: logged, and does NOT abort the rest of
// the batch, exactly like app/imagebuild.Builder.PumpOnce's own per-row
// isolation.
func (e *Engine) ReconcileOnce(ctx context.Context) error {
	inFlight, err := e.runs.ListInFlight(ctx, reconcileBatchSize)
	if err != nil {
		return fmt.Errorf("automation: list in-flight runs: %w", err)
	}

	for _, run := range inFlight {
		e.reconcileRun(ctx, run)
	}
	return nil
}

// reconcileRun re-derives run's own current status from its linked
// session's turn history and applies the resulting transition, if any --
// a no-op when DeriveRunStatus reports the SAME status this run already
// carries (the overwhelmingly common case on most ticks: nothing changed
// since the last one).
func (e *Engine) reconcileRun(ctx context.Context, run sqlcgen.AutomationRun) {
	logger := platform.Logger(ctx).With("automation_run_id", run.ID.String())

	if !run.SessionID.Valid {
		// A run reaches 'starting'/'running' (ListInFlight's own WHERE
		// clause) only via createRunAndSession's own successful path,
		// which always sets session_id before ever leaving 'starting' --
		// createFailedRun's own no-session path goes straight to
		// RunStatusFailed and is therefore never returned by
		// ListInFlight at all. Defensive, should be unreachable.
		logger.Warn("automation: in-flight run has no linked session; skipping")
		return
	}

	turns, err := e.turns.ListForSession(ctx, run.SessionID)
	if err != nil {
		logger.Error("automation: list turns for run's session failed", "error", err)
		return
	}

	newStatus := domainautomation.DeriveRunStatus(toTurnSummaries(turns))
	currentStatus := domainautomation.RunStatus(run.Status)
	if newStatus == currentStatus {
		return
	}

	switch newStatus {
	case domainautomation.RunStatusRunning:
		e.promoteRun(ctx, logger, run)
	case domainautomation.RunStatusSucceeded, domainautomation.RunStatusFailed:
		e.terminalizeRun(ctx, logger, run, newStatus)
	default:
		// RunStatusStarting: DeriveRunStatus never regresses a run that
		// was already observed Running/terminal back to Starting (its own
		// doc comment: "the strongest signal wins"), so this branch is
		// unreachable in practice -- left explicit rather than folded into
		// the case above so a future RunStatus addition doesn't silently
		// fall through here unnoticed.
	}
}

// promoteRun applies automation.RunTriggerProcessing: Starting -> Running.
func (e *Engine) promoteRun(ctx context.Context, logger *slog.Logger, run sqlcgen.AutomationRun) {
	if _, err := e.runs.PromoteToRunning(ctx, run.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already promoted (or already terminal) by a concurrent tick
			// -- harmless no-op.
			return
		}
		logger.Error("automation: promote run to running failed", "error", err)
	}
}

// toTurnSummaries converts a session's own []sqlcgen.Turn rows into
// []turn.Summary -- mirrors internal/app/sessionactor/timerfired.go's own
// identical `turn.Summary{Status: turn.State(t.Status)}` conversion
// exactly (FailureReason is left at its zero value: it is not a persisted
// turns column at all -- see turn.DeriveFailureReason's own doc comment --
// and domainautomation.DeriveRunStatus never inspects it, matching
// timerfired.go's own established precedent for a plain, non-overridden
// turn-status read).
func toTurnSummaries(turns []sqlcgen.Turn) []turn.Summary {
	out := make([]turn.Summary, len(turns))
	for i, t := range turns {
		out[i] = turn.Summary{Status: turn.State(t.Status)}
	}
	return out
}
