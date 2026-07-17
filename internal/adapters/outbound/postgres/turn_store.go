package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TurnStore is a thin, pass-through wrapper around the sqlc-generated turn
// queries (§4.3 TurnStore). No caching, no retries, no business rules —
// that lives in domain/turn (PR-08) and app/sessionactor (PR-11+).
type TurnStore struct {
	q *sqlcgen.Queries
}

// NewTurnStore builds a TurnStore backed by pool.
func NewTurnStore(pool *pgxpool.Pool) *TurnStore {
	return &TurnStore{q: sqlcgen.New(pool)}
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
