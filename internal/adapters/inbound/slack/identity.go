// This file (identity.go) implements Step 39's ("identities + full RBAC",
// §13.2) own auto-linking wiring for Slack ingress -- shared by both
// handler.go (the Events API route) and interactive.go (the Interactivity
// route), replacing this package's PREVIOUS unconditional bot-attribution
// (an always-invalid creator/decidedBy/actor pgtype.UUID, every prior
// Step's own explicit precedent) with "try auto-link, else bot
// attribution + link-prompt side effect", per this Step's own brief.
//
// A plain function (not a method on either Deps or InteractiveDeps, which
// are two DISTINCT struct types in this package) since both call sites
// need the identical logic over just three collaborators
// (SlackClient/IdentityLink/Timeouts) -- passing those three directly
// avoids either duplicating this function once per Deps type or forcing
// one bloated shared struct neither file's own Deps otherwise needs.

package slack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/actorauthz"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

// authzSurface is this package's own "surface" label passed to every
// actorauthz.AuthorizeResolvedActor call below -- see that function's own
// doc comment for why (keeps this package's log lines prefixed "slack: "
// exactly as they were before the Step 39 shared-helper extraction into
// internal/app/actorauthz, batch fix/audit-github-actor-rbac).
const authzSurface = "slack"

// resolveSlackActor resolves slackUserID to a real Narvi user_id via
// internal/app/identitylink.Resolve, fetching that user's profile email
// (with retry, platform.Retry via identitylink.FetchEmailWithRetry) from
// Slack's own users.info API first -- ONLY when slackUserID is non-empty
// (every real app_mention/message/interactivity payload this package
// processes carries one; empty is a defensive no-op, never expected in
// practice).
//
// Returns (actorUserID, notice): actorUserID is Valid iff this identity is
// now known to belong to a real user (already linked, or auto-linked THIS
// call); notice is §13.2's own "notify in-channel" text (identitylink.
// Resolution.NotificationText), or "" when there's nothing to say -- the
// CALLER decides how to deliver it, and every caller delivers it
// EPHEMERALLY (visible only to the acting user), never appended to
// whole-channel-visible text: handler.go delivers it via
// ack.postEphemeralBounded; interactive.go delivers its own sibling
// resolveSlackActorSingleAttempt's notice (below) via
// SlackClient.PostEphemeral. This replaced this Step's own PREVIOUS
// behavior -- appending the notice to the existing in-thread ack
// (handler.go) or to the plan-decision outcome text posted via
// chat.update (interactive.go) -- which a confirmed security review
// found let anyone in the channel read another user's link-prompt
// notice; do not reintroduce that whole-channel-visible hijack path by
// trusting a future edit of this comment over the actual delivery code.
func resolveSlackActor(ctx context.Context, logger *slog.Logger, slackClient *slackapi.Client, identityLinkDeps identitylink.Deps, timeouts platform.Timeouts, slackUserID string) (actorUserID pgtype.UUID, notice string) {
	if slackUserID == "" {
		return pgtype.UUID{}, ""
	}

	email, ok := identitylink.FetchEmailWithRetry(ctx, timeouts, func(attemptCtx context.Context) (string, bool, error) {
		e, o, err := slackClient.GetUserEmail(attemptCtx, slackUserID)
		if err != nil {
			if errors.Is(err, slackapi.ErrSlackUserNotFound) {
				// Slack's own definitive "no such user" -- never
				// retryable (see that sentinel's own doc comment).
				return "", false, platform.Permanent(err)
			}
			return "", false, err
		}
		return e, o, nil
	})

	res, err := identitylink.Resolve(ctx, identityLinkDeps, sqlcgen.IdentityProviderSlack, slackUserID, email, ok)
	if err != nil {
		logger.Warn("slack: identity auto-link resolve failed", "error", err, "slack_user_id", slackUserID)
		return pgtype.UUID{}, ""
	}
	return res.UserID, res.NotificationText()
}

// resolveSlackActorSingleAttempt is resolveSlackActor's own deliberately
// FASTER sibling, used ONLY by interactive.go's decideAndUpdateMessage/
// handleViewSubmission -- both share Slack's own hard ~3s interactivity-
// ack window with a real Postgres write (DecidePlan/CreateTurnCore) AND a
// second outbound Slack call (chat.update / views.open) inside the SAME
// budget (see platform.Timeouts.SlackInteractivityAckTimeout's own doc
// comment), so there is no room left for resolveSlackActor's own
// multi-attempt backoff loop (identitylink.FetchEmailWithRetry). This
// makes exactly ONE profile-email fetch attempt, bounded by fetchTimeout
// (platform.Timeouts.SlackInteractivityIdentityFetchTimeout in
// production wiring), and treats ANY failure (timeout, transient error,
// even Slack's own definitive "no such user") identically -- bot
// attribution for THIS click, deferring a full, properly-retried
// resolution to the next event from this same identity (an Events API
// message, a later click, a modal submission all naturally retry via
// resolveSlackActor's own full algorithm).
func resolveSlackActorSingleAttempt(ctx context.Context, logger *slog.Logger, slackClient *slackapi.Client, identityLinkDeps identitylink.Deps, fetchTimeout time.Duration, slackUserID string) (actorUserID pgtype.UUID, notice string) {
	if slackUserID == "" {
		return pgtype.UUID{}, ""
	}

	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	email, ok, err := slackClient.GetUserEmail(fetchCtx, slackUserID)
	if err != nil {
		email, ok = "", false
	}

	res, err := identitylink.Resolve(ctx, identityLinkDeps, sqlcgen.IdentityProviderSlack, slackUserID, email, ok)
	if err != nil {
		logger.Warn("slack: identity auto-link resolve failed", "error", err, "slack_user_id", slackUserID)
		return pgtype.UUID{}, ""
	}
	return res.UserID, res.NotificationText()
}

