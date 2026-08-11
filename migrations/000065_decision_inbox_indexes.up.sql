-- Step 60 ("decision inbox: read model + API", §16): a READ MODEL over
-- already-existing state -- no new table, no new state machine, no new
-- writer (§16.2's own explicit instruction). The only thing genuinely
-- required here, per this Step's own "at most an index" allowance, is
-- indexing the FOUR new query shapes this Step adds against tables that
-- were never previously queried this way (an earlier revision of this
-- comment claimed "three" and omitted the fourth below -- corrected,
-- §60 review "Also do" batch):
--
--   - ListFailedSessions (queries/sessions.sql) scans sessions WHERE
--     status = 'failed' -- no existing index covers this predicate at
--     all (sessions carries no index on status).
--   - ListDeadLetterOutboxEntries (queries/outbox.sql) scans outbox WHERE
--     status = 'dead_letter' -- the outbox table's only existing indexes
--     are the implicit primary key and (via CountPendingOutboxEntries'
--     own full-table-scan acceptance) none at all on status.
--   - ExistsSentinelFixByFixPRNumber (queries/sentinelfixes.sql, the §17
--     structural exclusion every discovered open PR is checked against)
--     scans sentinel_fixes WHERE repo_full_name = ? AND fix_pr_number = ?
--     -- the table's only existing indexes are its own UNIQUE
--     (repo_full_name, origin_pr_number) and a separate fix_child_
--     session_id partial index (migrations/000047_sentinel_fixes.up.sql)
--     -- NEITHER covers a lookup keyed on fix_pr_number.
--   - GetPRArtifactByURL (queries/artifacts.sql, isPlatformAuthored's own
--     "authored by a platform session" signal -- checked once per
--     discovered candidate PR, up to maxOpenPRsForUser=50 times per
--     inbox load) scans artifacts WHERE type = 'pr' AND url = ? -- the
--     artifacts table carries no index at all beyond its own implicit
--     primary key (migrations/000012_artifacts.up.sql), so this was a
--     full-table scan on a table every session's own createPRBestEffort
--     (pushpr.go) already writes to continuously.
--
-- Every index below is a PARTIAL index (WHERE-scoped to exactly the rows
-- its own query cares about) -- mirrors this codebase's own established
-- precedent throughout (e.g. automations_cron_trigger_idx, migrations/
-- 000055; sessions_parent_session_id_idx, migrations/000045): each stays
-- small (most sessions are not 'failed', most outbox rows are not
-- 'dead_letter', most sentinel_fixes rows have no fix_pr_number yet, most
-- artifacts rows are not type='pr') and never pays index-maintenance
-- cost on every OTHER row's own writes to these frequently-written
-- tables.

CREATE INDEX sessions_failed_idx ON sessions (updated_at DESC) WHERE status = 'failed' AND NOT archived;

CREATE INDEX outbox_dead_letter_idx ON outbox (created_at DESC) WHERE status = 'dead_letter';

CREATE INDEX sentinel_fixes_fix_pr_number_idx ON sentinel_fixes (repo_full_name, fix_pr_number) WHERE fix_pr_number IS NOT NULL;

CREATE INDEX artifacts_pr_url_idx ON artifacts (url) WHERE type = 'pr';
