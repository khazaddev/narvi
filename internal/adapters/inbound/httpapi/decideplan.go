// This file (decideplan.go) implements Step 38's ("plan mode,
// cross-channel", §8.1/§13.3) own central deliverable: the shared,
// transport-agnostic "decide a plan" function every entry point (the
// existing REST approve/reject endpoints, planapprove.go; the new Slack
// interactivity block_actions handler, internal/adapters/inbound/slack/
// interactive.go; the new Linear text-verdict parsing,
// internal/adapters/inbound/linear/webhook.go's handlePrompted) calls
// identically -- so "first verdict wins" (the plans_one_awaiting_approval_
// per_session partial unique index, migrations/000034_plan_mode.up.sql)
// and "notify the other channels" are each implemented exactly ONCE, not
// once per caller.
//
// Mirrors create.go's own CreateSessionCore/CreateSessionOnTx split
// exactly, for the exact same reason: DecidePlanOnTx takes an
// ALREADY-OPEN transaction (so a caller that has already locked the
// session row for its own authorization check -- the REST handlers,
// planapprove.go -- can decide inline, on that SAME connection, rather
// than opening a second, simultaneous one out of the pool) and never
// finalizes it; DecidePlan is the pool-based wrapper every OTHER caller
// (Slack, Linear -- neither has an open transaction of its own yet) uses.
//
// DecidePlanOnTx ALWAYS (re-)acquires the session row's own lock itself
// (GetActorEpochForUpdate), regardless of whether the caller already holds
// it -- harmless and idempotent within the SAME transaction (mirrors
// CreateSessionOnTx's own "validate again... deliberate, harmless"
// tolerance for the identical kind of redundancy), and is what lets every
// caller -- one that already locked for its own authorization check, and
// one that never had a reason to lock at all -- call this function
// identically.
//
// # Authorization is NOT this function's job
//
// This Step's own brief is explicit: Slack/Linear verdicts stay
// UNAUTHENTICATED-per-user, following the SAME existing precedent
// sessions.created_by/participants already establish for these two
// channels (a webhook-originated session already has no per-user gate
// today). Real domain/authz.Authorize (§13.3) is Step 39's own scope. So
// DecidePlanOnTx/DecidePlan never call canActOnPlan (planauthz.go) --
// planapprove.go's own REST handlers still call it themselves, BEFORE ever
// reaching this function, exactly as they did before this refactor; Slack/
// Linear callers skip it entirely, matching this codebase's own existing
// treatment of every other webhook-originated action.
//
// IMPLEMENTATION_PLAN.md row 38's own summary ("Slack/Linear verdicts via
// the same `Authorize`") describes this PROSPECTIVELY, not as today's
// behavior: it names the real domain/authz.Authorize call this package will
// route Slack/Linear verdicts through once it exists, which is exactly the
// Step 39 scope the paragraph above defers to -- today, with no Authorize
// to call yet, the intentional interim state is the bot-attributed,
// no-per-user-gate behavior this file actually implements, matching Steps
// 32-34's own identical precedent for every other webhook-originated
// action.
//
// # Step 39 ("identities + full RBAC") update: domain/authz now exists,
// but Slack/Linear verdicts STILL skip it -- see the paragraph above,
// unchanged: routing Slack/Linear through a real Authorize call needs a
// real resolved user_id/role for those actors, which needs identity
// auto-linking (§13.2), explicitly out of THIS Step's own scope (see
// docs/IMPLEMENTATION_PLAN.md row 39's hand-off). What DOES land now:
// planapprove.go's REST handlers call authz.Authorize (via canActOnPlan,
// planauthz.go) exactly as before this Step, just now backed by the real
// matrix instead of a bespoke predicate; and DecidePlanOnTx itself now
// writes an audit_log row (§13.3) on every winning decision, on the SAME
// tx, for EVERY caller alike -- REST (a real decidedBy) and Slack/Linear
// (an invalid, bot-attributed decidedBy) both get one, actor_user_id NULL
// for the latter, mirroring decidedBy's own existing NULL-for-bot
// convention. The audit write is unconditional on winning the decision,
// regardless of who decided or how they were (or weren't) authorized --
// it is a record of WHAT changed, not a second authorization gate.

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// PlanVerdict is the decision DecidePlan/DecidePlanOnTx renders.
type PlanVerdict string

