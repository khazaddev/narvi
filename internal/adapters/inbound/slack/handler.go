package slack

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/actorauthz"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/app/intentclassifier"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/authz"
	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
	"github.com/khazaddev/narvi/internal/platform"
)

// intentClassifierSurface is the sessions.spawn_source value (§18.1's
// IntentClassifierInput.Surface / §18.4's IntentDecisionRecord.Surface)
// this package's own messages are classified/recorded under.
const intentClassifierSurface = "slack"

// maxRequestBodyBytes bounds every Slack request body this package reads
// -- mirrors httpapi's own identical constant/reasoning; a real Slack
// event payload is always far smaller than this.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

// slackWebhookSignatureVersion is the fixed "v0" prefix Slack's own
// signing scheme uses on both the signed-string prefix and the
// X-Slack-Signature header value (confirmed against Slack's own current
// documentation -- see doc.go's own step 2).
const slackWebhookSignatureVersion = "v0"

// slackMessageClaimProvider is a SECOND, distinct WebhookDeliveryStore
// "provider" value (deliberately never "slack") used ONLY for the
// message-level coalescing claim below -- L3 audit fix ("Slack's own
// dual-delivery for one logical mention isn't coalesced"): Slack sends
// BOTH an app_mention event AND a message event (two DISTINCT event_id
// values) for a single @mention posted inside a thread this adapter
// already has mapped to a session. The outer (provider="slack",
// event_id) claim already made below can't coalesce them -- the two
// deliveries carry different event_ids, so both independently win that
// claim, both pass isIgnorable, and both would otherwise flow all the way
// through resolveOrClaimSession's existing-mapping branch into
// handleEvent's own addTurn call for the exact same underlying human
// action (a redundant resolveSlackActor call each, and -- absent this fix
// -- a confusing double ack: one "on it" for whichever twin wins the
// addTurn race, immediately followed by a second, separate "still
// working on the previous message" for its sibling).
//
// This claim is reused against the SAME (provider, delivery_id)-keyed
// webhook_deliveries table/primitive the outer claim already uses (§5.1's
// own "INSERT ... ON CONFLICT atomic claims" house style), keyed by
// messageClaimKey (event.go) -- the identity of the underlying MESSAGE
// OBJECT itself (channel, ts), NOT threadKey()/ThreadTS, which identifies
// the THREAD: both twin events carry the IDENTICAL ts value, since they
// describe the same Slack message object twice, while a genuinely
// DIFFERENT message posted later in the same thread carries a different
// ts and must NOT be coalesced with this one. Using a distinct provider
// value (rather than reusing "slack") keeps this claim space from ever
// colliding with a real event_id claimed above, even in the
// vanishingly-unlikely case a real event_id happened to look exactly like
// a "channel:ts" pair.
const slackMessageClaimProvider = "slack-message"

// ackNewSessionText/ackReplyText/ackBusyText/ackNotConfiguredText are the
// fixed in-thread ack messages this Step posts -- deliberately plain,
// static strings (no templating beyond what's inlined here), matching
// this Step's own "smallest possible direct call" scoping decision
// (doc.go).
const (
	ackNewSessionText = "On it — starting work on this now."
	ackReplyText      = "Got it — continuing work on this thread."
	// ackBusyText's wording is the M6 audit fix: the PREVIOUS text ("...
	// I'll pick this up next.") promised a retry/queue that never
	// actually happened -- a turn dropped for an already-busy session was
	// simply discarded, never picked up later. This wording is honest
	// about that instead.
	ackBusyText          = "Still working on the previous message in this thread — this one wasn't queued, please try again once it's done."
	ackNotConfiguredText = "Slack ingress isn't configured with a default repo yet, so I can't start new work from a mention. A reply on an existing thread still works."

	// ackNotAuthorizedText is Step 39's own addition ("identities + full
	// RBAC", §13.2/§13.3): posted instead of ackNewSessionText when the
	// acting user isn't authorized to create a session -- mirrors the REST
	// API's own 403 semantics ("not authorized to perform this action",
	// helpers.go) rather than silently creating the session anyway.
	//
	// Audit-fix batch update ("block unlinked actor state changes"): the
	// wording no longer says "Your linked Narvi account" -- that phrasing
	// assumed a link already existed, which is now wrong for the NEW
	// denial case this same text also covers: an actor whose auto-link
	// attempt hasn't resolved at all (AuthorizeLinkedActor denies before
	// ever consulting a role). The wording below reads correctly for
	// EITHER case (not linked yet, or linked but insufficient role) --
	// the separately-posted magic-link notice (resolveSlackActor's own
	// notice, delivered regardless of this denial) is what tells an
	// unlinked actor specifically how to fix it.
	ackNotAuthorizedText = "You're not authorized to start new sessions from Slack."

	// ackNotAuthorizedReplyText is this Step's own SECOND fix-pass
	// addition ("identities + full RBAC", §13.2/§13.3 -- a confirmed
	// re-review finding): posted instead of enqueuing a turn when the
	// acting user isn't authorized to prompt this session (a reply on an
	// ALREADY-MAPPED thread, or a brand-new mention that lost the
	// first-writer-wins race and falls back onto a DIFFERENT,
	// already-existing session) -- mirrors ackNotAuthorizedText's own
	// wording above (same audit-fix-batch update: no longer assumes a
	// link already exists), and Linear's identical denial text for the
	// equivalent ordinary-reply fallthrough (webhook.go's own
	// handlePrompted).
	ackNotAuthorizedReplyText = "You're not authorized to prompt this session."
)

