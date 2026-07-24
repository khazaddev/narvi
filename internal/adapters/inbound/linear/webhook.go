package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/intentclassifier"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
	plandomain "github.com/khazaddev/narvi/internal/domain/plan"
	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/platform"
)

// intentClassifierSurface is the sessions.spawn_source value (§18.1's
// IntentClassifierInput.Surface / §18.4's IntentDecisionRecord.Surface)
// this package's own agent sessions are classified/recorded under.
const intentClassifierSurface = "linear"

// maxRequestBodyBytes bounds the webhook body this handler reads --
// mirrors internal/adapters/inbound/httpapi's own identical constant
// (a package-private copy, not shared, matching this codebase's own
// per-package convention for this exact constant already established by
// that package).
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// acknowledgmentBody is the fixed text of the single `thought` Agent
// Activity this Step posts back immediately after creating a session --
// see internal/adapters/outbound/linearapi's own doc.go for why this is
// the one, minimal, direct outbound call this Step makes.
const acknowledgmentBody = "Narvi has started working on this."

// Deps bundles every dependency NewWebhookHandler needs -- a plain struct
// (rather than 10+ positional constructor parameters) since this handler
// genuinely needs this many collaborators: the webhook toolkit pieces
// (Step 31), the full session-creation path (CreateSessionCore), the
// Linear-specific dedupe/installation stores, and the outbound client.
type Deps struct {
	Pool          *pgxpool.Pool
	Sessions      *postgres.SessionStore
	Turns         *postgres.TurnStore
	Environments  *postgres.EnvironmentStore
	Registry      *sessionactor.Registry
	Deliveries    *postgres.WebhookDeliveryStore
	AgentSessions *postgres.LinearAgentSessionStore
	Installations *postgres.LinearInstallationStore
	LinearClient  *linearapi.Client

	// Plans/Outbox are Step 38's ("plan mode, cross-channel", §8.1/§13.3)
	// own additions -- handlePrompted's new plan-verdict keyword check
	// (below) needs Plans to find this session's own awaiting_approval
	// plan (if any) and, alongside AgentSessions/Registry above, to call
	// the shared httpapi.DecidePlan; Outbox is DecidePlan's own
	// cross-channel-notify dependency (decideplan.go).
	Plans  *postgres.PlanStore
	Outbox *postgres.OutboxStore

	// AuditLog is Step 39's own addition (§13.3) -- threaded through to
	// httpapi.CreateSessionCore/DecidePlan below exactly like Plans/Outbox
	// already are, so a Linear-originated session creation or plan
	// decision gets the SAME audit_log row every other caller of those two
	// shared functions now gets (actor_user_id NULL -- no human caller on
	// this channel yet, matching created_by/decidedBy's own existing
	// NULL-for-bot convention).
	AuditLog *postgres.AuditLogStore

	// IntentClassifier is Step 36's own wiring point (§8.3/§18): classify
	// + record runs ONCE, right after a `created` AgentSessionEvent's own
	// winning claim creates the backing session (decided_at_stage="create"
	// -- the full prompt text is already available at that point, via
	// payload.PromptContext). A `prompted` event on an already-backed
	// session never re-classifies. Optional (nil-safe): a nil
	// IntentClassifier simply skips classification entirely.
	IntentClassifier *intentclassifier.Service

	WebhookSecret      []byte
	TokenEncryptionKey []byte
	DefaultRepoName    string
	DefaultRepoURL     string

	Timeouts platform.Timeouts
}

