package opencode

import (
	"fmt"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
)

// turnOutcome is the result of deriveOutcome — the pure, directly
// unit-testable core of §7's own "treat 'no output' as failure" and
// error-name-to-outcome derivation logic. Kept in its own file/pure
// function specifically so it can be tested against constructed
// values rather than a live model (this Step's own testing instructions:
// "test the DERIVATION FUNCTION itself directly... this is both more
// reliable and a better test anyway").
type turnOutcome struct {
	Outcome sandboxws.ExecutionCompleteOutcome
	Reason  *string
}

// deriveOutcome implements the ground-truth mapping this Step's own
// research pass verified live (an aborted turn's own session.error +
// final message.updated both carried {"name":"MessageAbortedError"}):
//
//   - err != nil && err.Name == "MessageAbortedError" -> "cancelled"
//     (matches Narvi's own Stop-initiated cancellation semantics, §3.3's
//     taxonomy). No reason is set — the outcome itself already says why.
//   - err != nil (any other tagged-union name) -> "failed", with a short,
//     error-name-derived reason. The raw provider error body is NEVER
//     embedded here (even though err only carries a Name, never a raw
//     body, by construction — see openCodeTaggedError's own doc comment)
//     — mirroring the Step 12/15 "never leak a raw response body" lesson
//     defensively.
//   - err == nil && (hasText || hasToolCall) -> "completed".
//   - err == nil && !hasText && !hasToolCall -> "failed" (§7's own
//     explicit "treat 'no output' as failure" quirk: a model that
//     produced nothing must never silently read as success).
func deriveOutcome(err *openCodeTaggedError, hasText, hasToolCall bool) turnOutcome {
	if err != nil {
		if err.Name == "MessageAbortedError" {
			return turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeCancelled}
		}
		reason := fmt.Sprintf("opencode: %s", err.Name)
		return turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeFailed, Reason: &reason}
	}

	if hasText || hasToolCall {
		return turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeCompleted}
	}

	reason := "opencode: turn produced no output"
	return turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeFailed, Reason: &reason}
}

// subTaskOutcome maps an already-derived turn-level outcome onto
// SubTaskFinish's own (separately generated, but value-identical) outcome
// enum — see turn.go's own doc comment on why this adapter closes a
// still-open sub-task using the ENCLOSING turn's outcome rather than a
// truly isolated signal (a documented best-effort simplification, §7.1).
func subTaskOutcome(o sandboxws.ExecutionCompleteOutcome) sandboxws.SubTaskFinishOutcome {
	switch o {
	case sandboxws.ExecutionCompleteOutcomeCancelled:
		return sandboxws.SubTaskFinishOutcomeCancelled
	case sandboxws.ExecutionCompleteOutcomeFailed:
		return sandboxws.SubTaskFinishOutcomeFailed
	default:
		return sandboxws.SubTaskFinishOutcomeCompleted
	}
}
