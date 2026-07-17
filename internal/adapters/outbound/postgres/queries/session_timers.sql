-- Queries backing TimerScheduler (§4.3, §2). UpsertSessionTimer uses
-- ON CONFLICT (session_id, name) DO UPDATE per the "each is armed/re-armed
-- independently" semantics (§2) — re-arming updates the existing row, never
-- inserts a duplicate.

-- name: UpsertSessionTimer :one
INSERT INTO session_timers (session_id, name, fires_at)
VALUES ($1, $2, $3)
ON CONFLICT (session_id, name) DO UPDATE
    SET fires_at = EXCLUDED.fires_at
RETURNING *;

-- name: GetSessionTimer :one
SELECT * FROM session_timers
WHERE session_id = $1 AND name = $2;
