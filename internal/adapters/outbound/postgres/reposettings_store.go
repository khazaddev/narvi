package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// RepoSettingsStore is a thin, pass-through wrapper around the
// sqlc-generated repo_settings queries (§8.2/Step 47, §21.2) -- see
// migrations/000044_repo_settings.up.sql's own doc comment for the "one
// shared table, not one bespoke table per toggle" design this store's own
// narrow surface (today: BlockOnHighRisk alone) is expected to grow
// alongside. No caching, no retries, no business rules -- callers decide
// what a missing row (pgx.ErrNoRows, unwrapped) means for their own
// purposes (internal/adapters/inbound/httpapi/reviewverdict.go treats it,
// and any other read error, as "block_on_high_risk defaults to false",
// mirroring §24.5's own identical per-repo-policy-flag fail-closed
// precedent).
type RepoSettingsStore struct {
	q *sqlcgen.Queries
}

// NewRepoSettingsStore builds a RepoSettingsStore backed by pool.
func NewRepoSettingsStore(pool *pgxpool.Pool) *RepoSettingsStore {
	return &RepoSettingsStore{q: sqlcgen.New(pool)}
}

// Get fetches repoFullName's own settings row. pgx.ErrNoRows (unwrapped)
// means no row exists yet -- every flag on it defaults to its own safe
// value; this is not an error condition for a caller to alarm on.
func (s *RepoSettingsStore) Get(ctx context.Context, repoFullName string) (sqlcgen.RepoSetting, error) {
	return s.q.GetRepoSettings(ctx, repoFullName)
}

// Upsert idempotently creates-or-updates repoFullName's settings row with
// blockOnHighRisk as the new, full current value (never a delta/patch --
// see UpsertRepoSettings' own generated doc comment).
func (s *RepoSettingsStore) Upsert(ctx context.Context, repoFullName string, blockOnHighRisk bool) (sqlcgen.RepoSetting, error) {
	return s.q.UpsertRepoSettings(ctx, sqlcgen.UpsertRepoSettingsParams{
		RepoFullName:    repoFullName,
		BlockOnHighRisk: blockOnHighRisk,
	})
}
