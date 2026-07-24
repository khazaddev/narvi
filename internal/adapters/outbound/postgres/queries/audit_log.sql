-- Queries backing AuditLogStore (§13.3: "audit_log(actor_user_id, action,
-- resource_type, resource_id, detail_json, correlation_id, created_at)
-- written in the same transaction as the change"). This Step (39,
-- "identities + full RBAC") is the first to actually WRITE to this table
-- (migrations/000013_audit_log.up.sql created it, unused, back in PR-04)
-- -- CreateAuditLogEntry is therefore this file's only query: every
-- caller runs it via AuditLogStore.WithTx(tx), inside the SAME
-- transaction as the state change it is recording, never as a
-- freestanding pool-scoped write. No Get/List query exists yet -- Step
-- 39's own "members API" half (a later Step's job, per this Step's own
-- hand-off notes) is what actually surfaces "Settings -> Members ->
-- Audit log" (§13.4 Phase 7); this Step only makes the writes real.
--
-- actor_user_id is passed as a nullable pgtype.UUID -- NULL for a
-- bot/webhook-attributed change (mirrors sessions.created_by/plans.
-- decided_by's own identical NULL-for-bot convention, and the
-- $17.5-referenced allowance this table's own migration 000013 comment
-- already names: "actor_user_id NULL... for actions with no human
-- actor" -- no separate system-actor row is ever created or required).

-- name: CreateAuditLogEntry :one
INSERT INTO audit_log (actor_user_id, action, resource_type, resource_id, detail_json, correlation_id)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;
