package httpapi

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// canActOnPlan is Step 37's ("plan mode, web", §13.3) own bounded,
// honest authorization STOPGAP -- the real domain/authz.Authorize(actor,
// action, resource) table-driven RBAC package §13.3 describes is
// explicitly Step 39's own deliverable (identities + full RBAC), which
// lands AFTER this Step. This predicate implements as much of §13.3's own
// RBAC table rows 2-3 as today's data model supports:
//
//	"Create sessions, prompt, approve plans on own/joined sessions" (admin/
//	maintainer/member) and "approve any plan" (admin/maintainer only) --
//	viewer never.
//
// true iff ANY of:
//   - sessionRow.CreatedBy == actorUserID (the session's own creator), or
//   - a participants row exists for (sessionRow.ID, actorUserID) (§8.11's
//     own multiplayer "joined" concept -- queried defensively even though
//     nothing populates participants yet, see ParticipantStore's own doc
//     comment), or
//   - actorRole is admin or maintainer (§13.3: "approve any plan").
//
// A plain member who neither created nor joined the session, and a
// viewer regardless, get false -- the caller (planapprove.go) must treat
// that as a 403, never a silent no-op.
//
// Deliberately a REUSABLE PREDICATE, not inlined twice across the two
// approve/reject handlers: a later Step (51, "decision inbox", §16.1's
// own "awaiting_approval: plan-mode plans the user is entitled to
// approve (per Authorize, §13.3)") will need this IDENTICAL logic to gate
// a LISTING query (which sessions' plans does this user see in their
// inbox), not just this action -- mirrors how Step 36's own corroboration
// logic documented its own future-consumer boundary. When Step 39 lands
// the real domain/authz.Authorize, this function's call sites are the
// exact slot it replaces -- this predicate's own shape (actor, resource
// (sessionRow), implicit action "approve/reject a plan") was chosen to
// make that swap mechanical, not a rewrite.
func canActOnPlan(ctx context.Context, participants *postgres.ParticipantStore, sessionRow sqlcgen.Session, actorUserID pgtype.UUID, actorRole string) (bool, error) {
	if actorRole == string(sqlcgen.UserRoleAdmin) || actorRole == string(sqlcgen.UserRoleMaintainer) {
		return true, nil
	}

	if sessionRow.CreatedBy.Valid && sessionRow.CreatedBy == actorUserID {
		return true, nil
	}

	exists, err := participants.Exists(ctx, sessionRow.ID, actorUserID)
	if err != nil {
		return false, err
	}
	return exists, nil
}
