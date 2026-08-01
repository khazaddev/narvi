-- sessions.parent_session_id / sessions.spawn_depth (Step 48, "sentinels +
-- suggestions", §17.2): the "child session" mechanism §17.2/§14.4 both
-- describe as an "existing mechanism" is not, in fact, built anywhere yet
-- -- grepping this codebase for parent_session_id/spawn_depth before this
-- migration returns nothing except an unrelated OpenCode sub-task
-- correlator field (internal/adapters/outbound/opencode/types.go), a
-- different concept entirely (§7.1 is explicit that OpenCode's own
-- sub-tasks are "not Narvi's own child session"). This Step is the FIRST
-- one that actually has to build it, not reuse it -- see this Step's own
-- PR description for the full "what's already there vs. what the plan
-- assumes is there" writeup.
--
-- parent_session_id is NULLABLE: NULL for every ordinary, non-child
-- session (every session created before this Step, and every session
-- created by a real ingress surface/user after it) -- only a
-- sentinel-auto-fix fix session (this Step's own one real producer of a
-- child session today) ever sets it. ON DELETE SET NULL, not CASCADE -- a
-- parent session being deleted (not a real operation this codebase
-- performs today, but the safe default regardless) must never cascade
-- into deleting a child session's own independent history.
--
-- spawn_depth is NOT NULL DEFAULT 0 -- every ordinary session is spawn
-- depth 0 (no parent); a direct child of a depth-0 session is depth 1,
-- and so on. This Step's own only producer of a child session
-- (SpawnSentinelFixChildSession) always sets exactly 1 -- §17.1's "no
-- recursion" rule is enforced via provenance_tag (a sentinel-auto-fix
-- child session is never itself eligible to trigger another), not a
-- depth-counter check, per that section's own explicit "not a
-- depth-counter side effect" wording -- spawn_depth is still recorded here
-- as honest, queryable data (an audit/observability column), not because
-- any code path in this Step gates behavior on its numeric value.
ALTER TABLE sessions ADD COLUMN parent_session_id UUID REFERENCES sessions(id) ON DELETE SET NULL;
ALTER TABLE sessions ADD COLUMN spawn_depth INTEGER NOT NULL DEFAULT 0;

CREATE INDEX sessions_parent_session_id_idx ON sessions (parent_session_id) WHERE parent_session_id IS NOT NULL;
