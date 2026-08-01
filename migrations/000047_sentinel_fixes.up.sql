-- sentinel_fixes: the tracking row §17.4's merge-gating webhook looks up
-- by origin PR number, and §17.3's verdict-update step reads to know
-- which fix PR to reference (Step 48, "sentinels + suggestions", §17).
--
-- (repo_full_name, origin_pr_number) is the natural key -- a real GitHub
-- PR can have at most one sentinel-auto-fix in flight at a time (§17.1's
-- own scope: only ONE origin+fix pair is ever registered per PR, never an
-- N-deep chain, §17.6). Claimed via the SAME "INSERT ... ON CONFLICT DO
-- NOTHING then SELECT ... FOR UPDATE" idiom github_pr_sessions
-- (migrations/000028) already establishes for an identical "at most one
-- of these per PR, even under concurrent triggers" requirement -- a
-- second qualifying finding on a PR that already has a fix in flight
-- reuses this SAME row rather than racing a second child session.
--
-- origin_head_branch is captured ONCE, at claim time (read from the
-- origin review session's own sessions.repos JSON, itself set at PR-
-- mention time from the PR's real head branch, internal/adapters/inbound/
-- github/headresolve.go) -- this is the literal value the fix PR's own
-- Base is assigned to (never resolved via resolvePRBaseBranch,
-- internal/app/sessionactor/pushpr.go's own amendment, §17.2) and the
-- value the merge-gating cherry-pick step (§17.4) diffs against.
--
-- status is this row's own small lifecycle: 'pending' (claimed, child
-- session not yet spawned) -> 'spawned' (child session created) ->
-- 'fix_open' (fix PR opened, stack registration attempted) -> 'fix_merged'
-- (merge-gated in, §17.4's four checks all passed) -- terminal, or
-- 'abandoned' (the origin PR closed without merging, §17.5: "the fix PR
-- is simply left open as an ordinary review item -- never silently
-- discarded") -- terminal. There is deliberately NO 'failed' status: a
-- merge-gating check failing (§17.4: "leaves the fix PR as an ordinary
-- needs_review item instead of forcing it through") is not a terminal
-- state of ITS OWN -- the row simply stays 'fix_open' (a human can still
-- merge it normally, and a LATER origin-merge retry -- e.g. CI going
-- green -- can still succeed later); audit_log (§17.5) is where a failed
-- check's own detail is recorded, not this row's status.
--
-- stack_registered is a plain observability flag -- §17.6's own text is
-- explicit that the AUTHORITATIVE answer to "did registration actually
-- stick" is always a FRESH GetPullRequest call's own Stack field, never a
-- locally-persisted boolean (see internal/adapters/outbound/githubapi.
-- Adapter.GetPullRequest, which already decodes it) -- this column exists
-- only so an audit/observability view can show "did Narvi's own POST
-- .../stacks call return success" without a second live API round trip
-- just to render that.
CREATE TABLE sentinel_fixes (
    id                       UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_full_name           TEXT NOT NULL,
    origin_pr_number         INTEGER NOT NULL,
    origin_review_session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    origin_head_branch       TEXT NOT NULL,

    fix_child_session_id UUID REFERENCES sessions(id) ON DELETE SET NULL,
    fix_pr_number         INTEGER,

    status            TEXT NOT NULL DEFAULT 'pending',
    stack_registered  BOOLEAN NOT NULL DEFAULT false,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    UNIQUE (repo_full_name, origin_pr_number)
);

-- The reverse (fix session id -> tracking row) lookup pushpr.go's own
-- createSentinelFixPRBestEffort needs, once the fix session's OWN
-- push_complete event arrives -- mirrors github_pr_sessions_session_id_idx
-- (migrations/000032) exactly, the identical reverse-lookup need for an
-- almost identical table shape.
CREATE INDEX sentinel_fixes_fix_child_session_id_idx ON sentinel_fixes (fix_child_session_id) WHERE fix_child_session_id IS NOT NULL;
