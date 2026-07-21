-- Queries backing contract_drift_snapshots (Step 27, "mocking + contract
-- drift", §14.3, migrations/000025_mock_config_contract_drift.up.sql). See
-- that migration's own doc comment for the table's shape and the
-- last_contracts_fingerprint=="" sentinel meaning. Written/read ONLY by
-- app/sessionactor/contractdrift.go's own checkContractDrift, for
-- mock_configured Environments only.

-- name: GetContractDriftSnapshot :one
-- checkContractDrift's own "read the last recorded snapshot for this
-- repo" lookup -- pgx.ErrNoRows means no snapshot has ever been recorded
-- for repo_key yet (contractdrift.HasDrifted's own "first sighting"
-- case), NOT an error the caller need treat specially beyond that.
SELECT * FROM contract_drift_snapshots WHERE repo_key = $1;

-- name: UpsertContractDriftSnapshot :exec
-- checkContractDrift's own best-effort "persist the latest snapshot for
-- next time" write, run regardless of whether drift was detected this
-- round -- ON CONFLICT DO UPDATE so a repo already tracked simply has its
-- (last_repo_sha, last_contracts_fingerprint, updated_at) overwritten with
-- the current round's own resolved values.
INSERT INTO contract_drift_snapshots (repo_key, last_repo_sha, last_contracts_fingerprint)
VALUES ($1, $2, $3)
ON CONFLICT (repo_key) DO UPDATE SET last_repo_sha = $2, last_contracts_fingerprint = $3, updated_at = now();
