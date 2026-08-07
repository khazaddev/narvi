-- Queries backing ProviderCredentialStore (Step 53, "provider credential
-- injection", §25.1/§25.3) -- migrations/000056_provider_credentials.up.sql.
-- value_encrypted is ciphertext (platform.EncryptToken output) end to
-- end here; nothing in this file, or the Go store wrapping it, ever
-- decrypts -- that happens exactly once, server-side, in the sandbox-
-- facing delivery endpoint (internal/adapters/inbound/httpapi/
-- providercredentialsdelivery.go), only for the ONE row Resolve picked as
-- the winner.

-- name: CreateProviderCredential :one
-- scope_target_id is sqlc.narg: NULL for a global-scoped row (the CHECK
-- constraint provider_credentials_scope_target_id_shape enforces it can
-- ONLY be NULL for scope='global'), non-NULL otherwise. A duplicate
-- (scope, scope_target_id, provider) violates one of this table's own two
-- partial unique indexes -- the caller (httpapi.CreateProviderCredential)
-- maps that Postgres error into a 409 Conflict, never retried here.
INSERT INTO provider_credentials (scope, scope_target_id, provider, value_encrypted)
VALUES ($1, sqlc.narg('scope_target_id'), $2, $3)
RETURNING *;

-- name: GetProviderCredential :one
SELECT * FROM provider_credentials WHERE id = $1;

-- name: ListProviderCredentialsByScope :many
-- Lists every row at exactly one (scope, scope_target_id) pair -- e.g.
-- every provider configured for ONE repo, or every provider configured
-- for ONE environment, or (scope='global', scope_target_id NULL) every
-- global-scoped provider. scope_target_id is sqlc.narg (NULL for the
-- global case) with an explicit IS NOT DISTINCT FROM comparison, since a
-- plain "=" never matches NULL = NULL.
SELECT * FROM provider_credentials
WHERE scope = $1 AND scope_target_id IS NOT DISTINCT FROM sqlc.narg('scope_target_id')
ORDER BY provider;

-- name: UpdateProviderCredentialValue :one
-- Rotates ONLY the encrypted value -- scope/scope_target_id/provider are
-- immutable once created (see providercredentials.go's own doc comment
-- for why: changing what a row scopes to is modeled as delete-then-create,
-- never an in-place identity change).
UPDATE provider_credentials
SET value_encrypted = $2, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteProviderCredential :execrows
DELETE FROM provider_credentials WHERE id = $1;

-- name: UpsertOAuthProviderCredential :one
-- Step 59's own ChatGPT-account-OAuth link/relink flow (§29.3/§29.4),
-- internal/app/chatgptlink. Always scope='user', kind='oauth' -- the ONLY
-- creating path for either value (§29.4: "v1 creates user-scope rows ONLY
-- via the link flow"). The ON CONFLICT arbiter matches provider_
-- credentials_scoped_uniq's own exact partial-index shape (migrations/
-- 000056_provider_credentials.up.sql) -- "relink replaces the row (same
-- upsert)" (§29.3): a second link for the same user+provider overwrites
-- the stored ciphertext/expiry and clears any prior oauth_needs_relink,
-- never leaving a stale duplicate row behind.
INSERT INTO provider_credentials (scope, scope_target_id, provider, kind, value_encrypted, oauth_expires_at, oauth_needs_relink)
VALUES ('user', $1, $2, 'oauth', $3, $4, false)
ON CONFLICT (scope, scope_target_id, provider) WHERE scope_target_id IS NOT NULL
DO UPDATE SET value_encrypted = EXCLUDED.value_encrypted, oauth_expires_at = EXCLUDED.oauth_expires_at, oauth_needs_relink = false, updated_at = now()
RETURNING *;

-- name: GetOAuthProviderCredentialForUser :one
-- Backs GET /api/me/chatgpt-link's own "already linked?" check and DELETE
-- /api/me/chatgpt-link's unlink lookup (§29.3/§29.9) -- pgx.ErrNoRows means
-- "not linked", the caller's own ordinary not-found branch, never a
-- distinguished error.
SELECT * FROM provider_credentials
WHERE scope = 'user' AND scope_target_id = $1 AND provider = $2 AND kind = 'oauth';

