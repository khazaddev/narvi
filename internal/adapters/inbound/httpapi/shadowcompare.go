// This file (shadowcompare.go) implements GET /api/admin/shadow-compare
// -- §8.8's own "shadow-comparison tooling for review" deliverable
// (IMPLEMENTATION_PLAN.md Step 59 row, reusing §9.4/§18.5's shadow-mode
// discipline). See internal/domain/shadowcompare's own doc.go for the
// full "why a read-only two-turn comparison, not a re-execution
// orchestrator" structural decision.
//
// Admin/maintainer only, gated by the new authz.ActionViewShadowComparison
// (§13.3 row 3's own "ANY session, no member own/joined escape hatch"
// shape -- authz.ActionViewAnalytics, row 1's "everyone including viewer",
// would be too broad: this reads across ANY two turns/sessions, not just
// ones the caller created or joined) -- introspective/debugging tooling
// for deciding a model rollout, not an ordinary product surface.

package httpapi

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/domain/shadowcompare"
	"github.com/khazaddev/narvi/internal/platform"
)

// GetShadowComparison backs GET /api/admin/shadow-compare?turnA=<id>&turnB=<id>.
func GetShadowComparison(turns *postgres.TurnStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionViewShadowComparison, authz.Resource{}) {
			return
		}

		var turnAID, turnBID pgtype.UUID
		if err := turnAID.Scan(r.URL.Query().Get("turnA")); err != nil {
			writeError(w, http.StatusBadRequest, "turnA is missing or not a valid uuid")
			return
		}
		if err := turnBID.Scan(r.URL.Query().Get("turnB")); err != nil {
			writeError(w, http.StatusBadRequest, "turnB is missing or not a valid uuid")
			return
		}

		turnA, err := turns.Get(ctx, turnAID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "turnA not found")
				return
			}
			logger.Error("httpapi: shadow-compare: get turnA failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		turnB, err := turns.Get(ctx, turnBID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "turnB not found")
				return
			}
			logger.Error("httpapi: shadow-compare: get turnB failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		report := shadowcompare.Compare(turnToSnapshot(turnA), turnToSnapshot(turnB))
		writeJSON(w, http.StatusOK, shadowComparisonReportResponse(report))
	}
}

// turnToSnapshot converts a real sqlcgen.Turn row into the domain
// package's own adapter-independent TurnSnapshot (§11: the domain package
// never sees sqlcgen types directly).
func turnToSnapshot(t sqlcgen.Turn) shadowcompare.TurnSnapshot {
	snap := shadowcompare.TurnSnapshot{
		TurnID:    t.ID.String(),
		SessionID: t.SessionID.String(),
		ModelID:   t.ModelID,
		Effort:    t.Effort,
		Status:    string(t.Status),
	}
	if t.CreatedAt.Valid {
		snap.CreatedAt = t.CreatedAt.Time
	}
	if t.DispatchedAt.Valid {
		dispatchedAt := t.DispatchedAt.Time
		snap.DispatchedAt = &dispatchedAt
	}
	if t.CompletedAt.Valid {
		completedAt := t.CompletedAt.Time
		snap.CompletedAt = &completedAt
	}
	return snap
}

func shadowComparisonReportResponse(report shadowcompare.Report) restdtos.ShadowComparisonReport {
	return restdtos.ShadowComparisonReport{
		TurnA: shadowComparisonTurnResponse(report.TurnA),
		TurnB: shadowComparisonTurnResponse(report.TurnB),
	}
}

func shadowComparisonTurnResponse(s shadowcompare.TurnSnapshot) restdtos.ShadowComparisonTurn {
	return restdtos.ShadowComparisonTurn{
		TurnId:          s.TurnID,
		SessionId:       s.SessionID,
		ModelId:         restdtos.ShadowComparisonTurnModelId(s.ModelID),
		Effort:          restdtos.ShadowComparisonTurnEffort(s.Effort),
		Status:          restdtos.ShadowComparisonTurnStatus(s.Status),
		CreatedAt:       s.CreatedAt,
		DispatchedAt:    s.DispatchedAt,
		CompletedAt:     s.CompletedAt,
		DurationSeconds: restdtos.ShadowComparisonTurnDurationSeconds(s.DurationSeconds()),
	}
}
