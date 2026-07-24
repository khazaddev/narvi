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

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/identitylink"
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
func appendNotice(base, notice string) string {
	if notice == "" {
		return base
	}
	return base + "\n\n" + notice
}
