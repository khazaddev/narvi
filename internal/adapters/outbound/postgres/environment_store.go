package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// EnvironmentStore is a thin, pass-through wrapper around the sqlc-generated
// environments query (§14.1, migrations/000021_environments.up.sql). No
// caching, no retries, no business rules. httpapi.CreateSession is its only
// caller today -- an environments row is created INLINE, at session-creation
// time, only when the request carries a non-empty pathScope; there is no
// standalone create/list/update Environment endpoint (see this migration's
// own doc comment for the scope decision).
type EnvironmentStore struct {
	q *sqlcgen.Queries
}

// NewEnvironmentStore builds an EnvironmentStore backed by pool.
func NewEnvironmentStore(pool *pgxpool.Pool) *EnvironmentStore {
	return &EnvironmentStore{q: sqlcgen.New(pool)}
}

// WithTx returns an EnvironmentStore whose queries run on tx instead of the
// pool this store was built with -- used by httpapi.CreateSession, which
// needs the new environments row and the session row it back-references to
// commit in the SAME transaction (matching every other store's own WithTx
// convention in this package).
func (s *EnvironmentStore) WithTx(tx pgx.Tx) *EnvironmentStore {
	return &EnvironmentStore{q: s.q.WithTx(tx)}
}

// Create inserts a new environments row and returns it. pathScope is the
// caller-supplied pathScope, already marshaled to JSON and already
// validated by internal/domain/environment.ValidatePathScope -- this method
// performs no validation of its own.
func (s *EnvironmentStore) Create(ctx context.Context, pathScope []byte) (sqlcgen.Environment, error) {
	return s.q.CreateEnvironment(ctx, pathScope)
}
