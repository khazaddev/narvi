-- Upload lifecycle columns for artifacts (Step 58, §28.4): the artifacts
-- table has carried the 'upload' artifact_type value since
-- migrations/000012_artifacts.up.sql with no producer -- this migration
-- adds the columns Step 58's mint/confirm/abandonment-sweep lifecycle
-- needs. Existing pr/preview rows take the 'ready' DEFAULT -- they were
-- only ever recorded after the fact (§5.1's second-copy principle: no
-- pending phase ever existed for them), so 'ready' is what they always
-- were. Postgres applies a NOT NULL DEFAULT to existing rows as a
-- metadata-only operation (no table rewrite), so this is safe on a
-- populated table.
--
-- Type naming follows this schema's own established <table>_<column>
-- convention for enum types (artifact_type/type from 000012,
-- automation_trigger_type/trigger_type from 000055): artifact_status for
-- the status column, artifact_failure_reason for failure_reason.
CREATE TYPE artifact_status AS ENUM ('pending', 'ready', 'failed');
CREATE TYPE artifact_failure_reason AS ENUM ('size_exceeded', 'quota_exceeded', 'verification_failed', 'abandoned');

ALTER TABLE artifacts ADD COLUMN status artifact_status NOT NULL DEFAULT 'ready';
ALTER TABLE artifacts ADD COLUMN failure_reason artifact_failure_reason;
ALTER TABLE artifacts ADD COLUMN blob_key TEXT;
ALTER TABLE artifacts ADD COLUMN size_bytes BIGINT;
ALTER TABLE artifacts ADD COLUMN content_type TEXT;
ALTER TABLE artifacts ADD COLUMN filename TEXT;
-- NULL created_by means agent-produced (§17.5's no-human-actor allowance,
-- also already this table's own convention for sessions.created_by) --
-- never a sentinel/system user row.
ALTER TABLE artifacts ADD COLUMN created_by UUID REFERENCES users(id);

-- Serves the abandonment sweep's own query shape directly (WHERE status =
-- 'pending' AND created_at < cutoff) -- mirrors
-- automations_cron_trigger_idx's own partial-index precedent
-- (migrations/000055_automations_triggers_and_extras.up.sql).
CREATE INDEX artifacts_pending_created_at_idx ON artifacts (created_at) WHERE status = 'pending';
