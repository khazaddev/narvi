package httpapi

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/authz"
)

// canActOnPlan resolves whether (sessionRow.ID, actorUserID) counts as
// "own/joined" (sessionRow.CreatedBy == actorUserID, or a participants
// row exists -- §8.11's own multiplayer "joined" concept, queried
// defensively even though nothing populates participants yet, see
// ParticipantStore's own doc comment) and then renders the REAL §13.3
// verdict via domain/authz.Authorize(authz.ActionApprovePlan, ...) -- this
// function is no longer its own bespoke rule set (§8.1's own stopgap,
// see git history for that version): it is now a thin adapter translating
// this package's own (sessionRow, actorUserID, actorRole string) shape
// into authz.Actor/authz.Resource and back into the (bool, error) shape
// this file's own two callers (authorizePlanAction, planapprove.go)
// already expect, so neither of THEM needs to change at all.
//
// Preserves the exact same observable behavior the old stopgap already
// got right (this Step's own brief: "keep the same 'own/joined session'
// semantics it already gets right, just make it call the shared
// matrix") -- admin/maintainer approve any plan, a member only one they
// created or joined, viewer never, regardless of ownership. See
// domain/authz's own doc.go for the full matrix this now defers to.
func canActOnPlan(ctx context.Context, participants *postgres.ParticipantStore, sessionRow sqlcgen.Session, actorUserID pgtype.UUID, actorRole string) (bool, error) {
	ownedOrJoined := sessionRow.CreatedBy.Valid && sessionRow.CreatedBy == actorUserID
	if !ownedOrJoined {
		exists, err := participants.Exists(ctx, sessionRow.ID, actorUserID)
		if err != nil {
			return false, err
		}
		ownedOrJoined = exists
	}

	actor := authz.Actor{UserID: actorUserID.String(), Role: authz.Role(actorRole)}
	resource := authz.Resource{OwnedOrJoined: ownedOrJoined}
	if err := authz.Authorize(actor, authz.ActionApprovePlan, resource); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return false, nil
		}
		// ErrUnknownAction should be unreachable here (ActionApprovePlan is
		// always a valid matrix entry, proven by domain/authz's own
		// TestMatrix_CoversEveryAction) -- propagated as a genuine error
		// (500), never silently treated as "not allowed", so a caller bug
		// here is never masked as an ordinary 403.
		return false, err
	}
	return true, nil
}
