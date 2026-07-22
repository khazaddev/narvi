-- Queries backing SandboxStore (§4.3). Just enough to prove the pipeline
-- end to end (create + get) — the UNIQUE(session_id) constraint (§3.2) is
-- exercised by the integration test, not by these queries.

-- name: CreateSandbox :one
INSERT INTO sandboxes (session_id)
VALUES ($1)
RETURNING *;

-- name: GetSandbox :one
SELECT * FROM sandboxes
WHERE session_id = $1;

-- name: UpdateSandboxStatus :one
-- Sets a sandbox's status, plus last_seen_at when the caller supplies a
-- real timestamp (sqlc.narg + COALESCE, same pattern as
-- UpdateTurnStatus) -- per §3.2 "Liveness = max of all signals",
-- last_seen_at only ever moves forward on an actual signal, never as a
-- side effect of a plain status write.
UPDATE sandboxes
SET status = $2,
    last_seen_at = COALESCE(sqlc.narg('last_seen_at'), last_seen_at),
    updated_at = now()
WHERE session_id = $1
RETURNING *;

-- name: UpsertSandboxForSpawn :one
-- Step 21 ("e2e happy path"), design decision 3a: creates the sandbox row
-- (gen=1) if none exists yet, or bumps gen/resets status to 'spawning'/
-- rotates token_hash if one already does (§3.2: "every spawn/restore
-- increments sandbox.gen" -- paraphrased above, not a verbatim quote).
-- provider_id is deliberately NOT cleared here --
-- the second, post-CreateSandbox write (UpdateSandboxProviderID) is what
-- ever sets it, and a stale previous-gen provider_id lingering between
-- those two writes is harmless (SpawnState.ProviderObjectID is only read
-- BEFORE this upsert runs, from the row as it stood prior to this call).
--
-- Fix (audit finding F3): the ON CONFLICT (resume/restore/re-claim) branch
-- now also sets last_seen_at = now() -- this claim is itself a fresh sign
-- of life for the row, exactly like RecoverSandboxFromSuspect's own "this
-- write is itself the liveness signal" precedent below. A resume/restore
-- claim in particular can land on a row that sat in a terminal status
-- (Stopped/Failed/Stale) for arbitrarily long before being claimed again,
-- so without this, sinceLastSignOfLife in domain/sandbox.
-- EvaluateSpawnDecision's own Skip guard (measured from max(created_at,
-- last_seen_at)) would still reflect however long the box sat idle
-- beforehand, not this claim -- defeating that guard's "no-op a concurrent
-- second actor for free" purpose for exactly the case (resume/restore of a
-- long-terminal box) it most needs to cover. Because session_id is
-- UNIQUE, any second concurrent caller for the same session is guaranteed
-- to land on THIS branch (not the INSERT below) regardless of which one
-- physically ran first, so this is the only branch that needs to move
-- last_seen_at for that guard's sake.
--
-- The INSERT (fresh-row) branch below deliberately does NOT set
-- last_seen_at: leaving it NULL until the sandbox's own first liveness
-- signal (the column's doc comment, migrations/000006_sandboxes.up.sql)
-- is what tells domain/sandbox.EvaluateConnectingTimeout this box hasn't
-- connected yet, so it grants the longer FirstConnectBudget (240s) rather
-- than the steady-state SteadyHeartbeatBudget (90s) while it cold-starts.
-- Setting it here too was tried and reverted: it made every fresh spawn's
-- very first connecting-deadline check see a non-zero last_seen_at (==
-- created_at) and wrongly pick the 90s budget, false-positiving a
-- perfectly normal slow boot into "stuck spawn" territory. It also isn't
-- needed for the Skip guard above -- a fresh INSERT has no prior row for a
-- concurrent caller to race against; maxTime(created_at, zero) ==
-- created_at already reads as "just spawned".
INSERT INTO sandboxes (session_id, gen, status, token_hash)
VALUES ($1, 1, 'spawning', $2)
ON CONFLICT (session_id) DO UPDATE
SET gen = sandboxes.gen + 1,
    status = 'spawning',
    token_hash = $2,
    last_seen_at = now(),
    updated_at = now()
RETURNING *;

-- name: UpdateSandboxProviderID :one
-- Records the provider's own opaque handle (internal/app/ports.SandboxRef.
-- ProviderID) once CreateSandbox actually succeeds -- a SEPARATE write
-- from UpsertSandboxForSpawn above, deliberately run in its own
-- transaction AFTER the real CreateSandbox network call returns (a
-- network call must never hold a Postgres transaction open).
UPDATE sandboxes
SET provider_id = $2, updated_at = now()
WHERE session_id = $1
RETURNING *;

