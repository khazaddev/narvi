-- §30.4's demotion requirement, made recoverable.
--
-- Demotion (live_egress_enabled true -> false) must terminate every
-- sandbox of the repo and cancel its in-flight push signals, because a
-- write credential minted just before the flip stays served for the
-- ScmCredentialTTL window and the OAuth token underneath never expires on
-- that clock. The sweep that does this cannot run inside the flip's own
-- statement -- matching a sandbox to a repo means parsing each session's
-- repos JSONB and its clone URLs in Go -- so it runs after the commit.
--
-- Which made the requirement unrecoverable. The caller detected the
-- demotion by comparing a pre-transaction read against the declared
-- value, and the commit then DESTROYED that difference. A sweep that
-- failed halfway, or a process that died between commit and sweep, left
-- sandboxes holding write credentials with nothing anywhere recording
-- that they should have been terminated -- and the obvious operator
-- response, re-running the manifest, finds false -> false, reports
-- success, and sweeps nothing.
--
-- demotion_sweep_pending_at is that missing durable intent. It is set by
-- the SAME statement that performs the flip, on a genuine true -> false
-- transition only, and cleared only once a sweep has completed without
-- error. So the obligation outlives the transition that created it: the
-- reconciler retries any repo still carrying one, and a re-run is no
-- longer the operator's only recourse.
--
-- NULL means "no sweep owed" -- the ordinary state, including for a repo
-- that was never live. That polarity is deliberate and is the safe one
-- for THIS column: a row created by some future writer that forgets this
-- column has never been live, so it owes no sweep. The unsafe direction
-- would be a pending sweep silently dropped, which requires an explicit
-- write to NULL, not an omission.
ALTER TABLE repo_settings ADD COLUMN demotion_sweep_pending_at TIMESTAMPTZ;

-- Partial index: the reconciler's own per-tick scan asks only "is any
-- sweep owed", which on an ordinary deployment matches nothing at all.
CREATE INDEX idx_repo_settings_demotion_sweep_pending
    ON repo_settings (demotion_sweep_pending_at)
    WHERE demotion_sweep_pending_at IS NOT NULL;
