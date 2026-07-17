package turn_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/turn"
)

// TestRequiresSyntheticExecutionComplete is table-driven over every
// Trigger constant plus an out-of-range value, proving the split is
// exactly: real terminal events (Complete, Fail) need no synthesis;
// control-plane-internal decisions (Timeout, Abandon, Cancel) do; and
// non-terminal triggers (Dispatch, StartProcessing) — and anything
// unrecognized — report false since there is nothing to synthesize.
func TestRequiresSyntheticExecutionComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		trigger turn.Trigger
		want    bool
	}{
		{turn.TriggerDispatch, false},
		{turn.TriggerStartProcessing, false},
		{turn.TriggerComplete, false},
		{turn.TriggerFail, false},
		{turn.TriggerTimeout, true},
		{turn.TriggerAbandon, true},
		{turn.TriggerCancel, true},
		{turn.Trigger(999), false},
	}

	for _, tc := range tests {
		t.Run(tc.trigger.String(), func(t *testing.T) {
			t.Parallel()
			if got := turn.RequiresSyntheticExecutionComplete(tc.trigger); got != tc.want {
				t.Errorf("RequiresSyntheticExecutionComplete(%v) = %v, want %v", tc.trigger, got, tc.want)
			}
		})
	}
}