// Deps bundles every dependency NewHandler needs -- a small config
// struct (rather than a long positional parameter list, given the
// number of stores this adapter touches) mirroring this codebase's own
// existing precedent of grouping related construction parameters (e.g.
// modal.Config).
type Deps struct {
	Pool         *pgxpool.Pool
	Sessions     *postgres.SessionStore
	Turns        *postgres.TurnStore
	Environments *postgres.EnvironmentStore
	Registry     *sessionactor.Registry
	Deliveries   *postgres.WebhookDeliveryStore
	Threads      *postgres.SlackThreadSessionStore

	// AuditLog is Step 39's own addition (§13.3) -- threaded through to
	// httpapi.CreateSessionCore below exactly like Environments already
	// is, so a Slack-originated session creation gets the SAME audit_log
	// row every other CreateSessionCore caller now gets. actor_user_id is
	// NULL only until identity auto-linking (IdentityLink below) resolves
	// a real user -- see identity.go's own resolveSlackActor for the
	// replacement of the old unconditional bot-attribution precedent.
	AuditLog *postgres.AuditLogStore

	// Participants is this Step's own SECOND fix-pass addition
	// ("identities + full RBAC", §13.2/§13.3): authorizeSessionAction
	// (identity.go's own ownedOrJoined) needs this to resolve a `member`
	// actor's own "own/joined" carve-out exactly like InteractiveDeps.
	// Participants already does for the interactivity route -- so an
	// ordinary Events-API reply on an already-mapped thread renders the
	// IDENTICAL §13.3 verdict a REST/interactivity caller would for the
	// same (actor, session). Production wiring passes the SAME
	// participantStore instance every other caller already uses, never a
	// second, independently-constructed copy.
	Participants *postgres.ParticipantStore

	// IdentityLink/SlackClient are Step 39's own auto-linking wiring
	// (§13.2): resolveSlackActor (identity.go) uses SlackClient.
	// GetUserEmail to fetch ev.User's own profile email (with retry, via
	// Timeouts.IdentityEmailFetch*), then IdentityLink.Resolve to
	// auto-link or create a magic-link prompt the first time this package
	// sees a given Slack user id it doesn't already know about.
	// SlackClient is a SEPARATE client instance from this file's own
	// internal ackClient (newAckClient below) -- mirrors interactive.go's
	// own identical InteractiveDeps.SlackClient precedent; production
	// wiring (cmd/control-plane/main.go) passes the SAME *slackapi.Client
	// instance already constructed for the outbox delivery worker, rather
	// than building a third one.
	IdentityLink identitylink.Deps
	SlackClient  *slackapi.Client
	// Timeouts is Step 39's own addition, read for its
	// IdentityEmailFetch* fields only (identity.go) -- every OTHER
	// timeout this package needs is still an existing discrete field
	// below (TimestampWindow, AckTimeout), left untouched.
	Timeouts platform.Timeouts

	// IntentClassifier is Step 36's own wiring point (§8.3/§18): classify
	// + record runs ONCE, on the brand-new-thread's own first real turn
	// (decided_at_stage="first_prompt" -- a bare session is created with
	// no prompt at all, see resolveOrClaimSession; the real text only
	// arrives here, at handleEvent's own addTurn call). Optional (nil-
	// safe): a nil IntentClassifier simply skips classification entirely.
	IntentClassifier *intentclassifier.Service

	SigningSecret   string
	BotToken        string
	DefaultRepoName string
	DefaultRepoURL  string
	TimestampWindow time.Duration
	SlackAPIBaseURL string       // optional; defaults to defaultSlackAPIBaseURL (production wiring should still pass it explicitly)
	SlackHTTPClient *http.Client // optional; defaults to http.DefaultClient
	// AckTimeout bounds each ackClient.postAck call (platform.Timeouts.
	// SlackAckTimeout in production wiring) -- postAck is a genuine
	// outbound network call made synchronously in this handler's own
	// request path, so it must never run against the bare, deadline-free
	// r.Context() unbounded (mirrors sessionactor's own PRCreateTimeout
	// precedent). A zero value here means no deadline is applied at all,
	// so production wiring should always pass it explicitly.
	AckTimeout time.Duration
}

