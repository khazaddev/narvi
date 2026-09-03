package workflowengine

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/domain/workflow"
)

func strPtr(s string) *string { return &s }

// TestApplyStep_EffortOverride proves applyStep's Effort resolution mirrors
// its ModelID resolution exactly (§29.8's own "identical inherit-when-null
// semantics"): a nil step.Effort inherits the caller's own effort
// unchanged; a non-nil step.Effort overrides it, regardless of what the
// caller supplied. Table-driven, exercised alongside ModelID in the same
// cases so any future drift between the two fields' own resolution logic
// shows up here first.
func TestApplyStep_EffortOverride(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		stepModelID   *string
		stepEffort    *string
		callerModelID *string
		callerEffort  *string
		wantModelID   *string
		wantEffort    *string
	}{
		{
			name:          "both nil on step: caller's model/effort pass through unchanged (§25.8 zero-config proof)",
			callerModelID: strPtr("anthropic/claude-sonnet-4-5"), callerEffort: strPtr("high"),
			wantModelID: strPtr("anthropic/claude-sonnet-4-5"), wantEffort: strPtr("high"),
		},
		{
			name:        "both nil on step, caller also nil: stays nil (the fully zero-config case)",
			wantModelID: nil, wantEffort: nil,
		},
		{
			name:          "step ModelID set, step Effort nil: only model overrides, effort still passes through",
			stepModelID:   strPtr("openai/gpt-5.3-codex-spark"),
			callerModelID: strPtr("anthropic/claude-sonnet-4-5"), callerEffort: strPtr("medium"),
			wantModelID: strPtr("openai/gpt-5.3-codex-spark"), wantEffort: strPtr("medium"),
		},
		{
			name:          "step Effort set, step ModelID nil: only effort overrides, model still passes through",
			stepEffort:    strPtr("xhigh"),
			callerModelID: strPtr("anthropic/claude-sonnet-4-5"), callerEffort: strPtr("low"),
			wantModelID: strPtr("anthropic/claude-sonnet-4-5"), wantEffort: strPtr("xhigh"),
		},
		{
			name:        "both step ModelID and step Effort set: both override, independently of the caller",
			stepModelID: strPtr("openai/gpt-5.3-codex-spark"), stepEffort: strPtr("xhigh"),
			callerModelID: strPtr("anthropic/claude-sonnet-4-5"), callerEffort: strPtr("low"),
			wantModelID: strPtr("openai/gpt-5.3-codex-spark"), wantEffort: strPtr("xhigh"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			step := workflow.StepDefinition{
				ID:             "step-1",
				Order:          1,
				Kind:           workflow.StepKindAgent,
				ModelID:        tc.stepModelID,
				Effort:         tc.stepEffort,
				PromptTemplate: "{{prompt}}",
			}

			res := applyStep(context.Background(), step, "do the thing", tc.callerModelID, tc.callerEffort, true, pgtype.UUID{})

			if !stringPtrEqual(res.ModelID, tc.wantModelID) {
				t.Errorf("applyStep(...).ModelID = %s, want %s", derefOrNil(res.ModelID), derefOrNil(tc.wantModelID))
			}
			if !stringPtrEqual(res.Effort, tc.wantEffort) {
				t.Errorf("applyStep(...).Effort = %s, want %s", derefOrNil(res.Effort), derefOrNil(tc.wantEffort))
			}
			if res.Prompt != "do the thing" {
				t.Errorf("applyStep(...).Prompt = %q, want the rendered passthrough template unchanged", res.Prompt)
			}
		})
	}
}

// TestPassthrough_CarriesEffort proves passthrough (the fail-open fallback
// every ResolveStepForNewTurn branch degrades to) never drops effort --
// mirroring the same guarantee it already gives modelID.
func TestPassthrough_CarriesEffort(t *testing.T) {
	t.Parallel()

	modelID := strPtr("anthropic/claude-sonnet-4-5")
	effort := strPtr("high")

	res := passthrough("caller prompt", modelID, effort)

	if res.Prompt != "caller prompt" {
		t.Errorf("passthrough(...).Prompt = %q, want %q", res.Prompt, "caller prompt")
	}
	if !stringPtrEqual(res.ModelID, modelID) {
		t.Errorf("passthrough(...).ModelID = %s, want %s", derefOrNil(res.ModelID), derefOrNil(modelID))
	}
	if !stringPtrEqual(res.Effort, effort) {
		t.Errorf("passthrough(...).Effort = %s, want %s", derefOrNil(res.Effort), derefOrNil(effort))
	}
	if res.Tracked {
		t.Error("passthrough(...).Tracked = true, want false (passthrough never creates a bookkeeping row)")
	}
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func derefOrNil(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
