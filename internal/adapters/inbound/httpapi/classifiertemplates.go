// This file (classifiertemplates.go) closes an audit finding (M5): the
// prompt-template preview/upsert backend was never built. postgres.
// PromptTemplateStore's own Upsert method (prompttemplate_store.go) has
// existed, fully implemented, since §8.3 ("intent classifier", §18.6)
// -- but until this file, NOTHING in the entire codebase ever called it,
// and there was no way for an admin to see what a draft template
// assembles to before saving it either. This file adds exactly those two
// capabilities: a stateless PREVIEW (assembles a draft template's text
// against real variable values, without ever touching Postgres) and an
// UPSERT (validates, then persists via the already-existing store
// method).
//
// Both endpoints are gated by authz.ActionActivatePromptTemplate --
// domain/authz's own §13.3 row-6 action ("prompt-template activation",
// admin only), which likewise existed with its own passing RBAC-matrix
// tests (authorize_test.go) but had ZERO callers anywhere in the HTTP
// layer before this file. Reused as-is rather than inventing a new
// Action: row 6's own real-world meaning today, until some later Step
// adds an actual enabled/disabled column (§18.6's own explicit non-scope
// note -- prompt_templates has no such column, see
// prompttemplate_store.go's own doc comment), IS upserting/previewing a
// template's own content.
//
// Design decision (documented per this batch's own plan): both handlers
// below resolve a template's ALLOWED placeholder-variable NAMES from a
// small, server-side map (knownTemplateVars below) keyed by template
// name, rather than trusting a client-supplied allow-list in the request
// body. A client can never widen its own validation by passing a more
// permissive "vars" list than the server actually allows for that name --
// matching this codebase's own general preference for server-side
// enforcement over client-supplied validation parameters. Exactly one
// entry exists today (intent_classifier_system's own real "surface"
// variable -- internal/app/intentclassifier/schema.go's own
// templateNameSystem constant; unexported there, so re-declared as a
// plain literal below rather than reached for across that package
// boundary) -- extending this map is how a future template purpose
// gains its own real placeholder variable(s), never a change to either
// handler itself.
//
// A name this map has no entry for (knownTemplateVars[name] then simply
// being Go's own nil-slice zero value) is NOT rejected outright -- it is
// treated as "this name has zero allowed placeholder variables," exactly
// like an explicit empty list would be. Concretely: a brand-new template
// name whose text contains no "{{...}}" placeholder at all validates
// (and previews/upserts) successfully, since there is nothing in it for
// an allow-list to reject; a brand-new name whose text DOES reference
// any placeholder is rejected, since nothing has ever declared that
// placeholder allowed for that name. This is what lets an admin start
// curating a genuinely new template row ahead of the future code that
// will consume it (no different, in effect, from Postgres itself never
// having required prompt_templates.name to belong to any fixed enum),
// while still never letting a CLIENT (via either name OR a
// placeholder's own text) grant itself a substitution variable this
// server hasn't already decided is real for that name.
//
// Routes (mounted by cmd/control-plane/main.go, behind auth.Middleware,
// like every other route in this package):
//
//   - POST /api/intent-templates/preview -- PreviewIntentTemplate
//   - POST /api/intent-templates          -- UpsertIntentTemplate

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/auditlog"
	"github.com/khazaddev/narvi/internal/domain/authz"
	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
	"github.com/khazaddev/narvi/internal/platform"
)

// intentClassifierSystemTemplateName mirrors internal/app/intentclassifier/
// schema.go's own unexported templateNameSystem constant EXACTLY (same
// literal, "intent_classifier_system") -- re-declared here rather than
// imported since that constant is unexported and this package has no
// business reaching across an app-layer package boundary for an
// unexported symbol.
const intentClassifierSystemTemplateName = "intent_classifier_system"

// knownTemplateVars maps a prompt_templates.name to the fixed set of
// placeholder-variable names ValidateTemplate/AssembleTemplate treat as
// allowed for that name -- see this file's own top doc comment for why
// this lives server-side rather than in the request body. "surface" is
// the one real variable intentclassifier's own Service.Classify supplies
// at assembly time (the calling ingress surface's own spawn_source
// value) -- migrations/000033_intent_classifier.up.sql's own seeded
// template text is the one place this exact placeholder is used today.
var knownTemplateVars = map[string][]string{
	intentClassifierSystemTemplateName: {"surface"},
}

