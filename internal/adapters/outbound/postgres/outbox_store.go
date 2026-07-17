package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// OutboxStore is a thin, pass-through wrapper around the sqlc-generated
// outbox queries (§4.3 Outbox, §5.1 outbox pattern). No caching, no
// retries, no business rules — the delivery worker lands in PR-35.
type OutboxStore struct {
	q *sqlcgen.Queries
}

// NewOutboxStore builds an OutboxStore backed by pool.
func NewOutboxStore(pool *pgxpool.Pool) *OutboxStore {
	return &OutboxStore{q: sqlcgen.New(pool)}
}

// Create inserts a new outbox entry and returns it.
func (s *OutboxStore) Create(ctx context.Context, arg sqlcgen.CreateOutboxEntryParams) (sqlcgen.Outbox, error) {
	return s.q.CreateOutboxEntry(ctx, arg)
}

// Get fetches an outbox entry by id.
func (s *OutboxStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.Outbox, error) {
	return s.q.GetOutboxEntry(ctx, id)
}
