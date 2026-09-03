package turn_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/turn"
)

// TestHasInFlightTurn is table-driven over every shape of turn-status
// slice that matters: empty, all-terminal, a lone Pending, a lone
// Dispatched, a lone Processing, and a mix with the in-flight turn in
// different positions.
func TestHasInFlightTurn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		turns []turn.QueueEntry[string]
		want  bool
	}{
		{"empty", nil, false},
		{"all terminal, no in-flight", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateCompleted},
			{ID: "b", Status: turn.StateFailed},
			{ID: "c", Status: turn.StateCancelled},
		}, false},
		{"lone pending is not in-flight", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StatePending},
		}, false},
		{"lone dispatched is in-flight", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateDispatched},
		}, true},
		{"lone processing is in-flight", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateProcessing},
		}, true},
		{"in-flight turn buried after completed ones", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateCompleted},
			{ID: "b", Status: turn.StateProcessing},
			{ID: "c", Status: turn.StatePending},
		}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := turn.HasInFlightTurn(tc.turns); got != tc.want {
				t.Errorf("HasInFlightTurn(%+v) = %v, want %v", tc.turns, got, tc.want)
			}
		})
	}
}

// TestNextToDispatch is table-driven over: no turns at all, an in-flight
// turn present (nothing should dispatch regardless of pending turns),
// no pending turn waiting, and multiple pending turns (must pick the
// oldest, i.e. first-encountered).
func TestNextToDispatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		turns  []turn.QueueEntry[string]
		wantID string
		wantOK bool
	}{
		{"no turns", nil, "", false},
		{"in-flight turn blocks dispatch even with pending waiting", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateProcessing},
			{ID: "b", Status: turn.StatePending},
		}, "", false},
		{"no pending turn waiting", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateCompleted},
			{ID: "b", Status: turn.StateFailed},
		}, "", false},
		{"single pending turn dispatches", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateCompleted},
			{ID: "b", Status: turn.StatePending},
		}, "b", true},
		{"oldest-first among multiple pending turns", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StatePending},
			{ID: "b", Status: turn.StatePending},
		}, "a", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotID, gotOK := turn.NextToDispatch(tc.turns)
			if gotOK != tc.wantOK {
				t.Errorf("NextToDispatch(%+v) ok = %v, want %v", tc.turns, gotOK, tc.wantOK)
			}
			if gotID != tc.wantID {
				t.Errorf("NextToDispatch(%+v) id = %q, want %q", tc.turns, gotID, tc.wantID)
			}
		})
	}
}

// TestInFlightTurn is table-driven over: no turns, no in-flight turn (all
// terminal or all pending), a lone Dispatched turn, a lone Processing
// turn, and an in-flight turn buried among terminal ones -- mirroring
// TestHasInFlightTurn's own case selection, but asserting WHICH id comes
// back, not merely whether one does.
func TestInFlightTurn(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		turns  []turn.QueueEntry[string]
		wantID string
		wantOK bool
	}{
		{"empty", nil, "", false},
		{"no in-flight turn (all terminal)", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateCompleted},
			{ID: "b", Status: turn.StateFailed},
		}, "", false},
		{"no in-flight turn (a lone pending one)", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StatePending},
		}, "", false},
		{"lone dispatched turn is in-flight", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateDispatched},
		}, "a", true},
		{"lone processing turn is in-flight", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateProcessing},
		}, "a", true},
		{"in-flight turn buried after completed ones", []turn.QueueEntry[string]{
			{ID: "a", Status: turn.StateCompleted},
			{ID: "b", Status: turn.StateProcessing},
			{ID: "c", Status: turn.StateCancelled},
		}, "b", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotID, gotOK := turn.InFlightTurn(tc.turns)
			if gotOK != tc.wantOK {
				t.Errorf("InFlightTurn(%+v) ok = %v, want %v", tc.turns, gotOK, tc.wantOK)
			}
			if gotID != tc.wantID {
				t.Errorf("InFlightTurn(%+v) id = %q, want %q", tc.turns, gotID, tc.wantID)
			}
		})
	}
}

// TestNeedsReenqueue is table-driven over: a matching gen (must NOT
// re-enqueue -- the single most safety-critical case in this whole Step),
// a stale (lower) gen after a respawn, and a nil dispatched_sandbox_gen
// (a turn dispatched before this column existed, or otherwise never
// stamped -- treated identically to a genuine mismatch).
func TestNeedsReenqueue(t *testing.T) {
	t.Parallel()

	gen1 := 1
	gen2 := 2

	tests := []struct {
		name          string
		dispatchedGen *int
		currentGen    int
		want          bool
	}{
		{"matching gen -> already correctly dispatched, must not re-enqueue", &gen1, 1, false},
		{"stale gen after a respawn -> needs re-enqueue", &gen1, 2, true},
		{"nil dispatched_sandbox_gen -> treated as needing re-enqueue", nil, 1, true},
		{"matching higher gen -> must not re-enqueue", &gen2, 2, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := turn.NeedsReenqueue(tc.dispatchedGen, tc.currentGen); got != tc.want {
				t.Errorf("NeedsReenqueue(%v, %d) = %v, want %v", tc.dispatchedGen, tc.currentGen, got, tc.want)
			}
		})
	}
}
