// This file (interactive.go) implements Step 38's ("plan mode,
// cross-channel", §8.1/§13.3) own new Slack ingress route: POST
// /webhooks/slack/interactive, a STRUCTURALLY DIFFERENT payload shape from
// this package's existing POST /webhooks/slack (handler.go, Step 33's
// Events API ingress) -- Slack's own real Interactivity payload arrives
// form-encoded (Content-Type: application/x-www-form-urlencoded) with a
// single "payload" field carrying URL-encoded JSON, never the Events API's
// raw JSON body. This is deliberately a SEPARATE handler/route, never
// folded into NewHandler above: the two request shapes share almost
// nothing (form vs JSON body) beyond both being HMAC-signed the same way
// (platform.VerifyWebhookSignature operates on raw bytes regardless of
// encoding, so it is reused here unchanged -- see verifySlackRequest,
// handler.go).
//
// # Operator setup note (real, external, one-time -- not automatable here)
//
// This endpoint requires enabling "Interactivity & Shortcuts" in the
// Slack App's own configuration (api.slack.com/apps -> your app ->
// Interactivity & Shortcuts), with its "Request URL" pointed at this
// route (POST .../webhooks/slack/interactive) -- a DIFFERENT configured
// URL from the Events API's own "Request URL" (POST .../webhooks/slack).
// Until an operator does this, real button clicks and modal submissions
// will either never arrive at all, or (if pointed at the wrong URL) fail
// this handler's own signature verification -- no code change here can
// perform that one-time App-dashboard configuration step.
//
// # Dispatch, by the inbound payload's own "type" field
//
//   - "block_actions" (a button was clicked): approve_plan/reject_plan call
//     the shared httpapi.DecidePlan synchronously, then a real,
//     synchronous chat.update (slackapi.Client.UpdateMessage) reflects the
//     outcome on the SAME message -- both run under ONE context bounded by
//     platform.Timeouts.SlackInteractivityAckTimeout (see that field's own
//     doc comment, platform/timeouts.go, for why this is a SEPARATE, much
//     tighter constant from SlackAckTimeout), so the pair together never
//     blows past Slack's own real ~3-second interactivity-ack budget.
//     request_changes_plan calls views.open (using the inbound trigger_id,
//     valid only a few seconds), bounded by the same constant, and does NO
//     turn-creation work yet -- that happens on the LATER view_submission
//     below. Every path responds 200 immediately after, regardless of
//     whether the bounded work above finished or hit its own deadline.
//   - "view_submission" (the request-changes modal was submitted): the
//     submitted feedback text + planId/sessionId (from the view's own
//     private_metadata, set when views.open was called) create a new
//     turn (plan_mode=true) via httpapi.CreateTurnCore -- the EXACT SAME
//     function POST .../turns itself calls, never a third, duplicated
//     turn-creation call site. Responds with Slack's own required
//     empty-body 200 (closes the modal).
//   - anything else: logged and 200'd as a no-op -- a future Slack
//     interaction type this handler doesn't yet understand must degrade
//     gracefully, never crash or 500.
//
// Slack/Linear verdicts stay UNAUTHENTICATED-per-user throughout (this
// Step's own explicit precedent, decideplan.go's own top doc comment) --
// this handler never attempts to resolve a Slack user id to a Narvi
// user_id; every decision/turn this handler causes is bot-attributed
// (Valid == false), exactly like every other webhook-originated action in
// this codebase today.

package slack

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// InteractiveDeps bundles every dependency NewInteractivityHandler needs --
// a separate struct from Deps (handler.go): this route's own payload shape
// and downstream calls (DecidePlan, CreateTurnCore, real Block Kit
// updates/modals) share almost nothing with the Events API ingress's own
// Deps, so keeping them distinct mirrors how internal/adapters/inbound/
// linear already gives its OAuth-install handlers and its webhook handler
// each their own parameter shape rather than one bloated shared struct.
type InteractiveDeps struct {
	Pool                *pgxpool.Pool
	Sessions            *postgres.SessionStore
	Turns               *postgres.TurnStore
	Plans               *postgres.PlanStore
	Outbox              *postgres.OutboxStore
	LinearAgentSessions *postgres.LinearAgentSessionStore
	Registry            *sessionactor.Registry
	SlackClient         *slackapi.Client

	// AuditLog is Step 39's own addition (§13.3) -- threaded through to
	// httpapi.DecidePlan/CreateTurnCore below exactly like Plans/Outbox
	// already are, so a Slack-decided plan verdict or a Slack "Request
	// changes" turn gets the SAME audit_log row every other caller of
	// those two shared functions now gets (actor_user_id NULL -- no human
	// caller on this channel yet, mirrors decidedBy's own existing
	// NULL-for-bot convention).
	AuditLog *postgres.AuditLogStore

	SigningSecret string
	Timeouts      platform.Timeouts
}

