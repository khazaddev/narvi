package automation_test

import (
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/domain/automation"
)

func TestTransition(t *testing.T) {
	tests := []struct {
		name    string
		from    automation.Status
		trigger automation.Trigger
		want    automation.Status
		wantErr bool
	}{
		{"active auto-pauses", automation.StatusActive, automation.TriggerAutoPause, automation.StatusPaused, false},
		{"paused resumes", automation.StatusPaused, automation.TriggerResume, automation.StatusActive, false},
		{"active cannot resume", automation.StatusActive, automation.TriggerResume, "", true},
		{"paused cannot auto-pause again", automation.StatusPaused, automation.TriggerAutoPause, "", true},
		{"unknown status", automation.Status("bogus"), automation.TriggerAutoPause, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := automation.Transition(tt.from, tt.trigger)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (result %q)", got)
				}
				var target *automation.IllegalTransitionError
				if !errors.As(err, &target) {
					t.Fatalf("expected *IllegalTransitionError, got %T: %v", err, err)
				}
				if !errors.Is(err, automation.ErrIllegalTransition) {
					t.Fatalf("expected errors.Is ErrIllegalTransition, got %v", err)
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

func TestTriggerString(t *testing.T) {
	if got := automation.TriggerAutoPause.String(); got != "auto_pause" {
		t.Fatalf("got %q", got)
	}
	if got := automation.TriggerResume.String(); got != "resume" {
		t.Fatalf("got %q", got)
	}
	if got := automation.Trigger(99).String(); got != "Trigger(99)" {
		t.Fatalf("got %q", got)
	}
}