// The two verdicts a plan can be decided with -- PlanVerdictApprove flips
// the plan to 'approved' and dispatches the new implementation turn;
// PlanVerdictReject flips it to 'rejected' with no new turn.
const (
	PlanVerdictApprove PlanVerdict = "approve"
	PlanVerdictReject  PlanVerdict = "reject"
)

// ErrPlanOpenTurnInFlight is returned by DecidePlanOnTx/DecidePlan when an
// Approve verdict is refused because another turn for this session hasn't
// yet reached a terminal state -- the exact same gate ApprovePlan's own top
// doc comment (planapprove.go) already documents in full; callers map this
// to whatever their own transport's "not right now, try again shortly"
// shape is (409 for REST; a plain honest reply for Slack/Linear -- neither
// currently implements a retry surface for this narrow race, matching
// today's existing REST-only behavior).
var ErrPlanOpenTurnInFlight = errors.New("httpapi: a turn is already pending, dispatched, or processing for this session")

// DecidePlanOutcome is DecidePlan/DecidePlanOnTx's uniform,
// transport-agnostic result.
type DecidePlanOutcome struct {
	// Won reports whether THIS call performed the transition. false means
	// the plan was already approved/rejected/superseded by the time this
	// call's own guarded UPDATE ran (by any entry point, possibly
	// including a concurrent call from this SAME entry point) -- see
	// FinalStatus for the plan's real, current status either way.
	Won bool

	// FinalStatus is the plan's ACTUAL status string (mirrors
	// plandomain.Status's own values: "approved"/"rejected"/"superseded")
	// after this call: the verdict this call itself just rendered, if Won;
	// otherwise whatever it already was. Empty only in the defensive,
	// should-be-unreachable case where planID does not name any row of
	// sessionID's own -- callers treat that identically to "already
	// decided" (a plain, honest "no such awaiting plan", never a 404 that
	// implies the SESSION itself is missing).
	FinalStatus string

	// TurnID is the new implementation turn's id, set iff Won && the
	// rendered verdict was Approve.
	TurnID *string
}

// planDecisionOutcomeText builds the short, human-readable line every
// cross-channel notification (this Step's own point 6) carries -- shared
// so the Slack chat.update text and the Linear follow-up AgentActivity
// text are byte-for-byte the same wording for the same outcome.
func planDecisionOutcomeText(verdict PlanVerdict) string {
	switch verdict {
	case PlanVerdictApprove:
		return "Plan approved — implementation started."
	case PlanVerdictReject:
		return "Plan rejected."
	default:
		return "Plan decided."
	}
}

