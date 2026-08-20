-- Queries backing IdentityLinkPromptStore (§13.2's own "pending links"
-- table, migrations/000036_identity_link_prompts.up.sql -- see that
-- migration's own doc comment for the full column-naming reasoning,
-- especially why this is nonce_HASH, not the spec's own literal "nonce").
--
-- This is §13.2's ("identities + full RBAC") own auto-linking half: the
-- storage shape that migration built now gets its first real reader/
-- writer, internal/app/identitylink.Resolve (creates a row when a fetched
-- profile email matches zero or multiple users) and internal/adapters/
-- inbound/identitylink's magic-link consume handler (looks one up by
-- nonce hash when a user clicks the link).

-- name: CreateIdentityLinkPrompt :one
INSERT INTO identity_link_prompts (provider, external_id, nonce_hash, expires_at)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetIdentityLinkPromptByNonceHash :one
-- The magic-link consume handler's own lookup -- a miss (pgx.ErrNoRows)
-- means the presented nonce is wrong, already consumed (DeleteIdentityLinkPrompt
-- already ran), or never existed; that handler does not distinguish those
-- three from each other in its own response, mirroring auth.Middleware's
-- own identical "no differential signal to a prober" precedent.
SELECT * FROM identity_link_prompts
WHERE nonce_hash = $1;

-- name: GetLatestLinkPromptForProviderAndExternalID :one
-- The auto-link algorithm's own re-entry check (§13.2 step 4's own "zero
-- or multiple matches" branch, on a SECOND unresolved event for the same
-- still-unlinked provider identity): the most recently created prompt for
-- (provider, external_id), if any, so Resolve can decide whether a
-- still-live one already exists before minting and sending a brand new
-- magic link on every single inbound message (this table's own migration
-- comment explicitly leaves that "resend" policy as this algorithm's own
-- design decision to make -- see internal/app/identitylink's own doc.go
-- for the concrete choice: reuse a still-unexpired one, mint a fresh one
-- once the latest has expired).
SELECT * FROM identity_link_prompts
WHERE provider = $1 AND external_id = $2
ORDER BY created_at DESC
LIMIT 1;

-- name: DeleteIdentityLinkPrompt :exec
DELETE FROM identity_link_prompts
WHERE id = $1;

-- name: ListLinkPrompts :many
-- Backs the members API's own overview endpoint (§13.2/§13.3, §13.2's
-- own "linked identities incl. pending-link state" requirement) -- every
-- row currently in this table IS still pending (there is no soft-delete;
-- a consumed/superseded prompt is row-deleted, see
-- DeleteLinkPromptsForProviderAndExternalID below), including already-
-- expired ones -- the caller decides how to render an expired row (e.g.
-- a future Phase 7 UI might show "expired" rather than hide it outright);
-- this query itself makes no such judgment call.
SELECT * FROM identity_link_prompts
ORDER BY created_at DESC;

-- name: DeleteLinkPromptsForProviderAndExternalID :exec
-- Cleanup once (provider, external_id) is genuinely linked (by the
-- magic-link click itself, by a LATER auto-link resolving the same
-- still-unlinked identity, or by an admin force-link) -- every OTHER
-- still-pending prompt for that same identity (there can be more than
-- one, see this file's own migration's "no UNIQUE constraint" note) is
-- deleted too, so a stale magic link minted before the identity was
-- linked can never be clicked again afterward.
DELETE FROM identity_link_prompts
WHERE provider = $1 AND external_id = $2;
