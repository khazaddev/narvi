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

		created, _, cerr := CreateTurnCore(ctx, pool, sessions, turns, auditLog, registry, sessionID, req.Prompt, (*string)(req.ModelId), req.PlanMode, actorUserID, RejectIfOpen)
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

// CreateTurnPolicy selects CreateTurnCore's own behavior when sessionID
// already has a non-terminal ("open") turn -- audit-fix batch addition
// (findings H7/L12/L2/M6): before this batch, REST/Slack/Linear/GitHub-bot
// ingress each independently decided (and, for Linear, never even locked
// for) this exact question, in four separately-written, only-partly-
// consistent implementations. One shared enum, consulted by the one
// shared core (createTurnLocked below) that ALL FOUR now call through,
// replaces every one of those copies.
type CreateTurnPolicy int

const (
	// RejectIfOpen refuses to enqueue a second turn while one is already
	// open, returning a 409 CreateTurnError -- the REST relaunch
	// endpoint's own long-standing policy (CreateTurn's own doc comment
	// above). Also used, unchanged, by Slack's "Request changes" modal
	// submission (interactive.go's own handleViewSubmission), which has
	// always gone through this exact function.
	RejectIfOpen CreateTurnPolicy = iota
	// DropIfOpen silently declines to enqueue while a turn is already
	// open: the returned turn is the zero value, wasCreated is false, and
	// cerr is nil -- there is no 409 to surface here, only a caller-
	// rendered "still busy" response. Slack's addTurn (turn.go) and
	// Linear's handlePrompted ordinary-reply path (webhook.go) both use
	// this (M6 audit fix: before this batch each swallowed the same case
	// inconsistently -- Slack posted a false "I'll pick this up next"
	// promise that nothing ever fulfilled, Linear said nothing back to
	// the user at all).
	DropIfOpen
	// AlwaysQueue skips the open-turn check entirely, unconditionally
	// enqueuing -- CreateTurnForBot's own fixed policy (bot.go),
	// preserving GitHub's deliberate per-PR mention-coalescing Pending
	// backlog (see that function's own doc comment for why this is NOT a
	// general-purpose policy any other caller should reach for).
	AlwaysQueue
)

// CreateTurnCore is everything CreateTurn's own doc comment above
// describes AFTER decoding the request body: fetch (404), lock (the SAME
// GetActorEpochForUpdate call, closing the identical check-then-act race
// this function's own top doc comment already documents), then delegates
// the rest (the policy-gated open-turn check, insert, audit, commit,
// dispatch) to createTurnLocked below.
//
// Exported (Step 38, "plan mode, cross-channel", §8.1/§13.3) so Slack's
// own "Request changes" modal submission (internal/adapters/inbound/slack/
// interactive.go) can create a real plan_mode=true turn through the EXACT
// SAME path POST .../turns itself uses, rather than a third, duplicated
// turn-creation call site -- mirrors CreateSessionCore's own identical
// cross-package reuse precedent (internal/adapters/inbound/{slack,linear}
// already call that one directly).
//
// policy (audit-fix batch addition -- see CreateTurnPolicy's own doc
// comment) is now the ONE remaining difference between this function's
// current callers: REST's own CreateTurn above (RejectIfOpen), Slack's
// addTurn (turn.go, DropIfOpen) and its "Request changes" modal submission
// (interactive.go, RejectIfOpen -- unchanged), and Linear's handlePrompted
// ordinary-reply path (webhook.go, DropIfOpen). Every OTHER step this
// function performs is now identical for all of them -- closing L2
// (Linear's own previous unlocked, raced Turns.Create), H7 (Slack/Linear
// never wrote this turn's own audit_log row at all), and L12 (hasOpenTurn-
// shaped logic copy-pasted, not shared, across three packages) all at
// once. GitHub's bot-ingress path (CreateTurnForBot, bot.go) reuses the
// SAME createTurnLocked core with AlwaysQueue, but deliberately WITHOUT
// this function's own pre-transaction existence check below -- see that
// function's own doc comment for why.
//
// actorUserID is Step 39's own addition, for the audit_log row
// createTurnLocked writes on the SAME tx as the turn insert (§13.3): a
// real authenticated caller's id from CreateTurn (the REST handler above,
// which ALSO already ran authz.Authorize against this same actor before
// ever reaching this function), or an explicit invalid pgtype.UUID{} for
// a still-unresolved bot-attributed caller (Slack/Linear, when the
// mentioning/replying identity hasn't been auto-linked) -- mirrors
// CreateSessionOnTx's own identical createdBy convention exactly. This
// parameter carries NO authorization meaning here: CreateTurnCore itself
// still runs no Authorize check (that stays each caller's own job,
// precisely so a still-unlinked actor's call can keep its existing,
// documented bot-attribution behavior unchanged).
func CreateTurnCore(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, auditLog *postgres.AuditLogStore, registry *sessionactor.Registry, sessionID pgtype.UUID, prompt string, modelID *string, planMode bool, actorUserID pgtype.UUID, policy CreateTurnPolicy) (sqlcgen.Turn, bool, *CreateTurnError) {
	logger := platform.Logger(ctx)

	if _, err := sessions.Get(ctx, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusNotFound, "session not found"}
		}
		logger.Error("httpapi: get session failed", "error", err)
		return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	return createTurnLocked(ctx, pool, sessions, turns, auditLog, registry, sessionID, prompt, modelID, planMode, actorUserID, policy)
}

