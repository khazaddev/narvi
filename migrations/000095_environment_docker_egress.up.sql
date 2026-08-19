-- Step 74 ("sandbox substrate: docker, egress policy, toolchain", §27.5,
-- §27.6) extends environments (migrations/000021_environments.up.sql) with
-- the two per-Environment substrate flags this Step's own CreateSpec
-- duplicate-with-Validate discipline (ports/createspec.go) carries
-- alongside SESSION_CONFIG: docker_required (§27.5's "docker: required"
-- flag) and the egress_policy pair (§27.6's "egress_policy {mode:
-- open|allowlist, allowlist}"). Both stay their safe, unchanged-behavior
-- defaults (false / NULL / NULL) for every existing environments row and
-- every row created by a caller that never supplies either -- exactly
-- mirroring contracts_path's own nullable, opt-in shape
-- (migrations/000025_mock_config_contract_drift.up.sql).
--
-- egress_policy_mode is a plain, CHECK-constrained TEXT rather than a new
-- Postgres ENUM: this codebase's own precedent for a two-value closed
-- vocabulary that is also validated in Go before it ever reaches a write
-- (internal/domain/environment's own ValidateEgressPolicy) is a CHECK, not
-- always a dedicated ENUM type (e.g. sandbox_secrets.scope IS an ENUM, but
-- that column has FOUR values and is queried/branched on directly in SQL;
-- this one has two, is read back only to reconstruct a
-- SessionConfig.EgressPolicy value in Go, and staying TEXT avoids a second
-- migration the day a third mode is ever added). NULL means "no egress
-- policy attached to this Environment" -- today's unchanged, unrestricted
-- behavior -- distinct from the literal string 'open', which is a
-- customer's own explicit choice recorded the same way mock_configured/
-- contracts_path already distinguish "never configured" from "configured
-- with the default".
--
-- egress_policy_allowlist is JSONB (a plain array of hostname strings),
-- mirroring path_scope's own existing JSONB-array shape exactly -- only
-- ever non-NULL when egress_policy_mode = 'allowlist'; the CHECK below
-- enforces that pairing structurally, in the schema itself, rather than
-- relying solely on application-code discipline. The value stored here is
-- the CUSTOMER's own configured allowlist ONLY -- the non-negotiable
-- floor (CP host + this session's own git hosts, §27.6) is appended
-- fresh, in Go, every time a SessionConfig is assembled from this row
-- (internal/app/sessionactor's own assembleSessionConfig, via
-- internal/domain/environment.AppendAllowlistFloor) -- never persisted
-- into this column, so a later change to the CP's own host, or a
-- session's own repo set, is never stale here.
ALTER TABLE environments ADD COLUMN docker_required BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE environments ADD COLUMN egress_policy_mode TEXT
    CONSTRAINT environments_egress_policy_mode_check
    CHECK (egress_policy_mode IN ('open', 'allowlist'));
ALTER TABLE environments ADD COLUMN egress_policy_allowlist JSONB
    CONSTRAINT environments_egress_policy_allowlist_pairing_check
    CHECK (
        (egress_policy_mode = 'allowlist' AND egress_policy_allowlist IS NOT NULL)
        OR (egress_policy_mode IS DISTINCT FROM 'allowlist' AND egress_policy_allowlist IS NULL)
    );
