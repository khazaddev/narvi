-- Queries backing Outbox (§4.3, §5.1). Just enough to prove the pipeline
-- end to end (create + get) — the delivery worker lands in PR-35.

-- name: CreateOutboxEntry :one
INSERT INTO outbox (session_id, kind, payload)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetOutboxEntry :one
SELECT * FROM outbox
WHERE id = $1;
