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
