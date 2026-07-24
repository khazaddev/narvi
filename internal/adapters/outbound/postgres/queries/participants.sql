-- Queries backing ParticipantStore (migrations/000011_participants.up.sql).
-- ParticipantExists is Step 37's own first real reader of this table
-- (§13.3's own stopgap authorization predicate, internal/adapters/inbound/
-- httpapi/planauthz.go's canActOnPlan) -- nothing populates participants
-- yet (§8.11's own "distinct, not-yet-scoped concern"), so this always
-- returns false today; queried defensively anyway so canActOnPlan is
-- already correct the moment a future Step starts writing rows here, with
-- no change needed at this call site.

-- name: ParticipantExists :one
SELECT EXISTS (SELECT 1 FROM participants WHERE session_id = $1 AND user_id = $2);
