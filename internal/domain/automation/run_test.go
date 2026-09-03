package automation_test

import (
	"errors"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/automation"
	"github.com/narvidev/narvi/internal/domain/turn"
)

func TestRunTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    automation.RunStatus
		trigger automation.RunTrigger
		want    automation.RunStatus
		wantErr bool
	}{
		{"starting to running", automation.RunStatusStarting, automation.RunTriggerProcessing, automation.RunStatusRunning, false},
		{"starting create failed", automation.RunStatusStarting, automation.RunTriggerCreateFailed, automation.RunStatusFailed, false},
		{"starting orphaned", automation.RunStatusStarting, automation.RunTriggerOrphanTimeout, automation.RunStatusFailed, false},
		{"running completes", automation.RunStatusRunning, automation.RunTriggerComplete, automation.RunStatusSucceeded, false},
		{"running fails", automation.RunStatusRunning, automation.RunTriggerFail, automation.RunStatusFailed, false},
		{"running orphaned", automation.RunStatusRunning, automation.RunTriggerOrphanTimeout, automation.RunStatusFailed, false},
		{"starting cannot complete directly", automation.RunStatusStarting, automation.RunTriggerComplete, "", true},
		{"succeeded is terminal", automation.RunStatusSucceeded, automation.RunTriggerOrphanTimeout, "", true},
		{"failed is terminal", automation.RunStatusFailed, automation.RunTriggerOrphanTimeout, "", true},
		{"unknown status", automation.RunStatus("bogus"), automation.RunTriggerComplete, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := automation.RunTransition(tt.from, tt.trigger)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result %q)", got)
				}
				var target *automation.IllegalRunTransitionError
				if !errors.As(err, &target) {
					t.Fatalf("expected *IllegalRunTransitionError, got %T: %v", err, err)
				}
				if !errors.Is(err, automation.ErrIllegalRunTransition) {
					t.Fatalf("expected errors.Is ErrIllegalRunTransition, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsTerminal(t *testing.T) {
	tests := []struct {
		status automation.RunStatus
		want   bool
	}{
		{automation.RunStatusStarting, false},
		{automation.RunStatusRunning, false},
		{automation.RunStatusSucceeded, true},
		{automation.RunStatusFailed, true},
		{automation.RunStatus("bogus"), false},
	}
	for _, tt := range tests {
		if got := automation.IsTerminal(tt.status); got != tt.want {
			t.Errorf("IsTerminal(%q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestDeriveRunStatus(t *testing.T) {
	tests := []struct {
		name  string
		turns []turn.Summary
		want  automation.RunStatus
	}{
		{"no turns yet", nil, automation.RunStatusStarting},
		{"pending turn", []turn.Summary{{Status: turn.StatePending}}, automation.RunStatusStarting},
		{"dispatched turn", []turn.Summary{{Status: turn.StateDispatched}}, automation.RunStatusStarting},
		{"processing turn", []turn.Summary{{Status: turn.StateProcessing}}, automation.RunStatusRunning},
		{"completed turn", []turn.Summary{{Status: turn.StateCompleted}}, automation.RunStatusSucceeded},
		{"failed turn", []turn.Summary{{Status: turn.StateFailed, FailureReason: turn.FailureReasonFailed}}, automation.RunStatusFailed},
		{"timeout (failed) turn", []turn.Summary{{Status: turn.StateFailed, FailureReason: turn.FailureReasonTimeout}}, automation.RunStatusFailed},
		{"cancelled turn", []turn.Summary{{Status: turn.StateCancelled, FailureReason: turn.FailureReasonCancelled}}, automation.RunStatusFailed},
		{
			"one terminal, one still processing -- processing wins",
			[]turn.Summary{{Status: turn.StateCompleted}, {Status: turn.StateProcessing}},
			automation.RunStatusRunning,
		},
		{
			"one terminal, one still pending -- starting",
			[]turn.Summary{{Status: turn.StateFailed}, {Status: turn.StatePending}},
			automation.RunStatusStarting,
		},
		{
			"two terminal turns -- last one decides",
			[]turn.Summary{{Status: turn.StateFailed}, {Status: turn.StateCompleted}},
			automation.RunStatusSucceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := automation.DeriveRunStatus(tt.turns); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsOrphaned(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := automation.OrphanThresholds{
		StartingThreshold: 5 * time.Minute,
		RunningThreshold:  90 * time.Minute,
	}

	tests := []struct {
		name   string
		status automation.RunStatus
		since  time.Time
		want   bool
	}{
		{"starting exactly at threshold (not yet orphaned)", automation.RunStatusStarting, now.Add(-5 * time.Minute), false},
		{"starting just over threshold", automation.RunStatusStarting, now.Add(-5*time.Minute - time.Second), true},
		{"running just under threshold", automation.RunStatusRunning, now.Add(-90 * time.Minute), false},
		{"running just over threshold", automation.RunStatusRunning, now.Add(-90*time.Minute - time.Second), true},
		{"succeeded never orphaned", automation.RunStatusSucceeded, now.Add(-24 * time.Hour), false},
		{"failed never orphaned", automation.RunStatusFailed, now.Add(-24 * time.Hour), false},
		{"unknown status never orphaned", automation.RunStatus("bogus"), now.Add(-24 * time.Hour), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := automation.IsOrphaned(tt.status, tt.since, now, cfg); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunTriggerString(t *testing.T) {
	tests := map[automation.RunTrigger]string{
		automation.RunTriggerProcessing:    "processing",
		automation.RunTriggerComplete:      "complete",
		automation.RunTriggerFail:          "fail",
		automation.RunTriggerCreateFailed:  "create_failed",
		automation.RunTriggerOrphanTimeout: "orphan_timeout",
	}
	for trig, want := range tests {
		if got := trig.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
	if got := automation.RunTrigger(99).String(); got != "RunTrigger(99)" {
		t.Fatalf("got %q", got)
	}
}
