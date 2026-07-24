package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/platform"
)

// hasOpenTurn reports whether ANY turn in turns is non-terminal (Pending,
// Dispatched, or Processing) -- deliberately NOT turn.HasInFlightTurn,
// which only counts Dispatched/Processing (correct for its own callers:
// dispatch.go's NextToDispatch needs Pending turns to NOT count as
// "in flight", since a Pending turn is exactly the one it's looking to
// dispatch next). CreateTurn's own precondition is stricter: this
// endpoint must refuse to queue a SECOND turn while an earlier one is
// still Pending too, not just once it's actually Dispatched/Processing --
// otherwise concurrent relaunch calls against a session with zero
// existing turns would each see "no Dispatched/Processing turn" and all
// insert their own Pending row, defeating the 409 this handler exists to
// return (see this file's own CreateTurn doc comment).
func hasOpenTurn(turns []sqlcgen.Turn) bool {
	for _, t := range turns {
		if !turn.IsTerminal(turn.State(t.Status)) {
			return true
		}
	}
	return false
}

// CreateTurn backs POST /api/sessions/{sessionID}/turns (Step 28, "turn
// recovery", §8.7 "Recovery UX: relaunch-and-resume (conversation id
// replay)"): the relaunch-and-resume REST API. Enqueues a new Pending turn
// on an EXISTING session -- 404 if the session doesn't exist, 409 if
// another turn for it hasn't reached a terminal state yet, otherwise 201
// with restdtos.CreateTurnResponse.
//
// Because sessions.opencode_conversation_id already persists across turns
// (§3.3) and internal/app/sessionactor/dispatch.go's own buildPromptPayload
// already includes it automatically in every Prompt it builds, the new
// turn created here automatically resumes the SAME OpenCode conversation
// the moment it dispatches -- no separate "resume" flag or branch is
// needed in either the request DTO (CreateTurnRequest has no
// conversationId field at all) or this handler.
//
// The precondition check below (hasOpenTurn) is deliberately stricter than
// turn.NextToDispatch's own "in flight" concept (Dispatched/Processing
// only) -- this endpoint refuses to queue a SECOND turn while an earlier
// one is still merely Pending too, not just once it's Dispatched or
// Processing, since a session with zero prior turns would otherwise let
// every concurrent relaunch call see "nothing in flight yet" and each
// insert its own Pending row. It is also an APPLICATION-level enforcement
// of the "exactly one processing per session" invariant the domain turn
// machine and the DB's own partial unique index
// (turns_one_processing_per_session, migration 000005_turns.up.sql)
// already enforce elsewhere -- deliberately checked here, before ever
// inserting, rather than relying on that index to reject the insert: the
// index is scoped to status = 'processing' ONLY, so inserting a new
// 'pending' row while another turn is already Dispatched/Processing would
// NOT violate it at all -- it would silently succeed, creating a turn that
// can never legally dispatch while the OTHER one holds the session's one
// in-flight slot. This check closes that gap up front with a clear 409,
// rather than letting a caller queue something that would otherwise sit
// invisibly stuck.
//
// The check runs INSIDE the same transaction as the insert, serialized by
// SessionStore.GetActorEpochForUpdate's own row-level `SELECT ... FOR
// UPDATE` on the session (the exact same lock sessionactor's own dispatch
// path already uses, actor.go) -- a plain pre-transaction list-then-insert
// would leave a check-then-act race open: two concurrent requests could
// both observe "no in-flight turn" before either commits, both insert a
// Pending row, and neither gets the 409 this handler exists to return.
// Locking the session row first forces a second concurrent request to
// block until the first's transaction commits (or rolls back), so it
// re-reads the turns list only after that outcome is visible.
func CreateTurn(pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, participants *postgres.ParticipantStore, auditLog *postgres.AuditLogStore, registry *sessionactor.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, ok := parseSessionID(w, r)
		if !ok {
			return
		}
		ctx := platform.WithSessionID(r.Context(), sessionID.String())
		r = r.WithContext(ctx)
		logger := platform.Logger(ctx)

		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		// §13.3 row 2: "... prompt ... on own/joined sessions: admin,
		// maintainer, member" (viewer never; admin/maintainer bypass
		// ownership entirely). A plain, pool-scoped read (not WithTx) is
		// enough to resolve ownership here -- mirrors
		// lockSessionForPlanAction/authorizePlanAction's own identical
		// "fetch once for authz" precedent (planapprove.go): sessions.
		// created_by is immutable once set, so there is no meaningful
		// TOCTOU between this read and CreateTurnCore's own separate,
		// LOCKED re-fetch below. 404 here (not found) takes priority over
		// any 403 -- a caller must never learn "you can't prompt this"
		// about a session that doesn't exist at all.
		sessionRow, err := sessions.Get(ctx, sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "session not found")
				return
			}
			logger.Error("httpapi: get session for authorization failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		ownedOrJoined := sessionRow.CreatedBy.Valid && sessionRow.CreatedBy == actorUserID
		if !ownedOrJoined {
			exists, err := participants.Exists(ctx, sessionRow.ID, actorUserID)
			if err != nil {
				logger.Error("httpapi: check participant for authorization failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			ownedOrJoined = exists
		}
		if !authorize(w, r, authz.ActionPromptSession, authz.Resource{OwnedOrJoined: ownedOrJoined}) {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.CreateTurnRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}

		created, cerr := CreateTurnCore(ctx, pool, sessions, turns, auditLog, registry, sessionID, req.Prompt, (*string)(req.ModelId), req.PlanMode, actorUserID)
		if cerr != nil {
			logger.Error("httpapi: create turn failed", "status", cerr.Status, "message", cerr.Message)
			writeError(w, cerr.Status, cerr.Message)
			return
		}

		writeJSON(w, http.StatusCreated, restdtos.CreateTurnResponse{
			Id:     created.ID.String(),
			Status: restdtos.CreateTurnResponseStatus(created.Status),
		})
	}
}

// CreateTurnError carries the exact (status, message) pair a caller of
// CreateTurnCore should surface -- mirrors CreateSessionError's own
// identical purpose (create.go): a distinct type, not a plain error, so
// CreateTurn's own existing tests/messages stay byte-for-byte unchanged
// after this extraction, and so a non-HTTP caller (Slack's view_submission
// handling, internal/adapters/inbound/slack/interactive.go) can inspect
// Status/Message directly the same way internal/adapters/inbound/{slack,
// linear} already do for CreateSessionError.
type CreateTurnError struct {
	Status  int
	Message string
}

func (e *CreateTurnError) Error() string { return e.Message }

// CreateTurnCore is everything CreateTurn's own doc comment above
// describes AFTER decoding the request body -- pure extraction, not a
// behavior change (every existing CreateTurn test in this package's own
// _test.go files passes unchanged): fetch (404), lock (the SAME
// GetActorEpochForUpdate call, closing the identical check-then-act race
// this function's own top doc comment already documents), the hasOpenTurn
// 409 gate, insert, commit, then the SAME fire-and-forget GetOrSpawn+
// EnsureDispatched post-commit sequencing.
//
// Exported (Step 38, "plan mode, cross-channel", §8.1/§13.3) so Slack's
// own "Request changes" modal submission (internal/adapters/inbound/slack/
// interactive.go) can create a real plan_mode=true turn through the EXACT
// SAME path POST .../turns itself uses, rather than a third, duplicated
// turn-creation call site -- mirrors CreateSessionCore's own identical
// cross-package reuse precedent (internal/adapters/inbound/{slack,linear}
// already call that one directly).
//
// actorUserID is Step 39's own addition, for the audit_log row this
// function now writes on the SAME tx as the turn insert (§13.3): a real
// authenticated caller's id from CreateTurn (the REST handler above,
// which ALSO already ran authz.Authorize against this same actor before
// ever reaching this function), or an explicit invalid pgtype.UUID{} for
// Slack's own bot-attributed call -- mirrors CreateSessionOnTx's own
// identical createdBy convention exactly. This parameter carries NO
// authorization meaning here: CreateTurnCore itself still runs no
// Authorize check (that stays the REST handler's own job, precisely so
// Slack's call below can keep its existing, documented bot-attribution
// behavior unchanged -- identity auto-linking, which would let a Slack
// actor resolve to a real user_id and a real role, is explicitly Part
// 2's scope, not this Step's).
func CreateTurnCore(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, auditLog *postgres.AuditLogStore, registry *sessionactor.Registry, sessionID pgtype.UUID, prompt string, modelID *string, planMode bool, actorUserID pgtype.UUID) (sqlcgen.Turn, *CreateTurnError) {
	logger := platform.Logger(ctx)

	if _, err := sessions.Get(ctx, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlcgen.Turn{}, &CreateTurnError{http.StatusNotFound, "session not found"}
		}
		logger.Error("httpapi: get session failed", "error", err)
		return sqlcgen.Turn{}, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("httpapi: begin create-turn tx failed", "error", err)
		return sqlcgen.Turn{}, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}
	// Rollback is a safety net for every return path other than a
	// successful Commit below -- mirrors CreateSession's own identical
	// pattern (create.go).
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the session row before ever reading the turns list below --
	// see this function's own doc comment for why this closes the
	// check-then-act race a plain pre-transaction read would leave
	// open. A concurrent DELETE of the session between the earlier
	// sessions.Get above and this lock is the only way this can miss
	// (vanishingly rare, and 404 is still the right answer either way).
	if _, err := sessions.WithTx(tx).GetActorEpochForUpdate(ctx, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlcgen.Turn{}, &CreateTurnError{http.StatusNotFound, "session not found"}
		}
		logger.Error("httpapi: lock session row failed", "error", err)
		return sqlcgen.Turn{}, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	existingTurns, err := turns.WithTx(tx).ListForSession(ctx, sessionID)
	if err != nil {
		logger.Error("httpapi: list turns failed", "error", err)
		return sqlcgen.Turn{}, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}
	if hasOpenTurn(existingTurns) {
		return sqlcgen.Turn{}, &CreateTurnError{http.StatusConflict, "a turn is already pending, dispatched, or processing for this session"}
	}

	created, err := turns.WithTx(tx).Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusPending,
		Prompt:    &prompt,
		ModelID:   modelID,
		PlanMode:  planMode,
	})
	if err != nil {
		logger.Error("httpapi: create turn failed", "error", err)
		return sqlcgen.Turn{}, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	if err := recordAuditLog(ctx, auditLog.WithTx(tx), actorUserID, "turn.create", "turn", created.ID.String(), map[string]any{
		"session_id": sessionID.String(),
		"plan_mode":  planMode,
	}); err != nil {
		logger.Error("httpapi: record turn.create audit log failed", "error", err)
		return sqlcgen.Turn{}, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("httpapi: commit create-turn tx failed", "error", err)
		return sqlcgen.Turn{}, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	// Fire-and-forget, OUTSIDE the transact above, exactly mirroring
	// CreateSession's own identical post-commit sequencing (create.go) --
	// see that handler's own doc comment for why this never blocks the
	// response on how long the resulting spawn/dispatch decision takes.
	actor, spawnErr := registry.GetOrSpawn(ctx, sessionID)
	if spawnErr != nil {
		logger.Warn("httpapi: GetOrSpawn after turn create failed", "error", spawnErr)
	} else if sendErr := actor.Send(ctx, sessionactor.EnsureDispatched{}); sendErr != nil {
		logger.Warn("httpapi: send EnsureDispatched after turn create failed", "error", sendErr)
	}

	return created, nil
}
