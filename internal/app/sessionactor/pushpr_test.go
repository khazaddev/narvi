package sessionactor

import (
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/sandboxws"
	"github.com/narvidev/narvi/internal/domain/turn"
)

// TestExecutionOutcomeTrigger is table-driven over every
// sandboxws.ExecutionCompleteOutcome value its own generated
// UnmarshalJSON accepts, plus one unrecognized value, proving the exact
// (outcome -> trigger) mapping completeProcessingTurn relies on.
func TestExecutionOutcomeTrigger(t *testing.T) {
	tests := []struct {
		name    string
		outcome sandboxws.ExecutionCompleteOutcome
		want    turn.Trigger
		wantOK  bool
	}{
		{"completed", sandboxws.ExecutionCompleteOutcomeCompleted, turn.TriggerComplete, true},
		{"failed", sandboxws.ExecutionCompleteOutcomeFailed, turn.TriggerFail, true},
		{"cancelled", sandboxws.ExecutionCompleteOutcomeCancelled, turn.TriggerCancel, true},
		{"unrecognized", sandboxws.ExecutionCompleteOutcome("bogus"), 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := executionOutcomeTrigger(tc.outcome)
			if ok != tc.wantOK {
				t.Fatalf("executionOutcomeTrigger(%q) ok = %v, want %v", tc.outcome, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("executionOutcomeTrigger(%q) = %v, want %v", tc.outcome, got, tc.want)
			}
		})
	}
}

// parseOwnerRepo's own table-driven test used to live here -- audit-
// remediation batch B3 moved both this file's own parseOwnerRepo AND
// internal/app/imagebuild/builder.go's byte-for-byte fork of it into
// internal/domain/reposource.ParseOwnerRepo, and moved this exact test
// table with it: see TestParseOwnerRepo in
// internal/domain/reposource/reposource_test.go, which covers every edge
// case (https URL, with/without a trailing ".git", with/without a
// trailing slash, a non-GitHub host parsed generically, malformed inputs)
// both of this file's and builder.go's own pre-existing tests relied on.
