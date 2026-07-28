// This file (outboxenqueue.go) implements Step 35's ("outbox delivery",
// §5.1) own enqueue-side half: writing exactly one outbox row for a
// non-'web'-origin session's turn completion, in the SAME transaction as
// the turn's own terminal state write (pushpr.go's completeProcessingTurn
// calls enqueueOutboxNotification right after persisting that state
// change, before its own transact returns) -- §5.1's own explicit
// requirement: "Outbox pattern for every outbound side effect... written
// in the same tx as the state change".
//
// The DELIVERY side (claiming these rows, calling the right
// ports.Notifier, retrying with backoff, dead-lettering) is
// internal/app/outboxworker's own job -- this file's only responsibility
// is deciding WHETHER a turn completion needs a notification at all
// (sessions.spawn_source == 'web' -> never), and if so, WHICH channel to
// route it to and what payload to enqueue for that channel's own
// ports.Notifier implementation to later consume.

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/ports"
	plandomain "github.com/khazaddev/narvi/internal/domain/plan"
	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/platform"
)

// planApprovalLinearText builds the Linear plan-approval-request
// AgentActivity body -- the plan's own rendered content (planContentText)
// followed by the EXACT approve/reject keyword instructions handlePrompted
// itself parses against (plandomain.ApproveKeywords/RejectKeywords), so the
// instructions shown here can never drift out of sync with what is
// actually accepted (this file's own doc comment on plandomain.
// ApproveKeywords explains why both live off that one shared list).
func planApprovalLinearText(version int32, content string) string {
	return fmt.Sprintf(
		"Plan v%d is ready for review:\n\n%s\n\nReply %s to approve and build it, or %s to reject it.",
		version, content,
		strings.Join(plandomain.ApproveKeywords, "/"),
		strings.Join(plandomain.RejectKeywords, "/"),
	)
}

// outcomeText builds the short, human-readable outcome message every
// outbox notification payload carries as its own "what happened" line --
// deliberately simple (no PR URL/richer changelog): the PR itself, when
// one is opened, is created AFTER this SAME transact commits
// (createPRBestEffort, pushpr.go's own post-commit best-effort step), so
// its URL is not yet known at the point this function runs, inside the
// turn-completion transaction. A future Step could enrich a GitHub-origin
// notification with the real PR URL once one exists (e.g. a second,
// later outbox row, or updating this one's own payload post-commit) --
// not built here, named honestly as a follow-up rather than blocking this
// Step's own core "eventually delivered, no loss" guarantee on it.
func outcomeText(trig turn.Trigger, failureReason turn.FailureReason) string {
	switch trig {
	case turn.TriggerComplete:
		return "Turn completed successfully."
	case turn.TriggerCancel:
		return "Turn was cancelled."
	case turn.TriggerFail:
		if failureReason != "" {
			return fmt.Sprintf("Turn failed (%s).", failureReason)
		}
		return "Turn failed."
	default:
		return "Turn finished."
	}
}

