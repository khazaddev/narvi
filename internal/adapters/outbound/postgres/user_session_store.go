package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// UserSessionStore is a thin, pass-through wrapper around the sqlc-
// generated user_sessions queries (§13.1's own backend-issued cookie
// session mechanism, migrations/000017_auth_v1.up.sql). No caching, no
// retries, no business rules -- minting (generating the plaintext token,
// hashing it, choosing its TTL) and verifying a presented token against a
// stored hash are both internal/adapters/inbound/auth's job; this store
// only ever sees already-hashed values.
type UserSessionStore struct {
	q *sqlcgen.Queries
}

// NewUserSessionStore builds a UserSessionStore backed by pool.
func NewUserSessionStore(pool *pgxpool.Pool) *UserSessionStore {
	return &UserSessionStore{q: sqlcgen.New(pool)}
}

// Create inserts a new user_sessions row and returns it.
func (s *UserSessionStore) Create(ctx context.Context, arg sqlcgen.CreateUserSessionParams) (sqlcgen.UserSession, error) {
	return s.q.CreateUserSession(ctx, arg)
}

// GetByHash fetches a user_sessions row by its (already-hashed)
// token_hash -- the auth middleware's own verify-by-hash lookup on every
// gated request. Same "hash the presented token, look it up by its own
// indexed hash column" pattern as WSTokenStore.GetByHash (see that
// method's own doc comment for why this is the correct, standard pattern
// here rather than a fetch-then-constant-time-compare).
func (s *UserSessionStore) GetByHash(ctx context.Context, tokenHash string) (sqlcgen.UserSession, error) {
	return s.q.GetUserSessionByHash(ctx, tokenHash)
}

// Delete removes a user_sessions row by id -- logout's own real
// revocation (a genuine DELETE, not merely clearing the browser's
// cookie).
func (s *UserSessionStore) Delete(ctx context.Context, id pgtype.UUID) error {
	return s.q.DeleteUserSession(ctx, id)
}
