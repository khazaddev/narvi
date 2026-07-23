// This file (turn.go) adds a turn to an EXISTING session -- the "reply
// within an already-mapped thread" half of doc.go's own thread<->session
// mapping design, and also the final step of the "brand-new thread"
// path once its own session id is resolved (see doc.go's own numbered
// design writeup). Deliberately mirrors internal/adapters/inbound/
// httpapi/turn.go's own CreateTurn precondition/locking exactly (lock
// the session row, refuse a second turn while one is already Pending/
// Dispatched/Processing, dispatch post-commit) minus everything specific
// to being an HTTP handler (no *http.Request/ResponseWriter, no
// parseSessionID/writeError) -- there is no exported, HTTP-independent
// equivalent of that logic to import instead (turn.go's own hasOpenTurn
// and the whole precondition-then-insert sequence are unexported,
// package-private to httpapi), so it is duplicated here at the same
// small scale rather than exporting httpapi internals for one caller.

package slack

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/platform"
)

// hasOpenTurn reports whether ANY turn in turns is non-terminal --
// mirrors httpapi's own identical helper (turn.go) exactly; see that
// function's own doc comment for why this is deliberately stricter than
// turn.HasInFlightTurn (Pending must also block a second insert, not
// just Dispatched/Processing).
func hasOpenTurn(turns []sqlcgen.Turn) bool {
	for _, t := range turns {
		if !turn.IsTerminal(turn.State(t.Status)) {
			return true
		}
	}
	return false
}

// addTurn enqueues a new Pending turn carrying prompt on the EXISTING
// session sessionID, then fires the same post-commit GetOrSpawn+
// Send(EnsureDispatched{}) sequencing CreateSessionCore/CreateTurn both
// already use. created reports whether a turn was actually inserted:
// false (err == nil) means a turn was already open for this session
// (Pending/Dispatched/Processing) and this call was a deliberate no-op --
// the caller's own in-thread ack wording (handler.go) reflects that case
// distinctly ("still working on the previous message") rather than
// silently queuing a second turn behind it.
//
// The locking sequence -- GetActorEpochForUpdate BEFORE ListForSession,
// both inside the SAME transaction -- is httpapi.CreateTurn's own
// documented race-closing precedent verbatim: without it, two concurrent
// calls for the same session could each observe "nothing open" before
// either commits.
func addTurn(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, registry *sessionactor.Registry, sessionID pgtype.UUID, prompt string) (created bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := sessions.WithTx(tx).GetActorEpochForUpdate(ctx, sessionID); err != nil {
		return false, err
	}

	existingTurns, err := turns.WithTx(tx).ListForSession(ctx, sessionID)
	if err != nil {
		return false, err
	}
	if hasOpenTurn(existingTurns) {
		return false, nil
	}

	if _, err := turns.WithTx(tx).Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusPending,
		Prompt:    &prompt,
	}); err != nil {
		return false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}

	// Fire-and-forget, OUTSIDE the transaction above -- mirrors
	// CreateSessionCore/CreateTurn's own identical post-commit
	// sequencing exactly (see either's own doc comment for why this
	// never blocks on how long the resulting spawn/dispatch decision
	// takes). A failure here is logged and swallowed into the return
	// value already committed as created=true: the turn itself is
	// durably persisted regardless of whether dispatch could be kicked
	// off immediately, exactly like both of those callers already treat
	// it (log-and-continue, never surfaced as this call's own error).
	logger := platform.Logger(ctx)
	actor, spawnErr := registry.GetOrSpawn(ctx, sessionID)
	if spawnErr != nil {
		logger.Warn("slack: GetOrSpawn after turn create failed", "error", spawnErr)
	} else if sendErr := actor.Send(ctx, sessionactor.EnsureDispatched{}); sendErr != nil {
		logger.Warn("slack: send EnsureDispatched after turn create failed", "error", sendErr)
	}

	return true, nil
}