// splitRepoFullName splits a github_pr_sessions.repo_full_name value
// (always "owner/repo", the exact shape internal/adapters/inbound/github's
// own ingress path already writes it in) into its owner/repo halves. ok is
// false for anything not shaped exactly "owner/repo" (defensive; should be
// unreachable given that ingress path's own invariant).
func splitRepoFullName(repoFullName string) (owner, repo string, ok bool) {
	parts := strings.SplitN(repoFullName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// enqueueOutboxNotification decides whether sessionRow's turn completion
// (trig/failureReason, already-validated by completeProcessingTurn's own
// caller) needs an outbound notification at all, and if so, writes exactly
// one outbox row for it, inside tx. sessionRow is the SAME row
// completeProcessingTurn already fetched (a.stores.session.WithTx(tx).Get)
// moments ago -- passed in rather than re-fetched here, so this function
// never issues a redundant second SELECT of the same row inside the same
// transaction. A 'web'-origin session (sessionRow.SpawnSource ==
// sqlcgen.SessionSpawnSourceWeb) enqueues NOTHING -- there is no external
// channel to notify. A non-'web'-origin session whose own reverse-lookup
// row is missing (pgx.ErrNoRows from the matching GetBySessionID call --
// should be unreachable in practice, since every non-'web' session is
// created BY that same ingress path writing its own claim row first, but
// defensive against any future gap) also enqueues nothing, logged as a
// warning, never a hard failure of the whole turn completion.
//
// Step 38 ("plan mode, cross-channel", §8.1/§13.3) update: plan is the
// SAME (possibly nil) plan row recordPlanIfNeeded (planrecord.go) just
// returned, moments earlier in completeProcessingTurn's own sequencing --
// non-nil iff processing was a plan_mode=true turn that just genuinely
// completed. When non-nil AND the session is Slack- or Linear-origin, this
// function enqueues the RICHER plan-approval-request notification (plan
// steps/scope, real Slack buttons / Linear text-verdict instructions)
// INSTEAD of the generic outcomeText message above -- web needs neither
// (this Step's own explicit scope note: the web UI, whenever it exists,
// just re-reads the plan's own status) and GitHub keeps today's existing
// generic behavior unchanged (GitHub plan-mode verdicts are explicitly out
// of this Step's own scope).
func (a *Actor) enqueueOutboxNotification(ctx context.Context, tx pgx.Tx, sessionRow sqlcgen.Session, trig turn.Trigger, failureReason turn.FailureReason, processing sqlcgen.Turn, plan *sqlcgen.Plan) error {
	if sessionRow.SpawnSource == sqlcgen.SessionSpawnSourceWeb {
		return nil
	}

	text := outcomeText(trig, failureReason)
	success := trig == turn.TriggerComplete

	var kind ports.NotificationKind
	var payload any

	switch sessionRow.SpawnSource {
	case sqlcgen.SessionSpawnSourceSlack:
		row, err := a.stores.slackThreadSession.WithTx(tx).GetBySessionID(ctx, a.sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				a.logger.Warn("sessionactor: enqueue outbox notification: slack-origin session has no slack_thread_sessions row; skipping")
				return nil
			}
			return fmt.Errorf("sessionactor: enqueue outbox notification: get slack thread session: %w", err)
		}
		if plan != nil {
			kind = ports.NotificationKindSlackPlanApproval
			payload = slackapi.PlanApprovalPayload{
				PlanID:    plan.ID.String(),
				SessionID: a.sessionID.String(),
				ChannelID: row.ChannelID,
				ThreadTS:  row.ThreadTs,
				Version:   int(plan.Version),
				Text:      a.planContentText(ctx, processing),
			}
		} else {
			kind = ports.NotificationKindSlack
			payload = slackapi.Payload{ChannelID: row.ChannelID, ThreadTS: row.ThreadTs, Text: text}
		}

	case sqlcgen.SessionSpawnSourceGithub:
		row, err := a.stores.githubPRSession.WithTx(tx).GetBySessionID(ctx, a.sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				a.logger.Warn("sessionactor: enqueue outbox notification: github-origin session has no github_pr_sessions row; skipping")
				return nil
			}
			return fmt.Errorf("sessionactor: enqueue outbox notification: get github pr session: %w", err)
		}
		owner, repo, ok := splitRepoFullName(row.RepoFullName)
		if !ok {
			a.logger.Warn("sessionactor: enqueue outbox notification: github_pr_sessions.repo_full_name not in owner/repo shape; skipping",
				"repo_full_name", row.RepoFullName)
			return nil
		}
		kind = ports.NotificationKindGitHub
		payload = githubapi.Payload{Owner: owner, Repo: repo, PRNumber: int(row.PrNumber), Text: text}

	case sqlcgen.SessionSpawnSourceLinear:
		row, err := a.stores.linearAgentSession.WithTx(tx).GetBySessionID(ctx, a.sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				a.logger.Warn("sessionactor: enqueue outbox notification: linear-origin session has no linear_agent_sessions row; skipping")
				return nil
			}
			return fmt.Errorf("sessionactor: enqueue outbox notification: get linear agent session: %w", err)
		}
		kind = ports.NotificationKindLinear
		if plan != nil {
			payload = linearapi.Payload{
				AgentSessionID: row.AgentSessionID,
				OrganizationID: row.OrganizationID,
				Text:           planApprovalLinearText(plan.Version, a.planContentText(ctx, processing)),
				Success:        true,
			}
		} else {
			payload = linearapi.Payload{AgentSessionID: row.AgentSessionID, OrganizationID: row.OrganizationID, Text: text, Success: success}
		}

	default:
		// Defensive: sessions.spawn_source is a fixed 4-value enum
		// (web/slack/linear/github) -- this branch should be unreachable.
		a.logger.Warn("sessionactor: enqueue outbox notification: unrecognized spawn_source; skipping",
			"spawn_source", string(sessionRow.SpawnSource))
		return nil
	}

	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sessionactor: enqueue outbox notification: marshal payload: %w", err)
	}

	// Correlation ID propagation (Batch 11 audit-fix scope): carries the
	// enclosing request/webhook's own correlation id (platform.
	// CorrelationIDFromContext, minted at ingress -- see internal/platform/
	// correlation.go's own doc comment) through to the enqueued row, so
	// outboxworker.Builder's own attempt() (builder.go) can log it
	// alongside session_id at delivery time -- mirrors
	// internal/app/auditlog.Record's own identical "read from ctx if
	// present, else NULL" convention exactly. A turn completed outside such
	// a context (should be rare for a non-'web'-origin session, which by
	// definition originated from an inbound webhook) simply enqueues a null
	// correlation_id -- no id is ever invented here.
	var correlationID *string
	if id, ok := platform.CorrelationIDFromContext(ctx); ok && id != "" {
		correlationID = &id
	}

	if _, err := a.stores.outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID:     a.sessionID,
		Kind:          string(kind),
		Payload:       rawPayload,
		CorrelationID: correlationID,
	}); err != nil {
		return fmt.Errorf("sessionactor: enqueue outbox notification: create outbox entry: %w", err)
	}
	return nil
}
