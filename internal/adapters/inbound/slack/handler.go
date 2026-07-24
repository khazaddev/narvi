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

// ackNewSessionText/ackReplyText/ackBusyText/ackNotConfiguredText are the
// fixed in-thread ack messages this Step posts -- deliberately plain,
// static strings (no templating beyond what's inlined here), matching
// this Step's own "smallest possible direct call" scoping decision
// (doc.go).
const (
	ackNewSessionText    = "On it — starting work on this now."
	ackReplyText         = "Got it — continuing work on this thread."
	ackBusyText          = "Still working on the previous message in this thread — I'll pick this up next."
	ackNotConfiguredText = "Slack ingress isn't configured with a default repo yet, so I can't start new work from a mention. A reply on an existing thread still works."

	// ackNotAuthorizedText is Step 39's own addition ("identities + full
	// RBAC", §13.2/§13.3): posted instead of ackNewSessionText when the
	// auto-linked/already-linked actor's own role fails domain/authz.
	// Authorize(ActionCreateSession) -- mirrors the REST API's own 403
	// semantics ("not authorized to perform this action", helpers.go)
	// rather than silently creating the session anyway.
	ackNotAuthorizedText = "Your linked Narvi account isn't authorized to start new sessions from Slack."
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
			writeError(w, http.StatusBadRequest, "malformed event")
			return
		}

		if ev.isIgnorable() {
			w.WriteHeader(http.StatusOK)
			return
		}

		handleEvent(ctx, deps, ack, logger, ev)
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
}

// handleEvent implements doc.go's own thread<->session mapping design
// (steps 7-8): resolve or create the mapped session, add a turn, then
// best-effort ack. Every failure here is logged only -- by the time this
// runs, the delivery is already claimed (see doc.go's own tradeoff note),
// so the caller above always still answers Slack with 200.
func handleEvent(ctx context.Context, deps Deps, ack *ackClient, logger *slog.Logger, ev slackEvent) {
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

	if !ok || res.Skip {
		return
	}

	created, err := addTurn(ctx, deps.Pool, deps.Sessions, deps.Turns, deps.Registry, res.SessionID, prompt)
	if err != nil {
		logger.Error("slack: add turn failed", "error", err)
		return
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
		recordIntentDecision(ctx, deps, logger, res.SessionID, prompt)
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
func resolveOrClaimSession(ctx context.Context, deps Deps, ack *ackClient, logger *slog.Logger, ev slackEvent, channel, key string, creator pgtype.UUID) (sessionResolution, bool) {
	existing, err := deps.Threads.Get(ctx, channel, key)
	if err == nil {
		return sessionResolution{SessionID: existing.SessionID}, true
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		logger.Error("slack: lookup thread mapping failed", "error", err)
		return sessionResolution{}, false
	}

	// No mapping yet. Only an app_mention may start a brand-new thread --
	// a plain, unmapped "message" event is simply not ours (doc.go's own
	// step 6/7 reasoning).
	if !ev.isAppMention() {
		return sessionResolution{Skip: true}, true
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
	// create, mirroring create.go's own identical reasoning). A still-
	// unlinked (bot-attributed) creator is untouched -- authorizeResolvedActor
	// returns true immediately for that case, preserving §13.2's own
	// explicit "the action proceeds" precedent.
	if !authorizeResolvedActor(ctx, logger, deps.IdentityLink.Users, creator, authz.ActionCreateSession, authz.Resource{}) {
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
	// instead and continue exactly like an existing-mapping reply would.
	winner, err := deps.Threads.Get(ctx, channel, key)
	if err != nil {
		logger.Error("slack: lookup winning thread mapping after lost claim failed", "error", err)
		return sessionResolution{}, false
	}
	return sessionResolution{SessionID: winner.SessionID}, true
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

// recordIntentDecision runs Step 36's own classify+record step (§8.3/§18)
// against prompt, never fatal to the caller -- see handleEvent's own doc
// comment for why this is only ever called once per session, at the
// brand-new thread's first real turn. Runs entirely OUTSIDE any Postgres
// transaction (a real outbound LLM call must never hold one open,
// mirroring ports.Notifier.Deliver's/ports.SourceControl.CreatePR's own
// identical discipline) and never blocks the caller's own in-thread ack
// beyond this synchronous call -- shadow mode (§18.5, the default for
// every surface until explicitly configured active) means nothing
// downstream yet consumes the recorded Target/Mode for real behavior
// regardless.
func recordIntentDecision(ctx context.Context, deps Deps, logger *slog.Logger, sessionID pgtype.UUID, prompt string) {
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
		DecidedAtStage: intentdomain.DecidedAtStageFirstPrompt,
	}); err != nil {
		logger.Warn("slack: record intent decision failed", "error", err, "session_id", sessionID)
	}
}