// NewHandler builds the POST /webhooks/slack handler (§8.10, Step 33 --
// see doc.go's own full request-handling writeup).
func NewHandler(deps Deps) http.HandlerFunc {
	ack := newAckClient(deps.SlackHTTPClient, deps.SlackAPIBaseURL, deps.BotToken)

	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		body, ok := readBoundedBody(w, r)
		if !ok {
			return
		}

		if !verifySlackRequest(w, r, body, deps.SigningSecret, deps.TimestampWindow, logger) {
			return
		}

		// Cheapest possible check first: a url_verification handshake
		// carries no "event"/"event_id" at all, so it is recognized (and
		// fully handled) before ever attempting the fuller eventEnvelope
		// decode below.
		var challenge challengeEnvelope
		if err := json.Unmarshal(body, &challenge); err == nil && challenge.Type == "url_verification" {
			writeJSON(w, http.StatusOK, map[string]string{"challenge": challenge.Challenge})
			return
		}

		var envelope eventEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		if envelope.Type != "event_callback" {
			logger.Warn("slack: ignoring unrecognized outer envelope type", "type", envelope.Type)
			w.WriteHeader(http.StatusOK)
			return
		}

		if envelope.EventID == "" {
			writeError(w, http.StatusBadRequest, "missing event_id")
			return
		}

		claim, err := deps.Deliveries.Claim(ctx, "slack", envelope.EventID)
		if err != nil {
			logger.Error("slack: claim webhook delivery failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !claim.Inserted {
			// Already processed -- a genuine Slack redelivery of the
			// SAME event_id (§5.1's dedupe claim). Never reprocessed.
			w.WriteHeader(http.StatusOK)
			return
		}

		var ev slackEvent
		if err := json.Unmarshal(envelope.Event, &ev); err != nil {
			logger.Error("slack: decode inner event failed", "error", err)
			// H2 audit fix ("webhook claim/release parity"): this delivery
			// was claimed above but never actually processed -- release the
			// claim so that a genuine redelivery of this same event_id (a
			// human manually resending it, or Slack's own real retry
			// behavior on a slow/failed response) can actually reprocess it,
			// rather than being silently skipped forever as an
			// already-claimed duplicate. Mirrors github's own identical
			// parse-failure release (handler.go).
			if releaseErr := deps.Deliveries.Release(ctx, "slack", envelope.EventID); releaseErr != nil {
				logger.Error("slack: release webhook delivery claim failed", "error", releaseErr, "event_id", envelope.EventID)
			}
			writeError(w, http.StatusBadRequest, "malformed event")
			return
		}

		if ev.isIgnorable() {
			w.WriteHeader(http.StatusOK)
			return
		}

		// L3 audit fix ("Slack's own dual-delivery for one logical mention
		// isn't coalesced") -- see slackMessageClaimProvider's own doc
		// comment above for the full "why". Claimed right here: after
		// isIgnorable (no need to coalesce an event that would never reach
		// handleEvent anyway) and before handleEvent ever runs (so a
		// coalesced twin never redundantly calls resolveSlackActor or
		// posts a second ack).
		msgClaim, err := deps.Deliveries.Claim(ctx, slackMessageClaimProvider, ev.messageClaimKey())
		if err != nil {
			logger.Error("slack: claim message-level webhook delivery failed", "error", err)
			// Mirrors the "decode inner event failed" release just above:
			// the outer event_id claim already succeeded, but this event
			// was never actually processed, so release it too and let a
			// redelivery retry from scratch.
			if releaseErr := deps.Deliveries.Release(ctx, "slack", envelope.EventID); releaseErr != nil {
				logger.Error("slack: release webhook delivery claim failed", "error", releaseErr, "event_id", envelope.EventID)
			}
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !msgClaim.Inserted {
			// This exact underlying Slack message was already handled via
			// its twin event type (app_mention <-> message, or a genuine
			// redelivery of this same event type) -- skip entirely, never
			// posting a second, confusing ack.
			//
			// Residual, accepted tradeoff (re-review, mirrors this
			// codebase's own established "narrow the race, don't
			// eliminate every last window" precedent used throughout this
			// audit series): in the ONE case handleEvent's own
			// ReleaseMessageClaim signal below deliberately releases this
			// claim (a plain "message" event skipping as "not ours" on a
			// brand-new, not-yet-mapped thread), the differently-typed
			// twin that later reclaims it (the app_mention that actually
			// creates the session) DOES call resolveSlackActor a second
			// time -- the "never a redundant second one" guarantee below
			// holds for every OTHER outcome (the common already-mapped-
			// thread coalescing case, and every Skip that keeps this claim
			// held), just not this specific reclaim path, where a second
			// call is the deliberate cost of not losing the mention
			// entirely.
			w.WriteHeader(http.StatusOK)
			return
		}

		result := handleEvent(ctx, deps, ack, logger, ev)
		if !result.OK {
			// H2 audit fix: handleEvent hit a genuine post-claim processing
			// failure (a DB error resolving/creating the session or adding
			// the turn) -- release the claim and answer non-2xx so a
			// redelivery of this same event_id can retry, instead of
			// silently and permanently dropping the event now that it's
			// (correctly) claimed. Never released for a best-effort
			// notification failure (the in-thread ack, the identity-link
			// ephemeral notice) or a deliberate business skip (no default
			// repo configured, an authz denial) -- see handleEvent's own doc
			// comment for the exact boundary, mirroring github's own
			// ErrActorNotAuthorized-vs-genuine-error distinction
			// (coalesce.go).
			if releaseErr := deps.Deliveries.Release(ctx, "slack", envelope.EventID); releaseErr != nil {
				logger.Error("slack: release webhook delivery claim failed", "error", releaseErr, "event_id", envelope.EventID)
			}
			// L3 audit fix: the message-level claim above must ALSO be
			// released on this exact same failure path -- otherwise a
			// later genuine retry (of either twin event_id) would find this
			// claim already taken and be silently, incorrectly dropped
			// forever.
			if releaseErr := deps.Deliveries.Release(ctx, slackMessageClaimProvider, ev.messageClaimKey()); releaseErr != nil {
				logger.Error("slack: release message-level webhook delivery claim failed", "error", releaseErr, "claim_key", ev.messageClaimKey())
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		// HIGH audit fix ("message-level claim can permanently orphan its
		// own app_mention twin on a brand-new thread"): result.OK is true
		// here, so this is NOT the failure path just above, yet the
		// message-level claim can still need releasing -- see
		// sessionResolution's own ReleaseMessageClaim field and
		// handleEvent's own doc comment for the full "why" (the ONE
		// asymmetric Skip outcome between the two twin event types: a plain
		// "message" event landing first on a brand-new, not-yet-mapped
		// thread). Every OTHER ok=true outcome (a deliberate business skip
		// reached identically by either twin -- no default repo configured,
		// an authz denial -- or a fully successful addTurn) leaves
		// ReleaseMessageClaim false, so the claim stays held exactly as
		// before this fix.
		if result.ReleaseMessageClaim {
			if releaseErr := deps.Deliveries.Release(ctx, slackMessageClaimProvider, ev.messageClaimKey()); releaseErr != nil {
				logger.Error("slack: release message-level webhook delivery claim failed (asymmetric message-before-app_mention skip)", "error", releaseErr, "claim_key", ev.messageClaimKey())
			}
		}
		w.WriteHeader(http.StatusOK)
	}
}

// sessionResolution is resolveOrClaimSession's own result: either a real
// session to add a turn to (Skip == false), or nothing further to do at
// all (Skip == true -- either a deliberate no-op, e.g. a plain message
// with no mapping, or a gap already acked inline, e.g. no default repo
// configured). A genuine error is reported separately (see
// resolveOrClaimSession's own ok return value) and is always already
// logged by the time it's returned.
type sessionResolution struct {
	SessionID   pgtype.UUID
	IsNewThread bool
	Skip        bool

	// ReleaseMessageClaim is HIGH audit fix ("message-level claim can
	// permanently orphan its own app_mention twin on a brand-new thread"):
	// set ONLY by the "no mapping yet AND this event is not an app_mention"
	// Skip branch immediately below in resolveOrClaimSession -- the ONE
	// outcome in this function that is genuinely ASYMMETRIC between the two
	// twin event types Slack sends for a single logical mention
	// (app_mention and message, see slackMessageClaimProvider's own doc
	// comment above). A plain "message" event can NEVER create a new
	// session/thread mapping (only an app_mention can, see the check right
	// below), so if that message twin happens to win NewHandler's own
	// (channel, ts)-keyed message-level claim race on a brand-new thread, it
	// takes THIS branch, and unless the claim is released here, it stays
	// held forever -- silently discarding the app_mention twin (the ONLY
	// one that could ever have actually created the session) the moment it
	// arrives afterward, with both Slack deliveries still answering 200 OK
	// (so Slack never retries) and zero operator-visible signal.
	//
	// Every OTHER Skip outcome in this function (no default repo
	// configured, an authz denial on create, an authz denial on an
	// existing-thread reply via authorizeExistingSessionReply) is reached
	// IDENTICALLY regardless of which twin got there first -- both twins
	// would render the exact same verdict, so releasing the message-level
	// claim there would only let a genuinely-denied/misconfigured twin
	// retry pointlessly. Those leave this field false (the default), and
	// NewHandler's own release-path comment (above) still applies to them
	// unchanged.
	//
	// Residual, ACCEPTED race (re-review; mirrors this codebase's own
	// established "narrow the window, don't eliminate every last one"
	// tradeoff already used throughout this audit series, e.g. the SCM-
	// credentials disabled/role recheck, the outbox claim-lease CAS): this
	// release still depends on ORDERING relative to the twin's own Claim
	// attempt. If the two twin deliveries are handled by truly concurrent
	// requests (not just arbitrarily ORDERED ones, which this fix fully
	// closes) and the app_mention twin's own Claim call lands inside the
	// narrow window between the message twin's INSERT and its later
	// Release, the app_mention twin can still lose the race and be
	// skipped-with-200, same failure mode as the bug this field exists to
	// close, just requiring genuine concurrency rather than firing on
	// every ordering. Closing this fully would need a held, cross-request
	// lock spanning the whole message-twin attempt rather than a point-in-
	// time claim -- a materially bigger, more invasive mechanism than this
	// narrow finding warrants; the window this fix leaves is small (a
	// handful of DB round trips) and, given Slack's own real-world twin-
	// delivery timing, expected to be rare in practice.
	ReleaseMessageClaim bool
}

// handleEventResult is handleEvent's own result -- mirrors
// sessionResolution's own small, explicit struct shape rather than a bare
// bool (this codebase's own established preference, see that type's doc
// comment) now that handleEvent has two independent things to report: OK
// (see handleEvent's own doc comment below) and, orthogonally,
// ReleaseMessageClaim (HIGH audit fix, "message-level claim can
// permanently orphan its own app_mention twin on a brand-new thread") --
// whether NewHandler's own message-level claim (slackMessageClaimProvider)
// must ALSO be released even though OK is true, because
// resolveOrClaimSession's own res.ReleaseMessageClaim fired (see that
// field's own doc comment for the full "why"). ReleaseMessageClaim is
// meaningless when OK is false -- NewHandler's own failure path already
// unconditionally releases both claims regardless of this field.
type handleEventResult struct {
	OK                  bool
	ReleaseMessageClaim bool
}

// handleEvent implements doc.go's own thread<->session mapping design
// (steps 7-8): resolve or create the mapped session, add a turn, then
// best-effort ack. Returns OK=false ONLY for a genuine post-claim
// persistence failure (resolveOrClaimSession's own DB errors, addTurn
// failing, or -- MEDIUM audit fix, "authorizeSessionAction conflates a
// genuine backend error with a real authorization denial" --
// authorizeExistingSessionReply's own authorizeSessionAction call
// returning a genuine backend error, distinct from ErrActorNotAuthorized)
// -- H2 audit fix ("webhook claim/release parity"): the caller (NewHandler)
// releases the webhook-delivery claim and answers non-2xx when OK is
// false, so a redelivery of this same event_id can actually retry, instead
// of the event being silently and permanently dropped now that it's
// claimed. Every OTHER failure here (a best-effort in-thread
// ack/ephemeral-notice post failing, or a deliberate business skip -- no
// default repo configured, a genuine ErrActorNotAuthorized denial) is
// still only ever logged, returning OK=true: retrying those would either
// change nothing (an authz denial renders the exact same verdict again)
// or risks double-posting/double-processing something that already fully
// succeeded (the ack/notice text is best-effort exactly because the
// underlying session/turn work is already durably committed by the time
// either runs). See handleEventResult's own doc comment for the SECOND,
// orthogonal thing this now reports: ReleaseMessageClaim.
func handleEvent(ctx context.Context, deps Deps, ack *ackClient, logger *slog.Logger, ev slackEvent) handleEventResult {
	channel := ev.Channel
	key := ev.threadKey()
	prompt := normalizeMrkdwn(ev.Text)

	// Step 39 ("identities + full RBAC", §13.2) update: resolve the REAL
	// actor behind ev.User ONCE, regardless of whether this event ends up
	// starting a brand-new thread or replying to an existing one -- the
	// auto-link algorithm runs "on first event from an unknown provider
	// identity" (§13.2), on every event, not only session-creating ones.
	// actorUserID is only actually CONSUMED by resolveOrClaimSession below
	// (a bare session's own created_by) -- a reply on an existing thread
	// has nowhere to attribute it (turns carries no actor column, mirrors
	// this file's own addTurn, which has never taken one), but identity
	// resolution/notification still needs to run for it regardless.
	actorUserID, notice := resolveSlackActor(ctx, logger, deps.SlackClient, deps.IdentityLink, deps.Timeouts, ev.User)

	res, ok := resolveOrClaimSession(ctx, deps, ack, logger, ev, channel, key, actorUserID)

	// Security-remediation addition (Step 39, "identities + full RBAC",
	// §13.2): notice (the "connected your account" confirmation, or --
	// far more sensitive -- the magic-link URL itself) is posted via
	// chat.postEphemeral, visible ONLY to ev.User, NEVER appended to the
	// ordinary, whole-channel-visible ack below anymore -- see
	// ack.go's own postEphemeral doc comment for the confirmed hijack
	// this closes. Posted regardless of ok/res.Skip (a denied/skip
	// outcome already gets its own ack elsewhere above; the identity
	// notice, when there is one, is still this user's own business to
	// see either way) except when ev.User is empty (postEphemeral would
	// have nothing to scope to -- never expected in practice, see
	// resolveSlackActor's own identical defensive short-circuit).
	if notice != "" && ev.User != "" {
		if err := ack.postEphemeralBounded(ctx, deps.AckTimeout, channel, ev.User, key, notice); err != nil {
			logger.Warn("slack: post identity-link ephemeral notice failed", "error", err)
		}
	}

	if !ok {
		// A genuine backend error inside resolveOrClaimSession (already
		// logged there) -- distinct from res.Skip below, which reports a
		// deliberate business decision (never a failure). ReleaseMessageClaim
		// is irrelevant here (see handleEventResult's own doc comment) --
		// NewHandler's own OK==false path always releases both claims
		// regardless.
		return handleEventResult{OK: false}
	}
	if res.Skip {
		// HIGH audit fix: propagate res.ReleaseMessageClaim straight through
		// -- see sessionResolution's own doc comment for exactly which Skip
		// outcome sets this, and why every other one leaves it false.
		return handleEventResult{OK: true, ReleaseMessageClaim: res.ReleaseMessageClaim}
	}

	createdTurn, created, err := addTurn(ctx, deps.Pool, deps.Sessions, deps.Turns, deps.AuditLog, deps.Registry, res.SessionID, prompt, actorUserID)
	if err != nil {
		logger.Error("slack: add turn failed", "error", err)
		return handleEventResult{OK: false}
	}

	// L20 audit fix: this package previously logged NOTHING on a
	// successful turn add at all -- an on-call engineer investigating a
	// bad push from a Slack-originated turn had no session_id/turn_id to
	// correlate against in the logs. Mirrors github's own identical
	// successful-mention log line shape (coalesce.go). The busy/dropped
	// case (M6) gets its own, distinct log line instead of silence.
	if created {
		logger.Info("slack: added turn", "session_id", res.SessionID, "turn_id", createdTurn.ID)
	} else {
		logger.Warn("slack: session already has an open turn, dropping message", "session_id", res.SessionID)
	}

	// Step 36 ("intent classifier", §8.3/§18): classify + record ONCE, on
	// the brand-new thread's own first real turn only -- IntentDecisionRecord
	// is a per-SESSION record (§18.4), and every Slack-originated session
	// gets its first (and, per this thread's own res.IsNewThread gate,
	// only) classify+record attempt right here, the first time a real
	// prompt exists for it at all (decided_at_stage="first_prompt" -- the
	// bare session itself, resolveOrClaimSession above, carries no prompt
	// text whatsoever). A later reply on the SAME thread (res.IsNewThread
	// == false) never re-classifies.
	if res.IsNewThread && created && deps.IntentClassifier != nil {
		// classify+record is now the shared intentclassifier.Service.
		// ClassifyAndRecord (H9/L11 audit fix) -- see that method's own
		// doc comment for the full "why a single shared call" reasoning.
		// This package's own one genuine difference from GitHub/Linear is
		// DecidedAtStageFirstPrompt below (a bare Slack thread carries no
		// prompt text of its own -- see this function's own doc comment
		// above for why classification only happens here, at the first
		// real turn).
		deps.IntentClassifier.ClassifyAndRecord(ctx, res.SessionID, ports.IntentClassifierInput{
			Text:    prompt,
			Surface: intentClassifierSurface,
		}, intentdomain.DecidedAtStageFirstPrompt)
	}

	ackText := ackReplyText
	switch {
	case res.IsNewThread:
		ackText = ackNewSessionText
	case !created:
		ackText = ackBusyText
	}
	// notice is no longer appended here -- see this function's own top
	// doc comment for why it is now posted separately, ephemerally,
	// scoped to ev.User (Step 39's own security-remediation addition).
	if err := ack.postAckBounded(ctx, deps.AckTimeout, channel, key, ackText); err != nil {
		logger.Warn("slack: post in-thread ack failed", "error", err)
	}
	return handleEventResult{OK: true}
}

// resolveOrClaimSession implements doc.go's own numbered design: an
// existing mapping resolves directly; a brand-new thread creates a bare
// session, races to claim the mapping, and falls back to the winner's
// session id on a lost claim. ok reports whether the caller should
// continue at all (false on a genuine error, already logged). creator is
// handleEvent's own already-resolved actor (Step 39, "identities + full
// RBAC", §13.2) -- Valid iff the mentioning Slack user is already linked,
// or was just auto-linked this call; invalid (bot attribution) otherwise,
// exactly matching this function's own PREVIOUS unconditional-bot-
// attribution precedent for the still-unresolved case.
//
// Both paths that resolve to an ALREADY-EXISTING session this event's own
// actor did not just create right here (the existing-mapping branch
// immediately below, and the "lost the race, fall back to the winner"
// branch at the bottom) route through authorizeExistingSessionReply,
// gating this event's eventual addTurn (handleEvent) behind exactly the
// same domain/authz.Authorize(ActionPromptSession) verdict the REST API's
// own POST .../turns endpoint already renders -- this Step's own SECOND
// fix pass, closing a confirmed re-review finding: the existing-mapping
// branch previously returned the resolved session id UNCONDITIONALLY,
// with no authz check at all, unlike the brand-new-thread/
// ActionCreateSession branch below (already gated by this Step's FIRST
// fix pass).
func resolveOrClaimSession(ctx context.Context, deps Deps, ack *ackClient, logger *slog.Logger, ev slackEvent, channel, key string, creator pgtype.UUID) (sessionResolution, bool) {
	existing, err := deps.Threads.Get(ctx, channel, key)
	if err == nil {
		return deps.authorizeExistingSessionReply(ctx, ack, logger, channel, key, existing.SessionID, creator)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		logger.Error("slack: lookup thread mapping failed", "error", err)
		return sessionResolution{}, false
	}

	// No mapping yet. Only an app_mention may start a brand-new thread --
	// a plain, unmapped "message" event is simply not ours (doc.go's own
	// step 6/7 reasoning).
	//
	// HIGH audit fix ("message-level claim can permanently orphan its own
	// app_mention twin on a brand-new thread"): ReleaseMessageClaim: true
	// here is load-bearing, not decorative -- this is the ONE asymmetric
	// outcome between the two twin event types (see sessionResolution's own
	// ReleaseMessageClaim doc comment for the full reasoning). Without it, a
	// plain "message" twin that wins NewHandler's own message-level claim
	// race on a brand-new thread would hold that claim forever, silently
	// discarding its app_mention sibling -- the ONLY twin that can ever
	// actually create the session -- the moment it arrives afterward.
	if !ev.isAppMention() {
		return sessionResolution{Skip: true, ReleaseMessageClaim: true}, true
	}

	if deps.DefaultRepoName == "" || deps.DefaultRepoURL == "" {
		if ackErr := ack.postAckBounded(ctx, deps.AckTimeout, channel, key, ackNotConfiguredText); ackErr != nil {
			logger.Warn("slack: post not-configured ack failed", "error", ackErr)
		}
		return sessionResolution{Skip: true}, true
	}

	// Step 39 ("identities + full RBAC", §13.2/§13.3) update: creator is
	// no longer trusted unconditionally just because it resolved to a
	// REAL, linked user_id -- that user's own role must still pass
	// domain/authz.Authorize(ActionCreateSession), exactly like the REST
	// /api/sessions handler already requires (create.go's own authorize
	// call). Resource{} is always correct here (no ownership carve-out on
	// create, mirroring create.go's own identical reasoning).
	//
	// Audit-fix batch update ("block unlinked actor state changes"): a
	// still-unlinked (bot-attributed) creator is NO LONGER let through --
	// actorauthz.AuthorizeLinkedActor (unlike AuthorizeResolvedActor) denies
	// immediately when creator.Valid is false, since the magic-link prompt
	// already sent by resolveSlackActor above means this same actor can
	// simply retry the identical mention once their account is linked. See
	// that function's own doc comment for why this is the correct call here
	// (Slack has a pending-link mechanism GitHub does not).
	if !actorauthz.AuthorizeLinkedActor(ctx, logger, authzSurface, deps.IdentityLink.Users, creator, authz.ActionCreateSession, authz.Resource{}) {
		logger.Warn("slack: create-session denied by authz", "channel", channel, "thread_key", key, "user_id", creator.String())
		if ackErr := ack.postAckBounded(ctx, deps.AckTimeout, channel, key, ackNotAuthorizedText); ackErr != nil {
			logger.Warn("slack: post not-authorized ack failed", "error", ackErr)
		}
		return sessionResolution{Skip: true}, true
	}

	bare, cerr := httpapi.CreateSessionCore(ctx, deps.Pool, deps.Sessions, deps.Turns, deps.Environments, deps.AuditLog, deps.Registry, restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceSlack,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: deps.DefaultRepoName, Url: deps.DefaultRepoURL},
		},
	}, creator)
	if cerr != nil {
		logger.Error("slack: create bare session failed", "status", cerr.Status, "message", cerr.Message)
		return sessionResolution{}, false
	}

	_, won, err := deps.Threads.Claim(ctx, channel, key, bare.ID)
	if err != nil {
		logger.Error("slack: claim thread mapping failed", "error", err)
		return sessionResolution{}, false
	}
	if won {
		return sessionResolution{SessionID: bare.ID, IsNewThread: true}, true
	}

	// Lost the race -- a concurrent first message claimed this thread
	// first. bare.ID is left as a harmless, never-dispatched orphan (see
	// doc.go's own tradeoff note); resolve the WINNER's real session
	// instead and continue exactly like an existing-mapping reply would --
	// including this Step's own authorizeExistingSessionReply gate: this
	// actor was only just authorized to CREATE a session (above), never to
	// prompt the DIFFERENT session a concurrent racer actually won, so the
	// same ActionPromptSession check applies here too.
	winner, err := deps.Threads.Get(ctx, channel, key)
	if err != nil {
		logger.Error("slack: lookup winning thread mapping after lost claim failed", "error", err)
		return sessionResolution{}, false
	}
	return deps.authorizeExistingSessionReply(ctx, ack, logger, channel, key, winner.SessionID, creator)
}

