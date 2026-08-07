-- Step 59 ("models", §29.8's "workflow engine echo"): workflow_step_
-- definitions gains a nullable effort column with the IDENTICAL
-- inherit-when-null semantics its model_id column already has
-- (migrations/000057_workflows.up.sql) -- null means inherit exactly what
-- the session would use today (turns.effort/sessions.build_effort,
-- migrations/000063), non-null overrides it for this step, threaded
-- through the same engine -> turn -> BuildPromptPayload path model_id
-- already uses (§25.7/§25.8's zero-config proof extends unchanged, no
-- separate mechanism).
--
-- Same CHECK-constraint shape as workflow_step_definitions_model_id_shape
-- (000057) -- a non-null override is never the empty string.
ALTER TABLE workflow_step_definitions ADD COLUMN effort TEXT;
ALTER TABLE workflow_step_definitions ADD CONSTRAINT workflow_step_definitions_effort_shape CHECK (effort IS NULL OR effort <> '');
