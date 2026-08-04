-- handoff_sentinel_runs: Step 49's ("handoff-readiness sentinel", §14.4)
-- own idempotency claim -- one row per PR the handoff sentinel has ever
-- ACTED on (posted a comment + synced the "handoff" label). "Running the
-- sentinel twice must not duplicate the label, the comment, or the
-- issue": a redelivered/duplicate invocation for a PR that already has a
-- row here is a no-op by construction (the caller's own
-- INSERT ... ON CONFLICT (repo_full_name, pr_number) DO NOTHING claim,
-- internal/adapters/outbound/postgres/handoffsentinel_store.go).
--
-- Deliberately claimed ONLY when there is something to report (see
-- internal/app/sessionactor/handoffsentinel.go's own top comment) -- a
-- clean scoped-session PR (nothing found) never gets a row here at all:
-- "post nothing" is already idempotent on its own (there is nothing to
-- duplicate), so this table's own job is narrowly "never post the
-- comment/label a second time for a PR that already got one", not a
-- record of every PR this sentinel ever merely EVALUATED.
--
-- (repo_full_name, pr_number) is the natural key -- mirrors sentinel_fixes'
-- own identical (repo_full_name, origin_pr_number) precedent
-- (migrations/000047's own doc comment) for the SAME "at most one of
-- these per PR" requirement.
--
-- session_id is the originating SCOPED session (the one whose own
-- provenance_tag is provenance.ScopedEnvironment and that just created
-- this PR) -- carried purely for traceability/audit, never consulted by
-- the claim itself. ON DELETE CASCADE mirrors sentinel_fixes'
-- origin_review_session_id: a claim row naming a since-deleted session is
-- meaningless on its own.
CREATE TABLE handoff_sentinel_runs (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_full_name TEXT NOT NULL,
    pr_number      INTEGER NOT NULL,
    session_id     UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (repo_full_name, pr_number)
);
