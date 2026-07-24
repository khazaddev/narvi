// This file (planapprove.go) implements Step 37's ("plan mode, web",
// §8.1/§12.2 item 3) two new REST endpoints: POST
// /api/sessions/:id/plans/:planId/approve and its reject twin. Both
// follow CreateTurn's own exact pattern (turn.go) -- open a tx, lock the
// session row, run canActOnPlan (planauthz.go), then a guarded conditional
// UPDATE on the plans row (§16.2's own "actions re-validate server-side"
// invariant, here specialized to "first verdict wins" via the plans_one_
// awaiting_approval_per_session partial unique index -- see migrations/
// 000034_plan_mode.up.sql).
//
// Approve additionally inserts a new turn (plan_mode=false, model_id =
// the session's own build_model_id, a short fixed prompt) in the SAME
// transaction, then -- following CreateTurn's own identical post-commit
// sequencing exactly -- fire-and-forget GetOrSpawn + EnsureDispatched
// OUTSIDE the transaction, once it has actually committed. Reject has no
// new turn and no dispatch: it only ever flips the plan's own status.
//
// Neither endpoint re-explains the plan in the new turn's own prompt text
// -- sessions.opencode_conversation_id already persists across turns and
// is already threaded into every dispatched Prompt automatically
// (dispatch.go's buildPromptPayload), so the SAME OpenCode conversation
// the plan turn ran in continues with full context, no re-statement
// needed (see this Step's own brief for the precedent this reuses).
//
// ApprovePlan additionally applies CreateTurn's own hasOpenTurn 409 gate
// (turn.go) before ever touching the plans row: a plan stays
// 'awaiting_approval' in the DB for as long as no LATER plan-mode turn has
// yet completed, even while a "request changes" turn for it is already
// Pending/Dispatched/Processing -- approving it during that window would
// flip it to 'approved' and enqueue an implementation turn that
// auto-dispatches (handleEnsureDispatched, sandboxevent.go) the instant
// that in-flight revision completes, building the STALE plan before any
// human ever reviews its successor. See ApprovePlan's own inline comment
// for the full sequencing this closes.

package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// implementPlanPrompt is the short, fixed prompt text the approval-
// dispatched implementation turn carries -- sufficient because the
// SAME OpenCode conversation the plan turn itself ran in already has the
// full plan context (see this file's own top doc comment).
const implementPlanPrompt = "Implement the plan you just proposed."

// planActionResponse is both endpoints' own response body. Not a
// /contracts-defined DTO: this repo has no frontend code yet (this Step's
// own explicit scope note), and neither this Step's brief nor §6.3 names
// a specific wire shape for these two actions -- kept as a small, honest,
// locally-scoped shape rather than inventing a contract nothing consumes
// yet; a future Step building the actual UI can promote this into
// /contracts once a real consumer exists, the same way every other
// contract here was added when its own Step first needed it.
type planActionResponse struct {
	PlanID string  `json:"planId"`
	Status string  `json:"status"`
	TurnID *string `json:"turnId,omitempty"`
}

// parsePlanID parses chi's own "planId" URL path param as a UUID --
// mirrors parseSessionID's own exact shape (helpers.go).
func parsePlanID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	raw := chi.URLParam(r, "planId")
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		writeError(w, http.StatusBadRequest, "malformed plan id")
		return pgtype.UUID{}, false
	}
	return id, true
}

// lockSessionForPlanAction fetches sessionID (404 if missing) and then
// locks its row for the rest of the caller's own transaction (404 again
// if it vanished between the two, vanishingly rare) -- mirrors
// CreateTurn's own identical two-step shape (turn.go) exactly. Returns
// the UNLOCKED row fetched by the first call (sufficient for this
// caller's own read-only use of CreatedBy/BuildModelID; the lock itself,
// not a fresher read, is what matters here).
func lockSessionForPlanAction(w http.ResponseWriter, r *http.Request, tx pgx.Tx, sessions *postgres.SessionStore, sessionID pgtype.UUID) (sqlcgen.Session, bool) {
	ctx := r.Context()
	logger := platform.Logger(ctx)

	sessionRow, err := sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return sqlcgen.Session{}, false
		}
		logger.Error("httpapi: get session failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return sqlcgen.Session{}, false
	}

	if _, err := sessions.WithTx(tx).GetActorEpochForUpdate(ctx, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "session not found")
			return sqlcgen.Session{}, false
		}
		logger.Error("httpapi: lock session row failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return sqlcgen.Session{}, false
	}

	return sessionRow, true
}

