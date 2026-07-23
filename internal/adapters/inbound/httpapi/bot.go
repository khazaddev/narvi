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
	"github.com/khazaddev/narvi/internal/platform"
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
func CreateSessionForBot(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, environments *postgres.EnvironmentStore, registry *sessionactor.Registry, req restdtos.CreateSessionRequest) (sqlcgen.Session, error) {
	created, cerr := CreateSessionCore(ctx, pool, sessions, turns, environments, registry, req, pgtype.UUID{})
	if cerr != nil {
		return sqlcgen.Session{}, cerr
	}
	return created, nil
}

// CreateTurnForBot enqueues a new Pending turn on an EXISTING session for
// a non-browser ingress caller (Steps 32/33/34) living in its own
// package. Mirrors CreateTurn's own lock-then-insert-then-dispatch
// sequencing (turn.go: a session-row FOR UPDATE lock via
// GetActorEpochForUpdate -- so a concurrent CreateTurn REST call and a
// concurrent bot-ingress turn enqueue on the SAME session still serialize
// against each other correctly -- then insert, commit, GetOrSpawn +
// Send(EnsureDispatched{})).
//
// Deliberately DOES NOT apply CreateTurn's own hasOpenTurn 409 gate: that
// gate is a REST-endpoint-specific policy against a human queuing more
// than one relaunch at a time (turn.go's own doc comment) -- it is not a
// general domain invariant. domain/turn.NextToDispatch already supports
// an arbitrary backlog of Pending turns on one session, dispatching the
// oldest one only once nothing is Dispatched/Processing -- exactly the
// backlog Step 32's own per-PR @mention coalescing is meant to produce
// when N concurrent mentions land on a PR that already has a review
// session (internal/adapters/inbound/github/coalesce.go is this
// function's own caller for that case).
func CreateTurnForBot(ctx context.Context, pool *pgxpool.Pool, sessions *postgres.SessionStore, turns *postgres.TurnStore, registry *sessionactor.Registry, sessionID pgtype.UUID, prompt string, modelID *string, planMode bool) (sqlcgen.Turn, error) {
	logger := platform.Logger(ctx)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return sqlcgen.Turn{}, fmt.Errorf("httpapi: begin CreateTurnForBot tx: %w", err)
	}
	// Rollback is a safety net for every return path other than a
	// successful Commit below -- mirrors CreateTurn/CreateSession's own
	// identical pattern.
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the session row before ever inserting -- see CreateTurn's own
	// doc comment (turn.go) for why this closes the check-then-act race a
	// plain unlocked insert would leave open. This is the SAME row-locking
	// TECHNIQUE (not the same row) internal/adapters/inbound/github's own
	// per-PR coalescing claim (github_pr_sessions) uses for its own,
	// separate serialization need.
	if _, err := sessions.WithTx(tx).GetActorEpochForUpdate(ctx, sessionID); err != nil {
		return sqlcgen.Turn{}, fmt.Errorf("httpapi: lock session row for CreateTurnForBot: %w", err)
	}

	created, err := turns.WithTx(tx).Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusPending,
		Prompt:    &prompt,
		ModelID:   modelID,
		PlanMode:  planMode,
	})
	if err != nil {
		return sqlcgen.Turn{}, fmt.Errorf("httpapi: create turn for CreateTurnForBot: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return sqlcgen.Turn{}, fmt.Errorf("httpapi: commit CreateTurnForBot tx: %w", err)
	}

	// Fire-and-forget, OUTSIDE the transaction above -- mirrors
	// CreateSession/CreateTurn's own identical post-commit sequencing (see
	// either's own doc comment for why this never blocks on how long the
	// resulting spawn/dispatch decision takes).
	actor, spawnErr := registry.GetOrSpawn(ctx, sessionID)
	if spawnErr != nil {
		logger.Warn("httpapi: GetOrSpawn after CreateTurnForBot failed", "error", spawnErr)
	} else if sendErr := actor.Send(ctx, sessionactor.EnsureDispatched{}); sendErr != nil {
		logger.Warn("httpapi: send EnsureDispatched after CreateTurnForBot failed", "error", sendErr)
	}

	return created, nil
}
