ALTER TABLE sessions DROP COLUMN IF EXISTS epistemic_check_enabled;
ALTER TABLE turns DROP COLUMN IF EXISTS epistemic_outcome;
DROP TYPE IF EXISTS turn_epistemic_outcome;
