ALTER TABLE workflow_step_definitions DROP CONSTRAINT IF EXISTS workflow_step_definitions_effort_shape;
ALTER TABLE workflow_step_definitions DROP COLUMN IF EXISTS effort;
