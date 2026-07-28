// This file (turn.go) adds a turn to an EXISTING session -- the "reply
// within an already-mapped thread" half of doc.go's own thread<->session
// mapping design, and also the final step of the "brand-new thread"
// path once its own session id is resolved (see doc.go's own numbered
// design writeup).
//
// Audit-fix batch update (findings H7/L12/L2/M6): addTurn used to be a
// copy-pasted reimplementation of internal/adapters/inbound/httpapi/
// turn.go's own CreateTurn precondition/locking -- its own doc comment
// used to say so explicitly, since no shared, HTTP-independent core
// existed at the time it was written. httpapi.CreateTurnCore's own
// hasOpenTurn 409 gate is now parameterized by a CreateTurnPolicy
// (turn.go), so addTurn below is a thin wrapper over that ONE shared core
// instead: this closes L12 (hasOpenTurn was copy-pasted here, not shared)
// and H7 (this call now writes the SAME turn.create audit_log row every
// other CreateTurnCore caller gets).

package slack

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
)

// addTurn enqueues a new Pending turn carrying prompt on the EXISTING
// session sessionID, via httpapi.CreateTurnCore's own DropIfOpen policy --
// the SAME shared core REST's own CreateTurn, Linear's handlePrompted, and
// Slack's own "Request changes" modal submission (interactive.go) all now
// go through too (see CreateTurnPolicy's own doc comment, httpapi/turn.go).
// created reports whether a turn was actually inserted: false (err == nil)
// means a turn was already open for this session (Pending/Dispatched/
// Processing) and this call was a deliberate no-op, exactly as before this
// batch -- the caller's own in-thread ack wording (handler.go) reflects
// that case distinctly ("still working on the previous message") rather
// than silently queuing a second turn behind it.
//
// actorUserID (audit-fix batch addition) is attributed onto the resulting
// turn.create audit_log row only -- turns itself carries no per-row actor
// column (migrations/000005_turns.up.sql), so this mirrors handleEvent's
// own already-resolved actorUserID, which previously had nowhere at all to
// flow into for a reply on an existing thread.
func addTurn(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, auditLog *postgres.AuditLogStore, registry *sessionactor.Registry, sessionID pgtype.UUID, prompt string, actorUserID pgtype.UUID) (turn sqlcgen.Turn, created bool, err error) {
	turnRow, wasCreated, cerr := httpapi.CreateTurnCore(ctx, pool, sessions, turns, auditLog, registry, sessionID, prompt, nil, false, actorUserID, httpapi.DropIfOpen)
	if cerr != nil {
		return sqlcgen.Turn{}, false, cerr
	}
	return turnRow, wasCreated, nil
}
