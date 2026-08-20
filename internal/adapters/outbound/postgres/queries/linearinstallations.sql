-- Queries backing LinearInstallationStore ("Linear ingress",
-- §8.10's own "OAuth" scope -- migrations/000031_linear_installations.
-- up.sql's own doc comment has the full "why this table, why keyed by
-- organization_id" writeup).

-- name: UpsertLinearInstallation :one
-- Installing (or RE-installing/re-authorizing, e.g. after a scope change
-- or manual revoke-and-reconnect) the SAME workspace simply replaces its
-- token pair in place -- there is exactly one live installation per
-- organization_id, never a history of past ones.
INSERT INTO linear_installations (
    organization_id, app_user_id, access_token_encrypted,
    refresh_token_encrypted, expires_at, connected_by_user_id
) VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (organization_id) DO UPDATE SET
    app_user_id              = excluded.app_user_id,
    access_token_encrypted   = excluded.access_token_encrypted,
    refresh_token_encrypted  = excluded.refresh_token_encrypted,
    expires_at               = excluded.expires_at,
    connected_by_user_id     = excluded.connected_by_user_id,
    updated_at               = now()
RETURNING *;

-- name: GetLinearInstallationByOrganizationID :one
-- The outbound AgentActivity call's own lookup: which access token (if
-- any) can post back to this Linear workspace? A pgx.ErrNoRows result
-- means no admin has connected this workspace yet -- the caller skips the
-- outbound call entirely rather than failing the whole webhook.
SELECT * FROM linear_installations
WHERE organization_id = $1;

-- name: UpdateLinearInstallationToken :one
-- Refreshes a stored token pair after a real refresh_token exchange
-- (Linear's own access tokens are short-lived, §-confirmed during this
-- Step's investigation: "valid for 24 hours"), WITHOUT touching
-- app_user_id/connected_by_user_id -- a refresh never changes which
-- workspace or which app-user this installation is.
UPDATE linear_installations
SET access_token_encrypted  = $2,
    refresh_token_encrypted = $3,
    expires_at              = $4,
    updated_at               = now()
WHERE organization_id = $1
RETURNING *;
