-- Queries backing WSTokenStore (§4.3, §6.2's ws-token mint/verify
-- mechanism, migrations/000016_ws_tokens.up.sql). CreateWSToken mints a
-- fresh row -- the plaintext token itself, and hashing it, are the
-- caller's own job (internal/platform.GenerateToken / HashToken), this
-- query only ever sees the already-hashed value. GetWSTokenByHash is the
-- client hub's own verify-by-hash lookup at subscribe time (§6.2).

-- name: CreateWSToken :one
INSERT INTO ws_tokens (session_id, user_id, token_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetWSTokenByHash :one
SELECT * FROM ws_tokens
WHERE token_hash = $1;