// blockActionsPayload is the subset of Slack's own real block_actions
// interaction payload this handler needs (verified against Slack's current
// reference docs, docs.slack.dev/reference/interaction-payloads/
// block_actions-payload, during this Step's own investigation).
type blockActionsPayload struct {
	Type      string `json:"type"`
	TriggerID string `json:"trigger_id"`
	Channel   struct {
		ID string `json:"id"`
	} `json:"channel"`
	Message struct {
		Ts string `json:"ts"`
	} `json:"message"`
	Actions []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
}

// viewSubmissionPayload is the subset of Slack's own real view_submission
// interaction payload this handler needs. State.Values is Slack's own
// documented "block_id -> action_id -> {value}" shape for a plain_text_input
// element's submitted value.
type viewSubmissionPayload struct {
	Type string `json:"type"`
	View struct {
		CallbackID      string `json:"callback_id"`
		PrivateMetadata string `json:"private_metadata"`
		State           struct {
			Values map[string]map[string]struct {
				Value *string `json:"value"`
			} `json:"values"`
		} `json:"state"`
	} `json:"view"`
}

// interactionEnvelope is the minimal shape read first, JUST to learn the
// payload's own "type" -- mirrors handler.go's own challengeEnvelope/
// eventEnvelope "peek the cheap common field first" precedent.
type interactionEnvelope struct {
	Type string `json:"type"`
}

// NewInteractivityHandler backs POST /webhooks/slack/interactive -- see
// this file's own top doc comment for the full design and the real,
// external, one-time operator setup step this route requires.
func NewInteractivityHandler(deps InteractiveDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		body, ok := readBoundedBody(w, r)
		if !ok {
			return
		}

		// Signature verification is IDENTICAL to the Events API ingress's
		// own (verifySlackRequest, handler.go) -- Slack signs the exact raw
		// request body regardless of its own Content-Type/encoding, so
		// platform.VerifyWebhookSignature is directly reusable here
		// unchanged (this file's own top doc comment).
		if !verifySlackRequest(w, r, body, deps.SigningSecret, deps.Timeouts.WebhookTimestampFreshnessWindow, logger) {
			return
		}

		// The body is form-encoded (application/x-www-form-urlencoded),
		// NOT JSON -- url.ParseQuery on the already-read raw bytes (rather
		// than r.ParseForm, which would try to re-read the already-drained
		// r.Body) extracts the single "payload" field, Slack's own
		// URL-encoded JSON.
		values, err := url.ParseQuery(string(body))
		if err != nil {
			writeError(w, http.StatusBadRequest, "malformed form-encoded body")
			return
		}
		rawPayload := values.Get("payload")
		if rawPayload == "" {
			writeError(w, http.StatusBadRequest, "missing payload field")
			return
		}

		var envelope interactionEnvelope
		if err := json.Unmarshal([]byte(rawPayload), &envelope); err != nil {
			writeError(w, http.StatusBadRequest, "malformed payload JSON")
			return
		}

		switch envelope.Type {
		case "block_actions":
			deps.handleBlockActions(ctx, logger, []byte(rawPayload))
		case "view_submission":
			deps.handleViewSubmission(ctx, logger, []byte(rawPayload))
		default:
			// A future Slack interaction type this handler doesn't yet
			// understand -- logged, never a crash or 500 (this file's own
			// top doc comment).
			logger.Info("slack: interactivity: ignoring unrecognized payload type", "type", envelope.Type)
		}

		w.WriteHeader(http.StatusOK)
	}
}

// handleBlockActions dispatches on the clicked action's own action_id --
// approve_plan/reject_plan decide synchronously (and update the message
// synchronously, for immediate visual feedback); request_changes_plan
// opens the feedback modal. Every path here must stay fast: Slack requires
// an ack within 3 seconds, and the caller (NewInteractivityHandler above)
// writes that 200 immediately after this returns.
func (deps InteractiveDeps) handleBlockActions(ctx context.Context, logger *slog.Logger, raw []byte) {
	var payload blockActionsPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		logger.Warn("slack: interactivity: decode block_actions payload failed", "error", err)
		return
	}
	if len(payload.Actions) == 0 {
		logger.Warn("slack: interactivity: block_actions payload has no actions")
		return
	}
	action := payload.Actions[0]

	planIDStr, sessionIDStr, ok := slackapi.DecodePlanActionValue(action.Value)
	if !ok {
		logger.Warn("slack: interactivity: malformed action value", "action_id", action.ActionID, "value", action.Value)
		return
	}

	switch action.ActionID {
	case slackapi.ActionApprovePlan, slackapi.ActionRejectPlan:
		deps.decideAndUpdateMessage(ctx, logger, planIDStr, sessionIDStr, action.ActionID, payload.Channel.ID, payload.Message.Ts)
	case slackapi.ActionRequestChangesPlan:
		deps.openRequestChangesModal(ctx, logger, payload.TriggerID, planIDStr, sessionIDStr)
	default:
		logger.Info("slack: interactivity: unrecognized action_id", "action_id", action.ActionID)
	}
}