// authorizeExistingSessionReply gates a session id that this event's own
// actor did NOT just create in resolveOrClaimSession above (either branch
// -- see that function's own doc comment) behind
// domain/authz.Authorize(ActionPromptSession), replying in-thread with
// ackNotAuthorizedReplyText on denial instead of letting handleEvent's own
// addTurn enqueue a turn.
//
// Audit-fix batch update ("block unlinked actor state changes"): a
// still-unlinked (bot-attributed) creator is NO LONGER let through --
// authorizeSessionAction (identity.go) now returns ErrActorNotAuthorized
// immediately for that case (its own top-of-function short-circuit changed
// from "return nil" to "return ErrActorNotAuthorized"), which this function
// handles identically to a resolved-but-insufficient-role denial below: an
// in-thread ackNotAuthorizedReplyText reply, no turn enqueued. The magic-
// link notice (resolveSlackActor's own notice, delivered by the caller
// regardless of this denial) is what tells this actor how to fix it.
//
// ok (the second return value) is false ONLY for a genuine backend error
// authorizeSessionAction hit while checking (MEDIUM audit fix,
// "authorizeSessionAction conflates a genuine backend error with a real
// authorization denial") -- distinct from ErrActorNotAuthorized, a real
// denial, which still returns ok=true (Skip: true) exactly as before this
// fix. See handleEvent's own doc comment for how ok=false here flows into
// the release-the-claim-and-retry path.
func (deps Deps) authorizeExistingSessionReply(ctx context.Context, ack *ackClient, logger *slog.Logger, channel, key string, sessionID, creator pgtype.UUID) (sessionResolution, bool) {
	err := deps.authorizeSessionAction(ctx, logger, sessionID, creator, authz.ActionPromptSession)
	if err == nil {
		return sessionResolution{SessionID: sessionID}, true
	}
	if errors.Is(err, ErrActorNotAuthorized) {
		logger.Warn("slack: reply denied by authz", "channel", channel, "thread_key", key, "user_id", creator.String())
		if ackErr := ack.postAckBounded(ctx, deps.AckTimeout, channel, key, ackNotAuthorizedReplyText); ackErr != nil {
			logger.Warn("slack: post not-authorized-reply ack failed", "error", ackErr)
		}
		return sessionResolution{Skip: true}, true
	}
	// MEDIUM audit fix: a genuine backend failure while checking
	// authorization (already logged inside authorizeSessionAction) --
	// distinct from the real denial above -- must flow into the SAME
	// release-the-claim-and-retry path H2 already wired up for every other
	// post-claim failure, not be silently treated as "skip, no release"
	// the way a one-off DB blip previously was.
	return sessionResolution{}, false
}

