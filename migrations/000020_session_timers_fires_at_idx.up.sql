-- session_timers has UNIQUE(session_id, name) (migrations/000009_session_timers.up.sql),
-- an implicit index on that pair, but nothing on fires_at alone --
-- ListDueTimers (internal/adapters/outbound/postgres/queries/
-- session_timers.sql) runs `WHERE fires_at <= now() ORDER BY fires_at`
-- every platform.Timeouts.TimerPumpInterval (5s), so without this index
-- that query does a full sequential scan every tick, a real, growing cost
-- as the number of live sessions (each carrying up to 5 named timers)
-- increases. A plain, single-column index -- matching this table's own
-- existing index-naming convention (e.g. ws_tokens_token_hash_idx).
CREATE INDEX session_timers_fires_at_idx ON session_timers (fires_at);
