package workflowengine

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/domain/workflow"
)

// parseWorkflowID parses s (a domain/workflow.ID -- always a stringified
// UUID in this codebase's own postgres-backed implementation) back into a
// pgtype.UUID -- the boundary conversion domain/workflow.ID's own doc
// comment says callers own. Mirrors this codebase's existing
// `var id pgtype.UUID; id.Scan(s)` idiom (e.g. outboxworker/
// sentinelautofix.go's fixID.Scan(payload.SentinelFixID)) exactly.
func parseWorkflowID(id workflow.ID) (pgtype.UUID, error) {
	var out pgtype.UUID
	if err := out.Scan(string(id)); err != nil {
		return pgtype.UUID{}, fmt.Errorf("workflowengine: parse workflow id %q: %w", id, err)
	}
	return out, nil
}

// resolveBinding resolves the workflow_bindings row governing lane for a
// session whose own repos resolve to repoFullName/hasRepo (see
// repoFullNameFromSessionRepos) -- a repo-specific row always wins when one
// exists (§25.4: "shadows the global binding for that one repo only");
// GetGlobalBinding is the fallback, guaranteed to exist by migration
// 000057's own seed for every lane.
func resolveBinding(ctx context.Context, workflows *postgres.WorkflowStore, lane workflow.Lane, repoFullName string, hasRepo bool) (sqlcgen.WorkflowBinding, error) {
	if hasRepo {
		binding, err := workflows.GetBindingForRepo(ctx, string(lane), repoFullName)
		switch {
		case err == nil:
			return binding, nil
		case errors.Is(err, pgx.ErrNoRows):
			// No repo-specific override -- fall through to the global
			// binding below, exactly like the no-repo case.
		default:
			return sqlcgen.WorkflowBinding{}, fmt.Errorf("workflowengine: get repo binding: %w", err)
		}
	}
	binding, err := workflows.GetGlobalBinding(ctx, string(lane))
	if err != nil {
		return sqlcgen.WorkflowBinding{}, fmt.Errorf("workflowengine: get global binding: %w", err)
	}
	return binding, nil
}

// LoadDefinition assembles a full domain/workflow.Definition (the
// definition row plus its ordered steps, each carrying its own outgoing
// edges) from definitionID -- three plain reads (GetDefinition/
// ListStepDefinitions/ListEdgesForDefinition), grouped in Go: workflow_edges
// is a flat table with no per-step grouping of its own, unlike
// domain/workflow.StepDefinition.Edges.
func LoadDefinition(ctx context.Context, workflows *postgres.WorkflowStore, definitionID pgtype.UUID) (workflow.Definition, error) {
	def, err := workflows.GetDefinition(ctx, definitionID)
	if err != nil {
		return workflow.Definition{}, fmt.Errorf("workflowengine: get definition: %w", err)
	}
	steps, err := workflows.ListStepDefinitions(ctx, definitionID)
	if err != nil {
		return workflow.Definition{}, fmt.Errorf("workflowengine: list step definitions: %w", err)
	}
	edges, err := workflows.ListEdgesForDefinition(ctx, definitionID)
	if err != nil {
		return workflow.Definition{}, fmt.Errorf("workflowengine: list edges: %w", err)
	}

	edgesByStep := make(map[string][]workflow.Edge, len(edges))
	for _, e := range edges {
		from := e.FromStepID.String()
		edgesByStep[from] = append(edgesByStep[from], workflow.Edge{
			FromStepID: workflow.ID(from),
			OnStatus:   workflow.StepOutcomeStatus(e.OnStatus),
			ToStepID:   workflow.ID(e.ToStepID.String()),
		})
	}

	out := workflow.Definition{
		ID:        workflow.ID(def.ID.String()),
		Lane:      workflow.Lane(def.Lane),
		Name:      def.Name,
		IsBuiltIn: def.IsBuiltIn,
		Version:   int(def.Version),
		Steps:     make([]workflow.StepDefinition, 0, len(steps)),
	}
	for _, s := range steps {
		id := s.ID.String()
		out.Steps = append(out.Steps, workflow.StepDefinition{
			ID:                     workflow.ID(id),
			Order:                  int(s.StepOrder),
			Kind:                   workflow.StepKind(s.Kind),
			ModelID:                s.ModelID,
			Effort:                 s.Effort,
			PromptTemplate:         s.PromptTemplate,
			ExecutionScope:         workflow.ExecutionScope(s.ExecutionScope),
			ConversationContinuity: workflow.ConversationContinuity(s.ConversationContinuity),
			HITLBefore:             s.HitlBefore,
			HITLAfter:              s.HitlAfter,
			Edges:                  edgesByStep[id],
		})
	}
	return out, nil
}

// stepByID finds the step with the given id within def -- mirrors
// internal/domain/workflow's own unexported stepByID (nextstep.go), needed
// again here because this package must find a specific step BEFORE calling
// NextStep (to read its ExecutionScope/HITLAfter/PromptTemplate/ModelID),
// not just as NextStep's own internal detail.
func stepByID(def workflow.Definition, id workflow.ID) (workflow.StepDefinition, bool) {
	for _, s := range def.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return workflow.StepDefinition{}, false
}

// firstStepByOrder returns the step with the smallest Order in def -- the
// entry point for a brand-new WorkflowRun (ValidateDefinition already
// guarantees def.Steps is non-empty and every Order is unique, but NOT
// necessarily starting at 1 or contiguous, so this is a real min-scan, not
// a bare index 0).
func firstStepByOrder(def workflow.Definition) workflow.StepDefinition {
	best := def.Steps[0]
	for _, s := range def.Steps[1:] {
		if s.Order < best.Order {
			best = s
		}
	}
	return best
}
