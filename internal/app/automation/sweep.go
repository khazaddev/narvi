package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	domainautomation "github.com/khazaddev/narvi/internal/domain/automation"
	"github.com/khazaddev/narvi/internal/platform"
)

// SweepOnce runs exactly one recovery-sweep tick: §3.5's own two
// thresholds ("orphaned starting runs >5 min, running >90 min"). Exported
// (rather than only reachable through Run's own loop) so tests can drive
// exactly one tick deterministically, matching PumpOnce/ReconcileOnce's
// own precedent.
//
// Both sweeps run every tick (never independently ticked -- see this
// package's own doc.go for why one shared cadence is enough here, unlike
// app/imagebuild's own build-pump/refresh-pump split). A failure in either
// sweep's own list step aborts THAT sweep and is joined into the returned
// error (both are still attempted -- one threshold's own query trouble
// must not silently skip the other); a per-row terminalize failure is
// isolated exactly like every other per-row failure in this package.
func (e *Engine) SweepOnce(ctx context.Context) error {
	now := time.Now()

	startErr := e.sweepStarting(ctx, now)
	runErr := e.sweepRunning(ctx, now)

	if startErr != nil || runErr != nil {
		return fmt.Errorf("automation: sweep: starting=%v running=%v", startErr, runErr)
	}
	return nil
}

// sweepStarting reaps runs stuck in RunStatusStarting past
// platform.Timeouts.AutomationRunStartingOrphanThreshold, computed against
// now (injected, never time.Now() read a second time per row -- §11,
// mirrors app/imagebuild.RefreshOnce's own staleClaimCutoff precedent: one
// cutoff instant computed ONCE per tick).
func (e *Engine) sweepStarting(ctx context.Context, now time.Time) error {
	cutoff := pgtype.Timestamptz{Time: now.Add(-e.timeouts.AutomationRunStartingOrphanThreshold), Valid: true}

	orphaned, err := e.runs.ListOrphanedStarting(ctx, cutoff, sweepBatchSize)
	if err != nil {
		return fmt.Errorf("list orphaned starting runs: %w", err)
	}

	logger := platform.Logger(ctx)
	for _, run := range orphaned {
		// Defensive re-check against the pure predicate (automation.
		// IsOrphaned), even though the SQL query above already applied
		// the identical cutoff -- costs nothing, and guards against the
		// query's own predicate ever drifting out of sync with the
		// domain's own definition of "orphaned" in a future edit.
		if !domainautomation.IsOrphaned(domainautomation.RunStatusStarting, run.StartedAt.Time, now, e.orphanThresholds()) {
			continue
		}
		logger.Warn("automation: sweeping orphaned starting run", "automation_run_id", run.ID.String(), "started_at", run.StartedAt.Time)
		e.terminalizeRun(ctx, logger.With("automation_run_id", run.ID.String()), run, domainautomation.RunStatusFailed)
	}
	return nil
}

// sweepRunning reaps runs stuck in RunStatusRunning past
// platform.Timeouts.AutomationRunRunningOrphanThreshold -- same shape as
// sweepStarting immediately above, against running_at/its own distinct
// cutoff.
func (e *Engine) sweepRunning(ctx context.Context, now time.Time) error {
	cutoff := pgtype.Timestamptz{Time: now.Add(-e.timeouts.AutomationRunRunningOrphanThreshold), Valid: true}

	orphaned, err := e.runs.ListOrphanedRunning(ctx, cutoff, sweepBatchSize)
	if err != nil {
		return fmt.Errorf("list orphaned running runs: %w", err)
	}

	logger := platform.Logger(ctx)
	for _, run := range orphaned {
		if !domainautomation.IsOrphaned(domainautomation.RunStatusRunning, run.RunningAt.Time, now, e.orphanThresholds()) {
			continue
		}
		logger.Warn("automation: sweeping orphaned running run", "automation_run_id", run.ID.String(), "running_at", run.RunningAt.Time)
		e.terminalizeRun(ctx, logger.With("automation_run_id", run.ID.String()), run, domainautomation.RunStatusFailed)
	}
	return nil
}

// orphanThresholds builds the domainautomation.OrphanThresholds value from
// this Engine's own platform.Timeouts, once per sweep call.
func (e *Engine) orphanThresholds() domainautomation.OrphanThresholds {
	return domainautomation.OrphanThresholds{
		StartingThreshold: e.timeouts.AutomationRunStartingOrphanThreshold,
		RunningThreshold:  e.timeouts.AutomationRunRunningOrphanThreshold,
	}
}
