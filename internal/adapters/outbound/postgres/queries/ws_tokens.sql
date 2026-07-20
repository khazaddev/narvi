-- Queries backing WSTokenStore (§4.3, §6.2's ws-token mint/verify
-- mechanism, migrations/000016_ws_tokens.up.sql). CreateWSToken mints a
-- fresh row -- the plaintext token itself, and hashing it, are the
-- caller's own job (internal/platform.GenerateToken / HashToken), this
-- query only ever sees the already-hashed value. GetWSTokenByHash is the
-- client hub's own verify-by-hash lookup at subscribe time (§6.2).
-- DeleteExpiredWSTokens backs RunExpiredTokenCleanup's own periodic sweep
-- (expiredcleanup.go, audit-remediation config/platform-hardening batch):
-- expires_at is otherwise only ever checked at read/verify time, so
-- nothing else ever purges a row past its own TTL.

-- name: CreateWSToken :one
INSERT INTO ws_tokens (session_id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetWSTokenByHash :one
SELECT * FROM ws_tokens
WHERE token_hash = $1;

-- name: DeleteExpiredWSTokens :execrows
DELETE FROM ws_tokens
WHERE expires_at < now();
