-- Queries backing PromptTemplateStore (§18.6: DB-backed, editable prompt
-- templates -- migrations/000033_intent_classifier.up.sql's own prompt_templates
-- table). Deliberately minimal per §18.6's own scope note: no versioning/
-- audit history this Step, only the later Settings -> Prompt templates
-- Step (§12.2 item 5) needs that.

-- name: GetPromptTemplate :one
-- Fetches one named template by its own stable, human-chosen key (e.g.
-- "intent_classifier_system", seeded by this Step's own migration).
-- pgx.ErrNoRows means no template exists under that name -- the caller
-- (internal/app/intentclassifier) treats that as a genuine, real failure
-- of its own Classify call (mapped to FallbackReasonAPIError, §18.1's
-- own never-throw contract), never a panic.
SELECT * FROM prompt_templates
WHERE name = $1;

-- name: UpsertPromptTemplate :one
-- Creates or overwrites a named template's own text, bumping updated_at.
-- No versioning/audit trail (§18.6's own explicit scope note) -- this
-- Step only needs ONE current version of each template to exist; a
-- caller (validated via internal/domain/intent.ValidateTemplate BEFORE
-- calling this, at save time -- never inside this query itself) is
-- responsible for rejecting an unknown-placeholder template before it
-- ever reaches here.
INSERT INTO prompt_templates (name, template)
VALUES ($1, $2)
ON CONFLICT (name) DO UPDATE
SET template = EXCLUDED.template, updated_at = now()
RETURNING *;

-- name: ListPromptTemplates :many
-- §12.2 item 5: the first standalone READ over every
-- prompt_templates row, ordered by name -- the Settings -> Prompt
-- templates screen's own list data source. Adds no write path; Upsert
-- above is unchanged.
SELECT * FROM prompt_templates
ORDER BY name;
