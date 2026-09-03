// This file (workflowdefinitions.go) implements §25.10's ("wire
// contracts", §25.10) own definition-authoring routes -- the surface
// §25.4 shipped dark and §25.10's own routes amendment specifies:
// GET /api/workflow-definitions (list), GET /api/workflow-
// definitions/{id} (one whole document), POST /api/workflow-definitions
// (create a draft, or duplicate an existing definition), PUT
// /api/workflow-definitions/{id} (replace steps/edges wholesale), DELETE
// /api/workflow-definitions/{id}.
//
// All five routes are gated by authz.ActionManageWorkflowDefinitions
// (maintainer+, §25.11) -- the SAME row as authz.ActionManageAutomations.
// This is deliberately NOT the action that makes a definition live
// anywhere; that is authz.ActionActivateWorkflowBinding (admin-only,
// workflowbindings.go), a completely separate route group.
//
// # The load-bearing part: two structural refusals, plus a third this
// # Step adds
//
// §25.11's own amendment states the premise PUT/DELETE must honor: a
// maintainer-level "manage workflow definitions" action is only safe
// because it edits an UNBOUND draft with no effect on production until an
// admin activates it. As built, resolveBinding resolves (lane, repo) to a
// binding and LoadDefinition then reads the definition BY ID ALONE,
// never consulting workflow_bindings.definition_version -- so editing a
// definition that is already bound would change production dispatch
// immediately, with no admin involved. refusalReasonForMutation (below)
// closes this: PUT/DELETE on a definition referenced by ANY
// workflow_bindings row are refused UNCONDITIONALLY, exactly like
// is_built_in -- a STRUCTURAL invariant, never an RBAC row, so an admin
// gets the identical refusal a maintainer does. The path through is:
// duplicate (CreateWorkflowDefinition's own {sourceDefinitionId, name}
// mode, below) -> edit the copy -> an admin activates it
// (workflowbindings.go).
//
// A THIRD guard is added here, beyond the two §25.10/§25.11 name by
// word: workflow_runs.workflow_definition_id and workflow_step_runs.
// step_definition_id are both plain NO ACTION references (migration
// 000057_workflows.up.sql: "history outlives configuration"), so a
// definition that has EVER run cannot have its steps deleted-and-
// reinserted (PUT) or the row itself deleted (DELETE) without a raw
// FK-violation 500 -- reachable even when the definition is CURRENTLY
// unbound (an admin can rebind a lane to a duplicate, freeing the OLD
// definition's own workflow_bindings row while its workflow_runs history
// remains behind). Refused with its own distinct message, the same
// "validate first, name which rule broke" discipline the other two
// guards already follow (§25.10: "A constraint violation surfacing as a
// 500 is a defect").
//
// Each of the three refusals gets its OWN caller-facing message -- never
// collapsed into one generic "cannot edit" string -- so an operator knows
// whether to duplicate the definition (built-in, or has run history) or
// ask an admin to unbind it first (bound). All three render as 409
// Conflict: the request is well-formed and the caller is authorized, but
// refused because of the definition's own current state, the same class
// of refusal DecideWorkflowStep's "already decided" case already uses
// 409 for.
//
// # Duplication is deep, and the escape hatch
//
// POST /api/workflow-definitions accepts EITHER a whole new document
// (lane+name+steps) OR a {sourceDefinitionId, name} pair. The duplicate
// path deep-copies every step and every edge, always landing
// is_built_in=false, unbound, at version 1 -- whatever it was copied
// from, built-in or custom (§25.10). Steps in whole-document mode carry
// CLIENT-SUPPLIED ids (a canvas editor's own locally-generated uuid for a
// brand-new node), inserted verbatim; a duplicated definition's steps
// instead get SERVER-GENERATED ids, since reusing the source's own ids
// would collide with it.
//
// # Validation belongs to the save
//
// Every write (whole-document create, PUT) re-validates the resulting
// document against internal/domain/workflow.ValidateDefinition's closed
// model -- ordered steps, edges keyed only on the 3 StepOutcomeStatus
// values, every toStepId resolving inside this same definition, step
// order positive/unique, prompt_template non-empty -- BEFORE anything is
// written, returning a 400 naming which rule broke rather than letting a
// DB constraint violation surface as a 500 (§25.10).

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/domain/workflow"
	"github.com/narvidev/narvi/internal/platform"
)

