package workflow_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/workflow"
)

// passthroughStep builds a minimal valid step, mirroring the built-in
// review/request shape (§25.8): ModelID nil (inherit), passthrough
// prompt, same-session, continue, no HITL, no edges.
func passthroughStep(id workflow.ID, order int) workflow.StepDefinition {
	return workflow.StepDefinition{
		ID:                     id,
		Order:                  order,
		Kind:                   workflow.StepKindAgent,
		ModelID:                nil,
		PromptTemplate:         "{{prompt}}",
		ExecutionScope:         workflow.ExecutionScopeSameSession,
		ConversationContinuity: workflow.ConversationContinuityContinue,
	}
}

// validSingleStep is the built-in review/request shape (§25.8): one
// passthrough step, no edges.
func validSingleStep() workflow.Definition {
	return workflow.Definition{
		ID:        "def-1",
		Lane:      workflow.LaneReview,
		Name:      "review",
		IsBuiltIn: true,
		Version:   1,
		Steps:     []workflow.StepDefinition{passthroughStep("s1", 1)},
	}
}

// validPlanShape is the built-in plan shape (§25.8): two steps, HITL
// after step 1, an explicit needs_fix self-loop on step 1 (the revise
// loop, exempt from the circuit breaker per §25.8 -- the exemption is
// the engine's, not this package's).
func validPlanShape() workflow.Definition {
	planStep := passthroughStep("plan", 1)
	planStep.HITLAfter = true
	planStep.Edges = []workflow.Edge{{FromStepID: "plan", OnStatus: workflow.StepOutcomeNeedsFix, ToStepID: "plan"}}
	buildStep := passthroughStep("build", 2)
	return workflow.Definition{
		ID:        "def-plan",
		Lane:      workflow.LanePlan,
		Name:      "plan",
		IsBuiltIn: true,
		Version:   1,
		Steps:     []workflow.StepDefinition{planStep, buildStep},
	}
}

// validAuditLoopShape is §25.8's non-built-in override example
// (draft -> scaffold -> build -> audit, plus a fix step wired via
// §25.9's two ordinary edges: Edge{audit, needs_fix, fix} and
// Edge{fix, ok, audit}).
func validAuditLoopShape() workflow.Definition {
	gemini, opus, sonnet, codex := "google/gemini-3.5-pro", "anthropic/claude-opus-5", "anthropic/claude-sonnet-5", "openai/gpt-5.5-codex"

	draft := passthroughStep("draft", 1)
	draft.ModelID = &gemini
	scaffold := passthroughStep("scaffold", 2)
	scaffold.ModelID = &opus
	build := passthroughStep("build", 3)
	build.ModelID = &sonnet

	audit := passthroughStep("audit", 4)
	audit.ModelID = &codex
	audit.ConversationContinuity = workflow.ConversationContinuityFresh
	audit.Edges = []workflow.Edge{{FromStepID: "audit", OnStatus: workflow.StepOutcomeNeedsFix, ToStepID: "fix"}}

	fix := passthroughStep("fix", 5)
	fix.ExecutionScope = workflow.ExecutionScopeChildSession
	fix.Edges = []workflow.Edge{{FromStepID: "fix", OnStatus: workflow.StepOutcomeOK, ToStepID: "audit"}}

	return workflow.Definition{
		ID:      "def-audit-loop",
		Lane:    workflow.LaneRequest,
		Name:    "spec-scaffold-build-audit",
		Version: 1,
		Steps:   []workflow.StepDefinition{draft, scaffold, build, audit, fix},
	}
}