// decideAndUpdateMessage calls the shared httpapi.DecidePlan (decideplan.go)
// with bot attribution (an explicitly invalid decidedBy, matching this
// Step's own precedent for Slack/Linear-originated decisions), then
// synchronously updates the SAME Slack message to reflect the REAL final
// outcome -- whether THIS click won or the plan was already decided
// elsewhere (DecidePlanOutcome.FinalStatus reports the truth either way,
// so this never shows a misleading "pending" state after the fact).
//
// The WHOLE sequence (DecidePlan's own guarded-UPDATE transaction, then the
// chat.update call) runs under a SINGLE context bounded by
// deps.Timeouts.SlackInteractivityAckTimeout -- one shared budget for both
// calls together, not two separately-bounded calls that could each
// individually fit their own timeout yet still together blow past Slack's
// real ~3s interactivity-ack window (see that field's own doc comment,
// platform/timeouts.go). If this bounded context expires mid-flight (DB
// contention, a slow Slack response), DecidePlan/UpdateMessage simply
// return a context error quickly, which is logged here -- the caller
// (NewInteractivityHandler above) still answers Slack with its own
// unconditional 200 right after this function returns, exactly matching
// Slack's own documented contract of acking within the window regardless of
// whether the underlying work has actually finished.
func (deps InteractiveDeps) decideAndUpdateMessage(ctx context.Context, logger *slog.Logger, planIDStr, sessionIDStr, actionID, channel, messageTS string) {
	var planID, sessionID pgtype.UUID
	if err := planID.Scan(planIDStr); err != nil {
		logger.Warn("slack: interactivity: parse plan id failed", "error", err, "plan_id", planIDStr)
		return
	}
	if err := sessionID.Scan(sessionIDStr); err != nil {
		logger.Warn("slack: interactivity: parse session id failed", "error", err, "session_id", sessionIDStr)
		return
	}

	verdict := httpapi.PlanVerdictApprove
	if actionID == slackapi.ActionRejectPlan {
		verdict = httpapi.PlanVerdictReject
	}

	decideCtx, cancel := context.WithTimeout(ctx, deps.Timeouts.SlackInteractivityAckTimeout)
	defer cancel()

	var noDecider pgtype.UUID // Valid == false: bot/channel attribution, matching sessions.created_by's own existing convention for these two channels.
	outcome, err := httpapi.DecidePlan(decideCtx, deps.Pool, deps.Sessions, deps.Turns, deps.Plans, deps.Outbox, deps.LinearAgentSessions, deps.AuditLog, deps.Registry, sessionID, planID, verdict, noDecider)

	var text string
	switch {
	case err != nil && errors.Is(err, httpapi.ErrPlanOpenTurnInFlight):
		text = "A revision is already in progress for this plan — try again once it completes."
	case err != nil:
		logger.Error("slack: interactivity: decide plan failed", "error", err, "plan_id", planIDStr, "session_id", sessionIDStr)
		text = "Something went wrong recording this decision. Please try again."
	default:
		text = renderPlanOutcomeText(outcome)
	}

	deps.updateMessage(decideCtx, logger, channel, messageTS, text)
}

// renderPlanOutcomeText renders outcome.FinalStatus honestly, whether THIS
// call won (Won == true, the verdict it itself just rendered) or the plan
// was already decided by a different entry point (Won == false) -- see
// decideAndUpdateMessage's own doc comment for why both cases update the
// SAME message with the real truth, never a stale "pending" state.
func renderPlanOutcomeText(outcome httpapi.DecidePlanOutcome) string {
	switch outcome.FinalStatus {
	case "approved":
		if outcome.Won {
			return "✅ Approved — implementation started."
		}
		return "✅ Already approved (via a different channel)."
	case "rejected":
		if outcome.Won {
			return "❌ Rejected."
		}
		return "❌ Already rejected (via a different channel)."
	case "superseded":
		return "This plan was superseded by a newer revision."
	default:
		return "This plan is no longer awaiting approval."
	}
}

