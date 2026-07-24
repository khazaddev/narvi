// This file (planrecord.go) implements Step 37's ("plan mode, web",
// §8.1/§12.2 item 3) own plan-row-creation half: hooked into the SAME
// transaction pushpr.go's own completeProcessingTurn already writes a
// turn's terminal state in (recordPlanIfNeeded is called right after that
// write, alongside enqueueOutboxNotification -- see completeProcessingTurn
// itself) -- mirroring Step 35's outboxenqueue.go and Step 36's
// intent-decision-recording precedent exactly: a real side effect of a
// turn's completion, recorded in the SAME Postgres transaction as the
// state change that triggered it (§5.1).
//
// A plan_mode=true turn that completes SUCCESSFULLY (trig ==
// turn.TriggerComplete) creates exactly one new plans row: any existing
// awaiting_approval row for this session is superseded first (in the same
// transaction, before the insert, so the partial unique index
// plans_one_awaiting_approval_per_session never trips), then the new row
// is inserted with a version computed by internal/domain/plan's own pure
// NextVersion function. A plan_mode=false turn, or one that failed or was
// cancelled, does nothing here -- no plan was ever produced, so there is
// nothing to record.

package sessionactor

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	plandomain "github.com/khazaddev/narvi/internal/domain/plan"
	"github.com/khazaddev/narvi/internal/domain/turn"
)

// recordPlanIfNeeded is completeProcessingTurn's own plan-row-creation
// call site (pushpr.go) -- processing is the SAME turn row that function
// just drove to its terminal state; trig is the SAME trigger that
// transition used. Returns nil (no-op) for every case where no plan
// should be recorded: a non-plan-mode turn, or a plan-mode turn that
// didn't genuinely complete (failed/cancelled).
func (a *Actor) recordPlanIfNeeded(ctx context.Context, tx pgx.Tx, processing sqlcgen.Turn, trig turn.Trigger) error {
	if trig != turn.TriggerComplete || !processing.PlanMode {
		return nil
	}

	rows, err := a.stores.plan.WithTx(tx).ListSummariesForSession(ctx, a.sessionID)
	if err != nil {
		return fmt.Errorf("sessionactor: list plan summaries for session: %w", err)
	}

	summaries := make([]plandomain.Summary, len(rows))
	for i, r := range rows {
		summaries[i] = plandomain.Summary{
			ID:      plandomain.ID(r.ID.String()),
			Version: int(r.Version),
			Status:  plandomain.Status(r.Status),
		}
	}

	for _, id := range plandomain.ShouldSupersede(summaries) {
		var planID pgtype.UUID
		if err := planID.Scan(string(id)); err != nil {
			return fmt.Errorf("sessionactor: parse plan id to supersede: %w", err)
		}
		if err := a.stores.plan.WithTx(tx).Supersede(ctx, planID); err != nil {
			return fmt.Errorf("sessionactor: supersede prior plan: %w", err)
		}
	}

	version := plandomain.NextVersion(summaries)

	if _, err := a.stores.plan.WithTx(tx).Create(ctx, sqlcgen.CreatePlanParams{
		SessionID:   a.sessionID,
		TurnID:      processing.ID,
		Version:     int32(version),
		Status:      sqlcgen.PlanStatusAwaitingApproval,
		PlanModelID: processing.ModelID,
	}); err != nil {
		return fmt.Errorf("sessionactor: create plan: %w", err)
	}
	return nil
}
