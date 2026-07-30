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

// isContextOverflowError implements §7.2's own "classify on the typed
// discriminator already decoded" design point — err.Name ==
// "ContextOverflowError" is a genuine typed signal (openCodeTaggedError,
// types.go), not string-matching a provider error body, the same
// discipline §4.1 requires of ProviderError and §18.1 requires of
// FallbackReason. Used by Adapter.finalizeOrRecoverFromOverflow
// (adapter.go) to decide whether a failed outcome is even eligible for a
// compaction retry at all; nil (no error at all, or a nil tagged error) is
// never a ContextOverflowError.
func isContextOverflowError(err *openCodeTaggedError) bool {
	return err != nil && err.Name == "ContextOverflowError"
}

// enrichReasonForFailedRecovery builds the finalize reason used when a
// §7.2 compaction-retry recovery attempt itself failed -- either
// forceCompaction (the POST /session/{id}/summarize call) errored, or the
// retried postPromptAsync dispatch itself failed at the transport level
// (Adapter.attemptCompactionRetry, adapter.go). Deliberately references the
// ORIGINAL overflow outcome's own reason (originalReason) alongside detail
// (a short description of what failed, e.g. "forceCompaction: <err>"), so
// the final reason string an operator sees names BOTH facts: the original
// failure that triggered the recovery attempt, AND that a recovery was
// attempted and also failed -- never a bare "opencode: ContextOverflowError"
// indistinguishable from a turn where no recovery was ever attempted at
// all.
func enrichReasonForFailedRecovery(originalReason *string, detail string) string {
	original := "opencode: ContextOverflowError"
	if originalReason != nil {
		original = *originalReason
	}
	return fmt.Sprintf("%s (compaction retry attempted and failed: %s)", original, detail)
}

// enrichReasonForRepeatedOverflow builds the finalize reason used when the
// RETRIED prompt also overflowed (Adapter.finalizeOrRecoverFromOverflow,
// adapter.go, ts.compactionAlreadyAttempted() already true) -- §7.2 point
// 3's own "the original deferred error is what reaches deriveOutcome...
// never a silent double failure" requirement: without this, the final
// reason string would read identically to a first-time, never-retried
// overflow, giving an operator no way to tell a recovery WAS attempted.
// retryReason is the retried turn's OWN outcome.Reason (not the original
// turn's) -- by the time this is called, that IS the current failure worth
// naming, with the added context that a compaction retry already happened
// once this turn and is not going to happen again (§7.2's own at-most-once
// guard).
func enrichReasonForRepeatedOverflow(retryReason *string) string {
	reason := "opencode: ContextOverflowError"
	if retryReason != nil {
		reason = *retryReason
	}
	return fmt.Sprintf("%s (a compaction retry was already attempted this turn; the retried prompt also overflowed)", reason)
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
