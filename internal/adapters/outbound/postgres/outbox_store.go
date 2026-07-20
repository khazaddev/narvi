package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
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

// WithTx returns an OutboxStore whose queries run on tx instead of the
// pool this store was built with — mirrors the same WithTx convention
// every other store in this package already follows (e.g. EventStore),
// ready for app/sessionactor's transactional-write helper (§2) once a
// caller starts writing outbox entries inside that transaction; no such
// caller exists yet.
func (s *OutboxStore) WithTx(tx pgx.Tx) *OutboxStore {
	return &OutboxStore{q: s.q.WithTx(tx)}
}

// Create inserts a new outbox entry and returns it.
func (s *OutboxStore) Create(ctx context.Context, arg sqlcgen.CreateOutboxEntryParams) (sqlcgen.Outbox, error) {
	return s.q.CreateOutboxEntry(ctx, arg)
}

// Get fetches an outbox entry by id.
func (s *OutboxStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.Outbox, error) {
	return s.q.GetOutboxEntry(ctx, id)
}
