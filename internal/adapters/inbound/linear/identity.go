// This file (identity.go) implements Step 39's ("identities + full RBAC",
// §13.2) own auto-linking wiring for Linear ingress: replacing this
// package's PREVIOUS unconditional bot-attribution (an always-invalid
// decidedBy/creator pgtype.UUID, every prior Step's own explicit
// precedent -- see webhook.go's own git history) with "try auto-link,
// else bot-attribution + link-prompt side effect", per this Step's own
// brief.

package linear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/actorauthz"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

// authzSurface is this package's own "surface" label passed to every
// actorauthz.AuthorizeResolvedActor call below -- see that function's own
// doc comment for why (keeps this package's log lines prefixed "linear: "
// exactly as they were before the Step 39 shared-helper extraction into
// internal/app/actorauthz, batch fix/audit-github-actor-rbac).
const authzSurface = "linear"

// resolveActor resolves externalID (a Linear user id) to a real Narvi
// user_id via internal/app/identitylink.Resolve, fetching that user's
// profile email (with retry) from Linear's own API first ONLY when
// externalID is non-empty and not already linked -- an empty externalID
// (Linear's own "unset if automation-initiated" case, see payload.go's own
// doc comment on AgentSession.CreatorID) short-circuits to bot attribution
// immediately, with no lookup attempted at all (there is nothing to look
// up).
//
// Returns (actorUserID, notice): actorUserID is Valid iff this identity is
// now known to belong to a real user (already linked, or auto-linked THIS
// call); notice is §13.2's own "notify in-channel" text (Resolution.
// NotificationText), or "" when there's nothing to say -- the CALLER
// decides where to put it (this package has two shapes: appended to an
// existing outbound activity's own text, or posted as its own standalone
// activity when no other outbound call already exists on that path -- see
// webhook.go's own call sites).
func (deps Deps) resolveActor(ctx context.Context, logger *slog.Logger, organizationID, externalID string) (actorUserID pgtype.UUID, notice string) {
	if externalID == "" {
		return pgtype.UUID{}, ""
	}

	accessToken, ok := deps.decryptLinearAccessToken(ctx, logger, organizationID)
	if !ok {
		// No installation/token to fetch a profile email with -- fall
		// back to bot attribution for THIS event; a later event (once the
		// workspace is (re)connected) gets another chance.
		return pgtype.UUID{}, ""
	}

	email, emailOK := identitylink.FetchEmailWithRetry(ctx, logger, deps.Timeouts, sqlcgen.IdentityProviderLinear, func(attemptCtx context.Context) (string, bool, error) {
		e, err := deps.LinearClient.GetUserEmail(attemptCtx, accessToken, externalID)
		if err != nil {
			if errors.Is(err, linearapi.ErrLinearUserNotFound) {
				// Linear's own definitive "no such user" -- never
				// retryable (see that sentinel's own doc comment),
				// mirroring slack/identity.go's own identical
				// ErrSlackUserNotFound handling.
				return "", false, platform.Permanent(err)
			}
			return "", false, err
		}
		if e == "" {
			return "", false, nil
		}
		return e, true, nil
	})

	res, err := identitylink.Resolve(ctx, deps.IdentityLink, sqlcgen.IdentityProviderLinear, externalID, email, emailOK)
	if err != nil {
		logger.Warn("linear: identity auto-link resolve failed", "error", err, "external_id", externalID)
		return pgtype.UUID{}, ""
	}
	return res.UserID, res.NotificationText()
}

// decryptLinearAccessToken looks up and decrypts organizationID's stored
// Linear installation token -- a small, deliberate duplication of
// postAcknowledgment/postPlanOutcomeActivity's own identical inline
// lookup+decrypt shape (mirrors THIS package's own established "small,
// documented duplication over a forced shared abstraction" precedent,
// e.g. handler.go's own hasOpenTurn vs httpapi's identical helper) --
// extracted here specifically because resolveActor is a THIRD call site
// needing the exact same two steps, and duplicating it a third time
// starts to outweigh the cost of one small, obviously-correct shared
// function.
func (deps Deps) decryptLinearAccessToken(ctx context.Context, logger *slog.Logger, organizationID string) (accessToken string, ok bool) {
	install, err := deps.Installations.GetByOrganizationID(ctx, organizationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("linear: no installation for organization, skipping identity resolution", "organization_id", organizationID)
			return "", false
		}
		logger.Error("linear: look up installation failed", "error", err, "organization_id", organizationID)
		return "", false
	}

	decrypted, err := platform.DecryptToken(deps.TokenEncryptionKey, install.AccessTokenEncrypted)
	if err != nil {
		logger.Error("linear: decrypt installation access token failed", "error", err, "organization_id", organizationID)
		return "", false
	}
	return string(decrypted), true
}

