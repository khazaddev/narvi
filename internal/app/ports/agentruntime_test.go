package ports_test

import (
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/sandboxws"
	"github.com/narvidev/narvi/internal/app/ports"
)

func TestClassifyAgentEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		payload      any
		wantCritical bool
		wantAckID    string
	}{
		{
			name:         "execution_complete is critical",
			payload:      sandboxws.ExecutionComplete{AckId: "execution_complete:m1"},
			wantCritical: true,
			wantAckID:    "execution_complete:m1",
		},
		{
			name:         "error is critical",
			payload:      sandboxws.SandboxErrorEvent{AckId: "error:m1"},
			wantCritical: true,
			wantAckID:    "error:m1",
		},
		{
			name:         "snapshot_ready is critical",
			payload:      sandboxws.SnapshotReady{AckId: "snapshot_ready:m1"},
			wantCritical: true,
			wantAckID:    "snapshot_ready:m1",
		},
		{
			name:         "push_complete is critical",
			payload:      sandboxws.PushComplete{AckId: "push_complete:m1"},
			wantCritical: true,
			wantAckID:    "push_complete:m1",
		},
		{
			name:         "push_error is critical",
			payload:      sandboxws.PushError{AckId: "push_error:m1"},
			wantCritical: true,
			wantAckID:    "push_error:m1",
		},
		{
			name:         "sub_task_finish is critical (the 6th critical type, §6.1)",
			payload:      sandboxws.SubTaskFinish{AckId: "sub_task_finish:m1"},
			wantCritical: true,
			wantAckID:    "sub_task_finish:m1",
		},
		{
			name:         "sub_task_start is NOT critical",
			payload:      sandboxws.SubTaskStart{MessageId: "m1"},
			wantCritical: false,
		},
		{
			name:         "token is not critical",
			payload:      sandboxws.Token{MessageId: "m1"},
			wantCritical: false,
		},
		{
			name:         "tool_call is not critical",
			payload:      sandboxws.ToolCall{MessageId: "m1"},
			wantCritical: false,
		},
		{
			name:         "an unrecognized payload type is not critical",
			payload:      "not an event",
			wantCritical: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			critical, ackID := ports.ClassifyAgentEvent(tt.payload)
			if critical != tt.wantCritical {
				t.Errorf("ClassifyAgentEvent() critical = %v, want %v", critical, tt.wantCritical)
			}
			if ackID != tt.wantAckID {
				t.Errorf("ClassifyAgentEvent() ackID = %q, want %q", ackID, tt.wantAckID)
			}
		})
	}

	// Exactly 6 critical types total (§6.1) -- count them directly rather
	// than only trusting the table above's own selection.
	criticalCount := 0
	for _, tt := range tests {
		if tt.wantCritical {
			criticalCount++
		}
	}
	if criticalCount != 6 {
		t.Errorf("test table exercises %d critical cases, want exactly 6", criticalCount)
	}
}