-- name: DeleteOAuthProviderCredentialForUser :execrows
-- Unlink (§29.3's own "unlink deletes it"). :execrows so the caller can
-- distinguish "actually deleted a row" from "nothing to unlink" without a
-- separate SELECT.
DELETE FROM provider_credentials
WHERE scope = 'user' AND scope_target_id = $1 AND provider = $2 AND kind = 'oauth';

-- name: ListExpiringOAuthProviderCredentials :many
-- The refresh pump's own claim query (§29.5): every oauth row not already
-- marked oauth_needs_relink, expiring within margin of now -- ordered
-- soonest-first, FOR UPDATE SKIP LOCKED so a concurrent pump tick (this
-- pod's own next tick, racing in from another pod) claims a DISJOINT batch
-- rather than double-refreshing the same row (§29.5: "N concurrent
-- sandboxes... exactly the case OpenAI's docs prohibit" -- the identical
-- concurrency hazard applies to two pump instances racing each other, not
-- only to sandboxes). expiresBefore is the caller's own now()+margin;
-- limit mirrors outboxworker's own pumpBatchSize precedent.
SELECT * FROM provider_credentials
WHERE kind = 'oauth' AND oauth_needs_relink = false AND oauth_expires_at < $1
ORDER BY oauth_expires_at
FOR UPDATE SKIP LOCKED
LIMIT $2;

-- name: UpdateOAuthProviderCredentialTokens :one
-- The refresh pump's own success path: atomically rewrites value_encrypted
-- (the rotated {access, refresh, expires_ms, account_id} blob) and its
-- plaintext oauth_expires_at mirror together -- never one without the
-- other, so the two can never drift out of sync.
UPDATE provider_credentials
SET value_encrypted = $2, oauth_expires_at = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: MarkProviderCredentialNeedsRelink :one
-- The refresh pump's own terminal-failure path (§29.5: invalid_grant/
-- refresh_token_reused) -- the row stops being served by the delivery
-- endpoint's own resolution (providercredentialsdelivery.go filters
-- winners; a needs-relink row is left exactly as it was found, still
-- present so its own oauth_needs_relink flag can drive the Settings
-- "reconnect your ChatGPT account" card) until a fresh link (Upsert
-- above) clears it back to false.
UPDATE provider_credentials
SET oauth_needs_relink = true, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: ListProviderCredentialsForResolution :many
-- The delivery endpoint's own single, session-scoped read: every
-- candidate row (across ALL 4 scopes, for EVERY provider at once) that
-- could possibly apply to one session -- the global row for each
-- provider (always included), any repo-scoped row for one of this
-- session's own repo_full_names, the environment-scoped row for this
-- session's own environment_id (if any), and -- Step 59, §29.4's own
-- "resolution keys on sessions.created_by" rule -- the user-scoped row for
-- the session's own creator (if any). repo_full_names may be an empty
-- array (matches nothing, never an error); environment_id/user_id are
-- both sqlc.narg, NULL when the session has none (matches nothing --
-- scope_target_id is NEVER NULL for an environment- or user-scoped row,
-- so "IS NULL" correctly excludes every row of that scope when the caller
-- has none at all, rather than needing a separate branch). userID is
-- passed as NULL for a bot/automation session (sessions.created_by IS
-- NULL, migration 000004's own comment) -- it then simply contributes no
-- user candidate, falling through to the static-key scopes exactly as
-- today (§29.4's own named, accepted "creator's seat" consequence). The
-- caller (providercredentialsdelivery.go) groups the result by provider
-- and runs internal/domain/providercredential.Resolve over each group.
--
-- The trailing AND (kind <> 'oauth' OR oauth_needs_relink = false) is
-- Step 59's own addition (§29.5: "a terminal refresh failure ... the row
-- stops being served"): a needs-relink oauth row is excluded from the
-- candidate set entirely, so Resolve simply never sees it -- exactly as
-- if no credential were configured for that provider at that scope,
-- falling through to whatever OTHER scope might still resolve. A no-op
-- for every api_key row (oauth_needs_relink is NEVER true for kind <>
-- 'oauth', enforced by provider_credentials_kind_oauth_shape), so this
-- changes nothing for Step 53's own existing static-key resolution.
SELECT * FROM provider_credentials
WHERE (scope = 'global'
   OR (scope = 'repo' AND scope_target_id = ANY(sqlc.arg('repo_full_names')::text[]))
   OR (scope = 'environment' AND scope_target_id IS NOT NULL AND scope_target_id = sqlc.narg('environment_id'))
   OR (scope = 'user' AND scope_target_id IS NOT NULL AND scope_target_id = sqlc.narg('user_id')))
  AND (kind <> 'oauth' OR oauth_needs_relink = false)
ORDER BY provider;