// authorizePlanAction runs canActOnPlan (planauthz.go) against the
// currently authenticated request user, writing 500/403 and returning
// false on any failure -- shared by both ApprovePlan and RejectPlan so
// neither duplicates this sequencing.
func authorizePlanAction(w http.ResponseWriter, r *http.Request, participants *postgres.ParticipantStore, sessionRow sqlcgen.Session) (pgtype.UUID, bool) {
	ctx := r.Context()
	logger := platform.Logger(ctx)

	actorUserID, ok := authenticatedUserID(w, r)
	if !ok {
		return pgtype.UUID{}, false
	}

	authUser, ok := platform.UserFromContext(ctx)
	if !ok {
		// Unreachable behind auth.Middleware -- see authenticatedUserID's
		// own identical defensive precedent (helpers.go).
		logger.Error("httpapi: no authenticated user in context (route not mounted behind auth.Middleware?)")
		writeError(w, http.StatusInternalServerError, "internal error")
		return pgtype.UUID{}, false
	}

	allowed, err := canActOnPlan(ctx, participants, sessionRow, actorUserID, authUser.Role)
	if err != nil {
		logger.Error("httpapi: canActOnPlan failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return pgtype.UUID{}, false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "not authorized to act on this session's plans")
		return pgtype.UUID{}, false
	}

	return actorUserID, true
}

// ApprovePlan backs POST /api/sessions/:id/plans/:planId/approve (§12.2
// item 3's own "Approve & build" action). See this file's own top doc
// comment for the full sequencing.
func ApprovePlan(pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, plans *postgres.PlanStore, participants *postgres.ParticipantStore, registry *sessionactor.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		planID, ok := parsePlanID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		r = r.WithContext(ctx)
		logger := platform.Logger(ctx)

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: begin approve-plan tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		sessionRow, ok := lockSessionForPlanAction(w, r, tx, sessions, sessionID)
		if !ok {
			return
		}

		actorUserID, ok := authorizePlanAction(w, r, participants.WithTx(tx), sessionRow)
		if !ok {
			return
		}

		// Refuse to approve (and dispatch an implementation turn for) a
		// plan while ANY other turn for this session hasn't yet reached a
		// terminal state -- most importantly, a "request changes" turn
		// (planMode=true, submitted via the existing POST .../turns
		// endpoint) that is still Pending/Dispatched/Processing. That turn
		// has not completed yet, so recordPlanIfNeeded (planrecord.go)
		// has NOT superseded this plan row yet either -- it is still
		// genuinely 'awaiting_approval' in the DB even though a newer
		// revision is already being produced. Approving it here anyway
		// would flip it to 'approved' and enqueue a Pending implementation
		// turn that (per handleEnsureDispatched's own "dispatch next
		// pending" sequencing, sandboxevent.go) dispatches automatically
		// the moment the in-flight revision turn completes -- silently
		// building the STALE plan the instant its own successor becomes
		// awaiting_approval, with no human ever having approved that
		// successor. Mirrors CreateTurn's own hasOpenTurn 409 gate
		// (turn.go) exactly, including its message, applied here for the
		// symmetric reason: a human action must never race a turn that is
		// already in flight for the same session.
		existingTurns, err := turns.WithTx(tx).ListForSession(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: list turns for approve-plan open-turn check failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if hasOpenTurn(existingTurns) {
			writeError(w, http.StatusConflict, "a turn is already pending, dispatched, or processing for this session")
			return
		}

		rowsAffected, err := plans.WithTx(tx).ApproveIfAwaitingApproval(ctx, planID, sessionID, actorUserID)
		if err != nil {
			logger.Error("httpapi: approve plan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if rowsAffected == 0 {
			writeError(w, http.StatusConflict, "plan is not awaiting approval (already decided, or a stale id)")
			return
		}

		prompt := implementPlanPrompt
		createdTurn, err := turns.WithTx(tx).Create(ctx, sqlcgen.CreateTurnParams{
			SessionID: sessionID,
			Status:    sqlcgen.TurnStatusPending,
			Prompt:    &prompt,
			ModelID:   sessionRow.BuildModelID,
			PlanMode:  false,
		})
		if err != nil {
			logger.Error("httpapi: create implementation turn failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: commit approve-plan tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// Fire-and-forget, OUTSIDE the transaction above, mirroring
		// CreateTurn's own identical post-commit sequencing exactly
		// (turn.go).
		actor, spawnErr := registry.GetOrSpawn(ctx, sessionID)
		if spawnErr != nil {
			logger.Warn("httpapi: GetOrSpawn after plan approval failed", "error", spawnErr)
		} else if sendErr := actor.Send(ctx, sessionactor.EnsureDispatched{}); sendErr != nil {
			logger.Warn("httpapi: send EnsureDispatched after plan approval failed", "error", sendErr)
		}

		turnIDStr := createdTurn.ID.String()
		writeJSON(w, http.StatusOK, planActionResponse{PlanID: planID.String(), Status: "approved", TurnID: &turnIDStr})
	}
}

// RejectPlan backs POST /api/sessions/:id/plans/:planId/reject (§12.2
// item 3's own "Reject" action). Same guarded-UPDATE shape as ApprovePlan,
// with no new turn and no dispatch.
func RejectPlan(pool *pgxpool.Pool, sessions *postgres.SessionStore, plans *postgres.PlanStore, participants *postgres.ParticipantStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		planID, ok := parsePlanID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		r = r.WithContext(ctx)
		logger := platform.Logger(ctx)

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: begin reject-plan tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		sessionRow, ok := lockSessionForPlanAction(w, r, tx, sessions, sessionID)
		if !ok {
			return
		}

		actorUserID, ok := authorizePlanAction(w, r, participants.WithTx(tx), sessionRow)
		if !ok {
			return
		}

		rowsAffected, err := plans.WithTx(tx).RejectIfAwaitingApproval(ctx, planID, sessionID, actorUserID)
		if err != nil {
			logger.Error("httpapi: reject plan failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if rowsAffected == 0 {
			writeError(w, http.StatusConflict, "plan is not awaiting approval (already decided, or a stale id)")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: commit reject-plan tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, planActionResponse{PlanID: planID.String(), Status: "rejected"})
	}
}
