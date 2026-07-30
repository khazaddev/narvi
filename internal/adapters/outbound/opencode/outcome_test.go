package opencode

import (
	"strings"
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

// TestIsContextOverflowError mirrors TestDeriveOutcome's own table-driven
// style: isContextOverflowError is §7.2's own "classify on the typed
// discriminator already decoded" logic, hard-and-non-deterministic to
// elicit reliably from a real model (a real ContextOverflowError requires
// an actually-overflowed real context window), so tested directly against
// constructed values instead.
func TestIsContextOverflowError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *openCodeTaggedError
		want bool
	}{
		{name: "nil error is never a context overflow", err: nil, want: false},
		{name: "ContextOverflowError is a context overflow", err: &openCodeTaggedError{Name: "ContextOverflowError"}, want: true},
		{name: "MessageAbortedError is not a context overflow", err: &openCodeTaggedError{Name: "MessageAbortedError"}, want: false},
		{name: "ProviderAuthError is not a context overflow", err: &openCodeTaggedError{Name: "ProviderAuthError"}, want: false},
		{name: "empty name is not a context overflow", err: &openCodeTaggedError{Name: ""}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isContextOverflowError(tt.err); got != tt.want {
				t.Errorf("isContextOverflowError(%+v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestEnrichReasonForFailedRecovery proves the reason-enrichment logic
// used when a compaction/retry recovery attempt itself failed
// (Adapter.attemptCompactionRetry, adapter.go): the result must reference
// BOTH the original overflow reason AND the new failure detail, so an
// operator can distinguish this from a first-time, never-retried overflow.
func TestEnrichReasonForFailedRecovery(t *testing.T) {
	t.Parallel()

	original := "opencode: ContextOverflowError"
	tests := []struct {
		name           string
		originalReason *string
		detail         string
		wantContains   []string
	}{
		{
			name:           "nil original reason falls back to a fixed default",
			originalReason: nil,
			detail:         "forceCompaction: boom",
			wantContains:   []string{"ContextOverflowError", "forceCompaction: boom"},
		},
		{
			name:           "non-nil original reason is preserved verbatim",
			originalReason: &original,
			detail:         "retry postPromptAsync: boom",
			wantContains:   []string{original, "retry postPromptAsync: boom"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := enrichReasonForFailedRecovery(tt.originalReason, tt.detail)
			if got == "" {
				t.Fatal("enrichReasonForFailedRecovery() returned an empty string")
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("enrichReasonForFailedRecovery() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// TestEnrichReasonForRepeatedOverflow proves the reason-enrichment logic
// used when a RETRIED prompt also overflowed
// (Adapter.finalizeOrRecoverFromOverflow, adapter.go, compactionAttempted
// already true): the result must mention that a retry was already
// attempted, so the final reason string visibly differs from a first-time
// overflow (§7.2 point 3's own "never a silent double failure"
// requirement).
func TestEnrichReasonForRepeatedOverflow(t *testing.T) {
	t.Parallel()

	retryReason := "opencode: ContextOverflowError"
	tests := []struct {
		name         string
		retryReason  *string
		wantContains []string
	}{
		{
			name:         "nil retry reason falls back to a fixed default",
			retryReason:  nil,
			wantContains: []string{"ContextOverflowError", "already attempted"},
		},
		{
			name:         "non-nil retry reason is preserved verbatim",
			retryReason:  &retryReason,
			wantContains: []string{retryReason, "already attempted"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := enrichReasonForRepeatedOverflow(tt.retryReason)
			if got == "" {
				t.Fatal("enrichReasonForRepeatedOverflow() returned an empty string")
			}
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("enrichReasonForRepeatedOverflow() = %q, want it to contain %q", got, want)
				}
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
