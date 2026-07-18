package sessionactor

import (
	"encoding/json"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/sandbox"
)

// TestSandboxTransitionTrigger is table-driven over every (event type,
// LastBootPhase, status) combination that matters: the two in-scope
// mappings this Step implements, each exercised both ON its precondition
// and OFF it (proving the precondition check, not just the trigger kind,
// gates the decision), plus a handful of event types/status pairs this
// Step deliberately does not map (including a Suspect status, proving no
// recovery transition is ever speculatively attempted for it -- Step 24's
// own job, see this file's top comment).
func TestSandboxTransitionTrigger(t *testing.T) {
	t.Parallel()

	bootPhase := "web:starting"

	tests := []struct {
		name          string
		eventType     string
		lastBootPhase *string
		status        sandbox.State
		wantOK        bool
		wantKind      sandbox.TriggerKind
	}{
		{
			name:      "ready while connecting fires WSConnected",
			eventType: "ready",
			status:    sandbox.StateConnecting,
			wantOK:    true,
			wantKind:  sandbox.TriggerWSConnected,
		},
		{
			name:      "ready while already ready is a no-op",
			eventType: "ready",
			status:    sandbox.StateReady,
			wantOK:    false,
		},
		{
			name:      "ready while suspect is a no-op (no speculative recovery, Step 24)",
			eventType: "ready",
			status:    sandbox.StateSuspect,
			wantOK:    false,
		},
		{
			name:      "ready while pending is a no-op",
			eventType: "ready",
			status:    sandbox.StatePending,
			wantOK:    false,
		},
		{
			name:      "heartbeat with nil LastBootPhase while booting fires BootComplete",
			eventType: "heartbeat",
			status:    sandbox.StateBooting,
			wantOK:    true,
			wantKind:  sandbox.TriggerBootComplete,
		},
		{
			name:          "heartbeat with a non-nil LastBootPhase while booting is a no-op",
			eventType:     "heartbeat",
			lastBootPhase: &bootPhase,
			status:        sandbox.StateBooting,
			wantOK:        false,
		},
		{
			name:      "heartbeat with nil LastBootPhase while already ready is a no-op",
			eventType: "heartbeat",
			status:    sandbox.StateReady,
			wantOK:    false,
		},
		{
			name:      "heartbeat with nil LastBootPhase while suspect is a no-op",
			eventType: "heartbeat",
			status:    sandbox.StateSuspect,
			wantOK:    false,
		},
		{
			name:      "execution_complete never transitions anything",
			eventType: "execution_complete",
			status:    sandbox.StateReady,
			wantOK:    false,
		},
		{
			name:      "token event never transitions anything",
			eventType: "token",
			status:    sandbox.StateBooting,
			wantOK:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			trig, ok := sandboxTransitionTrigger(tc.eventType, tc.lastBootPhase, tc.status)
			if ok != tc.wantOK {
				t.Fatalf("sandboxTransitionTrigger(%q, %v, %s) ok = %v, want %v",
					tc.eventType, tc.lastBootPhase, tc.status, ok, tc.wantOK)
			}
			if ok && trig.Kind != tc.wantKind {
				t.Errorf("sandboxTransitionTrigger(%q, %v, %s) kind = %s, want %s",
					tc.eventType, tc.lastBootPhase, tc.status, trig.Kind, tc.wantKind)
			}
		})
	}
}

// TestPeekAckID proves the ackId extraction: present-and-non-empty on a
// synthetic critical-shaped payload, absent on a non-critical-shaped one,
// and defensively "" (never a panic) on malformed JSON.
func TestPeekAckID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "critical event carries ackId",
			raw:  json.RawMessage(`{"type":"execution_complete","messageId":"m1","ackId":"execution_complete:m1"}`),
			want: "execution_complete:m1",
		},
		{
			name: "non-critical event has no ackId field",
			raw:  json.RawMessage(`{"type":"token","messageId":"m1"}`),
			want: "",
		},
		{
			name: "ackId explicitly empty string",
			raw:  json.RawMessage(`{"type":"heartbeat","ackId":""}`),
			want: "",
		},
		{
			name: "malformed JSON never panics, returns empty",
			raw:  json.RawMessage(`not json`),
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := peekAckID(tc.raw); got != tc.want {
				t.Errorf("peekAckID(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
