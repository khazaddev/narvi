package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// OpenCodeConfigStore is a thin, pass-through wrapper around the
// sqlc-generated opencode_configs queries ("sandbox secrets &
// opencode config", §27.2, migrations/000091_opencode_configs.up.sql).
// Unlike ProviderCredentialStore/SandboxSecretStore, Document travels as
// PLAINTEXT bytes throughout -- this table holds config, not secret
// material (that table's own top migration comment) -- so, unusually for
// this package's own secret-store precedent, nothing here is ever
// encrypted/decrypted at any layer.
type OpenCodeConfigStore struct {
	q *sqlcgen.Queries
}

// NewOpenCodeConfigStore builds an OpenCodeConfigStore backed by pool.
func NewOpenCodeConfigStore(pool *pgxpool.Pool) *OpenCodeConfigStore {
	return &OpenCodeConfigStore{q: sqlcgen.New(pool)}
}

// UpsertEnvironment creates or replaces the (at most one) opencode_configs
// row for environmentID -- backs PUT /api/environments/{environmentID}/
// opencode-config.
func (s *OpenCodeConfigStore) UpsertEnvironment(ctx context.Context, environmentID string, document []byte) (sqlcgen.OpencodeConfig, error) {
	return s.q.UpsertEnvironmentOpenCodeConfig(ctx, sqlcgen.UpsertEnvironmentOpenCodeConfigParams{
		ScopeTargetID: &environmentID,
		Document:      document,
	})
}

// UpsertGlobal creates or replaces the (at most one, deployment-wide)
// global opencode_configs row -- backs PUT /api/opencode-config.
func (s *OpenCodeConfigStore) UpsertGlobal(ctx context.Context, document []byte) (sqlcgen.OpencodeConfig, error) {
	return s.q.UpsertGlobalOpenCodeConfig(ctx, document)
}

// GetEnvironment fetches environmentID's own row. pgx.ErrNoRows means "not
// configured yet" -- an ordinary, expected state, never an error
// condition.
func (s *OpenCodeConfigStore) GetEnvironment(ctx context.Context, environmentID string) (sqlcgen.OpencodeConfig, error) {
	return s.q.GetEnvironmentOpenCodeConfig(ctx, &environmentID)
}

// GetGlobal fetches the single global row, if any. pgx.ErrNoRows means
// "not configured yet", same convention as GetEnvironment.
func (s *OpenCodeConfigStore) GetGlobal(ctx context.Context) (sqlcgen.OpencodeConfig, error) {
	return s.q.GetGlobalOpenCodeConfig(ctx)
}

// DeleteEnvironment removes environmentID's own row, if any, returning the
// number of rows actually deleted (0 or 1) -- the caller renders 404 on
// 0, matching this package's own established "affected-row-count decides
// the caller's own not-found branch" convention.
func (s *OpenCodeConfigStore) DeleteEnvironment(ctx context.Context, environmentID string) (int64, error) {
	return s.q.DeleteEnvironmentOpenCodeConfig(ctx, &environmentID)
}

// DeleteGlobal removes the single global row, if any -- same
// affected-row-count convention as DeleteEnvironment.
func (s *OpenCodeConfigStore) DeleteGlobal(ctx context.Context) (int64, error) {
	return s.q.DeleteGlobalOpenCodeConfig(ctx)
}

// ListForDelivery fetches the global row (if any) and environmentID's own
// row (if any and if environmentID is non-nil) in one query -- the
// sandbox-facing delivery endpoint's own single read. Returns 0, 1, or 2
// rows; the caller (opencodeconfigdelivery.go) splits the result by its
// own Scope field.
func (s *OpenCodeConfigStore) ListForDelivery(ctx context.Context, environmentID *string) ([]sqlcgen.OpencodeConfig, error) {
	return s.q.ListOpenCodeConfigsForDelivery(ctx, environmentID)
}
