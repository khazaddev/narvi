-- webhook_deliveries: the dedupe/coalescing table §5.1 calls for
-- ("Dedupe/coalescing (webhook events, concurrent PR @mentions) via
-- INSERT ... ON CONFLICT atomic claims. Never eventually-consistent
-- storage for coordination.") -- Step 31 ("webhook toolkit"), the FIRST
-- of Phase 3's ingress Steps.
--
-- (provider, delivery_id) is the real, provider-agnostic unique identity
-- of one webhook delivery: GitHub sends X-GitHub-Delivery, Slack sends
-- an event_id in its JSON body, Linear sends its own delivery id header
-- -- each provider's own ingress adapter (Steps 32/33/34) supplies its
-- own provider name (a short fixed string, e.g. 'github'/'slack'/
-- 'linear') alongside whatever id its own webhook payload/headers carry.
-- The composite PRIMARY KEY IS the dedupe mechanism -- a second delivery
-- of the SAME (provider, delivery_id) (a real webhook redelivery, which
-- every one of these providers does on a timeout/5xx) hits this
-- constraint, not a race-prone read-then-write check.
--
-- Just enough to prove the claim end to end (mirrors migrations/
-- 000010_outbox.up.sql's own doc comment: "just enough to prove the
-- pipeline") -- received_at is the only payload column, since this
-- Step's own job is the atomic claim primitive itself, not storing or
-- replaying delivery bodies. No caller exists yet: the concrete GitHub/
-- Slack/Linear webhook endpoints that actually call this claim land in
-- Steps 32/33/34.
CREATE TABLE webhook_deliveries (
    provider     TEXT NOT NULL,
    delivery_id  TEXT NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, delivery_id)
);
