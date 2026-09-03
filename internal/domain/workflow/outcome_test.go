package workflow_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/workflow"
)

// TestAllStepOutcomeStatuses_MatchesConstants pins the closed 3-value
// vocabulary (§25.4) -- a fourth value appearing here without every
// consumer (edges, the outcome-posting endpoint, the Postgres enum)
// agreeing would be a contract break, so the exhaustive list itself is
// asserted.
func TestAllStepOutcomeStatuses_MatchesConstants(t *testing.T) {
	t.Parallel()

	want := []workflow.StepOutcomeStatus{workflow.StepOutcomeOK, workflow.StepOutcomeNeedsFix, workflow.StepOutcomeBlocked}
	if len(workflow.AllStepOutcomeStatuses) != len(want) {
		t.Fatalf("len(AllStepOutcomeStatuses) = %d, want %d", len(workflow.AllStepOutcomeStatuses), len(want))
	}
	for i, s := range want {
		if workflow.AllStepOutcomeStatuses[i] != s {
			t.Errorf("AllStepOutcomeStatuses[%d] = %s, want %s", i, workflow.AllStepOutcomeStatuses[i], s)
		}
	}
}

func TestIsValidStepOutcomeStatus(t *testing.T) {
	t.Parallel()

	for _, s := range workflow.AllStepOutcomeStatuses {
		if !workflow.IsValidStepOutcomeStatus(s) {
			t.Errorf("IsValidStepOutcomeStatus(%s) = false, want true", s)
		}
	}
	// review.Shippable's own values ("auto"/"needs_human"/"block") are a
	// DISTINCT axis (§25.4) -- none of them is a StepOutcomeStatus, and
	// this test pins that the two vocabularies never blur together.
	for _, s := range []workflow.StepOutcomeStatus{"", "OK", "auto", "needs_human", "block", "success"} {
		if workflow.IsValidStepOutcomeStatus(s) {
			t.Errorf("IsValidStepOutcomeStatus(%q) = true, want false", s)
		}
	}
}

// TestEnumHelpers covers the remaining closed vocabularies
// (StepKind/ExecutionScope/ConversationContinuity) the same way: every
// declared value valid, representative foreign values rejected, All*
// lists pinned.
func TestEnumHelpers(t *testing.T) {
	t.Parallel()

	if len(workflow.AllStepKinds) != 1 || workflow.AllStepKinds[0] != workflow.StepKindAgent {
		t.Errorf("AllStepKinds = %v, want exactly [agent]", workflow.AllStepKinds)
	}
	if !workflow.IsValidStepKind(workflow.StepKindAgent) {
		t.Error("IsValidStepKind(agent) = false, want true")
	}
	for _, k := range []workflow.StepKind{"", "gate", "tool", "Agent"} {
		if workflow.IsValidStepKind(k) {
			t.Errorf("IsValidStepKind(%q) = true, want false", k)
		}
	}

	wantScopes := []workflow.ExecutionScope{workflow.ExecutionScopeSameSession, workflow.ExecutionScopeChildSession}
	if len(workflow.AllExecutionScopes) != len(wantScopes) {
		t.Fatalf("len(AllExecutionScopes) = %d, want %d", len(workflow.AllExecutionScopes), len(wantScopes))
	}
	for i, s := range wantScopes {
		if workflow.AllExecutionScopes[i] != s {
			t.Errorf("AllExecutionScopes[%d] = %s, want %s", i, workflow.AllExecutionScopes[i], s)
		}
		if !workflow.IsValidExecutionScope(s) {
			t.Errorf("IsValidExecutionScope(%s) = false, want true", s)
		}
	}
	for _, s := range []workflow.ExecutionScope{"", "session", "child"} {
		if workflow.IsValidExecutionScope(s) {
			t.Errorf("IsValidExecutionScope(%q) = true, want false", s)
		}
	}

	wantCont := []workflow.ConversationContinuity{workflow.ConversationContinuityContinue, workflow.ConversationContinuityFresh}
	if len(workflow.AllConversationContinuities) != len(wantCont) {
		t.Fatalf("len(AllConversationContinuities) = %d, want %d", len(workflow.AllConversationContinuities), len(wantCont))
	}
	for i, c := range wantCont {
		if workflow.AllConversationContinuities[i] != c {
			t.Errorf("AllConversationContinuities[%d] = %s, want %s", i, workflow.AllConversationContinuities[i], c)
		}
		if !workflow.IsValidConversationContinuity(c) {
			t.Errorf("IsValidConversationContinuity(%s) = false, want true", c)
		}
	}
	for _, c := range []workflow.ConversationContinuity{"", "new", "Continue"} {
		if workflow.IsValidConversationContinuity(c) {
			t.Errorf("IsValidConversationContinuity(%q) = true, want false", c)
		}
	}
}
