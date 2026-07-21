-- Queries backing the environments table (§14.1, migrations/000021_environments.up.sql,
-- migrations/000025_mock_config_contract_drift.up.sql). Environment rows are
-- created INLINE by httpapi.CreateSession only, when a caller supplies a
-- non-empty pathScope AND/OR a mockConfig -- there is no standalone
-- create/list/update Environment endpoint in this codebase (see
-- migrations/000021_environments.up.sql's own scope decision).

-- name: CreateEnvironment :one
-- path_scope is the caller-supplied pathScope, already validated by
-- internal/domain/environment.ValidatePathScope BEFORE this is ever
-- called -- this query performs no validation of its own. mock_configured/
-- contracts_path (Step 27, "mocking + contract drift") are the caller's
-- own resolved mockConfig presence/path -- mock_configured=false and
-- contracts_path=NULL (the ordinary, unscoped-mock case) when the
-- request's mockConfig key was absent; see httpapi.CreateSession's own
-- doc comment for exactly how these three are resolved from one request.
-- Extended in place, as a single INSERT accepting all three columns,
-- rather than adding a second UPDATE query: environments rows are ALWAYS
-- created inline at session-creation time (this table's own doc comment),
-- never updated afterward, so there is no separate "attach a mock_config
-- later" path for a second query to serve.
INSERT INTO environments (path_scope, mock_configured, contracts_path)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetEnvironment :one
-- app/sessionactor/contractdrift.go's own checkContractDrift reads a
-- spawn/restore plan's environment_id back via this lookup, to check
-- MockConfigured and read ContractsPath.
SELECT * FROM environments
WHERE id = $1;
