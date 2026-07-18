-- user_sessions: the backend-issued, revocable, hash-verified bearer
-- credential backing the human/browser login session (§13.1: "Sessions:
-- backend-issued, host-scoped cookies... Token/refresh handling lives in
-- the Go control plane; the SPA holds no provider tokens"). Mirrors
-- ws_tokens' own shape/reasoning exactly (migrations/000016_ws_tokens.up.sql)
-- -- a real DB-backed session, not a stateless signed cookie, so that
-- internal/adapters/inbound/auth's own logout handler can actually revoke
-- it (a real DELETE), not merely ask the browser to forget the cookie.
CREATE TABLE user_sessions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fast verify-by-hash lookup: internal/adapters/inbound/auth's own
-- Middleware and logout handler look a presented token up by
-- platform.HashToken(token), never by id -- same pattern as
-- ws_tokens_token_hash_idx.
CREATE INDEX user_sessions_token_hash_idx ON user_sessions (token_hash);

-- identities.access_token_encrypted: AES-256-GCM ciphertext (a 12-byte
-- random nonce prepended, internal/platform.EncryptToken/DecryptToken) of
-- the provider's own OAuth access token (§13.1: "Provider tokens encrypted
-- at rest (AES-GCM), per-user"). Nullable -- only github-provider rows
-- populate it in this Step; slack/linear/google rows have no
-- token-consuming flow yet. Nothing in this Step ever DECRYPTS this column
-- outside its own round-trip test -- Step 21's SourceControl adapter
-- ("createPR, credential minting", §8.11) is the actual consumer; this
-- Step only obtains and stores it.
ALTER TABLE identities ADD COLUMN access_token_encrypted BYTEA;