// DecidePlanOnTx renders verdict on planID (which must belong to
// sessionRow.ID) inside the caller's own already-open transaction tx --
// see this file's own top doc comment for the full CreateSessionOnTx-
// mirroring contract (tx is never committed/rolled back here; the caller
// owns that entirely) and for why authorization is deliberately NOT this
// function's job.
//
// decidedBy is the acting user id, or an explicitly INVALID pgtype.UUID
// (Valid == false) for a bot/channel-attributed decision (Slack button
// click, Linear text reply) -- mirrors sessions.created_by's own existing
// NULL-for-bot convention exactly (this Step's own brief).
//
// Sequencing: lock the session row (see top doc comment on why this is
// unconditional) -> for Approve only, the SAME hasOpenTurn 409 gate
// ApprovePlan's own top doc comment describes (ErrPlanOpenTurnInFlight) ->
// the guarded conditional UPDATE (plans.ApproveIfAwaitingApproval/
// RejectIfAwaitingApproval) -> re-fetch the plan row (to learn its REAL
// current state either way, and -- on a win -- its own stored Slack message
// ref) -> on a win: for Approve, insert the new implementation turn exactly
// as ApprovePlan always has; either way, enqueue this Step's own
// cross-channel-notify outbox rows (enqueuePlanDecisionNotifications
// below), inside this SAME transaction, so they are visible if and only if
// the whole decision itself commits.
func DecidePlanOnTx(
	ctx context.Context,
	tx pgx.Tx,
	sessions *postgres.SessionStore,
	turns *postgres.TurnStore,
	plans *postgres.PlanStore,
	outbox *postgres.OutboxStore,
	linearAgentSessions *postgres.LinearAgentSessionStore,
	auditLog *postgres.AuditLogStore,
	sessionRow sqlcgen.Session,
	planID pgtype.UUID,
	verdict PlanVerdict,
	decidedBy pgtype.UUID,
) (DecidePlanOutcome, error) {
	logger := platform.Logger(ctx)

	if _, err := sessions.WithTx(tx).GetActorEpochForUpdate(ctx, sessionRow.ID); err != nil {
		return DecidePlanOutcome{}, fmt.Errorf("httpapi: lock session row for plan decision: %w", err)
	}

	if verdict == PlanVerdictApprove {
		existingTurns, err := turns.WithTx(tx).ListForSession(ctx, sessionRow.ID)
		if err != nil {
			return DecidePlanOutcome{}, fmt.Errorf("httpapi: list turns for plan-decision open-turn check: %w", err)
		}
		if hasOpenTurn(existingTurns) {
			return DecidePlanOutcome{}, ErrPlanOpenTurnInFlight
		}
	}

	var rowsAffected int64
	var err error
	switch verdict {
	case PlanVerdictApprove:
		rowsAffected, err = plans.WithTx(tx).ApproveIfAwaitingApproval(ctx, planID, sessionRow.ID, decidedBy)
	case PlanVerdictReject:
		rowsAffected, err = plans.WithTx(tx).RejectIfAwaitingApproval(ctx, planID, sessionRow.ID, decidedBy)
	default:
		return DecidePlanOutcome{}, fmt.Errorf("httpapi: unrecognized plan verdict %q", verdict)
	}
	if err != nil {
		return DecidePlanOutcome{}, fmt.Errorf("httpapi: guarded plan decision update: %w", err)
	}

	planRow, err := plans.WithTx(tx).Get(ctx, planID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Defensive: a stale/wrong plan id. Reported identically to
			// "already decided" (Won=false, FinalStatus empty) -- never a
			// hard error, matching planapprove.go's own pre-existing
			// "already decided, or a stale id" 409 message for this exact
			// case.
			return DecidePlanOutcome{Won: false}, nil
		}
		return DecidePlanOutcome{}, fmt.Errorf("httpapi: re-fetch plan after decision: %w", err)
	}
	if planRow.SessionID != sessionRow.ID {
		// Defensive, security-relevant: this re-fetch is BY PLAN ID ALONE
		// (plans.Get has no session_id filter), unlike the guarded UPDATE
		// above which IS correctly scoped to (planID, sessionRow.ID) and so
		// would already have affected 0 rows for a cross-session planID
		// (rowsAffected == 0, won == false below regardless). Without this
		// check, a caller supplying a planID that exists but belongs to a
		// DIFFERENT session (a forged/replayed Slack button value, a Linear
		// lookup bug, a malformed REST call, simple data confusion
		// elsewhere) would still have this re-fetch succeed and leak that
		// OTHER session's real, current plan status into FinalStatus --
		// which callers render straight into caller-facing text (e.g.
		// "already decided: <status>"). Treated EXACTLY like the
		// pgx.ErrNoRows case immediately above: never leak the mismatched
		// row's real status, just report "no such awaiting plan for THIS
		// session" (Won=false, FinalStatus empty).
		logger.Warn("httpapi: decide plan: planID belongs to a different session than the caller's own; refusing to leak its status", "plan_id", planID.String(), "requested_session_id", sessionRow.ID.String(), "actual_session_id", planRow.SessionID.String())
		return DecidePlanOutcome{Won: false}, nil
	}

	won := rowsAffected > 0
	outcome := DecidePlanOutcome{Won: won, FinalStatus: string(planRow.Status)}
	if !won {
		return outcome, nil
	}

	if verdict == PlanVerdictApprove {
		prompt := implementPlanPrompt
		createdTurn, err := turns.WithTx(tx).Create(ctx, sqlcgen.CreateTurnParams{
			SessionID: sessionRow.ID,
			Status:    sqlcgen.TurnStatusPending,
			Prompt:    &prompt,
			ModelID:   sessionRow.BuildModelID,
			PlanMode:  false,
		})
		if err != nil {
			return DecidePlanOutcome{}, fmt.Errorf("httpapi: create implementation turn: %w", err)
		}
		turnIDStr := createdTurn.ID.String()
		outcome.TurnID = &turnIDStr
	}

	if err := recordAuditLog(ctx, auditLog.WithTx(tx), decidedBy, "plan."+string(verdict), "plan", planID.String(), map[string]any{
		"session_id": sessionRow.ID.String(),
		"verdict":    string(verdict),
	}); err != nil {
		return DecidePlanOutcome{}, fmt.Errorf("httpapi: record plan decision audit log: %w", err)
	}

	if err := enqueuePlanDecisionNotifications(ctx, tx, outbox, linearAgentSessions, sessionRow, planRow, planDecisionOutcomeText(verdict)); err != nil {
		// Logged, not propagated: the decision itself (the guarded UPDATE
		// above, already durable once this transaction commits) must never
		// be rolled back merely because notifying an already-DECIDED
		// outcome to another channel failed to enqueue -- that would
		// re-litigate an already-final verdict over a notification
		// side-effect, exactly backwards from §5.1's own outbox philosophy
		// ("written in the same tx as the state change" describes
		// durability of the ENQUEUE, not a reason to fail the change
		// itself). A failed enqueue here is a real gap (the other
		// channel's message never gets its outcome update) -- logged
		// loudly so it is visible in practice; not built as a retried
		// background reconciliation, out of this Step's own scope.
		logger.Error("httpapi: enqueue plan decision cross-channel notifications failed", "error", err, "plan_id", planID.String())
	}

	return outcome, nil
}

