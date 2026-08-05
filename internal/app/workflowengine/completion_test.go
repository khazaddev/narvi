package workflowengine

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/domain/workflow"
)

// TestImplicitOutcome covers every terminal turn.Trigger OnTurnCompleted
// can ever be called with (see completion.go's own top doc comment for the
// three call sites) -- only TriggerComplete maps to StepOutcomeOK; every
// other one maps to StepOutcomeBlocked, never StepOutcomeNeedsFix (which
// is reserved for an agent's own explicit, typed report).
func TestImplicitOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		trig turn.Trigger
		want workflow.StepOutcomeStatus
	}{
		{trig: turn.TriggerComplete, want: workflow.StepOutcomeOK},
		{trig: turn.TriggerFail, want: workflow.StepOutcomeBlocked},
		{trig: turn.TriggerTimeout, want: workflow.StepOutcomeBlocked},
		{trig: turn.TriggerCancel, want: workflow.StepOutcomeBlocked},
	}

	for _, tc := range tests {
		t.Run(tc.trig.String(), func(t *testing.T) {
			t.Parallel()
			if got := implicitOutcome(tc.trig); got != tc.want {
				t.Errorf("implicitOutcome(%s) = %q, want %q", tc.trig, got, tc.want)
			}
		})
	}
}

// TestStepRunTerminalStatus covers the same four triggers, mapped onto
// workflow_step_run_status instead: TriggerComplete/TriggerCancel keep
// their own distinct status; TriggerFail and TriggerTimeout (both real,
// distinct reasons a turn ends up Failed, per internal/domain/turn's own
// State transition table) both land on "failed".
func TestStepRunTerminalStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		trig turn.Trigger
		want string
	}{
		{trig: turn.TriggerComplete, want: "completed"},
		{trig: turn.TriggerFail, want: "failed"},
		{trig: turn.TriggerTimeout, want: "failed"},
		{trig: turn.TriggerCancel, want: "cancelled"},
	}

	for _, tc := range tests {
		t.Run(tc.trig.String(), func(t *testing.T) {
			t.Parallel()
			if got := stepRunTerminalStatus(tc.trig); got != tc.want {
				t.Errorf("stepRunTerminalStatus(%s) = %q, want %q", tc.trig, got, tc.want)
			}
		})
	}
}
