package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TimerStore is a thin, pass-through wrapper around the sqlc-generated
// session_timers queries (§4.3 TimerScheduler, §2 named persistent
// timers). No caching, no retries, no business rules — the timer pump
// lands in PR-11.
type TimerStore struct {
	q *sqlcgen.Queries
}

// NewTimerStore builds a TimerStore backed by pool.
func NewTimerStore(pool *pgxpool.Pool) *TimerStore {
	return &TimerStore{q: sqlcgen.New(pool)}
}

// Upsert arms (or re-arms) a named timer for a session: ON CONFLICT
// (session_id, name) DO UPDATE, per §2 ("each is armed/re-armed
// independently") — never a duplicate row.
func (s *TimerStore) Upsert(ctx context.Context, arg sqlcgen.UpsertSessionTimerParams) (sqlcgen.SessionTimer, error) {
	return s.q.UpsertSessionTimer(ctx, arg)
}

// Get fetches a named timer for a session.
func (s *TimerStore) Get(ctx context.Context, arg sqlcgen.GetSessionTimerParams) (sqlcgen.SessionTimer, error) {
	return s.q.GetSessionTimer(ctx, arg)
}