-- name: UpdateSandboxCircuitBreaker :one
-- Persists internal/domain/sandbox.CircuitBreakerState verbatim: called
-- with (0, NULL) when EvaluateCircuitBreaker's own ShouldReset is true,
-- and with (incremented count, now) when a *ports.ProviderError with
-- Transient=false increments the breaker (§3.2: "3 permanent spawn
-- failures within 5 min blocks spawning"). Always a direct SET, never
-- COALESCE -- unlike status/last_seen_at, both fields are meant to be
-- overwritten with exactly the caller-computed value every time,
-- including back to NULL on reset.
UPDATE sandboxes
SET spawn_failure_count = $2, last_spawn_failure_at = $3, updated_at = now()
WHERE session_id = $1
RETURNING *;

-- name: UpdateSandboxSnapshotID :one
-- Step 22 ("snapshots & restore"), design decision 3: records a real,
-- sandbox-confirmed snapshot id once a "snapshot_ready" wire event
-- arrives -- read back as SpawnState.SnapshotImageID (internal/domain/
-- sandbox.EvaluateSpawnDecision's own restore-eligibility input) on a
-- later spawn decision. Deliberately a direct SET, mirroring
-- UpdateSandboxProviderID's own precedent exactly (not COALESCE-guarded
-- like status/last_seen_at -- every call here carries a real, just-
-- confirmed id meant to overwrite whatever was there before). Also
-- clears pending_snapshot_message_id back to NULL in the SAME statement:
-- this query's only caller (handleSnapshotReadyEvent's accept path) only
-- ever reaches here after already confirming the event's own
-- commandMessageId matches that column's current value, so the
-- outstanding attempt this call completes is, by construction, exactly
-- the one that column was tracking -- see that column's own migration
-- doc comment (migrations/000022_sandbox_snapshot_id.up.sql) for the full
-- race this closes.
UPDATE sandboxes
SET snapshot_id = $2, pending_snapshot_message_id = NULL, updated_at = now()
WHERE session_id = $1
RETURNING *;

-- name: UpdateSandboxStatusToSuspect :one
-- Step 24 ("two-phase terminalization"): the single write
-- transitionSandboxToSuspect (internal/app/sessionactor/timerfired.go)
-- performs when a watchdog moves a sandbox into Suspect. Sets status =
-- 'suspect' as a hardcoded literal -- mirroring UpsertSandboxForSpawn's
-- own hardcoded 'spawning' literal precedent exactly: this query has
-- exactly one legal target status, by construction of its own single call
-- site -- AND persists pre_suspect_status = the state being left, in the
-- SAME statement, so §3.2's own "any liveness signal during grace returns
-- to previous state" rule has somewhere to read that state back from
-- later (handleSandboxEvent's own recovery branch, sandboxevent.go,
-- RecoverSandboxFromSuspect below). Deliberately does NOT touch
-- last_seen_at -- entering Suspect is a watchdog's own classification of
-- silence, never itself a liveness signal.
UPDATE sandboxes
SET status = 'suspect',
    pre_suspect_status = $2,
    updated_at = now()
WHERE session_id = $1
RETURNING *;

