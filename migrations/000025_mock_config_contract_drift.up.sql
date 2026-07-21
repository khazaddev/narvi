-- Step 27 ("mocking + contract drift", §14.3) extends environments (Step
-- 10/migrations/000021_environments.up.sql) with contracts_path -- the
-- other half of §14.1's own "optional mock_config" (mock_configured itself
-- already exists; this is the one config value it names: where the
-- repo's own contract-driven mock spec lives). Nullable, and ONLY ever
-- non-null when mock_configured is true -- exactly like mock_configured
-- itself, this is set INLINE by httpapi.CreateSession, at session-creation
-- time, whenever the request carries a "mockConfig" key (see
-- contracts/rest/v1/dtos.schema.json's CreateSessionRequest.mockConfig
-- doc comment for the exact absent/null/value semantics); there is still
-- no standalone create/list/update Environment endpoint, per
-- migrations/000021_environments.up.sql's own scope decision, unchanged
-- here.
ALTER TABLE environments ADD COLUMN contracts_path TEXT;

-- contract_drift_snapshots: one row per repo (keyed by "owner/repo", the
-- SAME derivation internal/app/sessionactor/imageresolve.go's own
-- parseOwnerRepo helper already produces from a repo's clone URL --
-- reused as-is by app/sessionactor/contractdrift.go's own checkContractDrift,
-- never re-derived a second way), recording the LAST (repo_sha,
-- contracts_fingerprint) pair observed for that repo, across ALL sessions
-- that name it -- mirroring image_builds' own "no session/environment
-- ever owns a row here" precedent (migrations/000024_image_builds.up.sql):
-- several unrelated mock-configured sessions naming the same repo
-- legitimately share one snapshot row, since drift is a property of the
-- REPO, not of any one session.
--
-- last_contracts_fingerprint uses the empty string "" as an EXPLICIT
-- sentinel meaning "no contracts directory was found at that repo's own
-- configured contracts path, at last_repo_sha" -- a real
-- contractdrift.Fingerprint digest is a SHA-256 hex string and can never
-- itself be empty (internal/domain/contractdrift.Fingerprint's own doc
-- comment: even an empty, existing directory hashes to a fixed non-empty
-- digest), so "" unambiguously distinguishes "directory absent" from
-- "directory present but empty" at the type level, with no extra nullable
-- column needed.
--
-- This table is written ONLY by app/sessionactor/contractdrift.go's own
-- checkContractDrift, for mock_configured Environments only (§14.3's own
-- framing is entirely about the prototyping-with-mocks workflow) -- an
-- ordinary, unscoped session's spawn/restore never reads or writes a row
-- here at all.
CREATE TABLE contract_drift_snapshots (
    repo_key                    TEXT PRIMARY KEY,
    last_repo_sha               TEXT NOT NULL,
    last_contracts_fingerprint  TEXT NOT NULL,
    updated_at                  TIMESTAMPTZ NOT NULL DEFAULT now()
);