// NewWebhookHandler backs POST /webhooks/linear: verifies Linear's own
// real webhook signature (signature.go), dedupes via
// Deliveries.Claim (provider "linear"), and routes an AgentSessionEvent
// to CreateSessionCore (a `created` action) or an existing session/turn
// (a `prompted` action) -- see doc.go for the full design and payload.go
// for the exact wire shapes this parses.
//
// Response codes: 401 on a bad/missing signature or an expired
// webhookTimestamp (fail closed -- Linear's own request is never trusted
// enough to even parse further); 400 on a malformed body or a missing
// Linear-Delivery header (this handler refuses to process anything it
// cannot dedupe); 200 for everything else, INCLUDING a downstream
// processing failure after the delivery has already been claimed -- see
// this func's own inline comment on why a genuine internal error is
// logged, not surfaced as a 5xx, once the delivery is claimed.
func NewWebhookHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Signature verified over the RAW body, before any JSON parsing --
		// see signature.go's own doc comment for the full scheme
		// confirmation. Fails closed: any error (missing/malformed header,
		// mismatched HMAC) is 401, the body is never even unmarshaled.
		if err := verifySignatureHeader(deps.WebhookSecret, rawBody, signatureHeaderFrom(r)); err != nil {
			logger.Warn("linear: webhook rejected: signature verification failed", "error", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		var payload agentSessionEventWebhookPayload
		if err := json.Unmarshal(rawBody, &payload); err != nil {
			logger.Warn("linear: webhook rejected: malformed body", "error", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// webhookTimestamp is milliseconds (Linear's own schema -- see
		// payload.go's own doc comment); platform.VerifyWebhookTimestamp
		// takes unix SECONDS, so convert before checking. Checked AFTER
		// the signature (Linear's own worked example does the same order),
		// against LinearWebhookTimestampWindow (60s, Linear's own explicit
		// recommendation -- NOT the generic, wider
		// WebhookTimestampFreshnessWindow Step 31 added).
		webhookTimestampSeconds := int64(payload.WebhookTimestamp / 1000)
		if err := platform.VerifyWebhookTimestamp(webhookTimestampSeconds, time.Now(), deps.Timeouts.LinearWebhookTimestampWindow); err != nil {
			logger.Warn("linear: webhook rejected: stale timestamp", "error", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		deliveryID := deliveryIDFrom(r)
		if deliveryID == "" {
			logger.Warn("linear: webhook rejected: missing Linear-Delivery header")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		claim, err := deps.Deliveries.Claim(ctx, "linear", deliveryID)
		if err != nil {
			logger.Error("linear: claim webhook delivery failed", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !claim.Inserted {
			logger.Info("linear: duplicate webhook delivery, skipping", "delivery_id", deliveryID)
			w.WriteHeader(http.StatusOK)
			return
		}

		// From here on, the delivery is claimed -- see this func's own doc
		// comment on why every path below responds 200 (even a genuine
		// processing failure, logged instead): ClaimWebhookDelivery's own
		// design (Step 31) has no "un-claim on failure" mechanism, so
		// returning 5xx here to provoke a Linear retry would only ever hit
		// this SAME claim again and be silently skipped as a "duplicate" --
		// worse than just logging the failure once, clearly, here.
		eventType := eventTypeFrom(r)
		if eventType != agentSessionEventType && payload.Type != agentSessionEventType {
			logger.Info("linear: ignoring non-AgentSessionEvent webhook category", "event_type", eventType)
			w.WriteHeader(http.StatusOK)
			return
		}

		switch payload.Action {
		case "created":
			deps.handleCreated(ctx, payload)
		case "prompted":
			deps.handlePrompted(ctx, payload)
		default:
			logger.Warn("linear: unrecognized AgentSessionEvent action, ignoring", "action", payload.Action)
		}

		w.WriteHeader(http.StatusOK)
	}
}

// handleCreated processes a `created` AgentSessionEvent: claims this
// Linear agent session's own identity (first-writer-wins -- see
// migrations/000030_linear_agent_sessions.up.sql's own doc comment),
// and -- only for the winner -- creates the backing Narvi session via
// CreateSessionCore, attaches the resulting session id back onto the
// claimed row, and posts the minimal acknowledgment Agent Activity.
func (deps Deps) handleCreated(ctx context.Context, payload agentSessionEventWebhookPayload) {
	logger := platform.Logger(ctx)

	claim, err := deps.AgentSessions.Claim(ctx, payload.AgentSession.ID, payload.OrganizationID)
	if err != nil {
		logger.Error("linear: claim agent session failed", "error", err, "agent_session_id", payload.AgentSession.ID)
		return
	}
	if !claim.Inserted {
		logger.Info("linear: duplicate created event for agent session, skipping", "agent_session_id", payload.AgentSession.ID)
		return
	}

	var title *string
	if payload.AgentSession.Issue != nil {
		t := fmt.Sprintf("%s: %s", payload.AgentSession.Issue.Identifier, payload.AgentSession.Issue.Title)
		title = &t
	}

	// promptContext is documented as present for every `created` event
	// (payload.go's own doc comment); defended against a nil value anyway
	// (never a naked nil-deref) with a short, honest fallback rather than
	// silently creating a session with no prompt at all.
	prompt := "Linear delegated or mentioned this agent session; no promptContext was supplied."
	if payload.PromptContext != nil {
		prompt = *payload.PromptContext
	}

	// Repos: see internal/platform/config.go's own
	// linearDefaultRepoNameEnvVarName doc comment for the full scope note
	// this stopgap operates under -- Linear's own AgentSessionEvent
	// payload carries no repository information at all, and every
	// CreateSessionRequest requires a non-empty Repos list regardless of
	// ingress surface.
	req := restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceLinear,
		Title:       restdtos.CreateSessionRequestTitle(title),
		Prompt:      restdtos.CreateSessionRequestPrompt(&prompt),
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: deps.DefaultRepoName, Url: deps.DefaultRepoURL},
		},
	}

	var nilCreator pgtype.UUID // Valid == false: no cookie, no human caller (see CreateSessionCore's own doc comment).

	created, cerr := httpapi.CreateSessionCore(ctx, deps.Pool, deps.Sessions, deps.Turns, deps.Environments, deps.AuditLog, deps.Registry, req, nilCreator)
	if cerr != nil {
		logger.Error("linear: create session failed", "status", cerr.Status, "message", cerr.Message, "agent_session_id", payload.AgentSession.ID)
		return
	}

	if err := deps.AgentSessions.SetSessionID(ctx, payload.AgentSession.ID, created.ID); err != nil {
		logger.Error("linear: attach session id to agent session claim failed", "error", err, "agent_session_id", payload.AgentSession.ID)
		return
	}

	// Step 36 ("intent classifier", §8.3/§18): classify + record ONCE,
	// right here -- IntentDecisionRecord is a per-SESSION record (§18.4),
	// and every Linear-originated session is created exactly here, with
	// its full prompt text already in hand. Runs entirely OUTSIDE any
	// Postgres transaction (a real outbound LLM call must never hold one
	// open) and never blocks the caller's own acknowledgment beyond this
	// synchronous call -- shadow mode (§18.5, the default until a surface
	// is explicitly configured active) means nothing downstream yet
	// consumes the recorded Target/Mode for real behavior regardless.
	if deps.IntentClassifier != nil {
		deps.recordIntentDecision(ctx, logger, created.ID, prompt)
	}

	logger.Info("linear: created session from agent session", "agent_session_id", payload.AgentSession.ID, "session_id", created.ID.String())

	deps.postAcknowledgment(ctx, payload.OrganizationID, payload.AgentSession.ID)
}

// handlePrompted processes a `prompted` AgentSessionEvent: routes to the
// existing Narvi session this agent session already backs (never
// creating a second one), unless the event carries Linear's own "stop"
// signal.
//
// Step 38 ("plan mode, cross-channel", §8.1/§13.3) update: BEFORE the
// existing unconditional turn-creation below, this now checks whether
// sessionID currently has an awaiting_approval plan and, if so, matches
// the reply's own trimmed/lower-cased text against plandomain.MatchVerdict
// -- on a match, calls the SAME shared httpapi.DecidePlan every other entry
// point uses (never a duplicated decision path), then posts a follow-up
// AgentActivity confirming the REAL outcome (honest either way: this
// call's own verdict if it won, or "already decided elsewhere" if a
// different channel won first -- outcome.Won/outcome.FinalStatus report
// the truth). On NO match (including when there is no awaiting_approval
// plan at all), this falls through to the EXISTING create-turn behavior
// completely unchanged -- this IS "request changes" (Step 37 already
// established that reusing ordinary turn-creation for feedback is
// correct).
func (deps Deps) handlePrompted(ctx context.Context, payload agentSessionEventWebhookPayload) {
	logger := platform.Logger(ctx)

	if payload.AgentActivity == nil {
		logger.Warn("linear: prompted event missing agentActivity, ignoring", "agent_session_id", payload.AgentSession.ID)
		return
	}

	if payload.AgentActivity.Signal != nil && *payload.AgentActivity.Signal == stopSignal {
		// Scope decision (Step 34): no session/turn STOP mechanism exists
		// in internal/app/sessionactor yet (confirmed during this Step's
		// investigation -- no Stop command type). Wiring a real stop is
		// out of this Step's own scope; this is logged clearly rather than
		// silently swallowed or forced through a mechanism that doesn't
		// exist.
		logger.Warn("linear: received stop signal, no stop mechanism wired yet (out of scope for this Step)", "agent_session_id", payload.AgentSession.ID)
		return
	}

	row, err := deps.AgentSessions.GetByAgentSessionID(ctx, payload.AgentSession.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("linear: prompted event for unknown agent session, ignoring", "agent_session_id", payload.AgentSession.ID)
			return
		}
		logger.Error("linear: look up agent session failed", "error", err, "agent_session_id", payload.AgentSession.ID)
		return
	}
	if !row.SessionID.Valid {
		logger.Warn("linear: prompted event for agent session still being claimed, ignoring", "agent_session_id", payload.AgentSession.ID)
		return
	}
	sessionID := row.SessionID

	if deps.Plans != nil {
		if planID, hasAwaiting := deps.findAwaitingApprovalPlanID(ctx, logger, sessionID); hasAwaiting {
			if verdict, ok := plandomain.MatchVerdict(payload.AgentActivity.Content.Body); ok {
				deps.handlePlanVerdict(ctx, logger, sessionID, planID, verdict, payload.OrganizationID, payload.AgentSession.ID)
				return
			}
		}
	}

	existingTurns, err := deps.Turns.ListForSession(ctx, sessionID)
	if err != nil {
		logger.Error("linear: list turns failed", "error", err, "session_id", sessionID.String())
		return
	}
	if hasOpenTurn(existingTurns) {
		// Scope decision (Step 34): mirrors httpapi.CreateTurn's own 409
		// precondition (§8.7, "exactly one processing per session"), but
		// there is no HTTP caller here to return a 409 to -- so a prompt
		// that arrives while a turn is already in flight is simply
		// dropped (logged), rather than queued or forced in. A real queue
		// (mirroring Linear's own "queued activities" concept) is a
		// natural follow-up, not built here.
		logger.Warn("linear: session already has an open turn, dropping prompted message", "session_id", sessionID.String())
		return
	}

	prompt := payload.AgentActivity.Content.Body
	if _, err := deps.Turns.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusPending,
		Prompt:    &prompt,
	}); err != nil {
		logger.Error("linear: create turn failed", "error", err, "session_id", sessionID.String())
		return
	}

	actor, err := deps.Registry.GetOrSpawn(ctx, sessionID)
	if err != nil {
		logger.Warn("linear: GetOrSpawn after turn create failed", "error", err)
		return
	}
	if err := actor.Send(ctx, sessionactor.EnsureDispatched{}); err != nil {
		logger.Warn("linear: send EnsureDispatched after turn create failed", "error", err)
	}
}

// findAwaitingApprovalPlanID reports sessionID's own current
// awaiting_approval plan id, if any -- a plain scan over
// ListPlanSummariesForSession (the SAME query planrecord.go's own
// recordPlanIfNeeded already uses), since the partial unique index
// (plans_one_awaiting_approval_per_session) guarantees at most one match
// in practice; this scans defensively rather than assuming that. A lookup
// failure is logged and treated as "no awaiting plan" (false) -- a
// keyword-parsing convenience must never turn into a hard failure of the
// underlying `prompted` webhook handling.
func (deps Deps) findAwaitingApprovalPlanID(ctx context.Context, logger *slog.Logger, sessionID pgtype.UUID) (pgtype.UUID, bool) {
	summaries, err := deps.Plans.ListSummariesForSession(ctx, sessionID)
	if err != nil {
		logger.Warn("linear: list plan summaries for verdict check failed", "error", err, "session_id", sessionID.String())
		return pgtype.UUID{}, false
	}
	for _, s := range summaries {
		if s.Status == sqlcgen.PlanStatusAwaitingApproval {
			return s.ID, true
		}
	}
	return pgtype.UUID{}, false
}

// handlePlanVerdict calls the shared httpapi.DecidePlan with bot
// attribution (an explicitly invalid decidedBy, matching this Step's own
// precedent for Slack/Linear-originated decisions -- see decideplan.go's
// own top doc comment), then posts a follow-up `response` AgentActivity
// describing the REAL final outcome, whether this call itself won or a
// different channel already decided first.
func (deps Deps) handlePlanVerdict(ctx context.Context, logger *slog.Logger, sessionID, planID pgtype.UUID, verdict string, organizationID, agentSessionID string) {
	var noDecider pgtype.UUID // Valid == false: bot/channel attribution, matching sessions.created_by's own existing convention for these two channels.
	outcome, err := httpapi.DecidePlan(ctx, deps.Pool, deps.Sessions, deps.Turns, deps.Plans, deps.Outbox, deps.AgentSessions, deps.AuditLog, deps.Registry, sessionID, planID, httpapi.PlanVerdict(verdict), noDecider)
	if err != nil {
		if errors.Is(err, httpapi.ErrPlanOpenTurnInFlight) {
			deps.postPlanOutcomeActivity(ctx, logger, organizationID, agentSessionID, "A revision is already in progress for this plan -- try again once it completes.")
			return
		}
		logger.Error("linear: decide plan failed", "error", err, "plan_id", planID.String(), "session_id", sessionID.String())
		return
	}

	deps.postPlanOutcomeActivity(ctx, logger, organizationID, agentSessionID, renderLinearPlanOutcomeText(outcome))
}

// renderLinearPlanOutcomeText mirrors internal/adapters/inbound/slack's
// own renderPlanOutcomeText (interactive.go) -- reports outcome.FinalStatus
// honestly, whether THIS call won (the verdict it itself just rendered) or
// the plan was already decided by a DIFFERENT channel first (an honest
// "already decided elsewhere" reply, never a confusing duplicate --
// point 5 of this Step's own brief).
func renderLinearPlanOutcomeText(outcome httpapi.DecidePlanOutcome) string {
	switch outcome.FinalStatus {
	case "approved":
		if outcome.Won {
			return "Approved -- implementation started."
		}
		return "This plan was already approved via a different channel."
	case "rejected":
		if outcome.Won {
			return "Rejected."
		}
		return "This plan was already rejected via a different channel."
	case "superseded":
		return "This plan was superseded by a newer revision before your reply arrived."
	default:
		return "This plan is no longer awaiting approval."
	}
}

// postPlanOutcomeActivity posts a single `response` Agent Activity
// describing a plan decision's own outcome -- mirrors postAcknowledgment's
// own install-lookup/decrypt/bounded-call shape exactly, but always
// CreateResponseActivity (a rendered decision, approve or reject, is a
// normal outcome, never an "error" activity -- matches decideplan.go's own
// identical Success:true convention for the cross-channel notify path).
// Best-effort only: any failure is logged and swallowed, mirroring
// postAcknowledgment's own identical tolerance.
func (deps Deps) postPlanOutcomeActivity(ctx context.Context, logger *slog.Logger, organizationID, agentSessionID, text string) {
	install, err := deps.Installations.GetByOrganizationID(ctx, organizationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("linear: no installation for organization, skipping plan-outcome activity", "organization_id", organizationID)
			return
		}
		logger.Error("linear: look up installation failed", "error", err, "organization_id", organizationID)
		return
	}

	accessToken, err := platform.DecryptToken(deps.TokenEncryptionKey, install.AccessTokenEncrypted)
	if err != nil {
		logger.Error("linear: decrypt installation access token failed", "error", err, "organization_id", organizationID)
		return
	}

	activityCtx, cancel := context.WithTimeout(ctx, deps.Timeouts.LinearOutboundActivityTimeout)
	defer cancel()

	if err := deps.LinearClient.CreateResponseActivity(activityCtx, string(accessToken), agentSessionID, text); err != nil {
		logger.Error("linear: post plan-outcome activity failed", "error", err, "agent_session_id", agentSessionID)
	}
}

