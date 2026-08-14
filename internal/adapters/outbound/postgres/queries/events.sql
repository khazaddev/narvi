-- Queries backing EventStore (§4.3, §6.1's append-only per-session event
-- log). CreateEvent appends; ListEventsForSession is the cursor-paginated
-- read this same table's own migration comment predicted ("reads
-- (cursor-paginated fetch_history) land with the client WS hub (Steps
-- 18+)") -- this is that Step, backing both the client WS hub's own
-- fetch_history/replay and the REST GET .../events endpoint (one
-- implementation, two callers).

-- name: CreateEvent :one
-- Upsert-on-(session_id, message_id) (§6.1: "receiver dedupes by
-- upsert-on-messageId") -- a resend of an already-seen messageId hits the
-- unique index (migrations/000019_events_message_id.up.sql) and this
-- DO UPDATE (a deliberate, self-referential no-op: type is set back to
-- its own current value) guarantees RETURNING always yields exactly one
-- row either way, so callers never need a separate "0 rows means
-- duplicate" branch. `(xmax = 0) AS inserted` is the standard Postgres
-- idiom for "was this row just inserted by THIS statement" (xmax is 0
-- only immediately after a fresh insert, non-zero after any update) --
-- callers use it to decide whether to (re-)broadcast this event to live
-- subscribers.
INSERT INTO events (session_id, type, message_id, payload) VALUES ($1, $2, $3, $4)
ON CONFLICT (session_id, message_id) DO UPDATE SET type = events.type
RETURNING *, (xmax = 0) AS inserted;

-- name: ListEventsForSession :many
-- afterID = 0 means "from the beginning" -- matches a null fetch_history
-- cursor / a REST ?cursor= default. The monotonic BIGSERIAL id is the
-- natural pagination cursor (events_session_id_id_idx,
-- migrations/000008_events.up.sql's own doc comment).
SELECT * FROM events
WHERE session_id = $1 AND id > $2
ORDER BY id ASC
LIMIT $3;

-- name: ListRecentEventsForSession :many
-- The mirror-image pagination direction from ListEventsForSession's own
-- oldest-first cursor page: returns up to $2 of session_id's own MOST
-- RECENT events, newest id first -- for a caller that needs only the TAIL
-- of a possibly-long event log (e.g. sessionactor.planContentText's own
-- best-effort plan-content extraction, Step 38) without scanning forward
-- from the very beginning of a session's entire history, which for a
-- long-lived session (many prior turns) could leave the CURRENT turn's
-- own events entirely outside a bounded oldest-first window. Same
-- events_session_id_id_idx index (migrations/000008_events.up.sql) serves
-- this DESC scan equally well.
SELECT * FROM events
WHERE session_id = $1
ORDER BY id DESC
LIMIT $2;

-- Step 71 (§26.4/§7.1's own post-hoc sub-task corroboration): the two
-- queries below are this codebase's FIRST use of a payload->>'gen' JSONB
-- extraction filter -- checked against the rest of this file and every
-- other file in this directory before writing these, since there is no
-- existing precedent for filtering events by a field INSIDE payload
-- rather than by one of the table's own real columns (session_id, type,
-- id). Every sandbox-ws wire event, sub_task_start and sub_task_finish
-- included, carries its own "gen" field in the common envelope
-- (contracts/sandbox-ws/v1/events.schema.json) -- the SAME sandbox
-- generation the emitting sandbox process was live at when it produced
-- the event, distinct from the events TABLE's own session-scoped id
-- column, which has no gen concept of its own at all.
--
-- # gen alone is NOT sufficient -- see the dispatched_at bound below
--
-- An earlier version of these two queries (this same Step, before an
-- adversarial review of the PR caught this) scoped strictly to
-- (session_id, type, gen) on the theory that gen alone uniquely identifies
-- a turn's own dispatch. It does not: turns.dispatched_sandbox_gen
-- (migrations/000026_turn_dispatch_gen.up.sql) reuses sandboxes.gen, the
-- fencing idiom bumped ONLY on a fresh spawn/restore/resume
-- (internal/app/sessionactor's own planFreshSpawn/planRestore/planResume,
-- `gen = gen + 1`) -- NEVER on an ordinary dispatch to an already-live
-- (Ready/Suspect) sandbox. tryPlanDispatch (dispatch.go), the path used
-- whenever a Pending turn is dispatched to a sandbox that is already up,
-- stamps dispatched_sandbox_gen := sandboxRow.Gen VERBATIM, with no bump.
-- So a session whose sandbox stays alive across multiple review turns --
-- the ORDINARY case §24's automatic re-review on new commits describes,
-- not a rare corner case -- dispatches every one of those turns at the
-- SAME gen. Scoping by gen alone therefore lets an EARLIER turn's own
-- real, genuinely-completed counter-review trace spuriously corroborate a
-- LATER turn's self-report, simply because both turns happened to run on
-- the same still-live sandbox incarnation: exactly the unverified-self-
-- report bypass this whole Step exists to close, reopened.
--
-- The fix: an ADDITIONAL `created_at >= sqlc.arg('dispatched_at')` lower
-- bound, using the turn-being-verdicted's own turns.dispatched_at
-- (migrations/000005_turns.up.sql, already populated at every real
-- dispatch). turns_one_processing_per_session's own unique partial index
-- (migrations/000005_turns.up.sql) guarantees turns execute strictly
-- sequentially per session, so every event genuinely belonging to THIS
-- turn has created_at at or after this turn's own dispatched_at, and every
-- event belonging to an EARLIER turn on the same session was created
-- strictly before this turn was ever dispatched -- excluded by this bound
-- regardless of whether gen happens to match.
--
-- Neither condition alone is enough; both stay, because each closes a
-- DIFFERENT, independent gap:
--   - gen alone: as above, cannot distinguish two turns dispatched to the
--     SAME live sandbox incarnation (no gen bump between them).
--   - dispatched_at alone: cannot distinguish a genuinely different,
--     now-dead sandbox INCARNATION's stale, late-arriving event from a
--     current one -- a sub_task_finish sent by an old sandbox process
--     right before it died can be delivered late over the network and
--     land in Postgres (created_at, wall-clock insert time) AFTER a
--     LATER turn's own dispatched_at, even though it carries the OLD gen
--     in its own payload. The gen filter is what excludes that stale
--     cross-incarnation row; the timestamp bound alone would not.
-- Together: gen excludes cross-INCARNATION contamination (a different,
-- dead sandbox process); dispatched_at excludes cross-TURN-same-
-- incarnation contamination (an earlier turn dispatched to the SAME still
-- -live sandbox). The `::int` cast on gen is safe here specifically
-- because "gen" is schema-typed as a JSON integer on every event this
-- filters (never a string, unlike some other payload fields elsewhere in
-- this schema) -- a malformed/absent "gen" on some hypothetical future
-- producer would fail the cast (or NULL-compare against the arg, matching
-- nothing) rather than silently matching every gen, so this filter fails
-- toward "matches nothing", never toward "over-matches" -- the same
-- fail-conservative direction this codebase's own closed-enum defaults
-- already commit to elsewhere (review/doc.go's "fail-conservative policy
-- for every closed enum" section). The caller (corroborateCounterReview,
-- internal/adapters/inbound/httpapi/reviewverdict.go) applies the
-- identical fail-conservative treatment when dispatched_at itself is NULL
-- (should be unreachable -- a turn being verdicted is by definition
-- already dispatched -- but never assumed).

-- name: ListSubTaskStartEventsForTurn :many
SELECT * FROM events
WHERE session_id = $1
  AND type = 'sub_task_start'
  AND (payload->>'gen')::int = sqlc.arg('gen')::int
  AND created_at >= sqlc.arg('dispatched_at')::timestamptz
ORDER BY id ASC;

-- name: ListSubTaskFinishEventsForTurn :many
SELECT * FROM events
WHERE session_id = $1
  AND type = 'sub_task_finish'
  AND (payload->>'gen')::int = sqlc.arg('gen')::int
  AND created_at >= sqlc.arg('dispatched_at')::timestamptz
ORDER BY id ASC;
