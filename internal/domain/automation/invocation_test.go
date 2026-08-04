package automation_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/automation"
)

func TestInvocationTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    automation.InvocationStatus
		trigger automation.InvocationTrigger
		want    automation.InvocationStatus
		wantErr bool
	}{
		{"pending succeeds", automation.InvocationStatusPending, automation.TriggerAllRunsSucceeded, automation.InvocationStatusSucceeded, false},
		{"pending fails", automation.InvocationStatusPending, automation.TriggerAnyRunFailed, automation.InvocationStatusFailed, false},
		{"succeeded is terminal", automation.InvocationStatusSucceeded, automation.TriggerAllRunsSucceeded, "", true},
		{"failed is terminal", automation.InvocationStatusFailed, automation.TriggerAnyRunFailed, "", true},
		{"unknown status", automation.InvocationStatus("bogus"), automation.TriggerAllRunsSucceeded, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := automation.InvocationTransition(tt.from, tt.trigger)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result %q)", got)
				}
				var target *automation.IllegalInvocationTransitionError
				if !errors.As(err, &target) {
					t.Fatalf("expected *IllegalInvocationTransitionError, got %T: %v", err, err)
				}
				if !errors.Is(err, automation.ErrIllegalInvocationTransition) {
					t.Fatalf("expected errors.Is ErrIllegalInvocationTransition, got %v", err)
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

func TestEvaluateInvocationOutcome(t *testing.T) {
	tests := []struct {
		name                                string
		totalRuns, terminalRuns, failedRuns int
		wantReady, wantFailed               bool
		wantTrigger                         automation.InvocationTrigger
	}{
		{"not all terminal yet", 3, 2, 0, false, false, 0},
		{"zero terminal of many", 5, 0, 0, false, false, 0},
		{"all succeeded", 3, 3, 0, true, false, automation.TriggerAllRunsSucceeded},
		{"one of many failed", 3, 3, 1, true, true, automation.TriggerAnyRunFailed},
		{"all failed", 3, 3, 3, true, true, automation.TriggerAnyRunFailed},
		{"single target succeeds", 1, 1, 0, true, false, automation.TriggerAllRunsSucceeded},
		{"single target fails", 1, 1, 1, true, true, automation.TriggerAnyRunFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := automation.EvaluateInvocationOutcome(tt.totalRuns, tt.terminalRuns, tt.failedRuns)
			if got.Ready != tt.wantReady {
				t.Fatalf("Ready = %v, want %v", got.Ready, tt.wantReady)
			}
			if !tt.wantReady {
				return
			}
			if got.Failed != tt.wantFailed {
				t.Fatalf("Failed = %v, want %v", got.Failed, tt.wantFailed)
			}
			if got.Trigger != tt.wantTrigger {
				t.Fatalf("Trigger = %v, want %v", got.Trigger, tt.wantTrigger)
			}
		})
	}
}

func TestInvocationTriggerString(t *testing.T) {
	if got := automation.TriggerAllRunsSucceeded.String(); got != "all_runs_succeeded" {
		t.Fatalf("got %q", got)
	}
	if got := automation.TriggerAnyRunFailed.String(); got != "any_run_failed" {
		t.Fatalf("got %q", got)
	}
	if got := automation.InvocationTrigger(99).String(); got != "InvocationTrigger(99)" {
		t.Fatalf("got %q", got)
	}
}
