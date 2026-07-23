-- linear_installations: one row per Linear WORKSPACE (organization) that
-- has installed Narvi's Linear app (Step 34, "Linear ingress", §8.10's own
-- "OAuth" scope). Deliberately keyed by organization_id, NOT user_id --
-- this is NOT §13.1/§13.2's per-user identity-linking OAuth (already
-- covered by the pre-existing identities table + Step 20's GitHub login
-- flow); it is a separate, workspace-level "can the control plane call
-- Linear's own API on this workspace's behalf" connection, confirmed
-- against Linear's real developer docs during this Step's investigation:
-- app authentication there is "built on top of the standard OAuth2 flow"
-- but adds an `actor=app` authorization-url parameter specifically to
-- "switch to an app installation" at workspace scope (requires a workspace
-- admin to complete), issuing one access/refresh token pair per
-- workspace -- never a second way for a human to log into Narvi itself.
--
-- access_token_encrypted/refresh_token_encrypted: AES-256-GCM ciphertext
-- (internal/platform.EncryptToken/DecryptToken, the SAME primitive and key
-- migrations/000017_auth_v1.up.sql's own identities.access_token_encrypted
-- column already uses) of Linear's own OAuth token pair. refresh_token is
-- nullable purely defensively (every real exchange response is expected to
-- carry one per Linear's own OAuth2 docs, but nothing here assumes a
-- token this code didn't itself validate).
--
-- app_user_id is the workspace-scoped id Linear's own `query { viewer { id
-- } }` returns for this installation (Linear's docs: "Your app will have a
-- unique ID for each workspace it is installed within... we highly
-- recommend storing this ID alongside your access token") -- distinct
-- from organization_id (the workspace itself) and from any Narvi user id.
--
-- connected_by_user_id is the Narvi user who completed the install-time
-- OAuth callback (an audit/attribution convenience only -- nothing in
-- this Step's own scope reads it back); ON DELETE SET NULL rather than
-- CASCADE, since deleting that user's own Narvi account must never
-- silently revoke a whole workspace's Linear connection.
CREATE TABLE linear_installations (
    organization_id         TEXT PRIMARY KEY,
    app_user_id             TEXT NOT NULL,
    access_token_encrypted  BYTEA NOT NULL,
    refresh_token_encrypted BYTEA,
    expires_at              TIMESTAMPTZ NOT NULL,
    connected_by_user_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);