// parseWorkflowDefinitionID parses chi's own "id" URL path param as a
// UUID -- mirrors parseWorkflowRunID/parseWorkflowStepRunID's own exact
// shape (decideworkflowstep.go).
func parseWorkflowDefinitionID(w http.ResponseWriter, r *http.Request) (pgtype.UUID, bool) {
	raw := chi.URLParam(r, "id")
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		writeError(w, http.StatusBadRequest, "malformed workflow definition id")
		return pgtype.UUID{}, false
	}
	return id, true
}

// workflowEdgeToDTO/workflowStepDefinitionToDTO/workflowDefinitionToDTO
// assemble the wire WorkflowDefinition shape from plain sqlcgen rows --
// the SAME "read plain rows, assemble the domain/wire type in the app
// layer" split workflowengine.LoadDefinition already applies for the
// domain/workflow.Definition value (definition.go); this is that same
// three-query composition, rendered as the REST DTO instead.
// stepEdges returns a step's outgoing edges as a slice that always marshals
// as a JSON array, never as `null` -- see its call site for why the bare map
// lookup was wrong.
func stepEdges(edgesByStep map[string][]restdtos.WorkflowEdge, stepID string) []restdtos.WorkflowEdge {
	if found, ok := edgesByStep[stepID]; ok {
		return found
	}
	return []restdtos.WorkflowEdge{}
}

// editRefusalFor renders the SAME verdict refusalReasonForMutation enforces on
// the write path, onto the read shape, so a client presents a decision instead
// of re-deriving the rules. An editor that re-derived them would carry a second
// copy of the refusal logic and of its wording, and the two would drift; and
// the run-history reason is not derivable from this shape at all, so without it
// an editor only learns a definition is frozen by failing to save it, after the
// operator has already done the work.
//
// Order matches refusalReasonForMutation exactly (§25.11): built-in first, then
// bound, then run history. All three apply regardless of role.
func editRefusalFor(def sqlcgen.WorkflowDefinition, isBound, hasRuns bool) *restdtos.WorkflowDefinitionEditRefusal {
	switch {
	case def.IsBuiltIn:
		return &restdtos.WorkflowDefinitionEditRefusal{Value: "built_in"}
	case isBound:
		return &restdtos.WorkflowDefinitionEditRefusal{Value: "bound"}
	case hasRuns:
		return &restdtos.WorkflowDefinitionEditRefusal{Value: "has_runs"}
	default:
		return nil
	}
}

func workflowDefinitionToDTO(def sqlcgen.WorkflowDefinition, steps []sqlcgen.WorkflowStepDefinition, edges []sqlcgen.WorkflowEdge, isBound, hasRuns bool) restdtos.WorkflowDefinition {
	edgesByStep := make(map[string][]restdtos.WorkflowEdge, len(edges))
	for _, e := range edges {
		from := e.FromStepID.String()
		edgesByStep[from] = append(edgesByStep[from], restdtos.WorkflowEdge{
			FromStepId: from,
			OnStatus:   restdtos.WorkflowEdgeOnStatus(e.OnStatus),
			ToStepId:   e.ToStepID.String(),
		})
	}

	wireSteps := make([]restdtos.WorkflowStepDefinition, 0, len(steps))
	for _, s := range steps {
		id := s.ID.String()
		wireSteps = append(wireSteps, restdtos.WorkflowStepDefinition{
			Id:                     id,
			Order:                  int(s.StepOrder),
			Kind:                   restdtos.WorkflowStepDefinitionKind(s.Kind),
			ModelId:                restdtos.WorkflowStepDefinitionModelId(s.ModelID),
			Effort:                 restdtos.WorkflowStepDefinitionEffort(s.Effort),
			PromptTemplate:         s.PromptTemplate,
			ExecutionScope:         restdtos.WorkflowStepDefinitionExecutionScope(s.ExecutionScope),
			ConversationContinuity: restdtos.WorkflowStepDefinitionConversationContinuity(s.ConversationContinuity),
			HitlBefore:             s.HitlBefore,
			HitlAfter:              s.HitlAfter,
			CanvasPosition:         canvasPositionFromJSON(s.CanvasPosition),
			// stepEdges, never edgesByStep[id] directly: a step with no
			// outgoing edges is absent from the map, the lookup yields a nil
			// slice, and encoding/json renders a nil slice as `null`. The
			// schema declares edges as a required, non-nullable array and the
			// generated TS types it `WorkflowEdge[]`, so `step.edges.map(...)`
			// on the canvas would throw -- and every seeded built-in is a
			// single-step passthrough with zero edges, so this is the common
			// case, not an edge case. Member.identities already cost this
			// codebase the identical finding (members_integration_test.go).
			Edges: stepEdges(edgesByStep, id),
		})
	}

	return restdtos.WorkflowDefinition{
		Id:          def.ID.String(),
		Lane:        restdtos.WorkflowDefinitionLane(def.Lane),
		Name:        def.Name,
		IsBuiltIn:   def.IsBuiltIn,
		EditRefusal: editRefusalFor(def, isBound, hasRuns),
		Version:     int(def.Version),
		Steps:       wireSteps,
		CreatedAt:   def.CreatedAt.Time,
		UpdatedAt:   def.UpdatedAt.Time,
	}
}