// updateMessage calls slackapi.Client.UpdateMessage using ctx AS GIVEN, with
// no additional wrapping -- unlike this package's ack.go (postAckBounded),
// which owns the ONLY bounded context for its own single call, this
// function's caller (decideAndUpdateMessage above) already derived ctx from
// a SINGLE context.WithTimeout(deps.Timeouts.SlackInteractivityAckTimeout)
// shared across both the preceding httpapi.DecidePlan call and this
// chat.update call -- adding a second, independent budget here would
// reintroduce exactly the "two calls that each individually fit but
// together exceed Slack's real ack window" failure mode that shared budget
// exists to prevent.
func (deps InteractiveDeps) updateMessage(ctx context.Context, logger *slog.Logger, channel, messageTS, text string) {
	if channel == "" || messageTS == "" {
		logger.Warn("slack: interactivity: missing channel/message ts, skipping chat.update")
		return
	}
	if err := deps.SlackClient.UpdateMessage(ctx, channel, messageTS, text); err != nil {
		logger.Warn("slack: interactivity: chat.update failed", "error", err)
	}
}

// openRequestChangesModal calls views.open using triggerID from the
// inbound block_actions payload that just fired -- Slack's own trigger_id
// is valid for only a few seconds, so this must run promptly (no slow work
// before it, matching handleBlockActions's own fast-path requirement).
// Bounded by SlackInteractivityAckTimeout, not SlackAckTimeout: this call is
// on the SAME block_actions interactivity-ack path decideAndUpdateMessage's
// own doc comment describes (a single, real outbound API call, so it was
// already comfortably inside either budget), reused here for consistency
// now that a dedicated, correctly-scoped constant exists for this path.
func (deps InteractiveDeps) openRequestChangesModal(ctx context.Context, logger *slog.Logger, triggerID, planIDStr, sessionIDStr string) {
	if triggerID == "" {
		logger.Warn("slack: interactivity: request_changes_plan action with no trigger_id, cannot open modal")
		return
	}
	openCtx, cancel := context.WithTimeout(ctx, deps.Timeouts.SlackInteractivityAckTimeout)
	defer cancel()
	if err := deps.SlackClient.OpenView(openCtx, triggerID, planIDStr, sessionIDStr); err != nil {
		logger.Error("slack: interactivity: views.open failed", "error", err)
	}
}

// handleViewSubmission processes the "Request changes" modal's own
// submission: the feedback text (read back from view.state.values, keyed
// by slackapi.RequestChangesBlockID/RequestChangesActionID) becomes a new
// plan_mode=true turn's prompt, via httpapi.CreateTurnCore -- the EXACT
// SAME function POST .../turns itself calls (turn.go), never a third,
// duplicated turn-creation call site. planId is decoded from
// private_metadata only to validate the payload's own shape; the actual
// turn creation only needs sessionId (a plan's own identity is not
// re-checked here -- creating a plan_mode=true "request changes" turn is
// unconditionally safe regardless of the named plan's current status,
// exactly matching how the existing POST .../turns endpoint already
// behaves for every other "request changes" submission, Step 37's own
// design).
func (deps InteractiveDeps) handleViewSubmission(ctx context.Context, logger *slog.Logger, raw []byte) {
	var payload viewSubmissionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		logger.Warn("slack: interactivity: decode view_submission payload failed", "error", err)
		return
	}
	if payload.View.CallbackID != slackapi.RequestChangesCallbackID {
		logger.Info("slack: interactivity: unrecognized view_submission callback_id", "callback_id", payload.View.CallbackID)
		return
	}

	_, sessionIDStr, ok := slackapi.DecodePlanActionValue(payload.View.PrivateMetadata)
	if !ok {
		logger.Warn("slack: interactivity: malformed private_metadata", "private_metadata", payload.View.PrivateMetadata)
		return
	}

	var feedback string
	if block, ok := payload.View.State.Values[slackapi.RequestChangesBlockID]; ok {
		if elem, ok := block[slackapi.RequestChangesActionID]; ok && elem.Value != nil {
			feedback = *elem.Value
		}
	}
	if strings.TrimSpace(feedback) == "" {
		logger.Warn("slack: interactivity: empty feedback text in view_submission, ignoring")
		return
	}

	var sessionID pgtype.UUID
	if err := sessionID.Scan(sessionIDStr); err != nil {
		logger.Warn("slack: interactivity: parse session id failed", "error", err, "session_id", sessionIDStr)
		return
	}

	// noActor: Valid == false, bot/channel attribution -- mirrors
	// handlePlanVerdict's own noDecider precedent immediately above
	// exactly (§13.3, Step 39's own hand-off: a real per-user actor here
	// needs identity auto-linking, out of THIS Step's own scope).
	var noActor pgtype.UUID
	if _, cerr := httpapi.CreateTurnCore(ctx, deps.Pool, deps.Sessions, deps.Turns, deps.AuditLog, deps.Registry, sessionID, feedback, nil, true, noActor); cerr != nil {
		logger.Error("slack: interactivity: create request-changes turn failed", "status", cerr.Status, "message", cerr.Message, "session_id", sessionIDStr)
	}
}