-- name: RecoverSandboxFromSuspect :one
-- Step 24 ("two-phase terminalization"): the single write
-- handleSandboxEvent's own recovery branch (sandboxevent.go) performs
-- when ANY recognized inbound sandbox event arrives for a Suspect sandbox
-- that still carries a pre_suspect_status -- i.e. "any liveness signal
-- during grace returns to previous state" (§3.2), the event itself being
-- that liveness signal. Sets status = $2 (the recovered, previously-live
-- state sandbox.Transition(StateSuspect, gen, RecoverTrigger(...)) already
-- validated), clears pre_suspect_status back to NULL (no longer needed --
-- mirrors UpdateSandboxSnapshotID's own "clear the now-satisfied
-- outstanding column in the same statement" precedent), and sets
-- last_seen_at to the event's own arrival time -- unlike
-- UpdateSandboxStatusToSuspect above, THIS write is itself the liveness
-- signal that caused the recovery, so last_seen_at moves forward exactly
-- like the general per-event UpdateSandboxStatus write already does for
-- every other recognized event. Deliberately a direct SET (not
-- COALESCE'd), mirroring UpdateSandboxProviderID/UpdateSandboxSnapshotID's
-- own precedent: this call site always has a real, just-observed
-- timestamp to write.
UPDATE sandboxes
SET status = $2,
    pre_suspect_status = NULL,
    last_seen_at = $3,
    updated_at = now()
WHERE session_id = $1
RETURNING *;

-- name: ListLiveSandboxProviderIDs :many
-- Step 25 ("reconciler + GC", §5.3): the reconciler's own "expected still
-- alive" set -- the provider_id of every sandbox row currently in a LIVE
-- status, across ALL sessions. Unlike every OTHER query in this file
-- (each scoped to one session_id via WHERE session_id = $1), this one
-- genuinely needs to scan the whole table -- by design, not oversight:
-- app/reconciler.Reconciler.ReconcileOnce compares this set against
-- ports.SandboxProvider.List's real, currently-live provider-side refs,
-- and any ref with no corresponding row here is a genuine orphan (see
-- that method's own doc comment for the two ways one arises).
--
-- 'pending' is excluded: a pending sandbox has no provider object yet
-- (UpsertSandboxForSpawn only ever creates a row already in 'spawning').
-- 'stopped'/'failed' are excluded DELIBERATELY, not merely omitted: they
-- are exactly the terminal statuses whose own leaked provider objects
-- this reconciler exists to catch (StopSandbox has had no real caller
-- anywhere in this codebase before this Step), so a stale provider_id
-- still lingering on a terminal row (UpsertSandboxForSpawn's own doc
-- comment notes provider_id is never cleared on respawn) must NOT count
-- as "expected alive" here.
SELECT provider_id FROM sandboxes
WHERE status IN ('spawning', 'connecting', 'booting', 'ready', 'snapshotting', 'suspect')
  AND provider_id IS NOT NULL;

-- name: UpdateSandboxPendingSnapshotMessageID :one
-- Step 22 fix (message-id correlation): sets or clears (pass NULL)
-- pending_snapshot_message_id -- the MessageId of whichever Snapshot
-- command this sandbox is currently waiting on a snapshot_ready for.
-- triggerSnapshotBestEffort sets it, in the SAME transact that commits
-- the Ready->Snapshotting transition (sandboxevent.go); both
-- revertSnapshotBestEffort's compensating-write path and
-- handleSnapshotReadyEvent's decode-failure revert path clear it back to
-- NULL when they revert Snapshotting->Ready, so a stale attempt's
-- eventual real snapshot_ready, if it ever arrives, correctly finds no
-- matching pending id and is discarded as stale. Deliberately a direct
-- SET, mirroring UpdateSandboxProviderID's own precedent exactly (never
-- COALESCE-guarded -- every call here carries the caller's own
-- deliberately computed value, including NULL).
UPDATE sandboxes
SET pending_snapshot_message_id = $2, updated_at = now()
WHERE session_id = $1
RETURNING *;
