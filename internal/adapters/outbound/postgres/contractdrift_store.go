package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ContractDriftStore is a thin, pass-through wrapper around the sqlc-
// generated contract_drift_snapshots queries ("mocking + contract
// drift", §14.3, migrations/000025_mock_config_contract_drift.up.sql). No
// caching, no retries, no business rules -- HasDrifted's own truth table
// lives in internal/domain/contractdrift, the spawn-time read/best-effort-
// upsert lives in app/sessionactor/contractdrift.go's own checkContractDrift.
type ContractDriftStore struct {
	q *sqlcgen.Queries
}

// NewContractDriftStore builds a ContractDriftStore backed by pool.
func NewContractDriftStore(pool *pgxpool.Pool) *ContractDriftStore {
	return &ContractDriftStore{q: sqlcgen.New(pool)}
}

// Get fetches the contract_drift_snapshots row for repoKey, surfacing
// pgx.ErrNoRows unwrapped (no wrapping/translation) so callers can
// errors.Is(err, pgx.ErrNoRows) exactly like ImageBuildStore.Get's own
// identical precedent -- no row yet means no snapshot has ever been
// recorded for this repo (checkContractDrift's own "first sighting"
// case).
func (s *ContractDriftStore) Get(ctx context.Context, repoKey string) (sqlcgen.ContractDriftSnapshot, error) {
	return s.q.GetContractDriftSnapshot(ctx, repoKey)
}

// Upsert records repoKey's latest (repoSHA, contractsFingerprint) pair,
// creating a fresh row or overwriting the existing one -- see
// UpsertContractDriftSnapshot's own generated doc comment (ON CONFLICT DO
// UPDATE).
func (s *ContractDriftStore) Upsert(ctx context.Context, repoKey, repoSHA, contractsFingerprint string) error {
	return s.q.UpsertContractDriftSnapshot(ctx, sqlcgen.UpsertContractDriftSnapshotParams{
		RepoKey:                  repoKey,
		LastRepoSha:              repoSHA,
		LastContractsFingerprint: contractsFingerprint,
	})
}
