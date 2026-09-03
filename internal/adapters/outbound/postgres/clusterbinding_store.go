package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ClusterBindingStore is a thin, pass-through wrapper around the
// sqlc-generated cluster_bindings queries ("cloud identity:
// sandbox-side consumption + kubeconfig injection", §27.4,
// migrations/000094_cluster_bindings.up.sql). No caching, no retries, no
// business rules -- name/authKind/serverURL/caBundle/params validation
// (internal/domain/clusterbinding.Validate/ValidateParams) lives one layer
// up, in httpapi, mirroring this package's own established "pure
// pass-through" convention for every other store (CloudIdentityBindingStore,
// OpenCodeConfigStore, ...).
type ClusterBindingStore struct {
	q *sqlcgen.Queries
}

// NewClusterBindingStore builds a ClusterBindingStore backed by pool.
func NewClusterBindingStore(pool *pgxpool.Pool) *ClusterBindingStore {
	return &ClusterBindingStore{q: sqlcgen.New(pool)}
}

// Upsert creates or replaces the (at most one) cluster_bindings row for
// environmentID -- backs PUT /api/environments/{environmentID}/
// cluster-binding. serverURL/caBundle are nil for auth_kind='static' (see
// internal/domain/clusterbinding's own doc comment for why that rung
// needs neither).
func (s *ClusterBindingStore) Upsert(ctx context.Context, environmentID, name string, authKind sqlcgen.ClusterBindingAuthKind, serverURL, caBundle *string, params []byte) (sqlcgen.ClusterBinding, error) {
	return s.q.UpsertClusterBinding(ctx, sqlcgen.UpsertClusterBindingParams{
		EnvironmentID: environmentID,
		Name:          name,
		AuthKind:      authKind,
		Params:        params,
		ServerUrl:     serverURL,
		CaBundle:      caBundle,
	})
}

// Get fetches environmentID's own row. pgx.ErrNoRows means "not configured
// yet" -- an ordinary, expected state, never an error condition. Also the
// sandbox-facing delivery endpoint's own single read -- unlike
// OpenCodeConfigStore's separate ListForDelivery, this table has no
// global-scope row to also fetch (§27.4's own "no global fallback"), so
// the management GET and the delivery read are the exact same query.
func (s *ClusterBindingStore) Get(ctx context.Context, environmentID string) (sqlcgen.ClusterBinding, error) {
	return s.q.GetClusterBinding(ctx, environmentID)
}

// Delete removes environmentID's own row, if any, returning the number of
// rows actually deleted (0 or 1) -- the caller renders 404 on 0, matching
// this package's own established "affected-row-count decides the caller's
// own not-found branch" convention.
func (s *ClusterBindingStore) Delete(ctx context.Context, environmentID string) (int64, error) {
	return s.q.DeleteClusterBinding(ctx, environmentID)
}
