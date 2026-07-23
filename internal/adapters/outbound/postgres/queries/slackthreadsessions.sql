-- Queries backing SlackThreadSessionStore (§8.10's thread↔session
-- mapping, Step 33 "Slack ingress"). See
-- migrations/000029_slack_thread_sessions.up.sql's own doc comment for
-- the full atomic-claim design.

-- name: ClaimSlackThreadSession :one
-- Atomic first-writer-wins claim on (channel_id, thread_ts) for a
-- BRAND-NEW thread's session_id -- ON CONFLICT DO NOTHING (deliberately
-- NOT the self-referential DO UPDATE idiom ClaimWebhookDelivery uses:
-- unlike a webhook redelivery, whose second insert carries the exact
-- SAME identity and payload, a second racer here carries a DIFFERENT
-- (losing) session_id that must never overwrite the winner's). Returns
-- no row at all when the claim is lost (a real Postgres INSERT ...
-- ON CONFLICT DO NOTHING semantics -- callers must handle the
-- pgx.ErrNoRows a :one query surfaces in that case as "you lost the
-- race", never as a genuine error) -- GetSlackThreadSession below is how
-- a loser then discovers the winner's own real session_id.
INSERT INTO slack_thread_sessions (channel_id, thread_ts, session_id)
VALUES ($1, $2, $3)
ON CONFLICT (channel_id, thread_ts) DO NOTHING
RETURNING *;

-- name: GetSlackThreadSession :one
-- Looks up an existing thread's mapped session -- the REPLY path (a
-- message whose (channel_id, thread_ts) already has a mapping), and the
-- lost-the-race path immediately above.
SELECT * FROM slack_thread_sessions
WHERE channel_id = $1 AND thread_ts = $2;
