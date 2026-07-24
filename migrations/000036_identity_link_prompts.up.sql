-- identity_link_prompts: §13.2's own "pending links" table -- the auto-
-- link algorithm's step 4 ("zero or multiple matches -> never guess:
-- create a link prompt and reply in-channel with a short-lived magic
-- link") persists one row here per unresolved (provider, external_id)
-- pair it has just sent a magic link for, so a later click on that link
-- can look the pending link back up and complete it. reuses the
-- identity_provider enum migrations/000003_identities.up.sql already
-- defines -- provider values here mean the exact same thing they mean on
-- an identities row (github/slack/linear/google), just not YET linked to
-- any user.
--
-- nonce_hash, not the spec's own literal "nonce" column name: this is a
-- bearer secret embedded in the magic-link URL (whoever presents it
-- completes the link, no other check), so it is hashed at rest and looked
-- up by hash, exactly mirroring user_sessions.token_hash/ws_tokens.
-- token_hash's own existing convention in this codebase (migrations/
-- 000016_ws_tokens.up.sql, 000017_auth_v1.up.sql) -- see audit_log's own
-- migration 000013 for the precedent of a documented, deliberate
-- deviation from §13.2's literal column name where an established
-- security convention already answers the question the spec text left
-- implicit. The plaintext nonce itself is never persisted anywhere.
--
-- No UNIQUE constraint on (provider, external_id): the auto-link
-- algorithm's own "resend" behavior (whether a second prompt for the same
-- unresolved identity supersedes the first, or both stay valid until
-- whichever is clicked/expires first) is that algorithm's own design
-- decision to make (Step 39's own identity-auto-linking half, out of
-- THIS migration's scope) -- this table only provides the storage shape
-- §13.2 names, not yet the concurrency policy on top of it.
CREATE TABLE identity_link_prompts (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    provider    identity_provider NOT NULL,
    external_id TEXT NOT NULL,
    nonce_hash  TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fast verify-by-hash lookup when the magic link is clicked -- mirrors
-- ws_tokens_token_hash_idx/user_sessions' own identical index precedent.
CREATE INDEX identity_link_prompts_nonce_hash_idx ON identity_link_prompts (nonce_hash);

-- Fast lookup of any still-pending prompt(s) for a given unresolved
-- identity -- the auto-link algorithm's own step 4 re-entry path (a
-- second event from the same not-yet-linked provider identity, before the
-- first prompt is ever resolved).
CREATE INDEX identity_link_prompts_provider_external_id_idx ON identity_link_prompts (provider, external_id);