// hasOpenTurn reports whether ANY turn in turns is non-terminal --
// mirrors internal/adapters/inbound/httpapi's own identical helper
// (CreateTurn's own precondition, turn.go) exactly; not reachable from
// this package since it is unexported there, so duplicated here rather
// than exported solely for this one call site.
func hasOpenTurn(turns []sqlcgen.Turn) bool {
	for _, t := range turns {
		if !turn.IsTerminal(turn.State(t.Status)) {
			return true
		}
	}
	return false
}

// postAcknowledgment posts the single, minimal `thought` Agent Activity
// this Step's own outbound scope covers (see this package's own doc.go
// and internal/adapters/outbound/linearapi's own doc.go). Best-effort
// only: any failure (no installation for this workspace, an expired
// token, a network error) is logged and otherwise swallowed -- it must
// never fail the webhook response itself, since the Narvi session this
// event backs has already been created successfully by this point.
//
// Known limitation (Step 34, explicitly scoped out): this does not
// refresh an expired access token before use. Linear's own OAuth access
// tokens are short-lived (confirmed during this Step's investigation:
// "valid for 24 hours"); linear_installations.refresh_token_encrypted is
// stored precisely so a future Step can add real refresh-before-expiry
// logic, but implementing that is beyond "the smallest immediate
// outbound need" this Step's own scope note calls for. Until a future
// Step adds it, this call simply starts failing (logged, non-fatal) once
// a workspace's stored token expires, until an admin reconnects it.
func (deps Deps) postAcknowledgment(ctx context.Context, organizationID, agentSessionID string) {
	logger := platform.Logger(ctx)

	install, err := deps.Installations.GetByOrganizationID(ctx, organizationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("linear: no installation for organization, skipping acknowledgment activity", "organization_id", organizationID)
			return
		}
		logger.Error("linear: look up installation failed", "error", err, "organization_id", organizationID)
		return
	}

	accessToken, err := platform.DecryptToken(deps.TokenEncryptionKey, install.AccessTokenEncrypted)
	if err != nil {
		logger.Error("linear: decrypt installation access token failed", "error", err, "organization_id", organizationID)
		return
	}

	activityCtx, cancel := context.WithTimeout(ctx, deps.Timeouts.LinearOutboundActivityTimeout)
	defer cancel()

	if err := deps.LinearClient.CreateThoughtActivity(activityCtx, string(accessToken), agentSessionID, acknowledgmentBody); err != nil {
		logger.Error("linear: post acknowledgment activity failed", "error", err, "agent_session_id", agentSessionID)
	}
}