// postIdentityNotice posts notice as its own best-effort `thought` Agent
// Activity -- used ONLY by the ordinary (non-plan-verdict) reply path in
// handlePrompted, which has no OTHER outbound activity call on it to
// append notice to (unlike handleCreated's acknowledgment or
// handlePlanVerdict's outcome activity, both of which append notice to
// their own existing text instead of sending a second call). A no-op when
// notice is empty -- never an extra API call, an extra log line, or an
// extra activity when there's nothing to say.
func (deps Deps) postIdentityNotice(ctx context.Context, organizationID, agentSessionID, notice string) {
	if notice == "" {
		return
	}

	logger := platform.Logger(ctx)
	accessToken, ok := deps.decryptLinearAccessToken(ctx, logger, organizationID)
	if !ok {
		return
	}

	activityCtx, cancel := context.WithTimeout(ctx, deps.Timeouts.LinearOutboundActivityTimeout)
	defer cancel()

	if err := deps.LinearClient.CreateThoughtActivity(activityCtx, accessToken, agentSessionID, notice); err != nil {
		logger.Warn("linear: post identity-link notice activity failed", "error", err, "agent_session_id", agentSessionID)
	}
}

// appendNotice appends notice to base on its own line, or returns base
// unchanged when notice is empty -- shared wording glue between
// handleCreated's acknowledgment and handlePlanVerdict's outcome text,
// both of which append the SAME §13.2 "notify in-channel" text
// (identitylink.Resolution.NotificationText) rather than sending a
// second, separate activity.
//
// Residual risk (Step 39, "identities + full RBAC", §13.2 -- documented,
// deliberate, Linear-specific): unlike Slack (ack.go's own postEphemeral),
// this text -- including a still-sensitive magic-link URL -- is posted as
// an ordinary Agent Activity, visible to whoever can see this Linear
// AgentSession/issue, not scoped to ONE named recipient the way Slack's
// chat.postEphemeral is. This codebase's own linearapi client (internal/
// adapters/outbound/linearapi) implements only AgentActivity creation
// (thought/response/error) -- no private/personal notification mutation
// exists in it today, and this Step's own investigation found no
// confirmed equivalent to add without a new, unverified Linear API scope.
// The concretely DEMONSTRATED hijack path this Step's own security review
// found (service_integration_test.go's own TestConsume_
// LinksIdentityAndDeletesPrompt shape) is Slack's shared-channel
// chat.postMessage -- fixed above. Linear's own AgentSession/AgentActivity
// model is already scoped tighter than an arbitrary shared Slack channel
// (an agent session is tied to a specific issue/delegation, not "every
// member of a channel"), and the spec's own magic-link mechanism does not
// mandate delivery-scoping parity across every channel -- so this is
// accepted as a documented residual gap for Linear specifically, per this
// Step's own explicit brief, rather than guessed at with an unverified
// API call.
//
// Re-reviewed in this Step's own SECOND fix pass: also checked whether
// Consume (internal/app/identitylink.Consume) could at least be narrowed
// to the small, known candidate-user-id set for the "multiple matches"
// (ambiguous) sub-case specifically, shrinking -- not closing -- this
// same hijack window without needing any new Linear API capability.
// Investigated and NOT done: see identitylink/service.go's own Consume
// doc comment for the full reasoning (it would need a new migration/
// column, a Consume signature change, and a new outcome case in the
// magic-link consume handler -- a small redesign, but still a redesign,
// not a targeted fix). This gap remains exactly as documented above.
func appendNotice(base, notice string) string {
	if notice == "" {
		return base
	}
	return base + "\n\n" + notice
}

// authorizeResolvedActor/ownedOrJoined used to live here (Step 39,
// "identities + full RBAC", §13.2/§13.3) but moved verbatim into
// internal/app/actorauthz (batch fix/audit-github-actor-rbac), confirmed
// identical to Slack's own copy -- see that package's own doc.go for the
// full "why". authorizeSessionAction below calls actorauthz.
// AuthorizeResolvedActor/actorauthz.OwnedOrJoined directly now; nothing
// about ITS OWN behavior changed.

