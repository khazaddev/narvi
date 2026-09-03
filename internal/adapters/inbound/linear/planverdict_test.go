package linear

import (
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/inbound/httpapi"
)

// TestRenderLinearPlanOutcomeText is table-driven (§11) over every
// DecidePlanOutcome shape handlePlanVerdict can observe -- whether THIS
// reply's own verdict won, or a DIFFERENT channel already decided first
// (this Step's own point 5: "post an honest already-decided ... activity
// instead of a confusing duplicate"). Mirrors internal/adapters/inbound/
// slack's own renderPlanOutcomeText and its identical table-driven
// coverage.
func TestRenderLinearPlanOutcomeText(t *testing.T) {
	tests := []struct {
		name          string
		outcome       httpapi.DecidePlanOutcome
		wantSubstring string
		wantAlready   bool
	}{
		{
			name:          "won approve",
			outcome:       httpapi.DecidePlanOutcome{Won: true, FinalStatus: "approved"},
			wantSubstring: "Approved",
		},
		{
			name:          "lost, already approved elsewhere",
			outcome:       httpapi.DecidePlanOutcome{Won: false, FinalStatus: "approved"},
			wantSubstring: "already approved",
			wantAlready:   true,
		},
		{
			name:          "won reject",
			outcome:       httpapi.DecidePlanOutcome{Won: true, FinalStatus: "rejected"},
			wantSubstring: "Rejected",
		},
		{
			name:          "lost, already rejected elsewhere",
			outcome:       httpapi.DecidePlanOutcome{Won: false, FinalStatus: "rejected"},
			wantSubstring: "already rejected",
			wantAlready:   true,
		},
		{
			name:          "superseded by a newer revision",
			outcome:       httpapi.DecidePlanOutcome{Won: false, FinalStatus: "superseded"},
			wantSubstring: "superseded",
		},
		{
			name:          "unrecognized/empty final status",
			outcome:       httpapi.DecidePlanOutcome{Won: false, FinalStatus: ""},
			wantSubstring: "no longer awaiting approval",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := renderLinearPlanOutcomeText(tc.outcome)
			if got == "" {
				t.Fatal("renderLinearPlanOutcomeText() = \"\", want a non-empty message")
			}
			if !strings.Contains(got, tc.wantSubstring) {
				t.Errorf("renderLinearPlanOutcomeText(%+v) = %q, want it to contain %q", tc.outcome, got, tc.wantSubstring)
			}
			if tc.wantAlready && !strings.Contains(strings.ToLower(got), "different channel") {
				t.Errorf("renderLinearPlanOutcomeText(%+v) = %q, want an honest 'different channel' mention for a losing outcome", tc.outcome, got)
			}
		})
	}
}