// enqueuePlanDecisionNotifications implements this Step's own point 6
// ("notify the other channels"): if planRow carries a stored Slack message
// ref (slack_channel_id/slack_message_ts -- only ever set when the outbox's
// own Slack plan-approval notifier successfully posted THIS plan version's
// approval-request message), ALWAYS enqueue a
// ports.NotificationKindSlackPlanDecided row reflecting outcomeText,
// regardless of which channel actually rendered this decision (update-to-
// self is a harmless, no-op-shaped confirmation; update-to-a-different-
// channel is the real "notify" case -- this function does not need to know
// which). Likewise, if sessionRow is Linear-origin, ALWAYS enqueue a plain
// ports.NotificationKindLinear row (reusing the EXISTING kind/payload Step
// 35 already built -- no new Linear-specific kind needed, see this Step's
// own design note) describing the same outcome as a follow-up
// AgentActivity. A session can only ever be Slack-origin XOR Linear-origin
// (or web/GitHub) -- sessions.spawn_source is a single value -- so in
// practice at most one of these two branches ever fires for any given
// session; both are still checked unconditionally, since the ONLY signal
// this function needs is "does a delivery target exist for this plan/
// session", not "which one specific channel did the deciding".
func enqueuePlanDecisionNotifications(
	ctx context.Context,
	tx pgx.Tx,
	outbox *postgres.OutboxStore,
	linearAgentSessions *postgres.LinearAgentSessionStore,
	sessionRow sqlcgen.Session,
	planRow sqlcgen.Plan,
	outcomeText string,
) error {
	logger := platform.Logger(ctx)

	if planRow.SlackChannelID != nil && planRow.SlackMessageTs != nil && *planRow.SlackChannelID != "" && *planRow.SlackMessageTs != "" {
		payload, err := json.Marshal(slackapi.PlanDecidedPayload{
			ChannelID: *planRow.SlackChannelID,
			MessageTS: *planRow.SlackMessageTs,
			Text:      outcomeText,
		})
		if err != nil {
			return fmt.Errorf("marshal slack plan-decided payload: %w", err)
		}
		if _, err := outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
			SessionID: sessionRow.ID,
			Kind:      string(ports.NotificationKindSlackPlanDecided),
			Payload:   payload,
		}); err != nil {
			return fmt.Errorf("enqueue slack plan-decided outbox entry: %w", err)
		}
	}

	if sessionRow.SpawnSource == sqlcgen.SessionSpawnSourceLinear {
		row, err := linearAgentSessions.WithTx(tx).GetBySessionID(ctx, sessionRow.ID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Defensive -- see outboxenqueue.go's own identical
				// "should be unreachable in practice" note for the generic
				// turn-completion notification; never fatal to the
				// decision itself.
				logger.Warn("httpapi: linear-origin session has no linear_agent_sessions row; skipping plan-decided notify", "session_id", sessionRow.ID.String())
			} else {
				return fmt.Errorf("get linear agent session for plan-decided notify: %w", err)
			}
		} else {
			payload, err := json.Marshal(linearapi.Payload{
				AgentSessionID: row.AgentSessionID,
				OrganizationID: row.OrganizationID,
				Text:           outcomeText,
				Success:        true, // a rendered plan decision (approve or reject) is a normal outcome, never an "error" activity
			})
			if err != nil {
				return fmt.Errorf("marshal linear plan-decided payload: %w", err)
			}
			if _, err := outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
				SessionID: sessionRow.ID,
				Kind:      string(ports.NotificationKindLinear),
				Payload:   payload,
			}); err != nil {
				return fmt.Errorf("enqueue linear plan-decided outbox entry: %w", err)
			}
		}
	}

	return nil
}