// canvasPositionFromJSON decodes workflow_step_definitions.canvas_position's
// own opaque JSONB bytes into the wire {x,y} shape -- nil input (column
// NULL, never saved by any canvas yet) or a decode failure both render as
// nil, the SAME "not yet saved" wire value (§25.10's own doc comment:
// "absent/null means no layout has ever been saved for this step").
func canvasPositionFromJSON(raw []byte) *restdtos.WorkflowStepDefinitionCanvasPosition {
	if len(raw) == 0 {
		return nil
	}
	var pos struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
	}
	if err := json.Unmarshal(raw, &pos); err != nil {
		return nil
	}
	return &restdtos.WorkflowStepDefinitionCanvasPosition{X: pos.X, Y: pos.Y}
}

// canvasPositionToJSON is canvasPositionFromJSON's own inverse -- nil
// input (no layout attached to this step, the ordinary case for every
// built-in and every API-authored definition until a canvas first saves
// one) renders as nil bytes (a NULL column), never an empty object.
func canvasPositionToJSON(pos *restdtos.WorkflowStepDefinitionCanvasPosition) []byte {
	if pos == nil {
		return nil
	}
	b, err := json.Marshal(map[string]float64{"x": pos.X, "y": pos.Y})
	if err != nil {
		// Unreachable: a struct of two float64s always marshals.
		return nil
	}
	return b
}

// definitionDocumentFromRow assembles the full wire WorkflowDefinition
// for def (already-fetched definition row) -- 2 further reads (steps,
// edges). Shared by every handler in this file that already holds a
// definition row from a write (CreateDefinition/UpdateDefinitionNameAndBumpVersion),
// so it never re-fetches the definition row itself.
func definitionDocumentFromRow(ctx context.Context, workflows *postgres.WorkflowStore, def sqlcgen.WorkflowDefinition, isBound, hasRuns bool) (restdtos.WorkflowDefinition, error) {
	steps, err := workflows.ListStepDefinitions(ctx, def.ID)
	if err != nil {
		return restdtos.WorkflowDefinition{}, fmt.Errorf("httpapi: list step definitions: %w", err)
	}
	edges, err := workflows.ListEdgesForDefinition(ctx, def.ID)
	if err != nil {
		return restdtos.WorkflowDefinition{}, fmt.Errorf("httpapi: list edges for definition: %w", err)
	}
	return workflowDefinitionToDTO(def, steps, edges, isBound, hasRuns), nil
}

