package github

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// SessionCoalescer bundles the stores/registry CreateOrJoin needs -- a
// small struct rather than a long positional-parameter list, constructed
// once at wiring time (cmd/control-plane/main.go), mirroring this
// codebase's own construct-once-thread-through convention for every other
// store/registry pair. Environments is only here because
// httpapi.CreateSessionOnTx's own signature requires a
// *postgres.EnvironmentStore argument -- a GitHub-sourced
// restdtos.CreateSessionRequest never sets PathScope or MockConfig
// (handler.go never populates either), so the CreateSessionOnTx call below
// never actually exercises the environment-insert branch for any request
// this package ever hands it; Environments is simply threaded through
// unused on this path.
type SessionCoalescer struct {
	Pool         *pgxpool.Pool
	PRSessions   *postgres.GitHubPRSessionStore
	Sessions     *postgres.SessionStore
	Turns        *postgres.TurnStore
	Environments *postgres.EnvironmentStore
	Registry     *sessionactor.Registry
}

// CreateOrJoin is Step 32's own per-PR coalescing entry point -- see
// doc.go's own "Per-PR coalescing design" section for the full two-step
// atomic-claim sequencing this implements. isNewSession reports which
// branch was taken (true: req was used to create a brand-new review
// session; false: an existing session for this PR was reused and only a
// new turn was enqueued on it) -- callers use it purely for
// logging/observability, never for a different response to GitHub (both
// branches ack 200 identically).
//
// # Connection-pool safety note (why the WINNER path does NOT call
// httpapi.CreateSessionForBot)
//
// This function holds ONE claim-row lock (LockForUpdate below) inside ONE
// transaction (tx) for its own entire winner-path critical section. If
// that critical section called httpapi.CreateSessionForBot -- which opens
// its OWN, separate transaction via *pgxpool.Pool.Begin -- a single
// request would need TWO simultaneous connections out of the SAME pool:
// one held open by tx (this function's own claim transaction) and one
// acquired by CreateSessionForBot's own inner Begin. Under enough
// concurrent @mentions on the SAME PR (enough that every OTHER, losing
// goroutine's own LockForUpdate call has also already acquired a
// connection and is parked waiting on Postgres's own row lock), the pool
// could be fully exhausted by parked losers by the time the winner tries
// to acquire ITS OWN second connection -- a genuine connection-pool
// deadlock (nothing can release a connection until the winner commits,
// and the winner cannot commit until it acquires a second connection that
// will never come). This is NOT hypothetical: pgxpool's default MaxConns
// is a small, fixed number (independent of this request's own
// concurrency), so it is the wrong assumption to lean on "the pool
// probably has enough spare capacity".
//
// The fix: the winner path below calls httpapi.CreateSessionOnTx directly,
// INLINE on the SAME tx/connection the claim lock already holds -- never a
// second connection. CreateSessionOnTx is the shared, exported piece of
// CreateSessionCore's own logic (internal/adapters/inbound/httpapi/
// create.go) that takes an ALREADY-OPEN transaction the caller owns
// entirely, built for exactly this "already holding an unrelated lock on
// my own open transaction" shape -- so this package no longer needs to
// hand-duplicate any repo-validation/session-insert/turn-insert logic of
// its own to get the same never-a-second-connection guarantee.
// httpapi.CreateSessionForBot itself is untouched and still
// exported/tested (bot.go) as a general-purpose, no-coalescing entry
// point for a caller that is NOT simultaneously holding a claim-row lock
// (e.g. a future Slack/Linear ingress path with no per-thread coalescing
// of its own).
//
// The REUSE (loser) path below has no such risk: it commits tx BEFORE
// calling httpapi.CreateTurnForBot, so only ever one connection is open
// at a time there too.
func (c *SessionCoalescer) CreateOrJoin(ctx context.Context, repoFullName string, prNumber int32, req restdtos.CreateSessionRequest) (session sqlcgen.Session, turn sqlcgen.Turn, isNewSession bool, err error) {
	logger := platform.Logger(ctx)

	tx, err := c.Pool.Begin(ctx)
	if err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: begin claim tx: %w", err)
	}
	committed := false
	// Rollback is a safety net for every return path other than a
	// successful Commit below -- mirrors httpapi's own identical pattern
	// (create.go, turn.go, bot.go).
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	txPRSessions := c.PRSessions.WithTx(tx)
	if err := txPRSessions.EnsureRow(ctx, repoFullName, prNumber); err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: ensure claim row: %w", err)
	}

	// Locks the claim row for the rest of THIS transaction -- any
	// concurrent caller's own EnsureRow+LockForUpdate for the SAME
	// (repoFullName, prNumber) blocks here until this transaction commits
	// or rolls back. See migrations/000028_github_pr_sessions.up.sql's own
	// doc comment for the full reasoning.
	existing, err := txPRSessions.LockForUpdate(ctx, repoFullName, prNumber)
	if err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: lock claim row: %w", err)
	}

	if existing.Valid {
		// Reuse case: this PR already has a review session. Nothing to
		// write to the claim row itself -- commit now (releasing the
		// lock, and this transaction's own connection, for whoever, if
		// anyone, is still queued behind it) BEFORE doing the SEPARATE,
		// independent work of enqueuing a new turn on the existing
		// session. Only one connection is ever open at a time on this
		// path.
		if err := tx.Commit(ctx); err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: commit claim tx (reuse path): %w", err)
		}
		committed = true

		var prompt string
		if req.Prompt != nil {
			prompt = *req.Prompt
		}
		createdTurn, err := httpapi.CreateTurnForBot(ctx, c.Pool, c.Sessions, c.Turns, c.Registry, existing, prompt, (*string)(req.ModelId), req.PlanMode)
		if err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: create turn on existing session: %w", err)
		}

		existingSession, err := c.Sessions.Get(ctx, existing)
		if err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: get existing session: %w", err)
		}

		logger.Info("github: coalesced mention onto existing review session",
			"session_id", existingSession.ID, "turn_id", createdTurn.ID, "repo", repoFullName, "pr_number", prNumber)
		return existingSession, createdTurn, false, nil
	}

	// Winner case: still holding the claim row lock (this transaction is
	// uncommitted) -- create the session AND its turn INLINE, on this
	// SAME tx/connection, via the shared httpapi.CreateSessionOnTx (see
	// this function's own "connection-pool safety" doc comment above for
	// why NOT httpapi.CreateSessionForBot here). createdBy is left at its
	// pgtype.UUID zero value (Valid == false, a genuine SQL NULL) --
	// every bot/automation-created session has no direct human creator,
	// exactly CreateSessionOnTx's own documented convention for a nil
	// creator.
	created, hasPrompt, cerr := httpapi.CreateSessionOnTx(ctx, tx, c.Sessions, c.Turns, c.Environments, req, pgtype.UUID{})
	if cerr != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: create session: %w", cerr)
	}

	// A GitHub mention always carries a real comment body -- handler.go
	// always populates req.Prompt -- so hasPrompt is always true on this
	// path in practice; CreateSessionOnTx doesn't hand the inserted turn
	// row back directly, so it's fetched here, still INSIDE this same
	// uncommitted tx (WithTx(tx), not a fresh pool connection) and still
	// holding the claim-row lock -- the only turn that can possibly exist
	// for this brand-new session.ID at this point is the one
	// CreateSessionOnTx just inserted; no concurrent caller can have
	// enqueued a turn of its own onto this session yet, since SetSessionID
	// below (which is what makes this session visible to a concurrent
	// REUSE-path caller at all) hasn't even run yet, let alone committed.
	// Fetching this AFTER commit instead would be a genuine race: a
	// concurrent loser could observe the just-committed session_id and
	// enqueue its own turn before this function's own ListForSession call
	// ran, breaking the "exactly one turn" assumption below under real
	// concurrent load.
	var createdTurn sqlcgen.Turn
	if hasPrompt {
		turnRows, err := c.Turns.WithTx(tx).ListForSession(ctx, created.ID)
		if err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: list turns for new session: %w", err)
		}
		if len(turnRows) != 1 {
			return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: expected exactly one turn for new session, got %d", len(turnRows))
		}
		createdTurn = turnRows[0]
	}

	if err := txPRSessions.SetSessionID(ctx, repoFullName, prNumber, created.ID); err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: set claim session id: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: commit claim tx (winner path): %w", err)
	}
	committed = true

	// Fire-and-forget, OUTSIDE the transaction above, and ONLY if a
	// prompt/turn was actually created -- mirrors every other
	// CreateSessionOnTx caller's own post-commit TriggerDispatch
	// sequencing (create.go's own CreateSessionCore does the same,
	// gated on the same hasPrompt CreateSessionOnTx returned).
	if hasPrompt {
		httpapi.TriggerDispatch(ctx, c.Registry, created.ID)
	}

	logger.Info("github: created new review session for mention",
		"session_id", created.ID, "turn_id", createdTurn.ID, "repo", repoFullName, "pr_number", prNumber)
	return created, createdTurn, true, nil
}
