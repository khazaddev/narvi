package ports

import (
	"errors"
	"fmt"
)

// Op names the SandboxProvider method that produced a ProviderError — one
// constant per interface method (sandboxprovider.go) — so every
// implementation (Modal today, a future RWX provider at Step 48, ...)
// logs/reports failures against the SAME fixed vocabulary instead of each
// adapter inventing its own strings.
type Op string

// The complete set of Op values, one per SandboxProvider method.
const (
	OpCreateSandbox       Op = "CreateSandbox"
	OpStopSandbox         Op = "StopSandbox"
	OpResumeSandbox       Op = "ResumeSandbox"
	OpTakeSnapshot        Op = "TakeSnapshot"
	OpRestoreFromSnapshot Op = "RestoreFromSnapshot"
	OpBuildImage          Op = "BuildImage"
	OpDeleteImage         Op = "DeleteImage"
	OpList                Op = "List"
)

// ProviderError is the typed error every SandboxProvider method returns on
// failure (§4.1: "Errors are typed ProviderError{Transient bool} —
// classification by provider-specific error codes, never by
// string-matching messages").
//
// ProviderError itself is provider-agnostic: it holds the classification
// OUTCOME (Transient) and the provider's own code for debugging, but the
// CLASSIFICATION LOGIC — which codes map to which outcome — is entirely
// an adapter's own responsibility (e.g.
// internal/adapters/outbound/modal's status-code classification table; a
// future RWX provider would have its own). Nothing provider-specific
// belongs in this package.
type ProviderError struct {
	// Transient reports whether the caller should retry with backoff
	// (true) or the failure is permanent and should count against the
	// spawn circuit breaker (false; §3.2: "3 permanent spawn failures
	// within 5 min blocks spawning").
	Transient bool

	// Code is the provider-specific error code that drove the
	// classification (an HTTP status class, a provider error-code field,
	// ...) — kept for logging/debugging. Callers must never re-parse
	// this to reclassify the error themselves; that would reintroduce
	// the string-matching §4.1 forbids. Consult Transient instead.
	Code string

	// Op is which SandboxProvider method produced this error (one of the
	// Op constants above).
	Op Op

	// Err is the wrapped underlying error (an HTTP transport error, a
	// decode error, ...).
	Err error
}

func (e *ProviderError) Error() string {
	class := "permanent"
	if e.Transient {
		class = "transient"
	}
	return fmt.Sprintf("provider: %s: %s (code=%s): %v", e.Op, class, e.Code, e.Err)
}

// Unwrap exposes the wrapped underlying error to errors.Is/errors.As.
func (e *ProviderError) Unwrap() error { return e.Err }

// IsTransient reports whether err should be retried with backoff rather
// than treated as a permanent failure that trips the spawn circuit
// breaker.
//
// Contract: an err that is not a *ProviderError at all (whether directly
// or wrapped, via errors.As) is treated as TRANSIENT. This matches §3.2:
// "Unknown provider errors default to transient, never permanent — a
// novel transient failure must not trip the breaker." It is deliberately
// the safer default in both directions an unclassified error could go:
// treating an unknown failure as PERMANENT risks tripping the breaker
// (and blocking spawning for the session) on something that might have
// succeeded on retry — a context cancellation, a bug in some future
// adapter that returns a bare error instead of constructing a
// ProviderError, or a genuinely novel failure mode no classification
// table has seen yet. Treating it as transient instead only costs one
// extra retry — it never wrongly opens the breaker. Callers that need to
// distinguish "classified transient" from "unclassified entirely" should
// type-assert/errors.As for *ProviderError themselves instead of calling
// this helper.
//
// A nil err is treated as not transient (false): there is no failure to
// retry.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe.Transient
	}
	return true
}