// createTurnLocked is the genuinely shared core every one of this batch's
// four (now-consolidated) call sites eventually runs through -- factored
// out of CreateTurnCore specifically so CreateTurnForBot (bot.go) can
// reuse it WITHOUT CreateTurnCore's own pre-transaction sessions.Get
// existence check above: CreateTurnForBot has never had that pre-check
// (its own doc comment), and preserving that exact, deliberate difference
// is what "unchanged behavior" for GitHub's own ingress means here.
//
// Sequencing: lock the session row (GetActorEpochForUpdate -- the SAME
// race-closing lock CreateTurn's own top doc comment documents; this is
// what closes L2 for Linear's own caller, which previously took no lock
// at all before its own equivalent check), the policy-gated open-turn
// check (skipped entirely for AlwaysQueue -- see CreateTurnPolicy's own
// doc comment), insert, the SAME turn.create audit_log row every caller
// now gets (H7 audit fix), commit, then the SAME fire-and-forget
// GetOrSpawn+EnsureDispatched post-commit sequencing every turn-creation
// call site in this codebase has always used.
func createTurnLocked(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, auditLog *postgres.AuditLogStore, registry *sessionactor.Registry, sessionID pgtype.UUID, prompt string, modelID *string, planMode bool, actorUserID pgtype.UUID, policy CreateTurnPolicy) (sqlcgen.Turn, bool, *CreateTurnError) {
	logger := platform.Logger(ctx)

	tx, err := pool.Begin(ctx)
	if err != nil {
		logger.Error("httpapi: begin create-turn tx failed", "error", err)
		return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}
	// Rollback is a safety net for every return path other than a
	// successful Commit below -- mirrors CreateSession's own identical
	// pattern (create.go).
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the session row before ever reading the turns list below --
	// see this function's own doc comment for why this closes the
	// check-then-act race a plain pre-transaction read would leave
	// open. A concurrent DELETE of the session between an earlier
	// existence check (CreateTurnCore's own sessions.Get, when the caller
	// went through that wrapper) and this lock is the only way this can
	// miss (vanishingly rare, and 404 is still the right answer either
	// way).
	if _, err := sessions.WithTx(tx).GetActorEpochForUpdate(ctx, sessionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusNotFound, "session not found"}
		}
		logger.Error("httpapi: lock session row failed", "error", err)
		return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	if policy != AlwaysQueue {
		existingTurns, err := turns.WithTx(tx).ListForSession(ctx, sessionID)
		if err != nil {
			logger.Error("httpapi: list turns failed", "error", err)
			return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusInternalServerError, "internal error"}
		}
		if hasOpenTurn(existingTurns) {
			if policy == DropIfOpen {
				return sqlcgen.Turn{}, false, nil
			}
			return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusConflict, "a turn is already pending, dispatched, or processing for this session"}
		}
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
		return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	if err := recordAuditLog(ctx, auditLog.WithTx(tx), actorUserID, "turn.create", "turn", created.ID.String(), map[string]any{
		"session_id": sessionID.String(),
		"plan_mode":  planMode,
	}); err != nil {
		logger.Error("httpapi: record turn.create audit log failed", "error", err)
		return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	if err := tx.Commit(ctx); err != nil {
		logger.Error("httpapi: commit create-turn tx failed", "error", err)
		return sqlcgen.Turn{}, false, &CreateTurnError{http.StatusInternalServerError, "internal error"}
	}

	// Fire-and-forget, OUTSIDE the transaction above, exactly mirroring
	// CreateSession's own identical post-commit sequencing (create.go) --
	// see that handler's own doc comment for why this never blocks the
	// response on how long the resulting spawn/dispatch decision takes.
	actor, spawnErr := registry.GetOrSpawn(ctx, sessionID)
	if spawnErr != nil {
		logger.Warn("httpapi: GetOrSpawn after turn create failed", "error", spawnErr)
	} else if sendErr := actor.Send(ctx, sessionactor.EnsureDispatched{}); sendErr != nil {
		logger.Warn("httpapi: send EnsureDispatched after turn create failed", "error", sendErr)
	}

	return created, true, nil
}
