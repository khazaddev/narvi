-- Step 59 ("models", §29.4): extends provider_credentials with the second
-- half of the ChatGPT-account-OAuth storage design (the 'user' scope value
-- itself landed separately, migrations/000061, for the ALTER TYPE ... ADD
-- VALUE same-transaction restriction explained there). This migration adds
-- everything else: the kind discriminator, the oauth-specific columns, and
-- the short-lived link-attempt nonce table (§29.3) -- none of these
-- statements reference the literal 'user' scope value, so they are safe to
-- land together in one migration, mirroring §29.4's own "Schema changes
-- (one migration)" framing for the non-enum-value parts of this design.
--
-- kind is a real Postgres ENUM (not a boolean), matching provider_
-- credential_provider/scope's own precedent for a small, closed,
-- code-controlled vocabulary (migrations/000056's own doc comment) --
-- 'api_key' is the DEFAULT so every existing row (all of them, today)
-- keeps its exact current meaning unchanged.
CREATE TYPE provider_credential_kind AS ENUM ('api_key', 'oauth');
ALTER TABLE provider_credentials ADD COLUMN kind provider_credential_kind NOT NULL DEFAULT 'api_key';

-- oauth_expires_at is a PLAINTEXT mirror of the encrypted value_encrypted
-- blob's own expires_ms field (for an oauth-kind row, value_encrypted
-- holds EncryptToken ciphertext of {access, refresh, expires_ms,
-- account_id} -- one blob, rewritten atomically on every refresh, never
-- four separately-encrypted columns) -- so the refresh pump (§29.5) can
-- index-scan for expiring rows without decrypting anything. An expiry
-- timestamp is not itself a secret: user_sessions.expires_at/
-- identity_link_prompts.expires_at are already plaintext columns in this
-- same codebase.
--
-- oauth_needs_relink is the refresh pump's own terminal-failure marker
-- (§29.5: a terminal invalid_grant/refresh_token_reused sets this true and
-- the row stops being served), surfaced on the Settings card as
-- "reconnect your ChatGPT account".
--
-- The CHECK constraint is ONE-DIRECTIONAL, exactly as §29.4 states it:
-- "A CHECK constraint keeps both NULL/false for api_key rows" -- an
-- api_key row (kind <> 'oauth') must have oauth_expires_at NULL and
-- oauth_needs_relink false; an oauth row is unconstrained by this check
-- (both columns are legitimately NULL/false for the brief window between
-- an oauth row's own INSERT and its first token write, though in practice
-- the link flow, app/chatgptlink, always writes them together).
ALTER TABLE provider_credentials ADD COLUMN oauth_expires_at TIMESTAMPTZ;
ALTER TABLE provider_credentials ADD COLUMN oauth_needs_relink BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE provider_credentials ADD CONSTRAINT provider_credentials_kind_oauth_shape CHECK (
    kind = 'oauth' OR (oauth_expires_at IS NULL AND oauth_needs_relink = false)
);

-- chatgpt_link_attempts: the device-flow link attempt's own short-lived
-- nonce row (§29.3), the same shape precedent as identity_link_prompts
-- (migrations/000036_identity_link_prompts.up.sql: nonce/expiry, no
-- background sweep -- expiry enforced lazily at read/poll time, resend
-- policy lives in app code, not a UNIQUE constraint) adapted for THIS
-- flow's own fields: user_id (this is a direct per-user link, never a
-- provider+external-id match the way identity_link_prompts is), plus
-- device_auth_id/user_code (returned by POST .../deviceauth/usercode) and
-- last_polled_at (throttles the Settings page's own poll loop to at most
-- one upstream deviceauth/token attempt per server-provided interval,
-- §29.3 point 2 -- the human sitting on the Settings page IS the polling
-- loop; there is no background goroutine for this table to leak from).
--
-- Deliberately NO UNIQUE constraint on user_id, mirroring identity_link_
-- prompts' own documented choice: "relink replaces the row (same upsert)"
-- (§29.3) is app-layer policy (internal/app/chatgptlink), not a DB
-- constraint -- an expired-but-unresolved attempt is simply superseded by
-- a fresh one, never deleted out from under a concurrent poll.
CREATE TABLE chatgpt_link_attempts (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    device_auth_id TEXT NOT NULL,
    user_code      TEXT NOT NULL,
    last_polled_at TIMESTAMPTZ,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Lookup index for "does this user already have a pending attempt"
-- (createOrReuseLinkPrompt's own exact query shape, mirrored) and for the
-- GET/POST/DELETE /api/me/chatgpt-link handlers, all scoped by the
-- authenticated user.
CREATE INDEX chatgpt_link_attempts_user_id_idx ON chatgpt_link_attempts (user_id);