// intentTemplatePreviewRequest is POST /api/intent-templates/preview's own
// hand-written request DTO -- no /contracts codegen migration this batch
// (a clean, hand-written Go struct first, schema-first DTO migration a
// distinct, later concern -- see this batch's own scope note). Name
// selects which known template's allowed-vars set (knownTemplateVars)
// governs Template's own placeholder validation; Vars supplies the
// actual preview-time substitution VALUES. Vars is never validated
// against any allow-list itself -- nothing this endpoint does is ever
// persisted, so an over-permissive Vars key is harmless:
// intentdomain.AssembleTemplate only ever looks up a key some placeholder
// literally present in Template references.
type intentTemplatePreviewRequest struct {
	Name     string            `json:"name"`
	Template string            `json:"template"`
	Vars     map[string]string `json:"vars"`
}

// intentTemplatePreviewResponse is PreviewIntentTemplate's own success
// body: the exact final prompt string intentdomain.AssembleTemplate
// produced from the caller's draft text.
type intentTemplatePreviewResponse struct {
	Assembled string `json:"assembled"`
}

// PreviewIntentTemplate backs POST /api/intent-templates/preview:
// admin-only (authz.ActionActivatePromptTemplate). Never touches
// Postgres in any way -- Template is the caller's own DRAFT text, exactly
// as they are currently editing it, never read from or written to
// prompt_templates. Outcome:
//
//  1. Not authorized -> 403 (authorize()'s own standard body).
//  2. Malformed JSON body -> 400.
//  3. Name is empty -> 400 ("name is required") -- prompt_templates.name
//     is a TEXT PRIMARY KEY; an empty string is a degenerate value no
//     real caller ever means to send.
//  4. intentdomain.ValidateTemplate(Template, knownTemplateVars[Name])
//     fails (Template references a placeholder that name's own allowed
//     set -- empty, for a Name knownTemplateVars has no entry for --
//     doesn't cover) -> 400 with the *intentdomain.
//     UnknownPlaceholderError detail.
//  5. intentdomain.AssembleTemplate(Template, Vars) fails (Template
//     references an ALLOWED placeholder Vars itself doesn't supply a
//     preview value for) -> 400 with the same error detail -- a
//     distinct failure mode from step 4 (an allowed-but-unsupplied
//     placeholder, not a disallowed one), surfaced via the SAME
//     *intentdomain.UnknownPlaceholderError type either way (see
//     template.go's own AssembleTemplate doc comment).
//  6. Otherwise -> 200 with intentTemplatePreviewResponse{Assembled: the
//     real assembled string}.
func PreviewIntentTemplate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionActivatePromptTemplate, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req intentTemplatePreviewRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		// knownTemplateVars[req.Name] is nil (an empty allowed-vars set,
		// not a lookup failure worth its own branch) for any name this
		// map has no entry for -- see this file's own top doc comment for
		// why that is deliberate, not an oversight.
		if err := intentdomain.ValidateTemplate(req.Template, knownTemplateVars[req.Name]); err != nil {
			var unknownErr *intentdomain.UnknownPlaceholderError
			if errors.As(err, &unknownErr) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			logger.Error("httpapi: preview intent template: validate failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		assembled, err := intentdomain.AssembleTemplate(req.Template, req.Vars)
		if err != nil {
			var unknownErr *intentdomain.UnknownPlaceholderError
			if errors.As(err, &unknownErr) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			logger.Error("httpapi: preview intent template: assemble failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, intentTemplatePreviewResponse{Assembled: assembled})
	}
}

// intentTemplateUpsertRequest is POST /api/intent-templates's own
// hand-written request DTO (see intentTemplatePreviewRequest's own doc
// comment on why hand-written, not /contracts). No Vars field here --
// unlike Preview, Upsert never assembles anything; it only validates
// Template's own placeholders against Name's known allowed set and, on
// success, persists Template verbatim.
type intentTemplateUpsertRequest struct {
	Name     string `json:"name"`
	Template string `json:"template"`
}

// intentTemplateDTO is UpsertIntentTemplate's own success response shape
// -- prompt_templates' three real columns (migrations/
// 000033_intent_classifier.up.sql), no version/audit-trail fields since
// none exist (§18.6's own explicit scope note; see prompttemplate_store.
// go's own doc comment).
type intentTemplateDTO struct {
	Name      string    `json:"name"`
	Template  string    `json:"template"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// UpsertIntentTemplate backs POST /api/intent-templates: admin-only
// (authz.ActionActivatePromptTemplate). Outcome:
//
//  1. Not authorized -> 403.
//  2. Malformed JSON body -> 400.
//  3. Name is empty -> 400 ("name is required") -- checked BEFORE any
//     Postgres access.
//  4. intentdomain.ValidateTemplate(Template, knownTemplateVars[Name])
//     fails -> 400 with the *intentdomain.UnknownPlaceholderError detail
//     -- still before any Postgres access, exactly matching
//     prompttemplate_store.go's own Upsert doc comment: "Callers MUST
//     validate template ... before calling this". knownTemplateVars[Name]
//     is nil (an empty allowed set) for a Name this map has no entry
//     for -- see this file's own top doc comment for why that lets a
//     genuinely NEW, placeholder-free Name through rather than being
//     rejected outright.
//  5. templates.WithTx(tx).Upsert(ctx, Name, Template) fails -> 500.
//  6. Otherwise -> 200 with intentTemplateDTO{the real row Upsert
//     returned}, and an audit_log entry ("prompt_template.upserted")
//     recorded in the SAME transaction as the write -- mirrors this
//     package's own members.go precedent for every other admin-only
//     state change (UpdateMemberRole/LinkMemberIdentity/
//     UnlinkMemberIdentity all record one the same way).
//
// Always 200, never 201: detecting "this Name is brand new" vs
// "this Name already existed" would need an extra SELECT before the
// upsert itself, purely to pick a status code -- prompt_templates'
// own ON CONFLICT DO UPDATE (queries/prompt_templates.sql) already makes
// both cases behave identically from this handler's own point of view
// (one row, upserted), so this handler does too.
//
// The REAL schema behavior an audit finding flagged as a discrepancy
// from the plan's own summary: prompt_templates has no version history
// or "disabled" concept at all (migrations/000033_intent_classifier.up.
// sql's own "name TEXT PRIMARY KEY, template TEXT, updated_at" -- three
// columns, no more) -- a fresh Name creates a new row; an EXISTING Name's
// own template/updated_at are simply overwritten in place. This handler
// makes no attempt to invent versioning/disabling that does not exist.
func UpsertIntentTemplate(pool *pgxpool.Pool, templates *postgres.PromptTemplateStore, auditLog *postgres.AuditLogStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionActivatePromptTemplate, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		actorUserID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req intentTemplateUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body")
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		if err := intentdomain.ValidateTemplate(req.Template, knownTemplateVars[req.Name]); err != nil {
			var unknownErr *intentdomain.UnknownPlaceholderError
			if errors.As(err, &unknownErr) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			logger.Error("httpapi: upsert intent template: validate failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: upsert intent template: begin tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		row, err := templates.WithTx(tx).Upsert(ctx, req.Name, req.Template)
		if err != nil {
			logger.Error("httpapi: upsert intent template: upsert failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := auditlog.Record(ctx, auditLog.WithTx(tx), actorUserID, "prompt_template.upserted", "prompt_template", req.Name, map[string]any{
			"name": req.Name,
		}); err != nil {
			logger.Error("httpapi: upsert intent template: record audit log failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: upsert intent template: commit tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, intentTemplateDTO{
			Name:      row.Name,
			Template:  row.Template,
			UpdatedAt: row.UpdatedAt.Time,
		})
	}
}

// ListPromptTemplates backs GET /api/intent-templates (§12.2 item 5): the
// Settings -> Prompt templates screen's own list data source, closing the
// same "the write side had no way to be discovered" gap
// PreviewIntentTemplate/UpsertIntentTemplate above already exist for the
// write side of this table. Admin-only (authz.
// ActionActivatePromptTemplate), the SAME action Preview/Upsert already
// use -- one action gates every endpoint of this table's own small
// management surface. Unlike UpsertIntentTemplate's own hand-written
// intentTemplateDTO response shape (this file's own top doc comment: "no
// /contracts codegen migration this batch"), this NEW endpoint returns
// the /contracts-generated restdtos.PromptTemplate -- structurally
// identical (name/template/updatedAt), so both endpoints stay
// wire-compatible with the SAME frontend type despite one predating the
// other's schema entry.
func ListPromptTemplates(templates *postgres.PromptTemplateStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorize(w, r, authz.ActionActivatePromptTemplate, authz.Resource{}) {
			return
		}
		ctx := r.Context()
		logger := platform.Logger(ctx)

		rows, err := templates.List(ctx)
		if err != nil {
			logger.Error("httpapi: list prompt templates failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		wire := make([]restdtos.PromptTemplate, len(rows))
		for i, row := range rows {
			wire[i] = restdtos.PromptTemplate{
				Name:      row.Name,
				Template:  row.Template,
				UpdatedAt: row.UpdatedAt.Time,
			}
		}
		writeJSON(w, http.StatusOK, restdtos.ListPromptTemplatesResponse{PromptTemplates: wire})
	}
}
