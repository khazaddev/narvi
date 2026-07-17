package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// EventStore is a thin, pass-through wrapper around the sqlc-generated
// events queries (§4.3, §6.1's append-only per-session event log). No
// caching, no retries, no business rules — appending an event as part of
// a state transition is app/sessionactor's job (Step 11+), always inside
// that transition's own transaction (§2: "state transition + appended
// event + outbox entries commit in ONE Postgres transaction").
type EventStore struct {
	q *sqlcgen.Queries
}

// NewEventStore builds an EventStore backed by pool.
func NewEventStore(pool *pgxpool.Pool) *EventStore {
	return &EventStore{q: sqlcgen.New(pool)}
}

// WithTx returns an EventStore whose queries run on tx instead of the
// pool this store was built with — used by app/sessionactor's
// transactional-write helper (§2).
func (s *EventStore) WithTx(tx pgx.Tx) *EventStore {
	return &EventStore{q: s.q.WithTx(tx)}
}

// Create inserts a new event row and returns it.
func (s *EventStore) Create(ctx context.Context, arg sqlcgen.CreateEventParams) (sqlcgen.Event, error) {
	return s.q.CreateEvent(ctx, arg)
}
