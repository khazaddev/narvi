-- Queries backing UserSessionStore (§13.1's own backend-issued cookie
-- session mechanism, migrations/000017_auth_v1.up.sql). CreateUserSession
-- mints a fresh row -- the plaintext token itself, and hashing it, are the
-- caller's own job (internal/platform.GenerateToken / HashToken, reused
-- unchanged from Step 19's ws-token mechanism), this query only ever sees
-- the already-hashed value. GetUserSessionByHash is the auth middleware's
-- own verify-by-hash lookup on every gated request.
-- DeleteUserSession is logout's own real revocation -- a genuine DELETE,
-- not merely clearing the browser's cookie.
-- DeleteExpiredUserSessions backs RunExpiredTokenCleanup's own periodic
-- sweep (expiredcleanup.go, audit-remediation config/platform-hardening
-- batch): expires_at is otherwise only ever checked at read/verify time,
-- so nothing else ever purges a row past its own TTL.

-- name: CreateUserSession :one
INSERT INTO user_sessions (user_id, token_hash, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserSessionByHash :one
SELECT * FROM user_sessions
WHERE token_hash = $1;

-- name: DeleteUserSession :exec
DELETE FROM user_sessions
WHERE id = $1;

-- name: DeleteExpiredUserSessions :execrows
DELETE FROM user_sessions
WHERE expires_at < now();
