-- Queries backing WebhookDeliveryStore (§5.1's dedupe/coalescing claim,
-- §4.3). No caller exists yet -- Steps 32/33/34 (GitHub/Slack/Linear
-- ingress) are the ones that actually call ClaimWebhookDelivery, once
-- each provider's own adapter has verified its signature and parsed out
-- its own delivery id.

-- name: ClaimWebhookDelivery :one
-- Atomic first-writer-wins claim on (provider, delivery_id) -- the SAME
-- "(xmax = 0) AS inserted" idiom postgres/queries/events.sql's own
-- CreateEvent already establishes: a deliberate, self-referential no-op
-- update (received_at is set back to its own current value) so RETURNING
-- always yields exactly one row whether this call just inserted a fresh
-- row or found an already-claimed one from an earlier delivery of the
-- SAME (provider, delivery_id) -- a real webhook redelivery, which every
-- provider this table serves does on a timeout/5xx. Callers branch on
-- Inserted: true means "process this delivery", false means "already
-- claimed/processed -- skip, never double-act on a resend".
INSERT INTO webhook_deliveries (provider, delivery_id) VALUES ($1, $2)
ON CONFLICT (provider, delivery_id) DO UPDATE SET received_at = webhook_deliveries.received_at
RETURNING *, (xmax = 0) AS inserted;
