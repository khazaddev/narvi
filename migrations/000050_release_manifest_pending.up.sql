-- release_manifest_pending: blocking-finding fix #1 ("release PR review",
-- §15.2) -- the durable hand-off point that lets the release manifest
-- check's own real work run OUTSIDE any GitHub webhook request's own
-- cancellable context.
--
-- Before this table existed, internal/adapters/inbound/github's own
-- webhook handler called internal/app/releasereview.Run INLINE, on the
-- bare request context, BEFORE acking the webhook -- but Run's own real
-- work (ports.SourceControl.ListMergedBetween) can issue ~80+ sequential
-- GitHub API calls for a large release cut, budgeted up to
-- platform.Timeouts.GitHubListMergedBetweenTimeout (2 minutes) -- far
-- longer than GitHub's own ~10s webhook delivery timeout. When GitHub
-- gave up waiting, it closed the connection, the request context was
-- cancelled, the in-flight work died mid-flight, and the loss was
-- PERMANENT: the webhook delivery was already claimed (dedupe), so
-- GitHub's own "Redeliver" was short-circuited as a duplicate, and
-- nothing else ever re-triggered the check -- precisely for the large
-- release cuts it exists to audit.
--
-- The fix: the webhook handler now writes ONE cheap row here (a single
-- INSERT, immediately before its own fast ack) and returns immediately.
-- A SEPARATE background loop (internal/app/releasereview.Worker, started
-- alongside every other background loop in cmd/control-plane/main.go's
-- own errgroup, per this repo's CLAUDE.md "errgroup + context for all
-- concurrency, no naked goroutines" rule) claims rows here on its own
-- schedule and runs the actual check -- entirely decoupled from any
-- webhook request's own context/lifetime, bound only by its own
-- generous platform.Timeouts.ReleaseManifestCheckTimeout budget.
--
-- This mirrors this codebase's own existing outbox ("durable hand-off,
-- delivered later by a dedicated background loop") shape, but is kept as
-- its OWN dedicated table/loop -- deliberately never folded into the
-- generic outbox/outboxworker tier: releasereview.Run's own worst-case
-- per-item processing time (minutes) is utterly incompatible with
-- outboxworker.Builder's own tuned-for-real-time-notifications
-- OutboxDeliveryTimeout (15s, platform/timeouts.go) and its own strictly
-- SEQUENTIAL per-batch processing loop (internal/app/outboxworker/
-- builder.go's own PumpOnce) -- mixing the two would either force every
-- OTHER Slack/Linear/GitHub notification kind to tolerate a multi-minute
-- stall riding behind one slow release-manifest-check row in the same
-- batch, or force this row's own budget down to 15s, defeating the whole
-- point of this fix. Once the check itself completes,
-- internal/app/releasereview.Run enqueues the ALREADY-RENDERED comment
-- into the real outbox table exactly as before (kind='release_manifest')
-- -- that half of the pipeline is unchanged by this fix.
--
-- No retry/backoff/dead-letter columns, unlike outbox: releasereview.Run
-- itself is ALREADY fully best-effort/void (it has no error return at
-- all -- every internal failure is logged and it simply returns), so
-- there is no "delivery outcome" for this table to record or retry on --
-- claiming a row and calling Run on it IS the one and only attempt this
-- system ever makes, exactly mirroring the PRE-fix inline call's own
-- identical single-attempt, no-retry behavior (this fix changes WHEN and
-- on WHOSE context that one attempt runs, never how many times it runs).
-- Claiming atomically DELETES the row (see
-- queries/releasemanifestpending.sql's own ClaimDueReleaseManifestPending
-- doc comment) -- accepted, narrow risk: a pod crash between claim and
-- Run's own completion loses that one check, exactly like an in-flight
-- outbox Deliver call already risks today if the process is killed
-- mid-call; this is far rarer, and far smaller in blast radius, than the
-- bug this table exists to fix (which triggered on nearly every
-- sufficiently-large release PR, deterministically).
--
-- No idempotency claim needed either (unlike handoff_sentinel_runs):
-- internal/app/releasereview's own top doc comment already establishes
-- that github_pr_sessions' own per-PR atomic claim (Step 32) guarantees
-- at most one winning session-creation ever enqueues a row here per
-- release PR -- that structural guarantee is unchanged by this fix.
--
-- session_id is the just-created review session (traceability only).
-- ON DELETE CASCADE mirrors handoff_sentinel_runs' own identical
-- precedent: a row naming a since-deleted session is meaningless on its
-- own.
CREATE TABLE release_manifest_pending (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id UUID NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,

    owner     TEXT NOT NULL,
    repo      TEXT NOT NULL,
    pr_number INTEGER NOT NULL,
    base_ref  TEXT NOT NULL,
    head_ref  TEXT NOT NULL,

    correlation_id TEXT,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backs ClaimDueReleaseManifestPending's own "oldest first" ORDER BY.
CREATE INDEX release_manifest_pending_created_at_idx ON release_manifest_pending (created_at);
