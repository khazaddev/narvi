package opencode

import (
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
)

// deriveOutcome and the no-output-is-failure / error-name-to-outcome
// mapping are hard to elicit reliably from a REAL model (§7's own quirks).
// Per this Step's own testing instructions, test the DERIVATION FUNCTION
// directly against constructed values instead -- more reliable, and it
// isolates the logic from live-model non-determinism.
func TestDeriveOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         *openCodeTaggedError
		hasText     bool
		hasToolCall bool
		wantOutcome sandboxws.ExecutionCompleteOutcome
		wantReason  bool // whether a non-nil Reason is expected
	}{
		{
			name:        "aborted turn maps to cancelled with no reason",
			err:         &openCodeTaggedError{Name: "MessageAbortedError"},
			wantOutcome: sandboxws.ExecutionCompleteOutcomeCancelled,
			wantReason:  false,
		},
		{
			name:        "any other tagged error maps to failed with a reason",
			err:         &openCodeTaggedError{Name: "ProviderAuthError"},
			wantOutcome: sandboxws.ExecutionCompleteOutcomeFailed,
			wantReason:  true,
		},
		{
			name:        "context overflow error maps to failed with a reason",
			err:         &openCodeTaggedError{Name: "ContextOverflowError"},
			wantOutcome: sandboxws.ExecutionCompleteOutcomeFailed,
			wantReason:  true,
		},
		{
			name:        "no error, has text -> completed",
			hasText:     true,
			wantOutcome: sandboxws.ExecutionCompleteOutcomeCompleted,
			wantReason:  false,
		},
		{
			name:        "no error, has tool call but no text -> completed",
			hasToolCall: true,
			wantOutcome: sandboxws.ExecutionCompleteOutcomeCompleted,
			wantReason:  false,
		},
		{
			name:        "no error, has both text and tool call -> completed",
			hasText:     true,
			hasToolCall: true,
			wantOutcome: sandboxws.ExecutionCompleteOutcomeCompleted,
			wantReason:  false,
		},
		{
			name:        "no error, no text, no tool call -> failed (no output is failure)",
			wantOutcome: sandboxws.ExecutionCompleteOutcomeFailed,
			wantReason:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := deriveOutcome(tt.err, tt.hasText, tt.hasToolCall)
			if got.Outcome != tt.wantOutcome {
				t.Errorf("deriveOutcome() outcome = %q, want %q", got.Outcome, tt.wantOutcome)
			}
			if hasReason := got.Reason != nil; hasReason != tt.wantReason {
				t.Errorf("deriveOutcome() reason present = %v, want %v (reason=%v)", hasReason, tt.wantReason, got.Reason)
			}
			if got.Reason != nil {
				if *got.Reason == "" {
					t.Error("deriveOutcome() returned a non-nil but empty reason")
				}
				// Never leak the tagged error's own raw data verbatim --
				// only the short, fixed Name-derived string.
			}
		})
	}
}

func TestSubTaskOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   sandboxws.ExecutionCompleteOutcome
		want sandboxws.SubTaskFinishOutcome
	}{
		{sandboxws.ExecutionCompleteOutcomeCompleted, sandboxws.SubTaskFinishOutcomeCompleted},
		{sandboxws.ExecutionCompleteOutcomeFailed, sandboxws.SubTaskFinishOutcomeFailed},
		{sandboxws.ExecutionCompleteOutcomeCancelled, sandboxws.SubTaskFinishOutcomeCancelled},
	}

	for _, tt := range tests {
		if got := subTaskOutcome(tt.in); got != tt.want {
			t.Errorf("subTaskOutcome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
