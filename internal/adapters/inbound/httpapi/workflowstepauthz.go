package httpapi

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/authz"
)

// canActOnWorkflowStep resolves whether (sessionRow.ID, actorUserID) counts
// as "own/joined" (sessionRow.CreatedBy == actorUserID, or a participants
// row exists -- §8.11's own multiplayer "joined" concept) and then renders
// the REAL §25.11 verdict via domain/authz.Authorize(authz.
// ActionDecideWorkflowStep, ...) -- a thin adapter translating this
// package's own (sessionRow, actorUserID, actorRole string) shape into
// authz.Actor/authz.Resource and back into the (bool, error) shape this
// file's own caller (authorizeWorkflowStepDecision, decideworkflowstep.go)
// expects, mirroring canActOnPlan's own identical shape (planauthz.go)
// exactly -- ActionDecideWorkflowStep is "the SAME row as ActionApprovePlan"
// by §25.11's own explicit instruction: admin/maintainer decide any run,
// a member only one on a session they created or joined, a viewer never,
// regardless of ownership.
func canActOnWorkflowStep(ctx context.Context, participants *postgres.ParticipantStore, sessionRow sqlcgen.Session, actorUserID pgtype.UUID, actorRole string) (bool, error) {
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
	if err := authz.Authorize(actor, authz.ActionDecideWorkflowStep, resource); err != nil {
		if errors.Is(err, authz.ErrForbidden) {
			return false, nil
		}
		// ErrUnknownAction should be unreachable here (ActionDecideWorkflowStep
		// is always a valid matrix entry, proven by domain/authz's own
		// TestMatrix_CoversEveryAction) -- propagated as a genuine error
		// (500), never silently treated as "not allowed", mirroring
		// canActOnPlan's own identical precedent.
		return false, err
	}
	return true, nil
}
