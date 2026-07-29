package plan_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/plan"
)

// TestTransition_LegalEdges is table-driven over every legal (from,
// trigger) edge the plan machine defines: AwaitingApproval is the sole
// non-terminal status, and each of its three triggers lands on the
// matching terminal status. Mirrors internal/domain/turn's own
// TestTransition_LegalEdges shape exactly.
func TestTransition_LegalEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		from    plan.Status
		trigger plan.Trigger
		want    plan.Status
	}{
		{"awaiting_approval -> approved (approve)", plan.StatusAwaitingApproval, plan.TriggerApprove, plan.StatusApproved},
		{"awaiting_approval -> rejected (reject)", plan.StatusAwaitingApproval, plan.TriggerReject, plan.StatusRejected},
		{"awaiting_approval -> superseded (supersede)", plan.StatusAwaitingApproval, plan.TriggerSupersede, plan.StatusSuperseded},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := plan.Transition(tc.from, tc.trigger)
			if err != nil {
				t.Fatalf("Transition(%s, %v) unexpected error: %v", tc.from, tc.trigger, err)
			}
			if got != tc.want {
				t.Errorf("Transition(%s, %v) = %s, want %s", tc.from, tc.trigger, got, tc.want)
			}
		})
	}
}

// TestTransition_IllegalEdges covers every trigger applied to each of the
// three terminal statuses (Approved/Rejected/Superseded have no outgoing
// edges at all, matching turn's own "terminal states absent from the map"
// convention) plus an unrecognized status.
func TestTransition_IllegalEdges(t *testing.T) {
	t.Parallel()

	allTriggers := []plan.Trigger{plan.TriggerApprove, plan.TriggerReject, plan.TriggerSupersede}

	for _, from := range []plan.Status{plan.StatusApproved, plan.StatusRejected, plan.StatusSuperseded} {
		for _, trig := range allTriggers {
			t.Run("terminal status "+string(from)+" has no outgoing edge via "+trig.String(), func(t *testing.T) {
				t.Parallel()
				assertIllegal(t, from, trig)
			})
		}
	}

	t.Run("unknown status is always illegal", func(t *testing.T) {
		t.Parallel()
		assertIllegal(t, plan.Status("bogus"), plan.TriggerApprove)
	})
}

func assertIllegal(t *testing.T, from plan.Status, trig plan.Trigger) {
	t.Helper()

	_, err := plan.Transition(from, trig)
	if err == nil {
		t.Fatalf("Transition(%s, %v) = nil error, want an error", from, trig)
	}
	if !errors.Is(err, plan.ErrIllegalTransition) {
		t.Errorf("Transition(%s, %v) error = %v, want errors.Is(err, ErrIllegalTransition)", from, trig, err)
	}
	var illegal *plan.IllegalTransitionError
	if !errors.As(err, &illegal) {
		t.Fatalf("Transition(%s, %v) error = %v, want *IllegalTransitionError", from, trig, err)
	}
	if illegal.From != from || illegal.Trigger != trig {
		t.Errorf("IllegalTransitionError = %+v, want From=%s Trigger=%s", illegal, from, trig)
	}
	if illegal.Error() == "" {
		t.Error("IllegalTransitionError.Error() is empty")
	}
}

// TestTrigger_String covers every named Trigger constant plus the
// out-of-range fallback branch.
func TestTrigger_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		trigger plan.Trigger
		want    string
	}{
		{plan.TriggerApprove, "approve"},
		{plan.TriggerReject, "reject"},
		{plan.TriggerSupersede, "supersede"},
		{plan.Trigger(-1), "Trigger(-1)"},
		{plan.Trigger(999), "Trigger(999)"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.trigger.String(); got != tc.want {
				t.Errorf("Trigger(%d).String() = %q, want %q", int(tc.trigger), got, tc.want)
			}
		})
	}
}
