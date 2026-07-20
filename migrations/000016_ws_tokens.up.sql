-- ws_tokens: per-participant WS auth tokens minted via REST (§6.2: "WS
-- token: per-participant, hashed at rest, 24h TTL, minted via REST
-- (/api/sessions/:id/ws-token)"). Only token_hash is ever persisted -- the
-- plaintext token is returned to the caller exactly once, at mint time
-- (internal/platform.GenerateToken), never stored (§6.2's "hashed at rest"
-- taken literally).
--
-- user_id is nullable -- ON DELETE SET NULL if the referenced user is ever
-- removed, and NULL for any row minted before Step 20 ("auth v1") wired
-- real user identity. Since Step 20, MintWSToken (internal/adapters/
-- inbound/httpapi/wstoken.go) always scopes a freshly minted token to the
-- REAL authenticated caller (platform.UserFromContext) -- this mirrors
-- sandboxes.token_hash's own (Step 18, migrations/000015_sandbox_token_hash.
-- up.sql) "nullable, unused until a later Step" precedent, now resolved the
-- same way: the column already supported a real value, and the mint
-- endpoint now always supplies one.
CREATE TABLE ws_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id  UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
    token_hash  TEXT NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Fast verify-by-hash lookup: the client hub's own subscribe-time
-- verification (internal/adapters/inbound/wshub/client.go) looks a
-- presented token up by HashToken(token), never by id.
CREATE INDEX ws_tokens_token_hash_idx ON ws_tokens (token_hash);
