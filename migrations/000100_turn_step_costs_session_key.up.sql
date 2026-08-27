-- Re-key turn_step_costs on (session_id, step_id).
--
-- The idempotency key was (turn_id, step_id), and turn_id came from
-- "whichever turn is processing when the event lands". That makes the key
-- itself move: a reconnect replay (§6.1's sender buffers events and
-- re-sends) that arrives after the original turn ended and the next one
-- began resolves a DIFFERENT turn_id, so the INSERT does not conflict --
-- it inserts a second row and adds the same dollars again, to the new
-- turn. Measured against real Postgres: one $5.00 step delivered twice
-- across a turn boundary charged $10.00 in total, $5.00 on each turn.
--
-- An idempotency key must not be derived from state that changes between
-- the two deliveries it exists to tell apart. session_id does not change:
-- the sandbox websocket is session-scoped, so a replay of a step_finish
-- always lands on the session it was emitted for, whatever has happened
-- to that session's turns in between.
--
-- turn_id stays, as attribution rather than identity -- it is still which
-- turn's total the dollars were added to, and still the join the run view
-- reads. Its known limit is unchanged and stated in queries/turns.sql: a
-- step_finish in flight across a turn boundary is attributed to the turn
-- processing when it lands. That is a misattribution of at most one
-- event's cost; charging it twice was a different and worse thing.
ALTER TABLE turn_step_costs ADD COLUMN session_id UUID REFERENCES sessions(id) ON DELETE CASCADE;

UPDATE turn_step_costs SET session_id = turns.session_id
FROM turns WHERE turns.id = turn_step_costs.turn_id;

ALTER TABLE turn_step_costs ALTER COLUMN session_id SET NOT NULL;

ALTER TABLE turn_step_costs DROP CONSTRAINT turn_step_costs_pkey;
ALTER TABLE turn_step_costs ADD PRIMARY KEY (session_id, step_id);

-- turn_id is no longer part of the key, so give it its own index: the run
-- view reads per-turn rows, and that read was riding the old primary key.
CREATE INDEX turn_step_costs_turn_id_idx ON turn_step_costs (turn_id);
