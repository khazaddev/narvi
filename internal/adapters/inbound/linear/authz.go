// This file (authz.go) closes the gap a confirmed security-audit finding
// raised against this package's own workspace-install OAuth flow: both
// /auth/linear/install and /auth/linear/callback (install.go, callback.go)
// were mounted behind auth.Middleware ONLY -- any signed-in Narvi user, no
// role check at all -- despite install.go's/callback.go's own doc
// comments (as they read before this file landed) explicitly deferring
// that gating to a later Step that never actually added it. Connecting
// (or re-authorizing) a Linear workspace replaces the organization's
// SINGLE stored token pair (migrations/000031_linear_installations.
// up.sql's own "never a history of past ones" doc comment), so every
// later outbound Linear call and inbound AgentSessionEvent webhook runs on
// whatever token pair the LAST person to complete this flow supplied --
// exactly the "connecting/disconnecting a third-party integration"
// capability docs/TECHNICAL_PLAN.md §13.3's own permission matrix already
// names as admin-only (domain/authz.ActionManageIntegrations), so this
// file simply wires that already-defined, already-tested Action to its
// first two real call sites.

package linear

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

// requireManageIntegrations resolves the authenticated Narvi user
// auth.Middleware already attached to r's context (both routes this
// package mounts are behind it -- see cmd/control-plane/main.go's own
// /auth/linear route block) and renders domain/authz.Authorize's verdict
// for authz.ActionManageIntegrations against it -- §13.3's own
// "integrations ... admin only" matrix row (internal/domain/authz/
// action.go's own doc comment on that Action names exactly this:
// "connecting/disconnecting a third-party integration (Slack/Linear
// workspace, etc)").
//
// Shared by both NewInstallHandler and NewInstallCallbackHandler below
// rather than each re-deriving its own copy, since both gate the
// identical capability behind the identical context resolution -- mirrors
// internal/adapters/inbound/httpapi's own authorize()/authenticatedUserID
// pair (that package's helpers.go) in behavior, just reimplemented here
// rather than imported, since importing one inbound-adapter package
// (httpapi) from another (linear) would be a new, undesirable
// cross-adapter dependency neither package currently has.
//
// Returns the actor's own user id (parsed to pgtype.UUID -- the
// callback's own connected_by_user_id/audit-log attribution) and
// ok=true if permitted. Writes a 500 (no authenticated user in context --
// unreachable in production behind auth.Middleware, or that same user's
// own id failing to parse -- equally unreachable, both defended against
// anyway rather than silently proceeding, mirroring authenticatedUserID's
// own identical precedent) or 403 (Authorize rejected) and returns
// ok=false otherwise.
func requireManageIntegrations(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	ctx := r.Context()
	logger := platform.Logger(ctx)

	authUser, ok := platform.UserFromContext(ctx)
	if !ok {
		logger.Error("linear: no authenticated user in context (route not mounted behind auth.Middleware?)")
		http.Error(w, "internal error", http.StatusInternalServerError)
		return pgtype.UUID{}, false
	}

	actor := authz.Actor{UserID: authUser.ID, Role: authz.Role(authUser.Role)}
	if err := authz.Authorize(actor, authz.ActionManageIntegrations, authz.Resource{}); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			logger.Warn("linear: manage-integrations denied", "role", authUser.Role)
			http.Error(w, "admin role required to connect a Linear workspace", http.StatusForbidden)
			return pgtype.UUID{}, false
		}
		// ErrUnknownAction (a caller bug, never a legitimate "no" verdict --
		// see authz.ErrUnknownAction's own doc comment) or any other
		// unexpected error: 500, not 403, and logged loudly.
		logger.Error("linear: authz.Authorize failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return pgtype.UUID{}, false
	}

	var actorUserID pgtype.UUID
	if err := actorUserID.Scan(authUser.ID); err != nil {
		logger.Error("linear: parse authenticated user id failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return pgtype.UUID{}, false
	}
	return actorUserID, true
}
