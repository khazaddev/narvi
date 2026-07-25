-- Queries backing UserStore (§13.2 identity graph anchor, §13.3 RBAC role,
-- migrations/000002_users.up.sql). CreateUser inserts a user row once, at
-- first-sign-in creation time (internal/adapters/inbound/auth's own OAuth
-- callback handler) -- role is set once, at creation, per that handler's
-- own initial-admin logic. GetUserByID backs both the callback handler and
-- the auth middleware's own per-request lookup.
--
-- Step 39 ("identities + full RBAC", §13.2/§13.3) additions --
-- ListUsersOrderedByCreatedAt/UpdateUserRole/GetUserByPrimaryEmail back the
-- members API (internal/adapters/inbound/httpapi/members.go) and the
-- auto-link algorithm's own email-match step
-- (internal/app/identitylink.Resolve): the ONLY writes to an existing
-- user's own row this codebase makes past creation time.

-- name: CreateUser :one
INSERT INTO users (primary_email, display_name, role)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users
WHERE id = $1;

-- name: GetUserByPrimaryEmail :one
-- Case-insensitive: email addresses are conventionally
-- case-insensitive (at least in the local-part-plus-domain sense every
-- real mail provider actually applies), and this is ONLY ever compared
-- against an email another provider (GitHub/Slack/Linear) already
-- independently attests to, never user-supplied free text -- matching
-- lower(email) on both sides is the same convention
-- ListVerifiedIdentityUserIDsByEmail (identities.sql) uses for its own
-- verified-identity-email match, so the two halves of §13.2's own "match
-- against primary_email AND verified identity emails" never disagree on
-- case sensitivity.
SELECT * FROM users
WHERE lower(primary_email) = lower($1);

-- name: ListUsersOrderedByCreatedAt :many
-- Backs GET /api/members -- every user, oldest-first (matches
-- ListSummariesForSession's own "stable, predictable order" precedent
-- elsewhere in this codebase; oldest-first reads naturally as "founding
-- members first").
SELECT * FROM users
ORDER BY created_at ASC;

-- name: UpdateUserRole :one
-- Backs the admin-only role-change endpoint (§13.3's own "members & roles:
-- admin only" row) -- the ONLY column of an existing user's row this
-- codebase ever mutates past creation time.
UPDATE users
SET role = $2
WHERE id = $1
RETURNING *;

-- An audit finding (H8) confirmed UpdateMemberRole had no protection
-- against demoting the last remaining admin: doing so permanently locks
-- the deployment out of every admin-only endpoint, including role
-- management itself, with no recovery path short of direct DB surgery.
-- ListActiveAdminIDsForUpdate is that guard's own query -- FOR UPDATE
-- (not a plain count) so it locks every currently-qualifying row: a
-- SECOND concurrent demotion of a DIFFERENT admin, running this SAME
-- query inside its own transaction, blocks here until the first
-- transaction commits or rolls back, then re-evaluates against the
-- now-current row versions (Postgres' own read-committed row-locking
-- semantics) rather than both transactions reading a stale "2 admins"
-- snapshot and both proceeding.

-- name: ListActiveAdminIDsForUpdate :many
SELECT id FROM users
WHERE role = 'admin' AND disabled = false
FOR UPDATE;
