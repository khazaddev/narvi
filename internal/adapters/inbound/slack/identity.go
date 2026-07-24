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
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/identitylink"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

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
// CALLER decides where to put it (handler.go appends it to the existing
// in-thread ack; interactive.go appends it to the plan-decision outcome
// text it already posts via chat.update).
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

// authorizeResolvedActor closes the gap a confirmed security review found
// in this Step's own auto-linking wiring: resolveSlackActor/
// resolveSlackActorSingleAttempt above resolve a REAL, auto-linked
// user_id/role, but until this function existed, NOTHING ever ran that
// resolved actor's role back through domain/authz.Authorize before the
// caller's own state-changing effect -- so a `viewer` (or a `member` with
// no ownership/participation in the target session) could create
// sessions, approve/reject plans, or request changes via Slack even
// though the identical action through the REST API (which DOES call
// authorize()/canActOnPlan) would be rejected. This directly contradicts
// docs/TECHNICAL_PLAN.md §13.3's own "a Slack approval passes exactly the
// same check as a web one".
//
// actorUserID.Valid == false (still bot-attributed -- the identity has
// not been linked yet) always returns allowed=true immediately, with NO
// lookup/Authorize call at all: §13.2's own explicit "unlinked actors get
// bot attribution + a link prompt ... the action proceeds" precedent for
// the not-yet-linked case is UNCHANGED by this fix -- the bug this closes
// is specifically that a RESOLVED, linked identity's role was never
// actually checked.
//
// A role lookup failure (should be unreachable in practice: actorUserID
// was JUST resolved from identities.user_id, itself FK'd to users.id) or
// any unexpected Authorize error (ErrUnknownAction -- a caller bug, never
// a legitimate "no" verdict) fails CLOSED -- logged loudly, allowed=false
// -- never silently treated as "proceed".
//
// user.Disabled is checked BEFORE ever calling domain/authz.Authorize --
// this Step's own SECOND fix-pass addition (a confirmed re-review
// finding): a disabled account's role would otherwise still pass
// Authorize (Disabled and Role are independent columns, migrations/
// 000002_users.up.sql), letting a disabled user create sessions,
// approve/reject plans, or prompt sessions via Slack even though
// auth.Middleware's own Authenticate already rejects that SAME disabled
// user's web session outright (internal/adapters/inbound/auth/
// middleware.go). Mirrors that check exactly -- denies immediately, never
// falls through to a role-based verdict for a disabled user.
func authorizeResolvedActor(ctx context.Context, logger *slog.Logger, users *postgres.UserStore, actorUserID pgtype.UUID, action authz.Action, resource authz.Resource) bool {
	if !actorUserID.Valid {
		return true
	}

	user, err := users.GetByID(ctx, actorUserID)
	if err != nil {
		logger.Error("slack: authz: look up resolved actor's role failed", "error", err, "user_id", actorUserID.String(), "action", string(action))
		return false
	}

	if user.Disabled {
		logger.Warn("slack: authz: resolved actor's linked account is disabled, denying", "user_id", actorUserID.String(), "action", string(action))
		return false
	}

	actor := authz.Actor{UserID: actorUserID.String(), Role: authz.Role(user.Role)}
	if err := authz.Authorize(actor, action, resource); err != nil {
		if !errors.Is(err, authz.ErrForbidden) {
			logger.Error("slack: authz.Authorize failed", "error", err, "action", string(action))
		}
		return false
	}
	return true
}

// ownedOrJoined mirrors internal/adapters/inbound/httpapi's own
// canActOnPlan/CreateTurn "own/joined" resolution exactly (§13.3 row 2):
// true iff sessionRow was created by actorUserID, or actorUserID has an
// existing participants row for it. Duplicated here (rather than
// exporting httpapi's own unexported equivalent) mirroring this package's
// own established "small, documented duplication over a forced shared
// abstraction" precedent (hasOpenTurn, turn.go).
func ownedOrJoined(ctx context.Context, participants *postgres.ParticipantStore, sessionRow sqlcgen.Session, actorUserID pgtype.UUID) (bool, error) {
	if sessionRow.CreatedBy.Valid && sessionRow.CreatedBy == actorUserID {
		return true, nil
	}
	return participants.Exists(ctx, sessionRow.ID, actorUserID)
}

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
// actorUserID.Valid == false (still bot-attributed) short-circuits to
// allowed=true immediately, with NO session/participants lookup at all --
// preserving §13.2's own "unlinked actors get bot attribution ... the
// action proceeds" precedent, and avoiding any DB read at all on the
// common bot-attributed path.
func (deps Deps) authorizeSessionAction(ctx context.Context, logger *slog.Logger, sessionID, actorUserID pgtype.UUID, action authz.Action) bool {
	if !actorUserID.Valid {
		return true
	}

	sessionRow, err := deps.Sessions.Get(ctx, sessionID)
	if err != nil {
		logger.Error("slack: get session for authorization failed", "error", err, "session_id", sessionID.String(), "action", string(action))
		return false
	}

	joined, err := ownedOrJoined(ctx, deps.Participants, sessionRow, actorUserID)
	if err != nil {
		logger.Error("slack: check participant for authorization failed", "error", err, "session_id", sessionID.String(), "action", string(action))
		return false
	}

	return authorizeResolvedActor(ctx, logger, deps.IdentityLink.Users, actorUserID, action, authz.Resource{OwnedOrJoined: joined})
}
