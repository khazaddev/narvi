DROP INDEX IF EXISTS chatgpt_link_attempts_user_id_idx;
DROP TABLE IF EXISTS chatgpt_link_attempts;

ALTER TABLE provider_credentials DROP CONSTRAINT IF EXISTS provider_credentials_kind_oauth_shape;
ALTER TABLE provider_credentials DROP COLUMN IF EXISTS oauth_needs_relink;
ALTER TABLE provider_credentials DROP COLUMN IF EXISTS oauth_expires_at;
ALTER TABLE provider_credentials DROP COLUMN IF EXISTS kind;
DROP TYPE IF EXISTS provider_credential_kind;