// ErrActorNotAuthorized is authorizeSessionAction's own sentinel for a
// final, non-retryable denial -- "a resolved, linked actor's role
// genuinely failed domain/authz.Authorize" (MEDIUM audit fix,
// "authorizeSessionAction conflates a genuine backend error with a real
// authorization denial"), mirroring github's own identical
// ErrActorNotAuthorized (coalesce.go). Deliberately DISTINCT from any other
// error authorizeSessionAction returns: a caller checks for this one
// specifically and treats it as a final, non-retryable denial (skip
// without releasing the webhook-delivery claim -- redelivering the
// identical event would just render the same denial again), while any
// OTHER error is a genuine backend failure encountered WHILE checking
// authorization (e.g. deps.Sessions.Get hitting a dropped connection),
// which the caller instead routes into the SAME release-the-claim-and-
// retry path this batch's H2 fix already wired up for every other
// post-claim failure -- BEFORE the MEDIUM fix, authorizeSessionAction's own
// bare bool made the two indistinguishable, so a one-off DB blip was
// silently treated as a deliberate "skip, no release" business decision,
// dropping the user's legitimate message forever with no chance of
// redelivery ever retrying it.
//
// Audit-fix batch update ("block unlinked actor state changes", SECOND
// review pass): this sentinel USED to also cover "actorUserID never
// resolved to a linked account at all" -- collapsed into this same value
// because every existing caller's own errors.Is(err, ErrActorNotAuthorized)
// handling already did the right generic thing for both cases. A confirmed
// 3-lens adversarial review of that batch checked whether this package has
// the SAME button/message-stripping regression Slack's own interactive.go
// does for that collapse (see slack/identity.go's own identical doc
// comment for the full incident) -- it does NOT: handleCreated's own
// denial branch (webhook.go) posts a NEW `thought` Agent Activity
// (postAcknowledgment) and handlePlanVerdict's own denial branch posts a
// NEW `response` Agent Activity (postPlanOutcomeActivity); neither ever
// calls anything resembling Slack's own destructive chat.update (there is
// no Linear API call this package uses that mutates/replaces an existing
// Activity at all -- internal/adapters/outbound/linearapi only ever
// CREATES one). So collapsing "not yet linked" into ErrActorNotAuthorized
// here has no equivalent hidden side effect: kept as a single sentinel,
// deliberately NOT split into a second one the way Slack's was, since there
// is nothing here for a second sentinel to protect against.
var ErrActorNotAuthorized = errors.New("linear: actor not authorized")

// authorizeSessionAction renders the exact §13.3 verdict domain/authz.
// Authorize would for actorUserID attempting action against sessionID --
// shared by handlePrompted's own ordinary-reply ("request changes")
// fallthrough (ActionPromptSession) and handlePlanVerdict
// (ActionApprovePlan) below, mirroring internal/adapters/inbound/slack's
// own identical InteractiveDeps.authorizeSessionAction exactly.
//
// actorUserID.Valid == false (not yet linked -- the auto-link attempt for
// this identity did not resolve) now returns ErrActorNotAuthorized
// immediately, with NO session/participants lookup at all -- audit-fix
// batch update ("block unlinked actor state changes"): this used to return
// nil (allowed), preserving §13.2's own original "unlinked actors get bot
// attribution ... the action proceeds" precedent. That precedent was a
// deliberate, user-decided hardening target, not a "keep as-is": a
// not-yet-linked actor's ordinary reply or plan decision is now denied
// exactly like a linked-but-insufficient-role one, and the SAME magic-link
// prompt this identity already gets (resolveActor's own notice, appended by
// every caller regardless of this denial) is how they retry once actually
// linked. AgentActivity.UserID (the only actorUserID either caller below
// ever passes in) is Linear's own REQUIRED field -- never empty the way
// AgentSession.CreatorID can be (see webhook.go's own handleCreated for the
// separate, deliberately-carved-out automation-initiated case that
// authorizeSessionAction is never involved in at all) -- so every call
// here really is a genuine attempted identity, never a "no actor at all"
// case being swept up by this change.
//
// Returns ErrActorNotAuthorized when actorUserID is not linked at all
// (above) OR when a RESOLVED actor's own role genuinely fails
// domain/authz.Authorize -- both are final, non-retryable denials, handled
// identically by every caller. Returns any OTHER (non-nil) error for a
// genuine backend failure encountered while checking (deps.Sessions.Get/
// actorauthz.OwnedOrJoined erroring) -- MEDIUM audit fix, see
// ErrActorNotAuthorized's own doc comment for why this distinction matters
// to the caller.
func (deps Deps) authorizeSessionAction(ctx context.Context, logger *slog.Logger, sessionID, actorUserID pgtype.UUID, action authz.Action) error {
	if !actorUserID.Valid {
		return ErrActorNotAuthorized
	}

	sessionRow, err := deps.Sessions.Get(ctx, sessionID)
	if err != nil {
		logger.Error("linear: get session for authorization failed", "error", err, "session_id", sessionID.String(), "action", string(action))
		return fmt.Errorf("linear: get session for authorization: %w", err)
	}

	joined, err := actorauthz.OwnedOrJoined(ctx, deps.Participants, sessionRow, actorUserID)
	if err != nil {
		logger.Error("linear: check participant for authorization failed", "error", err, "session_id", sessionID.String(), "action", string(action))
		return fmt.Errorf("linear: check participant for authorization: %w", err)
	}

	if !actorauthz.AuthorizeResolvedActor(ctx, logger, authzSurface, deps.IdentityLink.Users, actorUserID, action, authz.Resource{OwnedOrJoined: joined}) {
		return ErrActorNotAuthorized
	}
	return nil
}
