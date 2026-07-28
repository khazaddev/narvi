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
//
// # Audit-fix batch: supersession is no longer silent (L19/L6)
//
// Superseding a prior awaiting_approval row used to be a bare Supersede
// call -- no log line, no audit_log row, and (critically) no notification
// to whichever channel had already been shown that plan's own Slack
// approval-request message: its Approve/Reject buttons stayed
// interactively alive even though the plan they'd act on no longer existed
// as awaiting_approval, so a click would hit authorizeSessionAction's own
// ApproveIfAwaitingApproval guard and land a confusing generic denial with
// no indication a v2 had replaced it.
//
// The supersede loop below now, for each row it supersedes: fetches it
// FIRST (before Supersede runs) so its own stored slack_channel_id/
// slack_message_ts -- set only if outboxworker's own planSlackNotifier.
// deliverApproval successfully posted THIS row's approval-request message
// -- is still available afterward; logs the transition (mirroring
// httpapi.DecidePlan's own "decided plan" log line shape); records a
// "plan.superseded" audit_log row in the SAME transaction (§13.3), with an
// explicitly invalid actor_user_id (pgtype.UUID{}) -- this is a
// SYSTEM-triggered transition (a new plan-mode turn completing, not a
// distinct user action), mirroring internal/app/identitylink.Resolve's own
// identical NULL-for-system-triggered-change precedent for its own
// "identity.auto_linked" row; and, iff a real Slack ref was stored,
// enqueues a ports.NotificationKindSlackPlanDecided outbox row -- reusing
// the EXISTING kind, which outboxworker's own planSlackNotifier.
// deliverDecided already routes to a bare chat.update with no blocks field,
// correctly stripping the stale buttons -- carrying an honest "superseded"
// outcome message, built the same way httpapi.enqueuePlanDecisionNotifications
// (decideplan.go) already builds its own real-decision equivalent.

package sessionactor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/app/ports"
	plandomain "github.com/khazaddev/narvi/internal/domain/plan"
	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/platform"
)

// recordPlanIfNeeded is completeProcessingTurn's own plan-row-creation
// call site (pushpr.go) -- processing is the SAME turn row that function
// just drove to its terminal state; trig is the SAME trigger that
// transition used. Returns (nil, nil) -- not an error -- for every case
// where no plan should be recorded: a non-plan-mode turn, or a plan-mode
// turn that didn't genuinely complete (failed/cancelled).
//
// Step 38 ("plan mode, cross-channel", §8.1/§13.3) update: now returns the
// newly-created plan row (rather than nothing) when one was recorded --
// enqueueOutboxNotification (outboxenqueue.go), called AFTER this function
// by completeProcessingTurn, needs the plan's own id/version to route a
// plan_mode=true turn's completion to the plan-approval-request
// notification instead of the generic one.
func (a *Actor) recordPlanIfNeeded(ctx context.Context, tx pgx.Tx, processing sqlcgen.Turn, trig turn.Trigger) (*sqlcgen.Plan, error) {
	if trig != turn.TriggerComplete || !processing.PlanMode {
		return nil, nil
	}

	rows, err := a.stores.plan.WithTx(tx).ListSummariesForSession(ctx, a.sessionID)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: list plan summaries for session: %w", err)
	}

	summaries := make([]plandomain.Summary, len(rows))
	for i, r := range rows {
		summaries[i] = plandomain.Summary{
			ID:      plandomain.ID(r.ID.String()),
			Version: int(r.Version),
			Status:  plandomain.Status(r.Status),
		}
	}

	// Computed here, BEFORE the supersede loop, rather than after the new
	// row's own insert further down: NextVersion is a pure function of
	// summaries (already fetched above), so computing it early costs
	// nothing and lets the supersede loop's own audit/notification detail
	// below reference "superseded by which version" without restructuring
	// around the later Create call.
	version := plandomain.NextVersion(summaries)

	for _, id := range plandomain.ShouldSupersede(summaries) {
		var planID pgtype.UUID
		if err := planID.Scan(string(id)); err != nil {
			return nil, fmt.Errorf("sessionactor: parse plan id to supersede: %w", err)
		}

		// Fetch the row BEFORE superseding it -- its own stored Slack
		// message ref (if any) must be read back before Supersede runs, so
		// it is still available afterward to route the cross-channel
		// "superseded" notification below (see this file's own top doc
		// comment).
		supersededRow, err := a.stores.plan.WithTx(tx).Get(ctx, planID)
		if err != nil {
			return nil, fmt.Errorf("sessionactor: get plan to supersede: %w", err)
		}

		if err := a.stores.plan.WithTx(tx).Supersede(ctx, planID); err != nil {
			return nil, fmt.Errorf("sessionactor: supersede prior plan: %w", err)
		}

		a.logger.Info("sessionactor: plan superseded by a newer version",
			"plan_id", planID.String(),
			"session_id", a.sessionID.String(),
			"superseded_by_version", version)

		if err := auditlog.Record(ctx, a.stores.auditLog.WithTx(tx), pgtype.UUID{}, "plan.superseded", "plan", planID.String(), map[string]any{
			"session_id":            a.sessionID.String(),
			"superseded_by_version": version,
		}); err != nil {
			return nil, fmt.Errorf("sessionactor: record plan supersede audit log: %w", err)
		}

		if supersededRow.SlackChannelID != nil && supersededRow.SlackMessageTs != nil && *supersededRow.SlackChannelID != "" && *supersededRow.SlackMessageTs != "" {
			payload, err := json.Marshal(slackapi.PlanDecidedPayload{
				ChannelID: *supersededRow.SlackChannelID,
				MessageTS: *supersededRow.SlackMessageTs,
				Text:      fmt.Sprintf("Superseded by plan v%d — see the new plan message.", version),
			})
			if err != nil {
				return nil, fmt.Errorf("sessionactor: marshal slack plan-superseded payload: %w", err)
			}

			// Correlation ID propagation, mirroring enqueueOutboxNotification's
			// own identical "read from ctx if present, else NULL" convention
			// (outboxenqueue.go) exactly.
			var correlationID *string
			if id, ok := platform.CorrelationIDFromContext(ctx); ok && id != "" {
				correlationID = &id
			}

			if _, err := a.stores.outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
				SessionID:     a.sessionID,
				Kind:          string(ports.NotificationKindSlackPlanDecided),
				Payload:       payload,
				CorrelationID: correlationID,
			}); err != nil {
				return nil, fmt.Errorf("sessionactor: enqueue slack plan-superseded outbox entry: %w", err)
			}
		}
	}

	created, err := a.stores.plan.WithTx(tx).Create(ctx, sqlcgen.CreatePlanParams{
		SessionID:   a.sessionID,
		TurnID:      processing.ID,
		Version:     int32(version),
		Status:      sqlcgen.PlanStatusAwaitingApproval,
		PlanModelID: processing.ModelID,
	})
	if err != nil {
		return nil, fmt.Errorf("sessionactor: create plan: %w", err)
	}
	return &created, nil
}
