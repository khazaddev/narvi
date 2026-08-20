package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TurnStore is a thin, pass-through wrapper around the sqlc-generated turn
// queries (§4.3 TurnStore). No caching, no retries, no business rules —
// that lives in domain/turn (Step 08) and app/sessionactor (Step 11+).
type TurnStore struct {
	q *sqlcgen.Queries
}

// NewTurnStore builds a TurnStore backed by pool.
func NewTurnStore(pool *pgxpool.Pool) *TurnStore {
	return &TurnStore{q: sqlcgen.New(pool)}
}

// WithTx returns a TurnStore whose queries run on tx instead of the pool
// this store was built with — used by app/sessionactor's transactional-
// write helper (§2).
func (s *TurnStore) WithTx(tx pgx.Tx) *TurnStore {
	return &TurnStore{q: s.q.WithTx(tx)}
}

// Create inserts a new turn row and returns it. The database rejects a
// second concurrent 'processing' turn for the same session via the
// turns_one_processing_per_session partial unique index (§3.3).
func (s *TurnStore) Create(ctx context.Context, arg sqlcgen.CreateTurnParams) (sqlcgen.Turn, error) {
	return s.q.CreateTurn(ctx, arg)
}

// Get fetches a turn by id.
func (s *TurnStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.Turn, error) {
	return s.q.GetTurn(ctx, id)
}

// ListForSession fetches the full turn history for a session, oldest
// first — the input shape domain/session.DeriveStatus requires.
func (s *TurnStore) ListForSession(ctx context.Context, sessionID pgtype.UUID) ([]sqlcgen.Turn, error) {
	return s.q.ListTurnsForSession(ctx, sessionID)
}

// UpdateStatus sets a turn's status, plus dispatched_at/completed_at when
// the caller supplies one (see UpdateTurnStatusParams' generated doc for
// the COALESCE semantics).
func (s *TurnStore) UpdateStatus(ctx context.Context, arg sqlcgen.UpdateTurnStatusParams) (sqlcgen.Turn, error) {
	return s.q.UpdateTurnStatus(ctx, arg)
}

// MarkProgressNotified atomically sets id's own progress_notified_at to
// now, but ONLY if it is still NULL -- an audit-fix batch's own addition
// (finding M16, "completeness") -- see MarkTurnProgressNotified's own
// generated doc comment (sqlcgen/turns.sql.go, sourced from queries/
// turns.sql) for the full race this guards against. Returns the number of
// rows actually updated (0 or 1): 0 means this turn already had its
// progress milestone fired by an earlier call.
func (s *TurnStore) MarkProgressNotified(ctx context.Context, id pgtype.UUID, now pgtype.Timestamptz) (int64, error) {
	return s.q.MarkTurnProgressNotified(ctx, sqlcgen.MarkTurnProgressNotifiedParams{
		ID:                 id,
		ProgressNotifiedAt: now,
	})
}

// GetProcessingTurnForSession fetches sessionID's own currently-live
// (status='processing') turn, if any (§20.2) -- mirrors
// WorkflowStore.GetRunningRunForSession's identical "resolve the caller's
// own live attempt from a session id alone" role, one layer down (a turn,
// not a workflow run). turns_one_processing_per_session (migrations/
// 000005_turns.up.sql) guarantees at most one row can ever match; a caller
// with no processing turn gets pgx.ErrNoRows, exactly like GetTurn's own
// not-found case.
func (s *TurnStore) GetProcessingTurnForSession(ctx context.Context, sessionID pgtype.UUID) (sqlcgen.Turn, error) {
	return s.q.GetProcessingTurnForSession(ctx, sessionID)
}

// SetEpistemicOutcome is the guarded write backing the epistemic-outcome-
// posting endpoint (§20.2) -- mirrors WorkflowStore.
// SetStepRunOutcome's own "guarded UPDATE, observed via :execrows" idiom
// exactly, one status value over (turns.status = 'processing' rather than
// workflow_step_runs.status = 'running'). Returns the number of rows
// actually updated (0 or 1): 0 means id is no longer the live processing
// turn (a race between this endpoint's own GetProcessingTurnForSession
// read and this write -- the turn completed/failed/was cancelled in
// between).
func (s *TurnStore) SetEpistemicOutcome(ctx context.Context, id pgtype.UUID, outcome sqlcgen.TurnEpistemicOutcome) (int64, error) {
	return s.q.SetTurnEpistemicOutcome(ctx, sqlcgen.SetTurnEpistemicOutcomeParams{
		ID:               id,
		EpistemicOutcome: &outcome,
	})
}
