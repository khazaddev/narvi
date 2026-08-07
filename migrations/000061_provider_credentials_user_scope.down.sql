-- Postgres has no ALTER TYPE ... DROP VALUE -- removing an enum value
-- requires recreating the type. Guarded: this down migration is only ever
-- safe on an environment that added the 'user' value but never actually
-- started using it (a local dev/CI rollback) -- it fails loudly rather
-- than silently orphaning a real linked-ChatGPT-account row if one exists.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM provider_credentials WHERE scope = 'user') THEN
        RAISE EXCEPTION 'cannot drop provider_credential_scope value ''user'': rows still reference it';
    END IF;
END $$;

-- provider_credentials_scope_target_id_shape's own compiled CHECK
-- expression embeds a 'global'::provider_credential_scope literal cast
-- against the OLD type object -- verified directly (this exact down
-- migration failed against a real Postgres instance with "operator does
-- not exist: provider_credential_scope = provider_credential_scope_old"
-- before this DROP/recreate was added): a rename does not retroactively
-- re-resolve that cast, so the ALTER COLUMN TYPE below would otherwise
-- try to validate the column's NEW type against the constraint's OLD-type
-- literal. Dropped before the swap, recreated after, byte-for-byte the
-- same constraint migration 000056 originally defined.
ALTER TABLE provider_credentials DROP CONSTRAINT provider_credentials_scope_target_id_shape;

ALTER TYPE provider_credential_scope RENAME TO provider_credential_scope_old;
CREATE TYPE provider_credential_scope AS ENUM ('repo', 'environment', 'global');
ALTER TABLE provider_credentials
    ALTER COLUMN scope TYPE provider_credential_scope
    USING scope::text::provider_credential_scope;
DROP TYPE provider_credential_scope_old;

ALTER TABLE provider_credentials ADD CONSTRAINT provider_credentials_scope_target_id_shape CHECK (
    (scope = 'global' AND scope_target_id IS NULL) OR
    (scope <> 'global' AND scope_target_id IS NOT NULL AND scope_target_id <> '')
);