// DecidePlan is the pool-based wrapper every caller with NO already-open
// transaction of its own uses (Slack's block_actions handler; Linear's
// handlePrompted keyword match) -- mirrors CreateSessionCore's own
// identical "own a single transaction start-to-finish, then trigger
// post-commit dispatch" shape exactly.
//
// A caller that is ALREADY holding an open transaction (e.g. one that took
// the session row's lock for its own authorization check first -- the REST
// approve/reject handlers, planapprove.go) must NOT call DecidePlan: doing
// so would open a SECOND, simultaneous connection out of the same pool
// while the first transaction's own connection is still held. That caller
// calls DecidePlanOnTx directly, inline on its own already-open tx, and
// calls TriggerDispatch itself once its own outer transaction has committed
// and outcome.Won && verdict == PlanVerdictApprove.
func DecidePlan(
	ctx context.Context,
	pool *pgxpool.Pool,
	sessions *postgres.SessionStore,
	turns *postgres.TurnStore,
	plans *postgres.PlanStore,
	outbox *postgres.OutboxStore,
	linearAgentSessions *postgres.LinearAgentSessionStore,
	auditLog *postgres.AuditLogStore,
	registry *sessionactor.Registry,
	sessionID, planID pgtype.UUID,
	verdict PlanVerdict,
	decidedBy pgtype.UUID,
) (DecidePlanOutcome, error) {
	logger := platform.Logger(ctx)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return DecidePlanOutcome{}, fmt.Errorf("httpapi: begin decide-plan tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	sessionRow, err := sessions.WithTx(tx).Get(ctx, sessionID)
	if err != nil {
		return DecidePlanOutcome{}, fmt.Errorf("httpapi: get session for plan decision: %w", err)
	}

	outcome, err := DecidePlanOnTx(ctx, tx, sessions, turns, plans, outbox, linearAgentSessions, auditLog, sessionRow, planID, verdict, decidedBy)
	if err != nil {
		return DecidePlanOutcome{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return DecidePlanOutcome{}, fmt.Errorf("httpapi: commit decide-plan tx: %w", err)
	}

	if outcome.Won && verdict == PlanVerdictApprove {
		TriggerDispatch(ctx, registry, sessionID)
	}

	logger.Info("httpapi: decided plan", "plan_id", planID.String(), "session_id", sessionID.String(), "verdict", string(verdict), "won", outcome.Won, "final_status", outcome.FinalStatus)
	return outcome, nil
}
