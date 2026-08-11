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

-- name: ListPlansForSession :many
-- Audit-fix batch (completeness/discoverability, M3): backs GET
-- /api/sessions/:id/plans (internal/adapters/inbound/httpapi/plans.go) --
-- the endpoint that closes the "Step 37 shipped approve/reject with no way
-- for a web client to ever discover a planId to approve" gap. Unlike
-- ListPlanSummariesForSession above (an internal, minimal shape feeding
-- internal/domain/plan's own NextVersion/ShouldSupersede), this returns
-- every FULL plan row for sessionID -- ordered by version for the same
-- "natural, human-debuggable order" reason -- so the handler can map each
-- one to restdtos.Plan (contracts/rest/v1/dtos.schema.json) for a real,
-- versioned v1->v2 history view.
SELECT * FROM plans
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

-- Step 38 ("plan mode, cross-channel", §8.1/§13.3) additions.
--
-- GetPlan backs httpapi.DecidePlanOnTx's own post-guarded-UPDATE re-fetch
-- (decideplan.go): whether THIS call's own guarded UPDATE won or lost, it
-- needs the plan's own CURRENT row -- on a win, to read back
-- slack_channel_id/slack_message_ts for the cross-channel notify step; on a
-- loss, to report the plan's own actual current status honestly (already
-- approved/rejected/superseded by whichever OTHER entry point won) rather
-- than a bare "conflict".
--
-- SetPlanSlackMessageRef backs internal/app/outboxworker's own Slack
-- plan-approval notifier: once (and only once) a real chat.postMessage call
-- for this plan version succeeds, the message's own real channel+ts (Slack's
-- own response, never invented/derived) is persisted so a LATER decision
-- (from any entry point) can chat.update that exact message.

-- name: GetPlan :one
SELECT * FROM plans WHERE id = $1;

-- name: SetPlanSlackMessageRef :exec
UPDATE plans SET slack_channel_id = $2, slack_message_ts = $3 WHERE id = $1;

-- name: ListAwaitingApprovalPlans :many
-- Step 60 ("decision inbox: read model + API", §16.1)'s own
-- awaiting_approval row source: every plan still 'awaiting_approval',
-- joined with its own session for the (created_by, title) the app-layer
-- aggregator needs both to render the row and to resolve own/joined RBAC
-- (mirrors httpapi.canActOnPlan's own identical CreatedBy check,
-- planauthz.go -- the OTHER half, a participants-table "joined" check, is
-- a separate per-session query this one does not embed, since a plan's
-- own UNIQUE-per-session "at most one awaiting_approval row" invariant
-- (plans_one_awaiting_approval_per_session) already keeps this result set
-- small). System-wide, unscoped by user -- this Step's own read model
-- resolves per-user ELIGIBILITY at the app layer, not in this query.
-- Ordered oldest-first (plans.created_at) -- the SAME "entered the queue"
-- instant the app-layer aggregator's own ranking needs, with no further
-- derivation required.
SELECT
    plans.id, plans.session_id, plans.turn_id, plans.version, plans.status, plans.plan_model_id,
    plans.created_at, plans.decided_at, plans.decided_by, plans.slack_channel_id, plans.slack_message_ts,
    sessions.created_by AS session_created_by,
    sessions.title AS session_title
FROM plans
JOIN sessions ON sessions.id = plans.session_id
WHERE plans.status = 'awaiting_approval'
ORDER BY plans.created_at;

-- name: ListRecentlyDecidedPlans :many
-- Step 60 ("decision inbox: read model + API", §16.2)'s own decision-
-- latency metric input for the plan-approval half of that computation
-- (median time from an item ENTERING the queue -- a plan's own
-- created_at -- to its ACTION -- decided_at): every plan decided (approved
-- or rejected, from ANY entry point -- web/Slack/Linear all funnel
-- through the SAME guarded UPDATE, decideplan.go) at or after $1, bounded
-- by $2 (§21.1's own "bounded from day one" discipline -- an explicit
-- recent window, never an unbounded historical scan).
SELECT * FROM plans
WHERE status IN ('approved', 'rejected') AND decided_at >= $1
ORDER BY decided_at DESC
LIMIT $2;
