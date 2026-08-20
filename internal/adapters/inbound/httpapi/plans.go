// This file (plans.go) implements the audit-fix batch's own completeness
// fix (M3): GET /api/sessions/:id/plans -- the endpoint that closes the gap
// §8.1 ("plan mode, web", §8.1/§12.2 item 3) left open by shipping plan
// mode write-only (approve/reject, planapprove.go) with no way for a web
// client to ever discover a planId to approve in the first place.
//
// Mirrors ListArtifacts/ListEvents's own exact shape (artifacts.go,
// events.go) rather than inventing a new one: parseSessionID, session-exists
// 404 check, then the list query, then writeJSON. Deliberately NO extra RBAC
// beyond "session exists" -- the whole /api/sessions route group already
// sits behind auth.Middleware, and a plain read of the plan list changes no
// state, unlike ApprovePlan/RejectPlan's own canActOnPlan gate (planauthz.go)
// which exists specifically because those two calls DO change state.
//
// Deliberately minimal per this batch's own explicit scope note: no new
// WS/event notification on plan creation, no pagination (a session's own
// plan history is expected to stay small, matching ArtifactsResponse's own
// identical "unbounded" precedent) -- later Steps (decision inbox, plan-mode
// UI) are already planned to build richer surfaces; this endpoint's only job
// is closing the "no way to ever get a planId" gap.

package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// ListPlans backs GET /api/sessions/{sessionID}/plans (audit finding M3,
// completeness). Session existence is checked first -- 404 if it doesn't
// exist; otherwise every plan VERSION for the session (PlanStore.
// ListForSession, ordered by version), mapped to restdtos.Plan and returned
// as restdtos.ListPlansResponse.
func ListPlans(sessions *postgres.SessionStore, plans *postgres.PlanStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		logger := platform.Logger(ctx)

		if _, err := sessions.Get(ctx, sessionID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: get session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		rows, err := plans.ListForSession(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: list plans failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		wire := make([]restdtos.Plan, len(rows))
		for i, p := range rows {
			wire[i] = planWireMap(p)
		}

		writeJSON(w, http.StatusOK, restdtos.ListPlansResponse{Plans: wire})
	}
}

// planWireMap maps one sqlcgen.Plan row onto restdtos.Plan -- deliberately
// dropping TurnID/SlackChannelID/SlackMessageTs, present on the underlying
// row but not on the wire DTO (see Plan's own schema doc comment,
// contracts/rest/v1/dtos.schema.json, for why).
func planWireMap(p sqlcgen.Plan) restdtos.Plan {
	var decidedAt *time.Time
	if p.DecidedAt.Valid {
		t := p.DecidedAt.Time
		decidedAt = &t
	}
	var decidedBy restdtos.PlanDecidedBy
	if p.DecidedBy.Valid {
		s := p.DecidedBy.String()
		decidedBy = &s
	}
	return restdtos.Plan{
		Id:          p.ID.String(),
		SessionId:   p.SessionID.String(),
		Version:     int(p.Version),
		Status:      restdtos.PlanStatus(p.Status),
		PlanModelId: restdtos.PlanPlanModelId(p.PlanModelID),
		CreatedAt:   p.CreatedAt.Time,
		DecidedAt:   decidedAt,
		DecidedBy:   decidedBy,
	}
}
