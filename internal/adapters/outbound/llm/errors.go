package llm

import (
	"context"
	"errors"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// classifyCompletionError maps err (whatever client.Messages.New returned)
// onto one of the FIXED FallbackReason-mirroring LLMErrorCode values,
// purely via typed/structural signals -- NEVER by string-matching the
// human-readable message (§18.1, mirroring ports.ProviderError's own
// house rule; see internal/adapters/outbound/modal/errors.go's identical
// "classification table, never string-matching" precedent).
//
// Classification table:
//
//	err wraps context.DeadlineExceeded (the SDK's own configured
//	  option.WithRequestTimeout abort — see internal/platform/timeouts.go's
//	  own IntentClassifierLLMTimeout doc comment for why this is the ONLY
//	  timeout layer, never raced against a second, manually-armed
//	  context.WithTimeout): CodeTimeout.
//	anything else (a real, non-2xx HTTP response from Anthropic's own
//	  API — surfaced by the SDK as a typed *anthropic.Error — or a
//	  transport-level failure with no HTTP response at all: DNS,
//	  connection refused, TLS, ...): CodeAPIError. Both this port's own
//	  taxonomy (§18.1) and every caller of it only ever need to
//	  distinguish "retry/fallback and move on" from "a request timed
//	  out" — a finer split within CodeAPIError (which specific non-2xx
//	  status, or transport vs. HTTP) has no caller today, exactly
//	  ports.SourceControl's own "no typed classification, no caller needs
//	  one yet" precedent (sourcecontrol.go).
func classifyCompletionError(provider string, err error) *ports.LLMError {
	if errors.Is(err, context.DeadlineExceeded) {
		return &ports.LLMError{Code: ports.CodeTimeout, Provider: provider, Err: err}
	}
	return &ports.LLMError{Code: ports.CodeAPIError, Provider: provider, Err: err}
}
