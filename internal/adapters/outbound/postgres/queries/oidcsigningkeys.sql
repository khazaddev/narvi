-- Queries backing OIDCSigningKeyStore ("cloud identity: OIDC
-- issuer, bindings, minting", §27.3) --
-- migrations/000092_oidc_signing_keys.up.sql. private_key_encrypted is
-- ciphertext (platform.EncryptToken output) end to end here; nothing in
-- this file, or the Go store wrapping it, ever decrypts -- that happens
-- exactly once, server-side, in the minting endpoint (internal/adapters/
-- inbound/httpapi/cloudidentitytoken.go), to sign one token with the
-- current active key.

-- name: CreateOIDCSigningKey :one
-- kid is CP-generated (internal/adapters/outbound/oidcsigning), never
-- caller-supplied -- a collision would violate this table's own PRIMARY
-- KEY, which the caller (OIDCSigningKeyStore.Rotate) treats as
-- unreachable in practice given kid's own generation entropy, not
-- specially handled.
INSERT INTO oidc_signing_keys (kid, private_key_encrypted, public_jwk)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetActiveOIDCSigningKey :one
-- The minting endpoint's own "which key signs a brand-new token" read --
-- pgx.ErrNoRows means no key has ever been provisioned (or every key has
-- somehow been retired with none replacing it, which RotateSigningKeys'
-- own atomic retire-then-insert makes unreachable in practice) -- the
-- caller treats either identically: mint fails closed, "no active signing
-- key configured".
SELECT * FROM oidc_signing_keys WHERE retired_at IS NULL;

-- name: ListPublishableOIDCSigningKeys :many
-- The JWKS endpoint's own single read: every key still inside its own
-- publish window -- the active key (retired_at IS NULL) plus any retired
-- key whose own retired_at + the caller's overlap-window argument has not
-- yet elapsed (sqlc.arg('cutoff'), the caller's own already-computed
-- now() - overlapWindow: a row publishes while retired_at > cutoff, i.e.
-- retirement happened more recently than one full overlap window ago).
-- Computing the cutoff in Go (internal/domain/oidckey, pure, given now +
-- the configured overlap window) rather than as SQL arithmetic here keeps
-- every clock read in the adapter layer, matching platform.Clock's own
-- "domain gets values passed in" discipline (§11) even though this query
-- itself lives in the adapter, not domain -- one fewer place a duration
-- literal could sneak into raw SQL.
SELECT * FROM oidc_signing_keys
WHERE retired_at IS NULL OR retired_at > sqlc.arg('cutoff')
ORDER BY created_at;

-- name: RetireOIDCSigningKey :one
-- Marks kid retired as of the caller's own now() -- the FIRST half of a
-- rotation (OIDCSigningKeyStore.Rotate calls this, then
-- CreateOIDCSigningKey, in the SAME transaction). A no-op WHERE clause
-- guard (retired_at IS NULL) makes this idempotent against ever
-- double-retiring an already-retired key, though the caller's own
-- transaction already prevents that in practice.
UPDATE oidc_signing_keys
SET retired_at = sqlc.arg('retired_at')
WHERE kid = sqlc.arg('kid') AND retired_at IS NULL
RETURNING *;
