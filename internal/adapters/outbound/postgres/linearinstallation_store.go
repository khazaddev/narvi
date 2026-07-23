package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// LinearInstallationStore is a thin, pass-through wrapper around the
// sqlc-generated linear_installations queries (Step 34, "Linear ingress",
// §8.10's own "OAuth" scope -- see migrations/000031_linear_installations.
// up.sql's own doc comment for why this table is keyed by organization_id
// rather than user_id). No caching, no retries, no business rules --
// internal/adapters/inbound/linear's own install-callback and webhook
// handlers are the only callers.
type LinearInstallationStore struct {
	q *sqlcgen.Queries
}

// NewLinearInstallationStore builds a LinearInstallationStore backed by pool.
func NewLinearInstallationStore(pool *pgxpool.Pool) *LinearInstallationStore {
	return &LinearInstallationStore{q: sqlcgen.New(pool)}
}

// WithTx returns a LinearInstallationStore whose queries run on tx instead
// of the pool this store was built with -- same convention as every other
// store in this package.
func (s *LinearInstallationStore) WithTx(tx pgx.Tx) *LinearInstallationStore {
	return &LinearInstallationStore{q: s.q.WithTx(tx)}
}

// Upsert installs (or re-installs/re-authorizes) a workspace's token pair
// -- the OAuth install-callback's own single write.
func (s *LinearInstallationStore) Upsert(ctx context.Context, arg sqlcgen.UpsertLinearInstallationParams) (sqlcgen.LinearInstallation, error) {
	return s.q.UpsertLinearInstallation(ctx, arg)
}

// GetByOrganizationID looks up a workspace's stored installation -- the
// outbound AgentActivity call's own lookup. A pgx.ErrNoRows result means
// no admin has connected this workspace yet.
func (s *LinearInstallationStore) GetByOrganizationID(ctx context.Context, organizationID string) (sqlcgen.LinearInstallation, error) {
	return s.q.GetLinearInstallationByOrganizationID(ctx, organizationID)
}

// UpdateToken refreshes a stored token pair after a real refresh_token
// exchange, without disturbing app_user_id/connected_by_user_id.
func (s *LinearInstallationStore) UpdateToken(ctx context.Context, arg sqlcgen.UpdateLinearInstallationTokenParams) (sqlcgen.LinearInstallation, error) {
	return s.q.UpdateLinearInstallationToken(ctx, arg)
}
