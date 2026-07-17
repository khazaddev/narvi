package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// SessionStore is a thin, pass-through wrapper around the sqlc-generated
// session queries (§4.3 SessionStore). No caching, no retries, no business
// rules — that lives in app/sessionactor (PR-11+).
type SessionStore struct {
	q *sqlcgen.Queries
}

// NewSessionStore builds a SessionStore backed by pool.
func NewSessionStore(pool *pgxpool.Pool) *SessionStore {
	return &SessionStore{q: sqlcgen.New(pool)}
}

// Create inserts a new session row and returns it.
func (s *SessionStore) Create(ctx context.Context, arg sqlcgen.CreateSessionParams) (sqlcgen.Session, error) {
	return s.q.CreateSession(ctx, arg)
}

// Get fetches a session by id.
func (s *SessionStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.Session, error) {
	return s.q.GetSession(ctx, id)
}
