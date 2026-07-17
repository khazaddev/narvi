package session_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/session"
	"github.com/khazaddev/narvi/internal/domain/turn"
)

// TestDeriveStatus is table-driven over every turn-outcome-history shape
// §3.1's derivation rule cares about: zero turns, every all-non-terminal
// shape (a lone Pending/Dispatched/Processing turn, and a non-terminal
// turn mixed with already-terminal ones), and every last-terminal outcome
// (Completed, Cancelled, and Failed with each of its three failure
// reasons) — including cases where an EARLIER turn's outcome differs from
// the last one, to prove only the last turn's outcome governs once all
// turns are terminal.
func TestDeriveStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []turn.Summary
		want session.DerivedStatus
	}{
		{
			name: "zero turns -> created",
			in:   nil,
			want: session.DerivedStatus{Status: session.StatusCreated},
		},
		{
			name: "lone pending turn -> active",
			in:   []turn.Summary{{Status: turn.StatePending}},
			want: session.DerivedStatus{Status: session.StatusActive},
		},
		{
			name: "lone dispatched turn -> active",
			in:   []turn.Summary{{Status: turn.StateDispatched}},
			want: session.DerivedStatus{Status: session.StatusActive},
		},
		{
			name: "lone processing turn -> active",
			in:   []turn.Summary{{Status: turn.StateProcessing}},
			want: session.DerivedStatus{Status: session.StatusActive},
		},
		{
			name: "terminal turn followed by a pending turn -> active",
			in: []turn.Summary{
				{Status: turn.StateCompleted},
				{Status: turn.StatePending},
			},
			want: session.DerivedStatus{Status: session.StatusActive},
		},
		{
			name: "non-terminal turn buried among terminal ones -> active",
			in: []turn.Summary{
				{Status: turn.StateFailed, FailureReason: turn.FailureReasonFailed},
				{Status: turn.StateProcessing},
				{Status: turn.StateCompleted},
			},
			want: session.DerivedStatus{Status: session.StatusActive},
		},
		{
			name: "all terminal, last completed -> completed, no reason",
			in: []turn.Summary{
				{Status: turn.StateFailed, FailureReason: turn.FailureReasonFailed},
				{Status: turn.StateCompleted},
			},
			want: session.DerivedStatus{Status: session.StatusCompleted},
		},
		{
			name: "all terminal, last cancelled -> cancelled",
			in: []turn.Summary{
				{Status: turn.StateCompleted},
				{Status: turn.StateCancelled, FailureReason: turn.FailureReasonCancelled},
			},
			want: session.DerivedStatus{Status: session.StatusCancelled, FailureReason: session.FailureReasonCancelled},
		},
		{
			name: "all terminal, last failed (generic failed) -> failed/failed",
			in: []turn.Summary{
				{Status: turn.StateCompleted},
				{Status: turn.StateFailed, FailureReason: turn.FailureReasonFailed},
			},
			want: session.DerivedStatus{Status: session.StatusFailed, FailureReason: session.FailureReasonFailed},
		},
		{
			name: "all terminal, last failed (timeout) -> failed/timeout",
			in: []turn.Summary{
				{Status: turn.StateCompleted},
				{Status: turn.StateFailed, FailureReason: turn.FailureReasonTimeout},
			},
			want: session.DerivedStatus{Status: session.StatusFailed, FailureReason: session.FailureReasonTimeout},
		},
		{
			name: "all terminal, last failed (never_started) -> failed/never_started",
			in: []turn.Summary{
				{Status: turn.StateCompleted},
				{Status: turn.StateFailed, FailureReason: turn.FailureReasonNeverStarted},
			},
			want: session.DerivedStatus{Status: session.StatusFailed, FailureReason: session.FailureReasonNeverStarted},
		},
		{
			name: "single completed turn -> completed",
			in:   []turn.Summary{{Status: turn.StateCompleted}},
			want: session.DerivedStatus{Status: session.StatusCompleted},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := session.DeriveStatus(tc.in)
			if got != tc.want {
				t.Errorf("DeriveStatus(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
