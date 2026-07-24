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
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

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

	email, emailOK := identitylink.FetchEmailWithRetry(ctx, deps.Timeouts, func(attemptCtx context.Context) (string, bool, error) {
		e, err := deps.LinearClient.GetUserEmail(attemptCtx, accessToken, externalID)
		if err != nil {
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

// authorizeResolvedActor mirrors internal/adapters/inbound/slack's own
// identical helper (identity.go) exactly -- see that copy's own doc
// comment for the full "why" this closes a confirmed security review
// finding (a resolved, auto-linked actor's own role was never actually
// checked against domain/authz.Authorize before Linear's own
// handleCreated/handlePrompted/handlePlanVerdict performed their
// state-changing effect). Duplicated here rather than exported from the
// slack package, mirroring this codebase's own established "small,
// documented duplication over a forced cross-package dependency"
// precedent (hasOpenTurn, webhook.go).
//
// user.Disabled is checked BEFORE ever calling domain/authz.Authorize --
// this Step's own SECOND fix-pass addition (a confirmed re-review
// finding), mirroring slack's own identical addition exactly: a disabled
// account's role would otherwise still pass Authorize, letting a disabled
// user create sessions, approve/reject plans, or prompt sessions via
// Linear even though auth.Middleware's own Authenticate already rejects
// that SAME disabled user's web session outright (internal/adapters/
// inbound/auth/middleware.go).
func authorizeResolvedActor(ctx context.Context, logger *slog.Logger, users *postgres.UserStore, actorUserID pgtype.UUID, action authz.Action, resource authz.Resource) bool {
	if !actorUserID.Valid {
		return true
	}

	user, err := users.GetByID(ctx, actorUserID)
	if err != nil {
		logger.Error("linear: authz: look up resolved actor's role failed", "error", err, "user_id", actorUserID.String(), "action", string(action))
		return false
	}

	if user.Disabled {
		logger.Warn("linear: authz: resolved actor's linked account is disabled, denying", "user_id", actorUserID.String(), "action", string(action))
		return false
	}

	actor := authz.Actor{UserID: actorUserID.String(), Role: authz.Role(user.Role)}
	if err := authz.Authorize(actor, action, resource); err != nil {
		if !errors.Is(err, authz.ErrForbidden) {
			logger.Error("linear: authz.Authorize failed", "error", err, "action", string(action))
		}
		return false
	}
	return true
}

// ownedOrJoined mirrors internal/adapters/inbound/slack's own identical
// helper exactly (§13.3 row 2's own "own/joined" carve-out).
func ownedOrJoined(ctx context.Context, participants *postgres.ParticipantStore, sessionRow sqlcgen.Session, actorUserID pgtype.UUID) (bool, error) {
	if sessionRow.CreatedBy.Valid && sessionRow.CreatedBy == actorUserID {
		return true, nil
	}
	return participants.Exists(ctx, sessionRow.ID, actorUserID)
}

// authorizeSessionAction renders the exact §13.3 verdict domain/authz.
// Authorize would for actorUserID attempting action against sessionID --
// shared by handlePrompted's own ordinary-reply ("request changes")
// fallthrough (ActionPromptSession) and handlePlanVerdict
// (ActionApprovePlan) below, mirroring internal/adapters/inbound/slack's
// own identical InteractiveDeps.authorizeSessionAction exactly.
//
// actorUserID.Valid == false (still bot-attributed) short-circuits to
// allowed=true immediately, with NO session/participants lookup at all --
// preserving §13.2's own "unlinked actors get bot attribution ... the
// action proceeds" precedent.
func (deps Deps) authorizeSessionAction(ctx context.Context, logger *slog.Logger, sessionID, actorUserID pgtype.UUID, action authz.Action) bool {
	if !actorUserID.Valid {
		return true
	}

	sessionRow, err := deps.Sessions.Get(ctx, sessionID)
	if err != nil {
		logger.Error("linear: get session for authorization failed", "error", err, "session_id", sessionID.String(), "action", string(action))
		return false
	}

	joined, err := ownedOrJoined(ctx, deps.Participants, sessionRow, actorUserID)
	if err != nil {
		logger.Error("linear: check participant for authorization failed", "error", err, "session_id", sessionID.String(), "action", string(action))
		return false
	}

	return authorizeResolvedActor(ctx, logger, deps.IdentityLink.Users, actorUserID, action, authz.Resource{OwnedOrJoined: joined})
}
