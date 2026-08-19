ALTER TABLE environments DROP COLUMN IF EXISTS egress_policy_allowlist;
ALTER TABLE environments DROP COLUMN IF EXISTS egress_policy_mode;
ALTER TABLE environments DROP COLUMN IF EXISTS docker_required;
