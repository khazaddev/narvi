-- Step 64 ("plan mode: follow-up intent classification (amend vs answer)",
-- §23): two additions, mirroring migrations/000033_intent_classifier.up.sql's
-- own "schema + seed in one migration" precedent.
--
-- 1) turns.answer_only: Step 64's own persisted plan_followup
-- classification result (§23.2: "the classification result is persisted
-- as an answer_only flag on the turn/message row"). Deliberately NULLABLE,
-- no DEFAULT, no DB-level COMMENT ON COLUMN -- mirrors sessions.
-- build_model_id/build_effort (migrations/000034_plan_mode.up.sql,
-- migrations/000063_turn_session_effort.up.sql) and sessions.
-- epistemic_check_enabled (migrations/000066_builder_epistemic_check.up.sql)'s
-- own established "plain nullable column, no DEFAULT" shape -- that
-- migration's own doc comment records the verification this Step reuses
-- verbatim: "turns.plan_mode itself has no such [nullable-override]
-- threading, but sessions.build_model_id/build_effort ... already
-- establish exactly this shape."
--
-- NULL means plan_followup classification never ran for this turn at all:
-- planMode was already true (a revise:-prefixed reply, a Slack "Request
-- changes" modal submission, or any other explicit plan_mode=true caller
-- -- §23 intro, "the revise: prefix stays as a deterministic override
-- that bypasses classification entirely"), no plan was awaiting_approval
-- for this session at turn-creation time, or the classifier/its
-- dependencies were unavailable at the moment httpapi.createTurnLocked's
-- own unlocked pre-check ran (§23.3's own fail-open floor -- the LOCKED
-- re-check inside that same function still enforces safely regardless).
--
-- FALSE is the only real value this column can ever hold in practice --
-- and that is intentional, not an oversight. httpapi.createTurnLocked's
-- own awaiting-plan gate (turn.go) declines turn creation outright (409
-- ErrPlanAwaitingApproval, no row inserted at all) whenever the
-- classifier reports -- or fails open toward -- "answer"; so a TRUE
-- verdict, by construction, never ends up on an actual persisted turn
-- row, since there IS no row when that happens. FALSE, when present, is
-- Step 64's own positive signal: the classifier confidently read this
-- reply as requesting a plan change, so this turn was promoted to a real
-- plan-revision turn (plan_mode := true) exactly like a revise:-prefixed
-- reply already is (internal/domain/plan.RevisePrefix's own doc comment,
-- migrations/000034_plan_mode.up.sql).
ALTER TABLE turns ADD COLUMN answer_only BOOLEAN;

-- 2) prompt_templates: Step 64's own seeded template row for the
-- plan_followup classification category (§23.1) -- a SECOND row in the
-- table migrations/000033_intent_classifier.up.sql created, one per
-- distinct classification category (that migration's own doc comment:
-- "there is exactly one row per distinct template purpose"). No
-- "{{variable_name}}" placeholder at all (internal/app/intentclassifier's
-- own ClassifyPlanFollowup, unlike Classify, has no per-ingress-surface
-- value of its own to substitute in here -- it is called identically from
-- every ingress path). The confidence rubric itself is deliberately NOT
-- templated in here either, for the exact same reason
-- migrations/000033's own seeded row omits it: it lives ONLY at the
-- confidence field's own schema-description level
-- (internal/app/intentclassifier/schema_planfollowup.go,
-- intentdomain.ConfidenceRubric), never floated separately in a system
-- prompt (§18.2).
INSERT INTO prompt_templates (name, template) VALUES (
    'intent_classifier_plan_followup',
    'You are the routing classifier for an internal engineering-coordination platform. A plan is currently awaiting a human''s approval, rejection, or revision request. Classify the user''s reply along one dimension and respond ONLY via the structured output schema provided:

- target: "amend" if the reply is requesting a change to the plan (asking for something different, additional, or corrected before it proceeds) -- treat this exactly like a "revise:" instruction would be treated; "answer" if the reply does not request any change to the plan (answering a question, making an observation, chit-chat, or anything else that does not ask for the plan itself to be different).
- confidence: assign a confidence level as defined by the schema''s own field description for this response.
- reasoning: a brief (one or two sentence) explanation of why you chose this target, grounded in the input text itself.

When genuinely unsure whether the reply requests a change, prefer lower confidence rather than guessing "amend" -- a human will be asked to clarify at low confidence, rather than an unapproved plan silently being revised or a build silently dispatched against it.'
);
