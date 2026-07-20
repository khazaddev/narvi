-- Queries backing the environments table (§14.1, migrations/000021_environments.up.sql).
-- Environment rows are created INLINE by httpapi.CreateSession only, when a
-- caller supplies a non-empty pathScope -- there is no standalone
-- create/list/update Environment endpoint in this codebase (see this
-- migration's own doc comment for the scope decision). This is the only
-- query this table needs today.

-- name: CreateEnvironment :one
-- path_scope is the caller-supplied pathScope, already validated by
-- internal/domain/environment.ValidatePathScope BEFORE this is ever
-- called -- this query performs no validation of its own. mock_configured
-- stays at its column default (false): nothing in this call path attaches
-- a mock_config.
INSERT INTO environments (path_scope)
VALUES ($1)
RETURNING *;
