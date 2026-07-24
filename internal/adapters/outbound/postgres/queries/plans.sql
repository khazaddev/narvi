-- Queries backing PlanStore (Step 37, "plan mode, web", §8.1/§12.2 item
-- 3), migrations/000034_plan_mode.up.sql.
--
-- CreatePlan/SupersedePlan/ListPlanSummariesForSession back
-- internal/app/sessionactor's own plan-row-creation hook (planrecord.go):
-- ListPlanSummariesForSession feeds internal/domain/plan's pure
-- NextVersion/ShouldSupersede functions; SupersedePlan is called (in the
-- SAME transaction, before CreatePlan) for every plan id
-- ShouldSupersede's own output names.
--
-- ApprovePlanIfAwaitingApproval/RejectPlanIfAwaitingApproval are the
-- guarded conditional updates backing POST .../plans/:planId/approve|
-- reject (internal/adapters/inbound/httpapi/planapprove.go): "WHERE ...
-- status = 'awaiting_approval'" makes first-verdict-wins a real,
-- race-safe DB guarantee -- a losing concurrent caller (already decided
-- by someone else, or a stale/wrong plan id) simply affects zero rows,
-- observed via :execrows, never a partial or corrupting write.

-- name: CreatePlan :one
INSERT INTO plans (session_id, turn_id, version, status, plan_model_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: SupersedePlan :exec
-- Guarded by "AND status = 'awaiting_approval'" even though every real
-- caller (planrecord.go) only ever passes an id ShouldSupersede itself
-- already filtered to StatusAwaitingApproval -- belt-and-suspenders
-- against ever superseding an already-decided (approved/rejected) row,
-- matching this migration's own doc comment ("only an awaiting_approval
-- row ever gets superseded").
UPDATE plans SET status = 'superseded'
WHERE id = $1 AND status = 'awaiting_approval';

-- name: ListPlanSummariesForSession :many
-- The minimal shape internal/domain/plan.Summary needs -- ordered by
-- version so NextVersion's own "highest existing version" scan is
-- reading them in a natural, human-debuggable order (not required for
-- correctness, NextVersion scans the whole slice regardless).
SELECT id, version, status FROM plans
WHERE session_id = $1
ORDER BY version;

-- name: ApprovePlanIfAwaitingApproval :execrows
UPDATE plans
SET status = 'approved', decided_at = now(), decided_by = $3
WHERE id = $1 AND session_id = $2 AND status = 'awaiting_approval';

-- name: RejectPlanIfAwaitingApproval :execrows
UPDATE plans
SET status = 'rejected', decided_at = now(), decided_by = $3
WHERE id = $1 AND session_id = $2 AND status = 'awaiting_approval';
