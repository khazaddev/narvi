-- Step 36 ("intent classifier", §8.3/§18): two additions.
--
-- 1) sessions.intent_decision: a nullable JSONB column holding the whole
-- §18.4 IntentDecisionRecord shape (internal/domain/intent.
-- IntentDecisionRecord), MINUS session_id (redundant with this row's own
-- PK, per §18.4's own instruction). Persisted write-once via a guarded
-- UPDATE ("UPDATE sessions SET intent_decision = ... WHERE
-- intent_decision IS NULL") -- NOT read-then-write, first decision wins,
-- no application-level lock needed (§18.4). Nullable because not every
-- session has been classified yet at the moment this migration runs, and
-- because "not yet decided" is a real, valid state for a brand-new
-- session between creation and its own decided_at_stage's own real point.
ALTER TABLE sessions ADD COLUMN intent_decision JSONB;

-- 2) prompt_templates: the DB-backed, editable prompt-template storage
-- §18.6 calls for ("no prior art exists ... designed from scratch when
-- Step 36 is implemented"). Deliberately simple, per §18.6's own scope
-- note: a name/key, the template text, updated_at -- versioning/audit
-- history is explicitly NOT required by this Step, only by the later
-- Settings UI Step (§12.2 item 5). `name` is the PK (a short, stable,
-- human-chosen key an admin/operator references, e.g.
-- "intent_classifier_system"), not a surrogate UUID -- there is exactly
-- one row per distinct template purpose, and a stable, readable key is
-- more useful than an opaque id for something a human is expected to
-- open directly in Settings -> Prompt templates once that UI exists.
CREATE TABLE prompt_templates (
    name       TEXT PRIMARY KEY,
    template   TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed exactly one default template row (§18.6: "Seed exactly one default
-- template row via the new migration so the classifier has something real
-- to assemble against out of the box") -- the classifier's own system
-- prompt, using this Step's own chosen "{{variable_name}}" placeholder
-- syntax (internal/domain/intent.AssembleTemplate). {{surface}} is the
-- one variable internal/app/intentclassifier supplies at assembly time
-- (the calling ingress surface's own spawn_source value). The confidence
-- rubric itself is deliberately NOT templated in here: it lives ONLY at
-- the confidence field's own schema-description level
-- (internal/app/intentclassifier/schema.go, intentdomain.ConfidenceRubric)
-- per §18.2 ("lives at the field-description level ... not floated
-- separately in a system prompt") -- duplicating it into this seeded
-- prompt text as well would let the two copies drift if this row is ever
-- edited independently via the future Settings -> Prompt templates UI.
INSERT INTO prompt_templates (name, template) VALUES (
    'intent_classifier_system',
    'You are the routing classifier for an internal engineering-coordination platform. Classify the user''s input along two independent dimensions and respond ONLY via the structured output schema provided:

- target: "review" if the input is asking for a review of existing/already-submitted work; "request" if it is asking for new work or a change to be made.
- mode: "plan" if the input asks for up-front discussion/planning before any code changes; "build" if it asks for direct implementation.
- confidence: assign a confidence level as defined by the schema''s own field description for this response.
- reasoning: a brief (one or two sentence) explanation of why you chose this target/mode, grounded in the input text itself.

This request originates from the "{{surface}}" ingress surface.'
);
