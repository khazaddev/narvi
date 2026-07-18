-- Queries backing UserStore (§13.2 identity graph anchor, §13.3 RBAC role,
-- migrations/000002_users.up.sql). CreateUser inserts a user row once, at
-- first-sign-in creation time (internal/adapters/inbound/auth's own OAuth
-- callback handler) -- role is set once, at creation, per that handler's
-- own initial-admin logic; no Update/List exists yet because nothing
-- changes a user's own row after creation in this Step (role/disabled
-- editing is Step 39's "members API" job). GetUserByID backs both the
-- callback handler and the auth middleware's own per-request lookup.

-- name: CreateUser :one
INSERT INTO users (primary_email, display_name, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1;
