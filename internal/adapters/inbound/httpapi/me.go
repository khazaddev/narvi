// This file (me.go) implements GET /api/me (§12.2 item 7, §13.1) -- the
// "who am I" endpoint the sign-in view's own identity auto-link panel and
// already-signed-in state read: the AUTHENTICATED caller's own role and
// currently-linked identities, reusing restdtos.Member (contracts/rest/
// v1/dtos.schema.json's own "Member" definition, already generated for
// members.go's ListMembers/UpdateMemberRole) unchanged rather than
// inventing a second, narrower DTO for the exact same shape.
//
// Deliberately distinct from GET /api/members (admin-only, EVERY member +
// system-wide pending link prompts): this route returns exactly ONE row
// -- the caller's own -- to ANY authenticated role including viewer (see
// authz.ActionViewOwnProfile's own doc comment for why that is the
// correct row, not ActionManageMembers's admin-only one). It also,
// deliberately, carries no PendingLinkPrompt data at all:
// identity_link_prompts rows have no user_id (§13.2's own "never guess"
// design -- an ambiguous/unmatched provider identity has no known target
// user until someone actually clicks its magic link), so there is no
// honest way for a self-view to say "a pending link is waiting for YOU"
// without fabricating an attribution the schema itself does not carry.

package httpapi

import (
	"net/http"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/platform"
)

// GetMe backs GET /api/me. Mounted behind auth.Middleware exactly like
// every other route in this package; the 401 an unauthenticated request
// gets is that middleware's own generic body, never a route-local one --
// see this package's own authenticatedUserID/authorize helpers.
func GetMe(users *postgres.UserStore, identities *postgres.IdentityStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}
		if !authorize(w, r, authz.ActionViewOwnProfile, authz.Resource{}) {
			return
		}

		userRow, err := users.GetByID(ctx, userID)
		if err != nil {
			// Unreachable in practice (auth.Middleware already loaded
			// this exact row to authenticate the request), defended
			// against anyway rather than assuming the two reads can
			// never observe different results -- mirrors
			// authenticatedUserID's own "should never happen, defend
			// anyway" precedent.
			logger.Error("httpapi: get me: load user failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		identityRows, err := identities.ListForUser(ctx, userRow.ID)
		if err != nil {
			logger.Error("httpapi: get me: list identities failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		identityDTOs := make([]restdtos.Identity, 0, len(identityRows))
		for _, i := range identityRows {
			identityDTOs = append(identityDTOs, identityToDTO(i))
		}

		writeJSON(w, http.StatusOK, restdtos.Member{
			Id:          userRow.ID.String(),
			Email:       userRow.PrimaryEmail,
			DisplayName: userRow.DisplayName,
			Role:        restdtos.MemberRole(userRow.Role),
			Disabled:    userRow.Disabled,
			CreatedAt:   userRow.CreatedAt.Time,
			Identities:  identityDTOs,
		})
	}
}