// authorizeResolvedActor/ownedOrJoined used to live here (Step 39,
// "identities + full RBAC", §13.2/§13.3) but moved verbatim into
// internal/app/actorauthz (batch fix/audit-github-actor-rbac) once GitHub
// ingress became a third consumer of the identical logic Linear's own
// identity.go already duplicated once -- see that package's own doc.go for
// the full "why". authorizeSessionAction below calls
// actorauthz.AuthorizeResolvedActor/actorauthz.OwnedOrJoined directly now;
// nothing about ITS OWN behavior (the sequencing, the short-circuit for an
// unresolved actor, the error logging) changed.

// ErrActorNotAuthorized is authorizeSessionAction's own sentinel for a
// final, non-retryable denial -- either "a resolved, linked actor's role
// genuinely failed domain/authz.Authorize", OR (audit-fix batch update,
// "block unlinked actor state changes") "actorUserID never resolved to a
// linked account at all". Originally added for the first case only (MEDIUM
// audit fix, "authorizeSessionAction conflates a genuine backend error with
// a real authorization denial"), mirroring github's own identical
// ErrActorNotAuthorized (coalesce.go) -- reused rather than duplicated for
// the second case, since every caller's existing errors.Is(err,
// ErrActorNotAuthorized) handling already does exactly the right thing for
// both: post the honest denial message, do not release the webhook claim,
// do not treat it as retryable. Deliberately DISTINCT from
// any other error authorizeSessionAction returns: a caller checks for this
// one specifically and treats it as a final, non-retryable denial (skip
// without releasing the webhook-delivery claim -- redelivering the
// identical event would just render the same denial again), while any
// OTHER error is a genuine backend failure encountered WHILE checking
// authorization (e.g. deps.Sessions.Get hitting a dropped connection),
// which the caller instead routes into the SAME release-the-claim-and-
// retry path this batch's H2 fix already wired up for every other
// post-claim failure -- BEFORE this fix, authorizeSessionAction's own bare
// bool made the two indistinguishable, so a one-off DB blip was silently
// treated as a deliberate "skip, no release" business decision, dropping
// the user's legitimate message forever with no chance of redelivery ever
// retrying it.
var ErrActorNotAuthorized = errors.New("slack: actor not authorized")

// authorizeSessionAction renders the exact §13.3 verdict domain/authz.
// Authorize would for actorUserID attempting action against sessionID --
// this file's own Deps (handler.go, the Events API ingress) twin of
// InteractiveDeps.authorizeSessionAction (interactive.go), added by this
// Step's own SECOND fix pass so handler.go's own addTurn call (an
// ordinary reply on an already-mapped thread, or a brand-new mention that
// lost the first-writer-wins race and falls back onto a DIFFERENT
// winning session -- see handler.go's own authorizeExistingSessionReply)
// renders the IDENTICAL verdict the REST API/interactivity route already
// render for the same (actor, session, action).
//
// actorUserID.Valid == false (not yet linked -- the auto-link attempt for
// this identity did not resolve, e.g. no email match found) now returns
// ErrActorNotAuthorized immediately, with NO session/participants lookup at
// all -- audit-fix batch update ("block unlinked actor state changes"):
// this used to return nil (allowed), preserving §13.2's own original
// "unlinked actors get bot attribution ... the action proceeds" precedent.
// That precedent was a deliberate, user-decided hardening target, not a
// "keep as-is": a not-yet-linked actor's state-changing action is now
// denied exactly like a linked-but-insufficient-role one, and the SAME
// magic-link prompt this identity already gets (resolveSlackActor's own
// notice, delivered by every caller regardless of this denial) is how they
// retry once actually linked. Reusing ErrActorNotAuthorized here (rather
// than a new sentinel) means every existing caller's own
// errors.Is(err, ErrActorNotAuthorized) handling -- post the honest denial
// message, do NOT release the webhook claim, do NOT treat it as a
// retryable backend failure -- applies unchanged; see handler.go's own
// authorizeExistingSessionReply and interactive.go's own
// decideAndUpdateMessage/handleViewSubmission for the confirmed call sites.
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
		logger.Error("slack: get session for authorization failed", "error", err, "session_id", sessionID.String(), "action", string(action))
		return fmt.Errorf("slack: get session for authorization: %w", err)
	}

	joined, err := actorauthz.OwnedOrJoined(ctx, deps.Participants, sessionRow, actorUserID)
	if err != nil {
		logger.Error("slack: check participant for authorization failed", "error", err, "session_id", sessionID.String(), "action", string(action))
		return fmt.Errorf("slack: check participant for authorization: %w", err)
	}

	if !actorauthz.AuthorizeResolvedActor(ctx, logger, authzSurface, deps.IdentityLink.Users, actorUserID, action, authz.Resource{OwnedOrJoined: joined}) {
		return ErrActorNotAuthorized
	}
	return nil
}
