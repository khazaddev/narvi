-- Queries backing WebhookDeliveryStore (§5.1's dedupe/coalescing claim,
-- §4.3). No caller exists yet -- §8.2/§8.10 (GitHub/Slack/Linear
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

-- name: GetLastInboundDeliveryAt :one
-- §12.5's own ("integrations read model & routes" amendment) GET
-- /api/integrations: "when did we last hear from this surface" --
-- MAX(received_at) across every (provider, delivery_id) row this
-- provider has ever claimed, an EXACT match on provider (never a prefix
-- match the way outbox.kind needs -- GetLatestOutboxEntryByKindPrefix,
-- postgres/queries/outbox.sql -- since every provider here writes its own
-- CONSTANT, exact string: githubDeliveryProvider "github",
-- internal/adapters/inbound/slack/handler.go's own literal "slack",
-- internal/adapters/inbound/linear/webhook.go's own literal "linear").
-- Deliberately does NOT match internal/adapters/inbound/slack/handler.go's
-- own SECOND, distinct claim namespace "slack-message"
-- (slackMessageClaimProvider) -- that one claims a MESSAGE for
-- coalescing, not a genuine webhook delivery, and an exact "=" comparison
-- (never LIKE) correctly leaves it out.
--
-- A bare aggregate with no GROUP BY always returns exactly ONE row
-- (MAX of zero matching rows is a genuine SQL NULL, not an absent row),
-- so this is a true :one query -- never pgx.ErrNoRows -- and the Go
-- caller reads pgtype.Timestamptz.Valid to distinguish "never heard from
-- this provider" (false) from a real timestamp.
SELECT MAX(received_at)::timestamptz AS last_received_at FROM webhook_deliveries
WHERE provider = $1;

-- name: ReleaseWebhookDelivery :exec
-- Un-claims a (provider, delivery_id) this same request just claimed via
-- ClaimWebhookDelivery, but failed to actually process (payload parse
-- error, transient DB error downstream of the claim, etc.) -- called ONLY
-- on those genuine-failure paths, never on a legitimate "nothing to do"
-- outcome. Without this, a claim that wins the INSERT but then fails
-- before the work it gates ever completes would permanently poison that
-- (provider, delivery_id): every subsequent redelivery (which every one
-- of these providers sends on a timeout/5xx -- the exact scenario a
-- mid-pipeline failure often causes) would see Inserted=false and skip
-- reprocessing forever, silently dropping the event. Deleting the row
-- lets the next redelivery re-claim and retry from scratch.
DELETE FROM webhook_deliveries WHERE provider = $1 AND delivery_id = $2;
