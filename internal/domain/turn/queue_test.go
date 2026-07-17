package turn_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/turn"
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
