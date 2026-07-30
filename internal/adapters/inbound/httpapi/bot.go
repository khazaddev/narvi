package httpapi

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
)

// CreateSessionForBot and CreateTurnForBot (below) are the two small,
// EXPORTED entry points create.go's own Step 31 doc comment anticipated:
// "a future webhook ingress handler (Steps 32-34) calls createSessionCore
// directly with its own already-decoded request and a NULL createdBy --
// never [CreateSession]." That anticipated caller living in the SAME
// package (since createSessionCore stays unexported). Step 32 ("GitHub
// ingress") instead places its handler in its own package,
// internal/adapters/inbound/github, mirroring
// internal/adapters/inbound/httpapi/doc.go's own alternative it left
// open ("or Steps 32-34 decide createSessionCore should be exported
// instead") -- a full export of createSessionCore was judged the wrong
// shape (it would hand a webhook adapter direct access to every REST-only
// concern: repo/pathScope/mockConfig validation error *shapes*, HTTP
// status codes, etc.), so instead these two thin wrappers translate to
// and from plain Go values/errors a non-HTTP caller actually wants.
//
// CreateSessionForBot forwards to CreateSessionCore with an explicit NULL
// creator (pgtype.UUID{}) -- every bot/automation-created session has no
// direct human creator, exactly CreateSessionCore's own doc comment and
// createcore_integration_test.go's own TestCreateSessionCore_NilCreator_*
// tests already establish and cover.
func CreateSessionForBot(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, environments *postgres.EnvironmentStore, auditLog *postgres.AuditLogStore, registry *sessionactor.Registry, req restdtos.CreateSessionRequest) (sqlcgen.Session, error) {
	created, cerr := CreateSessionCore(ctx, pool, sessions, turns, environments, auditLog, registry, req, pgtype.UUID{})
	if cerr != nil {
		return sqlcgen.Session{}, cerr
	}
	return created, nil
}

// CreateTurnForBot enqueues a new Pending turn on an EXISTING session for
// a non-browser ingress caller (Steps 32/33/34) living in its own
// package. Reuses createTurnLocked (turn.go) -- the SAME shared core
// CreateTurnCore itself calls -- with its own fixed AlwaysQueue policy,
// so the lock-then-insert-then-dispatch sequencing (a session-row FOR
// UPDATE lock via GetActorEpochForUpdate -- so a concurrent CreateTurn
// REST call and a concurrent bot-ingress turn enqueue on the SAME session
// still serialize against each other correctly -- then insert, audit,
// commit, GetOrSpawn+Send(EnsureDispatched{})) is no longer a
// hand-duplicated copy of CreateTurn's own logic.
//
// Deliberately calls createTurnLocked directly, NOT CreateTurnCore:
// CreateTurnCore's own pre-transaction sessions.Get existence check is a
// REST-only nicety this function has never had (an unchanged, deliberate
// difference -- an unknown sessionID here still ends up as a 404-shaped
// CreateTurnError from inside createTurnLocked's own locked
// GetActorEpochForUpdate call, just without that one extra round trip
// first).
//
// Deliberately uses AlwaysQueue, NOT CreateTurn's own RejectIfOpen: that
// policy is a REST-endpoint-specific choice against a human queuing more
// than one relaunch at a time (CreateTurnPolicy's own doc comment, turn.go)
// -- it is not a general domain invariant. domain/turn.NextToDispatch
// already supports an arbitrary backlog of Pending turns on one session,
// dispatching the oldest one only once nothing is Dispatched/Processing --
// exactly the backlog Step 32's own per-PR @mention coalescing is meant to
// produce when N concurrent mentions land on a PR that already has a
// review session (internal/adapters/inbound/github/coalesce.go is this
// function's own caller for that case).
//
// actorUserID/auditLog are audit-fix batch additions (closing H7's own
// finding that a GitHub-bot-created turn was invisible in the audit
// trail): this now writes the SAME turn.create audit_log row every other
// createTurnLocked caller gets, inside this SAME transaction. actorUserID
// mirrors CreateTurnCore's own identical convention -- a real, resolved
// commenter's user_id (github/coalesce.go's own actor, when linked) or an
// explicit invalid pgtype.UUID{} for a still bot-attributed commenter;
// carries no authorization meaning here, exactly like every other
// createTurnLocked caller.
//
// plans (Step 37/38 follow-up fix, §8.1) is threaded through to
// createTurnLocked's own awaiting-plan gate exactly like every other
// caller -- see that function's own doc comment (turn.go) for the nil-safe
// "skips the gate" contract this shares with them.
func CreateTurnForBot(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, plans *postgres.PlanStore, auditLog *postgres.AuditLogStore, registry *sessionactor.Registry, sessionID pgtype.UUID, prompt string, modelID *string, planMode bool, actorUserID pgtype.UUID) (sqlcgen.Turn, error) {
	created, _, cerr := createTurnLocked(ctx, pool, sessions, turns, plans, auditLog, registry, sessionID, prompt, modelID, planMode, actorUserID, AlwaysQueue)
	if cerr != nil {
		return sqlcgen.Turn{}, fmt.Errorf("httpapi: create turn for bot: %s", cerr.Message)
	}
	return created, nil
}
