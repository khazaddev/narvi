package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// EnvironmentStore is a thin, pass-through wrapper around the sqlc-generated
// environments queries (§14.1, migrations/000021_environments.up.sql,
// migrations/000025_mock_config_contract_drift.up.sql). No caching, no
// retries, no business rules. httpapi.CreateSession is Create's only caller
// today -- an environments row is created INLINE, at session-creation
// time, only when the request carries a non-empty pathScope and/or a
// mockConfig; there is no standalone create/list/update Environment
// endpoint (see migrations/000021_environments.up.sql's own scope
// decision). Get is app/sessionactor/contractdrift.go's own checkContractDrift
// reading a spawn/restore plan's environment_id back.
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

// Create inserts a new environments row and returns it. arg's fields are
// the caller's own already-validated/resolved values: pathScope already
// marshaled to JSON and already validated by internal/domain/environment.
// ValidatePathScope, mockConfigured/contractsPath already resolved from the
// request's own optional mockConfig key (httpapi.CreateSession's own doc
// comment), and dockerRequired/egressPolicyMode/egressPolicyAllowlist
// (§27.5/§27.6) already resolved from the request's own optional
// docker/egressPolicy keys and already validated by internal/domain/
// environment.ValidateEgressPolicy -- this method performs no validation
// of its own.
func (s *EnvironmentStore) Create(ctx context.Context, arg sqlcgen.CreateEnvironmentParams) (sqlcgen.Environment, error) {
	return s.q.CreateEnvironment(ctx, arg)
}

// Get fetches the environments row for id.
func (s *EnvironmentStore) Get(ctx context.Context, id pgtype.UUID) (sqlcgen.Environment, error) {
	return s.q.GetEnvironment(ctx, id)
}

// List returns every environments row, newest-first (§12.2 item 5) -- the
// Settings -> Environments screen's own list data source, and the only way
// a caller discovers a valid environments.id to reuse against any of the
// environment-scoped §27 sub-resources (sandbox-secrets, opencode-config,
// cloud-identity-bindings, cluster-binding).
func (s *EnvironmentStore) List(ctx context.Context) ([]sqlcgen.Environment, error) {
	return s.q.ListEnvironments(ctx)
}
