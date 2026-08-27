-- turn_step_costs: the idempotency key per-step cost accumulation actually
-- needs, and the reason turns.cost_usd can be trusted.
--
-- The first cut of §25.15's accumulation gated on the `inserted` flag
-- appendRawEvent returns -- "was this (session_id, message_id) row new to
-- the events table". That answers a DIFFERENT question than "have I
-- already counted this step_finish", and in production the two disagree
-- every single time: OpenCode emits step-start and step-finish as two
-- parts of the SAME assistant message, and both wire events carry that
-- enclosing message's id (internal/adapters/outbound/opencode/translate.go
-- says so outright -- the token event is the ONE part-derived event that
-- uses its own part id). step_start therefore claims the row first, every
-- step_finish upserts onto it and comes back inserted=false, and the cost
-- gate returned before ever reading a dollar figure. turns.cost_usd stayed
-- NULL for every turn that has ever run. The tests missed it because the
-- fixture minted a fresh message id per step_finish and never sent the
-- step_start that always precedes it.
--
-- The wire already carries the right key: step_finish.stepId (§6.1) is the
-- step-finish part's own id, one per step. Keyed on (turn_id, step_id) the
-- accumulation is idempotent by PRIMARY KEY rather than by a flag that
-- happens to be nearby -- a redelivered step_finish conflicts and adds
-- nothing, a genuinely new one inserts and moves the total, and no reader
-- has to reason about which of the two it is looking at.
--
-- Scoped by turn_id rather than a bare step_id primary key: part ids come
-- from another process's id space, and this table should not depend on
-- their global uniqueness to stay correct.
--
-- The per-step rows are kept, not discarded after summing. They are what
-- makes turns.cost_usd auditable -- a total that cannot be recomputed from
-- anything is a number nobody can check.
CREATE TABLE turn_step_costs (
    turn_id    UUID           NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    step_id    TEXT           NOT NULL,
    cost_usd   NUMERIC(14, 6) NOT NULL,
    created_at TIMESTAMPTZ    NOT NULL DEFAULT now(),
    PRIMARY KEY (turn_id, step_id)
);
