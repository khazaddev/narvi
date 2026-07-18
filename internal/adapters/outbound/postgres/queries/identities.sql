-- Queries backing IdentityStore (§13.2 identity graph,
-- migrations/000003_identities.up.sql + migrations/000017_auth_v1.up.sql's
-- own access_token_encrypted column). CreateIdentity links a fresh user to
-- a provider identity at first-sign-in time.
-- GetIdentityByProviderAndExternalID is the OAuth callback's own
-- returning-vs-first-time-sign-in check. UpdateIdentityAccessToken
-- re-encrypts and stores a fresh provider token on every successful
-- sign-in of an ALREADY-linked identity (GitHub's own classic OAuth tokens
-- don't expire, but re-storing on each login is harmless and keeps the
-- stored token fresh if the user ever re-authorized with different
-- scopes).

-- name: CreateIdentity :one
INSERT INTO identities (user_id, provider, external_id, email, email_verified, linked_via, access_token_encrypted)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetIdentityByProviderAndExternalID :one
SELECT * FROM identities
WHERE provider = $1 AND external_id = $2;

-- name: UpdateIdentityAccessToken :one
UPDATE identities
SET access_token_encrypted = $2
WHERE id = $1
RETURNING *;