// loadDefinitionDocument is definitionDocumentFromRow's own "by id"
// twin, for the two handlers below (GetWorkflowDefinition,
// ListWorkflowDefinitions) that do not already hold a definition row.
// Returns pgx.ErrNoRows (unwrapped) when id names no definition.
func loadDefinitionDocument(ctx context.Context, workflows *postgres.WorkflowStore, id pgtype.UUID) (restdtos.WorkflowDefinition, error) {
	row, err := workflows.GetDefinitionWithRefusalFacts(ctx, id)
	if err != nil {
		return restdtos.WorkflowDefinition{}, err
	}
	def := sqlcgen.WorkflowDefinition{
		ID:        row.ID,
		Lane:      row.Lane,
		Name:      row.Name,
		IsBuiltIn: row.IsBuiltIn,
		Version:   row.Version,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
	return definitionDocumentFromRow(ctx, workflows, def, row.IsBound, row.HasRuns)
}

// ListWorkflowDefinitions backs GET /api/workflow-definitions (§25.10):
// every definition, built-in and custom, each carrying its own full
// document.
func ListWorkflowDefinitions(workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionManageWorkflowDefinitions, authz.Resource{}) {
			return
		}

		rows, err := workflows.ListDefinitions(ctx)
		if err != nil {
			logger.Error("httpapi: list workflow definitions failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		out := make([]restdtos.WorkflowDefinition, 0, len(rows))
		for _, row := range rows {
			def := sqlcgen.WorkflowDefinition{
				ID:        row.ID,
				Lane:      row.Lane,
				Name:      row.Name,
				IsBuiltIn: row.IsBuiltIn,
				Version:   row.Version,
				CreatedAt: row.CreatedAt,
				UpdatedAt: row.UpdatedAt,
			}
			doc, err := definitionDocumentFromRow(ctx, workflows, def, row.IsBound, row.HasRuns)
			if err != nil {
				logger.Error("httpapi: assemble workflow definition document failed", "error", err, "definition_id", row.ID.String())
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			out = append(out, doc)
		}

		writeJSON(w, http.StatusOK, restdtos.ListWorkflowDefinitionsResponse{Definitions: out})
	}
}

// GetWorkflowDefinition backs GET /api/workflow-definitions/{id}
// (§25.10): 404 if id names no definition, else 200 with the whole
// document.
func GetWorkflowDefinition(workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionManageWorkflowDefinitions, authz.Resource{}) {
			return
		}
		id, ok := parseWorkflowDefinitionID(w, r)
		if !ok {
			return
		}

		doc, err := loadDefinitionDocument(ctx, workflows, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "workflow definition not found")
				return
			}
			logger.Error("httpapi: get workflow definition failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		writeJSON(w, http.StatusOK, doc)
	}
}

// stepDefinitionsToDomain converts the wire steps slice (a request
// body's own input shape) into internal/domain/workflow's own value
// types, for internal/domain/workflow.ValidateDefinition to judge --
// mirrors workflowengine.LoadDefinition's identical field-by-field
// conversion, just from the wire DTO instead of a sqlcgen row.
func stepDefinitionsToDomain(steps []restdtos.WorkflowStepDefinition) []workflow.StepDefinition {
	out := make([]workflow.StepDefinition, 0, len(steps))
	for _, s := range steps {
		edges := make([]workflow.Edge, 0, len(s.Edges))
		for _, e := range s.Edges {
			edges = append(edges, workflow.Edge{
				FromStepID: workflow.ID(e.FromStepId),
				OnStatus:   workflow.StepOutcomeStatus(e.OnStatus),
				ToStepID:   workflow.ID(e.ToStepId),
			})
		}
		out = append(out, workflow.StepDefinition{
			ID:                     workflow.ID(s.Id),
			Order:                  s.Order,
			Kind:                   workflow.StepKind(s.Kind),
			ModelID:                (*string)(s.ModelId),
			Effort:                 (*string)(s.Effort),
			PromptTemplate:         s.PromptTemplate,
			ExecutionScope:         workflow.ExecutionScope(s.ExecutionScope),
			ConversationContinuity: workflow.ConversationContinuity(s.ConversationContinuity),
			HITLBefore:             s.HitlBefore,
			HITLAfter:              s.HitlAfter,
			Edges:                  edges,
		})
	}
	return out
}

// validateStepsForWrite validates steps against internal/domain/workflow's
// closed model (§25.10: "Validation belongs to the save, not to the
// canvas") -- shared by CreateWorkflowDefinition's whole-document path
// and PutWorkflowDefinition below, the two routes that ever write a
// definition's own steps/edges. Checks each step id is a well-formed
// uuid FIRST (workflow.ValidateDefinition treats ids as opaque strings
// and cannot itself catch a malformed one -- these ids become real
// workflow_step_definitions primary keys), then delegates everything
// else -- ordered steps, edges keyed only on the 3 StepOutcomeStatus
// values, every toStepId resolving inside this same definition, step
// order positive/unique, prompt_template non-empty -- to
// ValidateDefinition itself, never restating its rules here. Returns a
// caller-facing message naming which rule broke, or "" when steps is
// executable.
func validateStepsForWrite(lane workflow.Lane, steps []restdtos.WorkflowStepDefinition) string {
	for _, s := range steps {
		var u pgtype.UUID
		if err := u.Scan(s.Id); err != nil {
			return fmt.Sprintf("step id %q is not a valid uuid", s.Id)
		}
		// order is an int on the wire and an int32 in the column, and the
		// conversion at the write site is a silent truncation. Without this
		// bound, 4294967297 validates as a positive, unique order here and
		// then lands as 1 in Postgres -- either tripping the def_order_uniq
		// constraint as a 500, or worse, executing in an order the operator
		// never authored. The domain validator cannot catch it: it works on
		// int, where the value is genuinely fine.
		if s.Order < 1 || s.Order > math.MaxInt32 {
			return fmt.Sprintf("step order %d is out of range (must be between 1 and %d)", s.Order, math.MaxInt32)
		}
	}
	candidate := workflow.Definition{
		ID:        "candidate",
		Lane:      lane,
		Name:      "candidate",
		IsBuiltIn: false,
		Version:   1,
		Steps:     stepDefinitionsToDomain(steps),
	}
	if err := workflow.ValidateDefinition(candidate); err != nil {
		return err.Error()
	}
	return ""
}

