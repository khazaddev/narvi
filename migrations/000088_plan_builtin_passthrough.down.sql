-- Reverses 000088_plan_builtin_passthrough.up.sql: restores the built-in
-- `plan` workflow to migration 000057's own ORIGINAL 2-step seed shape
-- (step ...031 with hitl_after = true, step ...032, and the needs_fix
-- self-loop edge ...041) -- the exact ids/columns/values 000057 itself
-- inserted, so a down-then-up round trip reproduces byte-for-byte the
-- same seed shape either way.
--
-- NOT restored, and this is a deliberate, accepted limitation rather
-- than an oversight: any `workflow_step_runs` rows the up-migration's
-- own defensive cleanup deleted (step_definition_id =
-- '...-000000000032'). A down-migration restores SCHEMA/SEED shape, it
-- cannot resurrect arbitrary application data this migration never had a
-- copy of. Per the up-migration's own header, no real deployment is
-- expected to have had such a row in the first place (the two-gate
-- conflict this migration exists to fix meant step ...032 could never be
-- legitimately reached), so this is expected to be a no-op in practice,
-- not a real data loss.
--
-- created_at/updated_at on the re-inserted rows are today's `now()`, not
-- 000057's own original install-time values -- the same, ordinary
-- limitation every down-then-reinsert migration in this codebase accepts
-- (there is no way to recover a value that was never independently
-- stored).
UPDATE workflow_step_definitions
SET hitl_after = true, updated_at = now()
WHERE id = '00000000-0000-4000-8000-000000000031';

INSERT INTO workflow_step_definitions
    (id, workflow_definition_id, step_order, kind, model_id, prompt_template, execution_scope, conversation_continuity, hitl_before, hitl_after)
VALUES
    ('00000000-0000-4000-8000-000000000032', '00000000-0000-4000-8000-000000000003', 2, 'agent', NULL, '{{prompt}}', 'same_session', 'continue', false, false);

INSERT INTO workflow_edges (id, workflow_definition_id, from_step_id, to_step_id, on_status) VALUES
    ('00000000-0000-4000-8000-000000000041', '00000000-0000-4000-8000-000000000003', '00000000-0000-4000-8000-000000000031', '00000000-0000-4000-8000-000000000031', 'needs_fix');