// recordIntentDecision runs Step 36's own classify+record step (§8.3/§18)
// against prompt -- see handleCreated's own doc comment for why this is
// only ever called once per session, right after its own creation.
func (deps Deps) recordIntentDecision(ctx context.Context, logger *slog.Logger, sessionID pgtype.UUID, prompt string) {
	decision := deps.IntentClassifier.Classify(ctx, ports.IntentClassifierInput{
		Text:    prompt,
		Surface: intentClassifierSurface,
	})

	var confidence, reasoning *string
	if decision.Source == ports.IntentSourceClassifier {
		confVal := decision.Confidence
		confidence = &confVal
		reasonVal := intentdomain.TruncateReasoning(decision.Reasoning)
		reasoning = &reasonVal
	}

	if _, err := deps.IntentClassifier.RecordDecision(ctx, sessionID, intentdomain.IntentDecisionRecord{
		Surface:        intentClassifierSurface,
		Source:         decision.Source,
		Target:         decision.Target,
		Mode:           decision.Mode,
		Confidence:     confidence,
		Reasoning:      reasoning,
		DecidedAt:      time.Now(),
		DecidedAtStage: intentdomain.DecidedAtStageCreate,
	}); err != nil {
		logger.Warn("linear: record intent decision failed", "error", err, "session_id", sessionID)
	}
}
