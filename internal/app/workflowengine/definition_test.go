package workflowengine

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/workflow"
)

func passthroughStep(id workflow.ID, order int) workflow.StepDefinition {
	return workflow.StepDefinition{
		ID:                     id,
		Order:                  order,
		Kind:                   workflow.StepKindAgent,
		PromptTemplate:         "{{prompt}}",
		ExecutionScope:         workflow.ExecutionScopeSameSession,
		ConversationContinuity: workflow.ConversationContinuityContinue,
	}
}

// TestStepByID mirrors internal/domain/workflow's own unexported stepByID
// test coverage -- this package needs its own copy (definition.go) to
// inspect a step's fields BEFORE calling workflow.NextStep, not just as
// that function's own internal detail.
func TestStepByID(t *testing.T) {
	t.Parallel()

	def := workflow.Definition{Steps: []workflow.StepDefinition{
		passthroughStep("plan", 1),
		passthroughStep("build", 2),
	}}

	if step, ok := stepByID(def, "build"); !ok || step.ID != "build" {
		t.Errorf("stepByID(def, %q) = (%+v, %v), want the build step", "build", step, ok)
	}
	if _, ok := stepByID(def, "ghost"); ok {
		t.Error("stepByID(def, \"ghost\") ok = true, want false")
	}
}

// TestFirstStepByOrder proves the entry point for a brand-new run is a
// genuine min-scan over Order, not index 0 -- Order is unique but NOT
// required contiguous or sorted within Steps (domain/workflow.
// ValidateDefinition's own documented contiguity carve-out).
func TestFirstStepByOrder(t *testing.T) {
	t.Parallel()

	def := workflow.Definition{Steps: []workflow.StepDefinition{
		passthroughStep("build", 7),
		passthroughStep("plan", 1),
	}}

	if got := firstStepByOrder(def); got.ID != "plan" {
		t.Errorf("firstStepByOrder(def) = %+v, want the order-1 (\"plan\") step even though it is not Steps[0]", got)
	}
}

// TestParseWorkflowID covers parseWorkflowID's own two outcomes: a real
// UUID string round-trips; a malformed one is a real, reported error, not
// a silently-zeroed pgtype.UUID.
func TestParseWorkflowID(t *testing.T) {
	t.Parallel()

	const validUUID = "00000000-0000-4000-8000-000000000011"
	got, err := parseWorkflowID(workflow.ID(validUUID))
	if err != nil {
		t.Fatalf("parseWorkflowID(%q) error = %v, want nil", validUUID, err)
	}
	if !got.Valid || got.String() != validUUID {
		t.Errorf("parseWorkflowID(%q) = %+v, want a valid pgtype.UUID round-tripping to the same string", validUUID, got)
	}

	if _, err := parseWorkflowID("not-a-uuid"); err == nil {
		t.Error("parseWorkflowID(\"not-a-uuid\") error = nil, want a non-nil error")
	}
}
