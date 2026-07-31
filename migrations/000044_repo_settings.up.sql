-- repo_settings: a small, extensible table of admin-configured, per-repo
-- policy flags for the code-review domain (§8.2/Step 47, §21.2). The
-- ONLY column landing in this Step is block_on_high_risk (Step 47's own
-- "an admin, per-repo, strict-boolean setting" that reuses the SAME
-- formal-review submission path §17's future sentinel-auto-fix toggle,
-- §21's future per-repo auto-merge toggle, and §24's future per-repo
-- automatic-re-review opt-in toggle are all expected to join later, ADDED
-- as further columns on this SAME table rather than one bespoke table per
-- toggle -- every one of those is likewise "an admin, per-repo, strict
-- boolean" (§13.3's own row 6), so this table is deliberately named/shaped
-- to be that shared home from day one, not re-derived per Step.
--
-- repo_full_name is the natural key -- the SAME "owner/repo" shape
-- github_pr_sessions.repo_full_name already uses (migrations/
-- 000028_github_pr_sessions.up.sql) -- a repo has settings independent of
-- any one PR/session, so this is its own table, not a column bolted onto
-- github_pr_sessions.
--
-- A row's ABSENCE means every flag defaults to its own safe value (here:
-- block_on_high_risk = false) -- callers read this table with a
-- fail-closed default on a missing row or a transient read error (mirrors
-- §24.5's own "if the setting cannot be read... treated as OFF" precedent
-- for a comparable per-repo policy flag), never invent an implicit
-- opposite default.
CREATE TABLE repo_settings (
    repo_full_name     TEXT NOT NULL PRIMARY KEY,
    block_on_high_risk BOOLEAN NOT NULL DEFAULT false,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