// readBoundedBody reads r.Body (capped via http.MaxBytesReader, mirroring
// httpapi's own identical precedent) BEFORE any signature verification --
// Slack's own signature is computed over the exact raw bytes, so this
// must happen before any JSON decoding attempt (see doc.go's own step 1).
func readBoundedBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return nil, false
		}
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return nil, false
	}
	return body, true
}

// verifySlackRequest implements doc.go's own steps 2-3: assemble
// "v0:{timestamp}:{raw body}", verify the signature, then verify
// freshness. Fails closed (401) on any missing header, malformed
// timestamp, invalid signature, or expired timestamp -- never falls back
// to "assume valid".
func verifySlackRequest(w http.ResponseWriter, r *http.Request, body []byte, signingSecret string, window time.Duration, logger *slog.Logger) bool {
	tsHeader := r.Header.Get("X-Slack-Request-Timestamp")
	sigHeader := r.Header.Get("X-Slack-Signature")

	if tsHeader == "" || sigHeader == "" {
		writeError(w, http.StatusUnauthorized, "missing signature headers")
		return false
	}

	ts, err := strconv.ParseInt(tsHeader, 10, 64)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "malformed timestamp header")
		return false
	}

	presentedHex := strings.TrimPrefix(sigHeader, slackWebhookSignatureVersion+"=")
	signedPayload := []byte(slackWebhookSignatureVersion + ":" + tsHeader + ":" + string(body))

	if err := platform.VerifyWebhookSignature([]byte(signingSecret), signedPayload, presentedHex); err != nil {
		logger.Warn("slack: signature verification failed", "error", err)
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return false
	}

	if err := platform.VerifyWebhookTimestamp(ts, time.Now(), window); err != nil {
		logger.Warn("slack: timestamp freshness check failed", "error", err)
		writeError(w, http.StatusUnauthorized, "expired timestamp")
		return false
	}

	return true
}

// writeError writes a minimal {"error": message} JSON body at status --
// mirrors httpapi's own identical helper (this package cannot import
// that one's unexported writeError, and duplicating one tiny function is
// simpler and more honest than exporting an httpapi internal for it).
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeJSON writes v as a JSON body at status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