// writeStepsAndEdges inserts every step in steps (each carrying a
// CLIENT-SUPPLIED id) followed by every edge, in two passes: an edge's
// own composite FK (workflow_edges_from_step_fk/to_step_fk, migration
// 000057) requires BOTH endpoints' step rows to already exist, including
// a forward reference to a step declared later in steps -- so every step
// must land before any edge is attempted. Caller's own transaction;
// assumes steps has already passed validateStepsForWrite.
func writeStepsAndEdges(ctx context.Context, workflows *postgres.WorkflowStore, definitionID pgtype.UUID, steps []restdtos.WorkflowStepDefinition) error {
	for _, s := range steps {
		var stepID pgtype.UUID
		if err := stepID.Scan(s.Id); err != nil {
			return fmt.Errorf("httpapi: writeStepsAndEdges: step id %q: %w", s.Id, err)
		}
		if _, err := workflows.CreateStepDefinition(ctx, sqlcgen.CreateWorkflowStepDefinitionParams{
			ID:                     stepID,
			WorkflowDefinitionID:   definitionID,
			StepOrder:              int32(s.Order),
			ModelID:                (*string)(s.ModelId),
			Effort:                 (*string)(s.Effort),
			PromptTemplate:         s.PromptTemplate,
			ExecutionScope:         sqlcgen.WorkflowExecutionScope(s.ExecutionScope),
			ConversationContinuity: sqlcgen.WorkflowConversationContinuity(s.ConversationContinuity),
			HitlBefore:             s.HitlBefore,
			HitlAfter:              s.HitlAfter,
			CanvasPosition:         canvasPositionToJSON(s.CanvasPosition),
		}); err != nil {
			return fmt.Errorf("httpapi: create workflow step definition: %w", err)
		}
	}
	for _, s := range steps {
		var fromID pgtype.UUID
		if err := fromID.Scan(s.Id); err != nil {
			return fmt.Errorf("httpapi: writeStepsAndEdges: step id %q: %w", s.Id, err)
		}
		for _, e := range s.Edges {
			var toID pgtype.UUID
			if err := toID.Scan(e.ToStepId); err != nil {
				return fmt.Errorf("httpapi: writeStepsAndEdges: edge toStepId %q: %w", e.ToStepId, err)
			}
			if _, err := workflows.CreateEdge(ctx, definitionID, fromID, toID, string(e.OnStatus)); err != nil {
				return fmt.Errorf("httpapi: create workflow edge: %w", err)
			}
		}
	}
	return nil
}