func TestValidateDefinition(t *testing.T) {
	t.Parallel()

	emptyModel := ""

	tests := []struct {
		name    string
		mutate  func(*workflow.Definition)
		def     func() workflow.Definition
		wantErr error
	}{
		{name: "valid single-step (built-in review/request shape)", def: validSingleStep},
		{name: "valid two-step plan shape with needs_fix self-loop", def: validPlanShape},
		{name: "valid five-step audit-loop shape with backward ok edge", def: validAuditLoopShape},
		{
			name: "valid: forward edge to a later-declared step",
			def:  validPlanShape,
			mutate: func(d *workflow.Definition) {
				d.Steps[0].Edges = append(d.Steps[0].Edges,
					workflow.Edge{FromStepID: "plan", OnStatus: workflow.StepOutcomeBlocked, ToStepID: "build"})
			},
		},
		{
			name: "valid: order gaps are legal (contiguity not required)",
			def:  validPlanShape,
			mutate: func(d *workflow.Definition) {
				d.Steps[1].Order = 7
			},
		},
		{
			name:    "invalid lane",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Lane = "release" },
			wantErr: workflow.ErrInvalidLane,
		},
		{
			name:    "empty name",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Name = "" },
			wantErr: workflow.ErrEmptyName,
		},
		{
			name:    "zero version",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Version = 0 },
			wantErr: workflow.ErrInvalidVersion,
		},
		{
			name:    "no steps",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Steps = nil },
			wantErr: workflow.ErrNoSteps,
		},
		{
			name:    "empty step id",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Steps[0].ID = "" },
			wantErr: workflow.ErrEmptyStepID,
		},
		{
			name:    "duplicate step id",
			def:     validPlanShape,
			mutate:  func(d *workflow.Definition) { d.Steps[1].ID = "plan" },
			wantErr: workflow.ErrDuplicateStepID,
		},
		{
			name:    "zero step order",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Steps[0].Order = 0 },
			wantErr: workflow.ErrInvalidStepOrder,
		},
		{
			name:    "duplicate step order",
			def:     validPlanShape,
			mutate:  func(d *workflow.Definition) { d.Steps[1].Order = 1 },
			wantErr: workflow.ErrDuplicateStepOrder,
		},
		{
			name:    "invalid step kind",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Steps[0].Kind = "gate" },
			wantErr: workflow.ErrInvalidStepKind,
		},
		{
			name:    "model id set but empty",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Steps[0].ModelID = &emptyModel },
			wantErr: workflow.ErrEmptyModelID,
		},
		{
			name:    "empty prompt template",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Steps[0].PromptTemplate = "" },
			wantErr: workflow.ErrEmptyPromptTemplate,
		},
		{
			name:    "invalid execution scope",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Steps[0].ExecutionScope = "detached" },
			wantErr: workflow.ErrInvalidExecutionScope,
		},
		{
			name:    "invalid conversation continuity",
			def:     validSingleStep,
			mutate:  func(d *workflow.Definition) { d.Steps[0].ConversationContinuity = "reset" },
			wantErr: workflow.ErrInvalidConversationContinuity,
		},
		{
			name: "edge FromStepID differs from owning step",
			def:  validPlanShape,
			mutate: func(d *workflow.Definition) {
				d.Steps[0].Edges[0].FromStepID = "build"
			},
			wantErr: workflow.ErrEdgeFromMismatch,
		},
		{
			name: "edge with invalid on-status",
			def:  validPlanShape,
			mutate: func(d *workflow.Definition) {
				d.Steps[0].Edges[0].OnStatus = "needs_human"
			},
			wantErr: workflow.ErrInvalidEdgeStatus,
		},
		{
			name: "edge targeting a step not in the definition",
			def:  validPlanShape,
			mutate: func(d *workflow.Definition) {
				d.Steps[0].Edges[0].ToStepID = "ghost"
			},
			wantErr: workflow.ErrEdgeUnknownTarget,
		},
		{
			name: "two edges on the same (step, on-status)",
			def:  validPlanShape,
			mutate: func(d *workflow.Definition) {
				d.Steps[0].Edges = append(d.Steps[0].Edges,
					workflow.Edge{FromStepID: "plan", OnStatus: workflow.StepOutcomeNeedsFix, ToStepID: "build"})
			},
			wantErr: workflow.ErrDuplicateEdge,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			def := tc.def()
			if tc.mutate != nil {
				tc.mutate(&def)
			}

			err := workflow.ValidateDefinition(def)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateDefinition() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ValidateDefinition() = %v, want errors.Is(err, %v)", err, tc.wantErr)
			}
		})
	}
}
