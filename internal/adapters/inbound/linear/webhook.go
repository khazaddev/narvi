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
	"github.com/khazaddev/narvi/internal/app/actorauthz"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/app/intentclassifier"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/authz"
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

	// Participants is Step 39's own addition ("identities + full RBAC",
	// §13.2/§13.3) -- identity.go's own authorizeSessionAction/ownedOrJoined
	// need this to resolve a `member` actor's own "own/joined" carve-out
	// exactly like httpapi's canActOnPlan/CreateTurn already do, so a
	// Linear-decided plan verdict or ordinary reply ("request changes")
	// renders the IDENTICAL §13.3 verdict a REST caller would for the same
	// (actor, session).
	Participants *postgres.ParticipantStore

	// AuditLog is Step 39's own addition (§13.3) -- threaded through to
	// httpapi.CreateSessionCore/DecidePlan below exactly like Plans/Outbox
	// already are, so a Linear-originated session creation or plan
	// decision gets the SAME audit_log row every other caller of those two
	// shared functions now gets. actor_user_id is NULL only until identity
	// auto-linking (IdentityLink below) resolves a real user -- see this
	// file's own resolveActor for the replacement of the old unconditional
	// bot-attribution precedent.
	AuditLog *postgres.AuditLogStore

	// IdentityLink is Step 39's own auto-linking wiring (§13.2): resolves
	// a Linear user id (AgentSession.CreatorID for a `created` event,
	// AgentActivity.UserID for a `prompted` one) to a real Narvi user_id,
	// auto-linking or creating a magic-link prompt the first time this
	// package sees a given Linear user id it doesn't already know about.
	// See resolveActor's own doc comment for the full replacement of this
	// package's previous unconditional bot-attribution behavior.
	IdentityLink identitylink.Deps

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

	// SessionIDSetter, when non-nil, is used INSTEAD of AgentSessions for
	// setSessionIDWithRetry's own retried call (retry.go) -- nil-safe:
	// every real caller (cmd/control-plane/main.go) leaves this unset,
	// falling back to AgentSessions itself (which already satisfies this
	// narrow interface), exactly as if this field did not exist at all.
	// Exists ONLY so this package's own tests can substitute a fake that
	// fails SetSessionID a controlled number of times before delegating to
	// a real store, without needing to also fake Claim/Release/
	// GetByAgentSessionID (mirrors github's own PullRequestResolver
	// nil-safe-fallback precedent, headresolve.go).
	SessionIDSetter sessionIDSetter
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
// cannot dedupe); 200 for a duplicate delivery, an ignored event category,
// or a genuinely handled `created`/`prompted` event (including a
// deliberate business decision inside it, like an authz denial -- see
// handleCreated's/handlePrompted's own doc comments for which branches
// those are); 500, WITH the webhook-delivery claim released
// (WebhookDeliveryStore.Release), for a genuine post-claim processing
// failure (a DB error resolving/creating the session or turn) --
// H2 audit fix ("webhook claim/release parity"), correcting this
// comment's own previous, factually stale claim that no such release
// mechanism exists: WebhookDeliveryStore.Release has existed since Step
// 31 (postgres/webhookdelivery_store.go) and github's own handler.go
// already uses it identically. Releasing lets a redelivery of this same
// Linear-Delivery id actually retry, rather than the event being silently
// and permanently dropped now that it's claimed.
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

		// From here on, the delivery is claimed. H2 audit fix ("webhook
		// claim/release parity"): handleCreated/handlePrompted below report
		// ok=false ONLY for a genuine post-claim processing failure -- see
		// each one's own doc comment for the exact ok=false branches --
		// which this func releases the claim for and answers non-2xx,
		// mirroring github's own identical release-on-failure pattern
		// (handler.go), so a redelivery of this same Linear-Delivery id can
		// actually retry rather than being silently skipped forever as an
		// already-claimed duplicate.
		eventType := eventTypeFrom(r)
		if eventType != agentSessionEventType && payload.Type != agentSessionEventType {
			logger.Info("linear: ignoring non-AgentSessionEvent webhook category", "event_type", eventType)
			w.WriteHeader(http.StatusOK)
			return
		}

		ok := true
		switch payload.Action {
		case "created":
			ok = deps.handleCreated(ctx, payload)
		case "prompted":
			ok = deps.handlePrompted(ctx, payload)
		default:
			logger.Warn("linear: unrecognized AgentSessionEvent action, ignoring", "action", payload.Action)
		}

		if !ok {
			if releaseErr := deps.Deliveries.Release(ctx, "linear", deliveryID); releaseErr != nil {
				logger.Error("linear: release webhook delivery claim failed", "error", releaseErr, "delivery_id", deliveryID)
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
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
//
// Returns ok=false ONLY for a genuine post-(webhook-delivery-)claim
// processing failure that happens BEFORE any session/turn exists at all --
// H2 audit fix ("webhook claim/release parity"): the AgentSessions.Claim
// call itself erroring, or a CreateSessionCore error. The caller
// (NewWebhookHandler) releases the WEBHOOK-DELIVERY claim and answers
// non-2xx on ok=false, so a redelivery of this same Linear-Delivery id can
// retry -- safe here specifically because nothing has been created or
// dispatched yet, so a redelivery-triggered re-run of this whole function
// can only ever produce ONE session, never a duplicate. Every other return
// (a duplicate `created` event, an authz denial, and -- see below -- a
// SetSessionID failure) is ok=true: either a deliberate business decision
// (retrying would render the identical duplicate/denial verdict again,
// mirroring github's own ErrActorNotAuthorized-vs-genuine-error
// distinction, coalesce.go) or, for SetSessionID, a case where a retry via
// redelivery would actively make things WORSE (see below).
//
// Independently of the webhook-delivery claim above, THIS function also
// manages the SEPARATE linear_agent_sessions claim it wins via
// AgentSessions.Claim (H3 audit fix, same "webhook claim/release parity"
// finding): on the authz-denial and CreateSessionCore-error branches ONLY,
// it releases that claim too (AgentSessions.Release, guarded to a
// still-NULL session_id) -- otherwise EITHER would leave the row
// permanently stuck (NULL session_id forever), dropping every future
// prompt/redelivery for this agent_session_id regardless of what the
// webhook-delivery claim itself does. The authz-denial branch releases the
// AGENT-SESSION claim but deliberately does NOT release the webhook-
// delivery claim (still ok=true) -- these are genuinely different
// identities with different "would a retry help" answers: a redelivery of
// this SAME delivery id would hit the identical denial again (no help),
// but a LATER, distinct event for this SAME agent_session_id (e.g. a
// subsequent `prompted` event, once the actor is actually granted access)
// must not find the agent-session claim already permanently poisoned.
//
// A SetSessionID failure is deliberately NOT a third release branch (HIGH
// audit fix, "releasing the linear_agent_sessions claim after a
// SetSessionID failure can spawn a duplicate, independently-dispatched
// agent" -- correcting this function's own PREVIOUS behavior, which
// treated it exactly like the two branches above): by the time
// SetSessionID could ever fail, CreateSessionCore has ALREADY committed a
// real session with a Pending turn AND fired TriggerDispatch below -- the
// session is genuinely alive and already being worked on, NOT an inert,
// never-dispatched row the way Slack's own lost-the-race orphan is (that
// prior comparison was wrong: Slack's bare-session orphan never sets
// Prompt, so it is genuinely never dispatched; Linear's is). Releasing
// either claim here and answering non-2xx would let Linear redeliver this
// SAME `created` event, running this ENTIRE function again and spawning a
// SECOND, independently-dispatched session/turn for the identical
// agent_session_id, while the FIRST, real, already-running session becomes
// permanently unreachable by any future Linear event for it -- strictly
// worse than the gap it would claim to close. setSessionIDWithRetry
// (retry.go) instead retries the safe, idempotent UPDATE itself a bounded
// number of times; if every attempt still fails, this logs at Error (this
// specific agent_session_id now needs manual reconciliation -- its
// linear_agent_sessions row has no session_id even though a real,
// dispatched session exists) and continues on to the SAME success path
// (acknowledgment, intent classification, ok=true) as if SetSessionID had
// succeeded -- the task IS genuinely progressing from Linear's own
// perspective regardless.
func (deps Deps) handleCreated(ctx context.Context, payload agentSessionEventWebhookPayload) bool {
	logger := platform.Logger(ctx)

	claim, err := deps.AgentSessions.Claim(ctx, payload.AgentSession.ID, payload.OrganizationID)
	if err != nil {
		logger.Error("linear: claim agent session failed", "error", err, "agent_session_id", payload.AgentSession.ID)
		return false
	}
	if !claim.Inserted {
		logger.Info("linear: duplicate created event for agent session, skipping", "agent_session_id", payload.AgentSession.ID)
		return true
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

	// Step 39 ("identities + full RBAC", §13.2) update: creator is no
	// longer unconditionally invalid -- resolveActor auto-links (or
	// creates a magic-link prompt for) payload.AgentSession.CreatorID the
	// first time this package sees it, and reports back whichever
	// notification text (if any) the caller should surface. Nil CreatorID
	// (an automation-initiated session, "unset if automation-initiated"
	// per Linear's own schema) resolves to bot attribution unconditionally
	// -- there is no external id to look up at all in that case.
	creatorID := ""
	if payload.AgentSession.CreatorID != nil {
		creatorID = *payload.AgentSession.CreatorID
	}
	creator, notice := deps.resolveActor(ctx, logger, payload.OrganizationID, creatorID)

	// Step 39 ("identities + full RBAC", §13.2/§13.3) update: a creator
	// that resolved to a REAL, linked user_id must still pass domain/authz.
	// Authorize(ActionCreateSession) -- exactly what the REST /api/sessions
	// handler already requires (create.go's own authorize call). Resource{}
	// is always correct here (no ownership carve-out on create). A still-
	// unlinked (bot-attributed, including every automation-initiated,
	// nil-CreatorID session) creator is untouched --
	// actorauthz.AuthorizeResolvedActor returns true immediately for that
	// case, preserving §13.2's own existing precedent.
	if !actorauthz.AuthorizeResolvedActor(ctx, logger, authzSurface, deps.IdentityLink.Users, creator, authz.ActionCreateSession, authz.Resource{}) {
		logger.Warn("linear: create session denied by authz", "agent_session_id", payload.AgentSession.ID, "user_id", creator.String())
		// H3 audit fix: release the SEPARATE linear_agent_sessions claim
		// this call just won -- see this function's own top doc comment for
		// why this is safe/needed even though the webhook-delivery claim
		// below is deliberately left alone.
		if releaseErr := deps.AgentSessions.Release(ctx, payload.AgentSession.ID); releaseErr != nil {
			logger.Error("linear: release agent session claim failed", "error", releaseErr, "agent_session_id", payload.AgentSession.ID)
		}
		deps.postAcknowledgment(ctx, payload.OrganizationID, payload.AgentSession.ID, appendNotice("Your linked Narvi account isn't authorized to start new sessions from Linear.", notice))
		return true
	}

	created, cerr := httpapi.CreateSessionCore(ctx, deps.Pool, deps.Sessions, deps.Turns, deps.Environments, deps.AuditLog, deps.Registry, req, creator)
	if cerr != nil {
		logger.Error("linear: create session failed", "status", cerr.Status, "message", cerr.Message, "agent_session_id", payload.AgentSession.ID)
		// H2/H3 audit fix: release BOTH claims this delivery is holding --
		// the linear_agent_sessions claim (guarded, see this function's own
		// top doc comment) and, via this func's own false return, the
		// webhook-delivery claim (NewWebhookHandler) -- a transient DB
		// failure here must not permanently strand either one.
		if releaseErr := deps.AgentSessions.Release(ctx, payload.AgentSession.ID); releaseErr != nil {
			logger.Error("linear: release agent session claim failed", "error", releaseErr, "agent_session_id", payload.AgentSession.ID)
		}
		return false
	}

	if err := deps.setSessionIDWithRetry(ctx, payload.AgentSession.ID, created.ID); err != nil {
		// HIGH audit fix ("releasing the linear_agent_sessions claim after
		// a SetSessionID failure can spawn a duplicate, independently-
		// dispatched agent"): created.ID is a REAL, ALREADY-DISPATCHED
		// session (CreateSessionCore committed it and fired TriggerDispatch
		// before this point) -- setSessionIDWithRetry (retry.go) already
		// retried this safe, idempotent UPDATE a bounded number of times.
		// Every attempt failing means this specific agent_session_id's own
		// linear_agent_sessions row now needs MANUAL reconciliation (it has
		// no session_id even though a real, dispatched session exists) --
		// logged at Error for exactly that. The claim is deliberately left
		// exactly as it is: NEVER released here (unlike the two branches
		// above), since releasing it would let a redelivery of this
		// identical `created` event run this whole function again,
		// spawning a SECOND, independently-dispatched session for the same
		// agent_session_id while this first, real one becomes permanently
		// unreachable -- see this function's own top doc comment for the
		// full failure mode this replaces. Falls through to the SAME
		// success path (acknowledgment, intent classification, ok=true)
		// below as if SetSessionID had succeeded: the task IS genuinely
		// progressing from Linear's own perspective regardless, and
		// returning a failure code here would only trigger the exact
		// duplicate-dispatch redelivery this fix exists to prevent.
		logger.Error("linear: attach session id to agent session claim failed after exhausting retries, manual reconciliation needed",
			"error", err, "agent_session_id", payload.AgentSession.ID, "session_id", created.ID.String())
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

	deps.postAcknowledgment(ctx, payload.OrganizationID, payload.AgentSession.ID, appendNotice(acknowledgmentBody, notice))
	return true
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
//
// Returns ok=false ONLY for a genuine post-(webhook-delivery-)claim
// backend failure -- H2 audit fix ("webhook claim/release parity"):
// GetByAgentSessionID erroring for any reason OTHER than pgx.ErrNoRows,
// ListForSession erroring, Turns.Create erroring, or (MEDIUM audit fix,
// "authorizeSessionAction conflates a genuine backend error with a real
// authorization denial") authorizeSessionAction returning a genuine
// backend error (any error other than ErrActorNotAuthorized) while
// checking whether this reply's own actor may prompt sessionID. The caller
// (NewWebhookHandler) releases the webhook-delivery claim and answers
// non-2xx on ok=false, so a redelivery of this same Linear-Delivery id
// can retry. Every other return (missing agentActivity, a stop signal,
// an unknown/still-claiming agent session, a genuine ErrActorNotAuthorized
// denial, an already-open turn) is ok=true: a deliberate business decision
// or an accepted, already-documented scope limitation (see each branch's
// own comment), never a failure a retry could plausibly change.
func (deps Deps) handlePrompted(ctx context.Context, payload agentSessionEventWebhookPayload) bool {
	logger := platform.Logger(ctx)

	if payload.AgentActivity == nil {
		logger.Warn("linear: prompted event missing agentActivity, ignoring", "agent_session_id", payload.AgentSession.ID)
		return true
	}

	if payload.AgentActivity.Signal != nil && *payload.AgentActivity.Signal == stopSignal {
		// Scope decision (Step 34): no session/turn STOP mechanism exists
		// in internal/app/sessionactor yet (confirmed during this Step's
		// investigation -- no Stop command type). Wiring a real stop is
		// out of this Step's own scope; this is logged clearly rather than
		// silently swallowed or forced through a mechanism that doesn't
		// exist.
		logger.Warn("linear: received stop signal, no stop mechanism wired yet (out of scope for this Step)", "agent_session_id", payload.AgentSession.ID)
		return true
	}

	row, err := deps.AgentSessions.GetByAgentSessionID(ctx, payload.AgentSession.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("linear: prompted event for unknown agent session, ignoring", "agent_session_id", payload.AgentSession.ID)
			return true
		}
		logger.Error("linear: look up agent session failed", "error", err, "agent_session_id", payload.AgentSession.ID)
		return false
	}
	if !row.SessionID.Valid {
		logger.Warn("linear: prompted event for agent session still being claimed, ignoring", "agent_session_id", payload.AgentSession.ID)
		return true
	}
	sessionID := row.SessionID

	// Step 39 ("identities + full RBAC", §13.2) update: resolve the REAL
	// actor behind this activity ONCE, regardless of which branch below
	// ends up handling it -- the auto-link algorithm runs "on first event
	// from an unknown provider identity" (§13.2), not only on plan-verdict
	// replies, so an ordinary reply must trigger it exactly the same way.
	// UserID is Linear's own REQUIRED "who authored this activity" field
	// (payload.go's own doc comment) -- never nil/empty the way
	// AgentSession.CreatorID can be, so resolveActor is always given a
	// real external id to look up here.
	actorUserID, notice := deps.resolveActor(ctx, logger, payload.OrganizationID, payload.AgentActivity.UserID)

	if deps.Plans != nil {
		if planID, hasAwaiting := deps.findAwaitingApprovalPlanID(ctx, logger, sessionID); hasAwaiting {
			if verdict, ok := plandomain.MatchVerdict(payload.AgentActivity.Content.Body); ok {
				// handlePlanVerdict's own internal failures (DecidePlan
				// erroring, the outcome-activity post failing) are already
				// logged and swallowed there, exactly like before this
				// batch's own H2/H3 changes -- the session this plan belongs
				// to already exists and is otherwise healthy, so a failed
				// plan-verdict decision is deliberately left out of THIS
				// function's own ok signal (never something this batch's
				// audit findings called out).
				deps.handlePlanVerdict(ctx, logger, sessionID, planID, verdict, actorUserID, notice, payload.OrganizationID, payload.AgentSession.ID)
				return true
			}
		}
	}

	// Step 39 ("identities + full RBAC", §13.2/§13.3) update: this
	// fallthrough IS "request changes" for Linear (this function's own top
	// doc comment) -- the same state-changing command POST .../turns
	// itself gates behind ActionPromptSession (turn.go's own authorize
	// call). An actorUserID that resolved to a REAL, linked user_id must
	// pass that same check before this reply is allowed to create a turn.
	// A still-unlinked (bot-attributed) actorUserID is untouched --
	// authorizeSessionAction returns nil immediately for that case,
	// preserving §13.2's own "the action proceeds" precedent for the
	// not-yet-linked case.
	if err := deps.authorizeSessionAction(ctx, logger, sessionID, actorUserID, authz.ActionPromptSession); err != nil {
		if errors.Is(err, ErrActorNotAuthorized) {
			logger.Warn("linear: prompted reply denied by authz", "session_id", sessionID.String(), "user_id", actorUserID.String())
			deps.postIdentityNotice(ctx, payload.OrganizationID, payload.AgentSession.ID, appendNotice("Your linked Narvi account isn't authorized to prompt this session.", notice))
			return true
		}
		// MEDIUM audit fix ("authorizeSessionAction conflates a genuine
		// backend error with a real authorization denial"): a transient
		// backend failure WHILE checking authorization (already logged
		// inside authorizeSessionAction) is NOT a denial -- flows into the
		// SAME release-the-claim-and-retry path H2 already wired up for
		// every other post-claim failure in this function, instead of
		// being silently treated as "skip, no release" the way a one-off
		// DB blip previously was.
		return false
	}

	existingTurns, err := deps.Turns.ListForSession(ctx, sessionID)
	if err != nil {
		logger.Error("linear: list turns failed", "error", err, "session_id", sessionID.String())
		return false
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
		return true
	}

	prompt := payload.AgentActivity.Content.Body
	if _, err := deps.Turns.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusPending,
		Prompt:    &prompt,
	}); err != nil {
		logger.Error("linear: create turn failed", "error", err, "session_id", sessionID.String())
		return false
	}
	// turns carries no per-row actor column at all (migrations/
	// 000005_turns.up.sql) -- unlike sessions.created_by/plans.decided_by,
	// there is nothing further to attribute actorUserID onto for this
	// ordinary-reply path (mirrors internal/adapters/inbound/slack's own
	// addTurn, which has never taken an actor parameter either). notice
	// (if any) still needs to reach the user, though -- posted as its own
	// best-effort activity, since (unlike the plan-verdict branch above)
	// there is no other outbound activity this path already sends to
	// append it to.
	deps.postIdentityNotice(ctx, payload.OrganizationID, payload.AgentSession.ID, notice)

	// GetOrSpawn/Send below are dispatch-trigger side effects on a turn
	// that has ALREADY been durably created and persisted above -- a
	// failure here (like the in-thread ack/notice posts elsewhere in this
	// package) is logged only, never treated as a release-worthy failure:
	// the turn itself is safely committed regardless, and hasOpenTurn's
	// own guard above means a retry would find this exact turn already
	// open and simply drop again, never double-create.
	actor, err := deps.Registry.GetOrSpawn(ctx, sessionID)
	if err != nil {
		logger.Warn("linear: GetOrSpawn after turn create failed", "error", err)
		return true
	}
	if err := actor.Send(ctx, sessionactor.EnsureDispatched{}); err != nil {
		logger.Warn("linear: send EnsureDispatched after turn create failed", "error", err)
	}
	return true
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

// handlePlanVerdict calls the shared httpapi.DecidePlan with decidedBy --
// Step 39's ("identities + full RBAC", §13.2) own resolveActor result
// (Valid iff the replying Linear user is already linked, or was just
// auto-linked this call; invalid/bot-attribution otherwise, matching this
// package's own PREVIOUS unconditional-bot-attribution precedent for the
// still-unresolved case) -- then posts a follow-up `response` AgentActivity
// describing the REAL final outcome, whether this call itself won or a
// different channel already decided first, with identityNotice (§13.2's
// own "notify in-channel", empty when there's nothing to say) appended.
func (deps Deps) handlePlanVerdict(ctx context.Context, logger *slog.Logger, sessionID, planID pgtype.UUID, verdict string, decidedBy pgtype.UUID, identityNotice, organizationID, agentSessionID string) {
	// Step 39 ("identities + full RBAC", §13.2/§13.3) update: a decidedBy
	// that resolved to a REAL, linked user_id must still pass domain/authz.
	// Authorize(ActionApprovePlan) -- exactly what the REST approve/reject
	// endpoints already require via canActOnPlan (planauthz.go). A still-
	// unlinked (bot-attributed) decidedBy is untouched -- authorizeSessionAction
	// returns nil immediately for that case, preserving this package's own
	// existing "Linear verdicts stay unauthenticated-per-user until linked"
	// precedent (decideplan.go's own top doc comment).
	//
	// Unlike handlePrompted's own ordinary-reply gate, this does NOT
	// distinguish ErrActorNotAuthorized from a genuine backend error --
	// handlePlanVerdict's own internal failures are already logged and
	// swallowed here, exactly like before this batch's own H2/H3/MEDIUM
	// changes (this function's own caller, handlePrompted, deliberately
	// leaves ITS failures out of its own ok signal too -- see that
	// function's own call site comment); out of this batch's own explicit
	// MEDIUM-finding scope (identity.go's own ErrActorNotAuthorized doc
	// comment), which only rewires handlePrompted's ordinary-reply call
	// site into the release-the-claim path.
	if err := deps.authorizeSessionAction(ctx, logger, sessionID, decidedBy, authz.ActionApprovePlan); err != nil {
		logger.Warn("linear: plan decision denied by authz", "plan_id", planID.String(), "session_id", sessionID.String(), "user_id", decidedBy.String())
		deps.postPlanOutcomeActivity(ctx, logger, organizationID, agentSessionID, appendNotice("You don't have permission to approve or reject this plan.", identityNotice))
		return
	}

	outcome, err := httpapi.DecidePlan(ctx, deps.Pool, deps.Sessions, deps.Turns, deps.Plans, deps.Outbox, deps.AgentSessions, deps.AuditLog, deps.Registry, sessionID, planID, httpapi.PlanVerdict(verdict), decidedBy)
	if err != nil {
		if errors.Is(err, httpapi.ErrPlanOpenTurnInFlight) {
			deps.postPlanOutcomeActivity(ctx, logger, organizationID, agentSessionID, appendNotice("A revision is already in progress for this plan -- try again once it completes.", identityNotice))
			return
		}
		logger.Error("linear: decide plan failed", "error", err, "plan_id", planID.String(), "session_id", sessionID.String())
		return
	}

	deps.postPlanOutcomeActivity(ctx, logger, organizationID, agentSessionID, appendNotice(renderLinearPlanOutcomeText(outcome), identityNotice))
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
//
// body is now a parameter (Step 39, "identities + full RBAC", §13.2
// update) rather than always the fixed acknowledgmentBody constant --
// handleCreated's own caller passes acknowledgmentBody with an identity-
// link notice appended (appendNotice), when there is one; every other
// property of this function is unchanged.
func (deps Deps) postAcknowledgment(ctx context.Context, organizationID, agentSessionID, body string) {
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

	if err := deps.LinearClient.CreateThoughtActivity(activityCtx, string(accessToken), agentSessionID, body); err != nil {
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