// duplicateDefinition deep-copies sourceID's own definition (every step,
// every edge) into a brand-new workflow_definitions row named name --
// always is_built_in=false, unbound, version 1 (§25.10), regardless of
// the source's own is_built_in -- a built-in is copyable exactly like
// anything else. lane is INHERITED from the source, never a caller
// choice. Every copied step gets a SERVER-GENERATED id (never reusing
// the source's own), so source-id -> new-id remapping translates the
// source's own edges onto the copy. Returns pgx.ErrNoRows (unwrapped)
// when sourceID names no definition. Caller's own transaction.
func duplicateDefinition(ctx context.Context, workflows *postgres.WorkflowStore, sourceID pgtype.UUID, name string) (sqlcgen.WorkflowDefinition, error) {
	source, err := workflows.GetDefinition(ctx, sourceID)
	if err != nil {
		return sqlcgen.WorkflowDefinition{}, err
	}
	sourceSteps, err := workflows.ListStepDefinitions(ctx, sourceID)
	if err != nil {
		return sqlcgen.WorkflowDefinition{}, fmt.Errorf("httpapi: list source step definitions: %w", err)
	}
	sourceEdges, err := workflows.ListEdgesForDefinition(ctx, sourceID)
	if err != nil {
		return sqlcgen.WorkflowDefinition{}, fmt.Errorf("httpapi: list source edges: %w", err)
	}

	newDef, err := workflows.CreateDefinition(ctx, string(source.Lane), name)
	if err != nil {
		return sqlcgen.WorkflowDefinition{}, fmt.Errorf("httpapi: create duplicate workflow definition: %w", err)
	}

	stepIDMap := make(map[string]pgtype.UUID, len(sourceSteps))
	for _, s := range sourceSteps {
		newStep, err := workflows.DuplicateStepDefinition(ctx, sqlcgen.DuplicateWorkflowStepDefinitionParams{
			WorkflowDefinitionID:   newDef.ID,
			StepOrder:              s.StepOrder,
			ModelID:                s.ModelID,
			Effort:                 s.Effort,
			PromptTemplate:         s.PromptTemplate,
			ExecutionScope:         s.ExecutionScope,
			ConversationContinuity: s.ConversationContinuity,
			HitlBefore:             s.HitlBefore,
			HitlAfter:              s.HitlAfter,
			CanvasPosition:         s.CanvasPosition,
		})
		if err != nil {
			return sqlcgen.WorkflowDefinition{}, fmt.Errorf("httpapi: duplicate workflow step definition: %w", err)
		}
		stepIDMap[s.ID.String()] = newStep.ID
	}
	for _, e := range sourceEdges {
		fromID, ok := stepIDMap[e.FromStepID.String()]
		if !ok {
			// Unreachable against a ValidateDefinition-clean source (every
			// edge's FromStepID names a step in the SAME definition) --
			// refused defensively rather than silently dropping the edge.
			return sqlcgen.WorkflowDefinition{}, fmt.Errorf("httpapi: duplicate workflow definition: source edge references from-step %q not in the copied step set", e.FromStepID.String())
		}
		toID, ok := stepIDMap[e.ToStepID.String()]
		if !ok {
			return sqlcgen.WorkflowDefinition{}, fmt.Errorf("httpapi: duplicate workflow definition: source edge references to-step %q not in the copied step set", e.ToStepID.String())
		}
		if _, err := workflows.CreateEdge(ctx, newDef.ID, fromID, toID, string(e.OnStatus)); err != nil {
			return sqlcgen.WorkflowDefinition{}, fmt.Errorf("httpapi: duplicate workflow edge: %w", err)
		}
	}
	return newDef, nil
}

