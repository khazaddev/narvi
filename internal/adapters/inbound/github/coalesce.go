package github

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// SessionCoalescer bundles the stores/registry CreateOrJoin needs -- a
// small struct rather than a long positional-parameter list, constructed
// once at wiring time (cmd/control-plane/main.go), mirroring this
// codebase's own construct-once-thread-through convention for every other
// store/registry pair. No *postgres.EnvironmentStore here on purpose: a
// GitHub-sourced restdtos.CreateSessionRequest never sets PathScope or
// MockConfig (handler.go never populates either), so sessionAndTurnOnTx's
// own trimmed session-creation logic below never needs one.
type SessionCoalescer struct {
	Pool       *pgxpool.Pool
	PRSessions *postgres.GitHubPRSessionStore
	Sessions   *postgres.SessionStore
	Turns      *postgres.TurnStore
	Registry   *sessionactor.Registry
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
// The fix: the winner path below creates the session AND its turn using
// sessionAndTurnOnTx, INLINE on the SAME tx/connection the claim lock
// already holds -- never a second connection. sessionAndTurnOnTx
// deliberately duplicates only the small subset of createSessionCore's
// own logic (internal/adapters/inbound/httpapi/create.go) that a
// GitHub-sourced request ever actually exercises (repo validation +
// session insert + turn insert) -- a GitHub mention never sets
// pathScope/mockConfig, so environment creation is simply never part of
// this path's own behavior to replicate. httpapi.CreateSessionForBot
// itself is untouched and still exported/tested (bot.go) as a
// general-purpose, no-coalescing entry point for a caller that is NOT
// simultaneously holding a claim-row lock (e.g. a future Slack/Linear
// ingress path with no per-thread coalescing of its own).
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
	// SAME tx/connection (see this function's own "connection-pool
	// safety" doc comment above for why NOT httpapi.CreateSessionForBot
	// here).
	created, createdTurn, err := sessionAndTurnOnTx(ctx, tx, c.Sessions, c.Turns, req)
	if err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: create session: %w", err)
	}

	if err := txPRSessions.SetSessionID(ctx, repoFullName, prNumber, created.ID); err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: set claim session id: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, false, fmt.Errorf("github: commit claim tx (winner path): %w", err)
	}
	committed = true

	// Fire-and-forget, OUTSIDE the transaction above -- mirrors
	// createSessionCore/CreateTurnForBot's own identical post-commit
	// sequencing (see either's own doc comment for why this never blocks
	// on how long the resulting spawn/dispatch decision takes).
	actor, spawnErr := c.Registry.GetOrSpawn(ctx, created.ID)
	if spawnErr != nil {
		logger.Warn("github: GetOrSpawn after session create failed", "error", spawnErr)
	} else if sendErr := actor.Send(ctx, sessionactor.EnsureDispatched{}); sendErr != nil {
		logger.Warn("github: send EnsureDispatched after session create failed", "error", sendErr)
	}

	logger.Info("github: created new review session for mention",
		"session_id", created.ID, "turn_id", createdTurn.ID, "repo", repoFullName, "pr_number", prNumber)
	return created, createdTurn, true, nil
}

// sessionAndTurnOnTx creates a session and its (always-present, for a
// real mention) initial turn on tx -- a deliberately trimmed-down subset
// of createSessionCore's own logic (internal/adapters/inbound/httpapi/
// create.go), duplicated here (rather than called through
// httpapi.CreateSessionForBot) so CreateOrJoin's own winner path never
// needs a second, simultaneous pool connection -- see CreateOrJoin's own
// doc comment for the full reasoning. Safe to trim: a GitHub-sourced
// restdtos.CreateSessionRequest never sets PathScope or MockConfig (this
// package's own handler.go never populates either), so the environment-
// creation branch createSessionCore's own fuller logic has is simply
// never reachable behavior for any request this function is ever called
// with -- there is no hidden divergence for the cases this path actually
// serves.
func sessionAndTurnOnTx(ctx context.Context, tx pgx.Tx, sessions *postgres.SessionStore, turns *postgres.TurnStore, req restdtos.CreateSessionRequest) (sqlcgen.Session, sqlcgen.Turn, error) {
	if len(req.Repos) < 1 {
		return sqlcgen.Session{}, sqlcgen.Turn{}, fmt.Errorf("repos must be non-empty")
	}

	// Validate every repo's Name/Url/Branch BEFORE the insert -- the SAME
	// trust-boundary precedent createSessionCore's own doc comment
	// establishes (create.go), stopping at the first failure.
	for i, repo := range req.Repos {
		if err := reposource.ValidateRepoName(repo.Name); err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, fmt.Errorf("repos[%d].name: %w", i, err)
		}
		if err := reposource.ValidateRepoURL(repo.Url); err != nil {
			return sqlcgen.Session{}, sqlcgen.Turn{}, fmt.Errorf("repos[%d].url: %w", i, err)
		}
		if repo.Branch != nil {
			if err := reposource.ValidateBranch(*repo.Branch); err != nil {
				return sqlcgen.Session{}, sqlcgen.Turn{}, fmt.Errorf("repos[%d].branch: %w", i, err)
			}
		}
	}

	reposJSON, err := json.Marshal(req.Repos)
	if err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, fmt.Errorf("marshal repos: %w", err)
	}

	created, err := sessions.WithTx(tx).Create(ctx, sqlcgen.CreateSessionParams{
		Title:       (*string)(req.Title),
		SpawnSource: sqlcgen.SessionSpawnSourceGithub,
		// CreatedBy left at its pgtype.UUID zero value -- Valid == false,
		// a genuine SQL NULL -- every bot/automation-created session has
		// no direct human creator, exactly createSessionCore's own
		// identical convention for a nil creator.
		Repos: reposJSON,
	})
	if err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, fmt.Errorf("create session: %w", err)
	}

	// A GitHub mention always carries a real comment body -- handler.go
	// always populates req.Prompt -- so a turn is unconditionally created,
	// unlike createSessionCore's own conditional hasPrompt branch.
	var prompt string
	if req.Prompt != nil {
		prompt = *req.Prompt
	}
	createdTurn, err := turns.WithTx(tx).Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: created.ID,
		Status:    sqlcgen.TurnStatusPending,
		Prompt:    &prompt,
		ModelID:   (*string)(req.ModelId),
		PlanMode:  req.PlanMode,
	})
	if err != nil {
		return sqlcgen.Session{}, sqlcgen.Turn{}, fmt.Errorf("create turn: %w", err)
	}

	return created, createdTurn, nil
}
