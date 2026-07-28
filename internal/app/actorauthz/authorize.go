package actorauthz

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/authz"
)

// AuthorizeResolvedActor closes the gap a confirmed security review found
// in Step 39's own auto-linking wiring (Slack/Linear): resolving an inbound
// event to a REAL, linked user_id/role is not enough by itself -- something
// must ALSO run that resolved actor's role back through domain/authz.
// Authorize before the caller's own state-changing effect, or a `viewer`
// (or a `member` with no ownership/participation in the target session)
// could act via a channel even though the identical action through the
// REST API (which DOES call authorize()/canActOnPlan) would be rejected.
// This directly implements docs/TECHNICAL_PLAN.md §13.3's own "a channel
// approval passes exactly the same check as a web one" requirement.
//
// surface is a short, caller-supplied label ("slack", "linear", "github")
// used only to prefix this function's own log lines -- so a log reader can
// still tell which ingress adapter produced them, exactly as if each
// package still had its own private copy of this function.
//
// actorUserID.Valid == false (still bot-attributed -- the identity has not
// resolved to a real user at all) always returns allowed=true immediately,
// with NO lookup/Authorize call at all: every caller's own "unlinked actors
// get bot attribution, the action proceeds" precedent for the not-yet-known
// case is preserved -- the thing this function exists to gate is
// specifically a RESOLVED, known identity's role.
//
// A role lookup failure (should be unreachable in practice: actorUserID was
// just resolved from identities.user_id, itself FK'd to users.id) or any
// unexpected Authorize error (ErrUnknownAction -- a caller bug, never a
// legitimate "no" verdict) fails CLOSED -- logged loudly, allowed=false --
// never silently treated as "proceed".
//
// user.Disabled is checked BEFORE ever calling domain/authz.Authorize: a
// disabled account's role would otherwise still pass Authorize (Disabled
// and Role are independent columns, migrations/000002_users.up.sql),
// letting a disabled user act via a channel even though auth.Middleware's
// own Authenticate already rejects that SAME disabled user's web session
// outright (internal/adapters/inbound/auth/middleware.go). Mirrors that
// check exactly -- denies immediately, never falls through to a role-based
// verdict for a disabled user.
func AuthorizeResolvedActor(ctx context.Context, logger *slog.Logger, surface string, users *postgres.UserStore, actorUserID pgtype.UUID, action authz.Action, resource authz.Resource) bool {
	if !actorUserID.Valid {
		return true
	}

	user, err := users.GetByID(ctx, actorUserID)
	if err != nil {
		logger.Error(surface+": authz: look up resolved actor's role failed", "error", err, "user_id", actorUserID.String(), "action", string(action))
		return false
	}

	if user.Disabled {
		logger.Warn(surface+": authz: resolved actor's linked account is disabled, denying", "user_id", actorUserID.String(), "action", string(action))
		return false
	}

	actor := authz.Actor{UserID: actorUserID.String(), Role: authz.Role(user.Role)}
	if err := authz.Authorize(actor, action, resource); err != nil {
		if !errors.Is(err, authz.ErrForbidden) {
			logger.Error(surface+": authz.Authorize failed", "error", err, "action", string(action))
		}
		return false
	}
	return true
}

// AuthorizeLinkedActor is the audit-hardening counterpart to
// AuthorizeResolvedActor above, for exactly ONE call shape: an inbound
// Slack/Linear actor that has NOT (yet) resolved to a real Narvi user_id at
// all (actorUserID.Valid == false) is DENIED outright, rather than allowed
// to proceed under bot attribution -- the audit finding this function
// exists to close (docs/TECHNICAL_PLAN.md §13.2's own previous "the action
// proceeds, a magic-link prompt is sent in parallel" behavior was a
// user-decided hardening target, not a "keep as-is": letting a
// not-yet-linked identity's state-changing action through at all, even
// under bot attribution, is no longer acceptable). The magic-link prompt
// itself is UNCHANGED and still sent by the caller exactly as before -- only
// whether the state-changing action proceeds while that prompt is pending
// changes. Once actorUserID IS Valid, this delegates to
// AuthorizeResolvedActor unchanged, so an already-linked actor's role/
// disabled/ownership verdict is identical either way.
//
// Do NOT collapse this back into AuthorizeResolvedActor, and do NOT change
// AuthorizeResolvedActor's own actorUserID.Valid == false short-circuit to
// match this one -- the two functions exist specifically BECAUSE Slack and
// Linear have a mechanism GitHub does not. Slack/Linear's own auto-link
// algorithm (internal/app/identitylink) treats an unresolved identity as
// "not yet linked, but a magic link is on its way" -- a transient,
// self-resolving state the actor can clear themselves by clicking the link
// and retrying the identical action. GitHub's own commenter-identity
// resolution (github/identity.go) is structurally different: it resolves
// directly from an existing GitHub-OAuth-login identity with no deferred
// "auto-link pending" mechanism at all -- an unresolved GitHub commenter has
// simply never logged into Narvi via GitHub OAuth, a different and more
// permanent case with no pending link to wait for. GitHub's own callers
// (github/coalesce.go) must keep calling AuthorizeResolvedActor exactly as
// today; only Slack/Linear's own direct session-creation gates and their
// authorizeSessionAction helpers call this function instead.
func AuthorizeLinkedActor(ctx context.Context, logger *slog.Logger, surface string, users *postgres.UserStore, actorUserID pgtype.UUID, action authz.Action, resource authz.Resource) bool {
	if !actorUserID.Valid {
		return false
	}
	return AuthorizeResolvedActor(ctx, logger, surface, users, actorUserID, action, resource)
}

// OwnedOrJoined mirrors internal/adapters/inbound/httpapi's own
// canActOnPlan/CreateTurn "own/joined" resolution exactly (§13.3 row 2):
// true iff sessionRow was created by actorUserID, or actorUserID has an
// existing participants row for it.
func OwnedOrJoined(ctx context.Context, participants *postgres.ParticipantStore, sessionRow sqlcgen.Session, actorUserID pgtype.UUID) (bool, error) {
	if sessionRow.CreatedBy.Valid && sessionRow.CreatedBy == actorUserID {
		return true, nil
	}
	return participants.Exists(ctx, sessionRow.ID, actorUserID)
}