// CreateWorkflowDefinition backs POST /api/workflow-definitions (§25.10):
// EITHER a whole new document (req.SourceDefinitionId nil; req.Lane/
// req.Steps required) OR a duplicate of an existing definition
// (req.SourceDefinitionId non-nil; req.Lane/req.Steps ignored). 201 with
// the resulting whole document on success.
func CreateWorkflowDefinition(pool *pgxpool.Pool, workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionManageWorkflowDefinitions, authz.Resource{}) {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.CreateWorkflowDefinitionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: begin create-workflow-definition tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		txWorkflows := workflows.WithTx(tx)

		var newDef sqlcgen.WorkflowDefinition

		if req.SourceDefinitionId != nil {
			var sourceID pgtype.UUID
			if err := sourceID.Scan(*req.SourceDefinitionId); err != nil {
				writeError(w, http.StatusBadRequest, "malformed sourceDefinitionId")
				return
			}
			newDef, err = duplicateDefinition(ctx, txWorkflows, sourceID, req.Name)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, "source workflow definition not found")
					return
				}
				if isUniqueViolation(err) {
					writeError(w, http.StatusConflict, fmt.Sprintf("a workflow definition named %q already exists for this lane", req.Name))
					return
				}
				logger.Error("httpapi: duplicate workflow definition failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		} else {
			if req.Lane == nil {
				writeError(w, http.StatusBadRequest, "lane is required when sourceDefinitionId is null")
				return
			}
			if len(req.Steps) == 0 {
				writeError(w, http.StatusBadRequest, "steps is required and must be non-empty when sourceDefinitionId is null")
				return
			}
			lane := workflow.Lane(*req.Lane)
			if msg := validateStepsForWrite(lane, req.Steps); msg != "" {
				writeError(w, http.StatusBadRequest, "invalid workflow definition: "+msg)
				return
			}

			newDef, err = txWorkflows.CreateDefinition(ctx, string(lane), req.Name)
			if err != nil {
				if isUniqueViolation(err) {
					writeError(w, http.StatusConflict, fmt.Sprintf("a workflow definition named %q already exists for this lane", req.Name))
					return
				}
				logger.Error("httpapi: create workflow definition failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if err := writeStepsAndEdges(ctx, txWorkflows, newDef.ID, req.Steps); err != nil {
				if isUniqueViolation(err) {
					writeError(w, http.StatusConflict, "one or more step ids collide with an existing step (ids must be globally unique)")
					return
				}
				logger.Error("httpapi: write workflow definition steps/edges failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}

		// A freshly created or duplicated definition is unbound with no run
		// history by construction (§25.10: the copy always lands unbound at
		// version 1), so both refusal facts are false here rather than re-read.
		doc, err := definitionDocumentFromRow(ctx, txWorkflows, newDef, false, false)
		if err != nil {
			logger.Error("httpapi: assemble created workflow definition document failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: commit create-workflow-definition tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		logger.Info("httpapi: created workflow definition", "definition_id", newDef.ID.String(), "duplicate", req.SourceDefinitionId != nil)
		writeJSON(w, http.StatusCreated, doc)
	}
}

// refusalReasonForMutation checks §25.10/§25.11's two named structural
// refusals (is_built_in, bound-by-workflow_bindings) plus this Step's own
// third guard (has run history -- see this file's own top doc comment)
// against def, in that fixed order, so the returned message always names
// the FIRST rule that fires -- mirrors internal/domain/workflow.
// ValidateDefinition's own "first violation wins" convention. Returns
// (message, true, nil) when PUT/DELETE must be refused, ("", false, nil)
// when the mutation may proceed, or a non-nil err on a genuine store
// failure (never conflated with "refused").
func refusalReasonForMutation(ctx context.Context, workflows *postgres.WorkflowStore, def sqlcgen.WorkflowDefinition) (string, bool, error) {
	if def.IsBuiltIn {
		return "workflow definition is built-in: built-in definitions cannot be edited or deleted, even by an admin -- duplicate it (POST /api/workflow-definitions with sourceDefinitionId) and edit the copy instead", true, nil
	}
	bound, err := workflows.ExistsBindingForDefinition(ctx, def.ID)
	if err != nil {
		return "", false, fmt.Errorf("httpapi: check workflow definition bound: %w", err)
	}
	if bound {
		return "workflow definition is bound: it is referenced by at least one workflow binding and cannot be edited or deleted while bound, even by an admin -- duplicate it, edit the copy, then have an admin activate the copy instead", true, nil
	}
	hasRuns, err := workflows.ExistsRunForDefinition(ctx, def.ID)
	if err != nil {
		return "", false, fmt.Errorf("httpapi: check workflow definition run history: %w", err)
	}
	if hasRuns {
		return "workflow definition has run history: it has been used by at least one workflow run and cannot be edited or deleted -- duplicate it and edit the copy instead", true, nil
	}
	return "", false, nil
}

// PutWorkflowDefinition backs PUT /api/workflow-definitions/{id}
// (§25.10): the complete desired state of name+steps (see this file's own
// top doc comment for the two structural refusals plus this Step's own
// third guard). Replaces the ENTIRE existing steps/edges set --
// workflow_step_definitions/workflow_edges cascade-delete from the
// definition and are re-inserted from the body, never hand-diffed
// (§25.10). Bumps version by exactly 1 on success.
func PutWorkflowDefinition(pool *pgxpool.Pool, workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionManageWorkflowDefinitions, authz.Resource{}) {
			return
		}
		id, ok := parseWorkflowDefinitionID(w, r)
		if !ok {
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		var req restdtos.UpdateWorkflowDefinitionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "malformed request body: "+err.Error())
			return
		}
		if req.Name == "" {
			writeError(w, http.StatusBadRequest, "name is required")
			return
		}
		if len(req.Steps) == 0 {
			writeError(w, http.StatusBadRequest, "steps is required and must be non-empty")
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: begin put-workflow-definition tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		txWorkflows := workflows.WithTx(tx)

		// LockDefinitionForUpdate, not GetDefinition: the refusal checks
		// below are a read, and without a row lock they are a read-then-write.
		// The bound check could see no binding, an admin could activate the
		// definition, and this edit would still land -- on a definition that
		// is bound at COMMIT time, which is precisely the past-the-admin-gate
		// dispatch change §25.11's amendment says the refusal prevents. The
		// binding upsert takes the same lock, so the two serialise. It also
		// serialises two concurrent PUTs, which otherwise each deleted only
		// the steps visible in their own snapshot and merged their step sets
		// instead of replacing.
		existing, err := txWorkflows.LockDefinitionForUpdate(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "workflow definition not found")
				return
			}
			logger.Error("httpapi: lock workflow definition for put failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if reason, refused, err := refusalReasonForMutation(ctx, txWorkflows, existing); err != nil {
			logger.Error("httpapi: check workflow definition mutation refusal failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		} else if refused {
			writeError(w, http.StatusConflict, reason)
			return
		}

		// Lane and is_built_in are immutable post-creation (this file's
		// own top doc comment: neither appears on
		// UpdateWorkflowDefinitionRequest at all) -- validated against
		// the EXISTING row's own lane, never a value the caller could
		// influence.
		lane := workflow.Lane(existing.Lane)
		if msg := validateStepsForWrite(lane, req.Steps); msg != "" {
			writeError(w, http.StatusBadRequest, "invalid workflow definition: "+msg)
			return
		}

		if err := txWorkflows.DeleteStepDefinitionsForDefinition(ctx, id); err != nil {
			logger.Error("httpapi: delete existing workflow step definitions failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if err := writeStepsAndEdges(ctx, txWorkflows, id, req.Steps); err != nil {
			if isUniqueViolation(err) {
				writeError(w, http.StatusConflict, "one or more step ids collide with an existing step (ids must be globally unique)")
				return
			}
			logger.Error("httpapi: write workflow definition steps/edges failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		updated, err := txWorkflows.UpdateDefinitionNameAndBumpVersion(ctx, id, req.Name)
		if err != nil {
			logger.Error("httpapi: update workflow definition name/version failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		// This PUT only got here by passing refusalReasonForMutation, which
		// means neither a binding nor a run references it -- and the row lock
		// held since that check keeps it true through COMMIT.
		doc, err := definitionDocumentFromRow(ctx, txWorkflows, updated, false, false)
		if err != nil {
			logger.Error("httpapi: assemble updated workflow definition document failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: commit put-workflow-definition tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		logger.Info("httpapi: updated workflow definition", "definition_id", id.String(), "version", doc.Version)
		writeJSON(w, http.StatusOK, doc)
	}
}

// DeleteWorkflowDefinition backs DELETE /api/workflow-definitions/{id}
// (§25.10): see this file's own top doc comment for the two structural
// refusals plus this Step's own third guard. 204 with no body on
// success, mirroring DeleteChatGPTLink's own identical "DELETE returns
// 204, not a response shape" precedent.
func DeleteWorkflowDefinition(pool *pgxpool.Pool, workflows *postgres.WorkflowStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		if !authorize(w, r, authz.ActionManageWorkflowDefinitions, authz.Resource{}) {
			return
		}
		id, ok := parseWorkflowDefinitionID(w, r)
		if !ok {
			return
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			logger.Error("httpapi: begin delete-workflow-definition tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()
		txWorkflows := workflows.WithTx(tx)

		// Locked for the same reason PutWorkflowDefinition locks -- see its
		// own comment: an unlocked refusal check is a read-then-write, and a
		// binding committed in the window would leave this DELETE removing a
		// definition that is bound.
		existing, err := txWorkflows.LockDefinitionForUpdate(ctx, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "workflow definition not found")
				return
			}
			logger.Error("httpapi: lock workflow definition for delete failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		if reason, refused, err := refusalReasonForMutation(ctx, txWorkflows, existing); err != nil {
			logger.Error("httpapi: check workflow definition mutation refusal failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		} else if refused {
			writeError(w, http.StatusConflict, reason)
			return
		}

		rowsAffected, err := txWorkflows.DeleteDefinition(ctx, id)
		if err != nil {
			logger.Error("httpapi: delete workflow definition failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if rowsAffected == 0 {
			// Raced away between the Get above and this DELETE (another
			// caller deleted it first) -- 404, never a silent no-op 204.
			writeError(w, http.StatusNotFound, "workflow definition not found")
			return
		}

		if err := tx.Commit(ctx); err != nil {
			logger.Error("httpapi: commit delete-workflow-definition tx failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		logger.Info("httpapi: deleted workflow definition", "definition_id", id.String())
		w.WriteHeader(http.StatusNoContent)
	}
}
