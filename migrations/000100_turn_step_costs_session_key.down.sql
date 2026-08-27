DROP INDEX IF EXISTS turn_step_costs_turn_id_idx;
ALTER TABLE turn_step_costs DROP CONSTRAINT turn_step_costs_pkey;
ALTER TABLE turn_step_costs ADD PRIMARY KEY (turn_id, step_id);
ALTER TABLE turn_step_costs DROP COLUMN IF EXISTS session_id;
