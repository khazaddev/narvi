package ports

import (
	"context"
	"encoding/json"
	"fmt"
)

// LLM is a genuinely reusable, provider-agnostic structured-output text-
// completion port (§4.3) -- deliberately NOT shaped narrowly around
// "classify intent". internal/app/intentclassifier is its first caller
// (Step 36), but the future model catalog/code-review work (§4.3, §8.8)
// is designed to reuse the SAME port rather than invent its own.
//
// Two real implementations are expected over time, exactly like
// SandboxProvider/SourceControl before it (CLAUDE.md: "don't couple a
// port to a single adapter"): internal/adapters/outbound/llm (Anthropic,
// this Step, real) and a future internal/adapters/outbound/openai (PR-50,
// still a stub as of this Step). Nothing Anthropic- or OpenAI-specific may
// leak into this file -- no vendor SDK types, no vendor error codes, no
// vendor HTTP status semantics. Config names a provider (CompletionRequest
// .Provider) as an explicit, separately-validated dimension precisely so a
// caller/implementer never needs to infer it from a model string's own
// naming convention.
type LLM interface {
	// Complete asks the named provider/model to produce ONE structured-
	// output completion matching ResponseSchema, and returns a
	// CompletionResponse on success (audit fix L18: additive over this
	// port's original bare-json.RawMessage return -- see CompletionResponse
	// below for what changed and why).
	//
	// Never returns a caller-fatal error the caller must guess how to
	// handle: every failure is a *LLMError with one of the Code values
	// below, classified by the implementation's own typed/structural
	// signals -- NEVER by string-matching a human-readable message (§18.1,
	// mirroring ProviderError's identical house rule, providererror.go).
	//
	// Timeouts: implementations rely on their own underlying HTTP/SDK
	// client's own configured request-timeout option -- callers must
	// NEVER additionally race a manually-armed context.WithTimeout against
	// it as a second, redundant layer (§18.1: "the SDK's own internal
	// abort always resolves first, so an outer wrapper timeout would
	// never actually fire"). The timeout VALUE itself is configured once,
	// at construction, from internal/platform/timeouts.go -- never a
	// literal inside Complete's own implementation.
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// CompletionResponse is Complete's own successful-call return shape (audit
// fix L18, "IntentDecisionRecord.CostUSD is structurally dead"). Raw is the
// SAME raw, schema-validated JSON response body this port returned bare
// before this change -- every pre-existing caller needs only a one-line
// update (read .Raw instead of the bare return value) to keep working
// exactly as before; no existing information is lost or changed, which is
// what makes this an ADDITIVE signature change despite touching every
// implementation and caller.
//
// Usage/CostUSD are the new information this audit fix adds: Usage is
// populated whenever the underlying provider's own response reports token
// counts (every real provider does); CostUSD is the already-computed
// dollar cost of this one call, or nil when it genuinely cannot be
// computed (an unrecognized/newly-added model an implementation's own
// pricing table doesn't yet know) -- omitted, never guessed, mirroring
// intentdomain.IntentDecisionRecord.CostUSD's identical "populated only
// when the real cost is known, omitted, never guessed" contract, which is
// the whole reason this field exists: to finally give that column a real
// value to hold.
//
// CostUSD is computed HERE, by the implementation, rather than left for
// the caller to derive from Usage plus a model-to-price table of its own:
// pricing is inherently vendor-specific business logic (each provider's
// own $/token rates, which model IDs even exist, how a provider prices
// cached tokens, ...), so computing it anywhere outside the implementation
// that already owns "which provider, which model" would mean duplicating
// (or worse, re-deriving) that vendor-specific knowledge at the call site
// -- exactly the leak ports/doc.go's own import-direction rule and this
// port's "nothing Anthropic- or OpenAI-specific may leak into this file"
// rule both exist to prevent. Nothing vendor-specific reaches this struct
// itself: by the time CostUSD lands here it is already just a dollar
// figure.
type CompletionResponse struct {
	// Raw is the raw, schema-validated JSON response body -- identical to
	// what this port's Complete returned bare before this audit fix.
	Raw json.RawMessage
	// Usage is this call's own token accounting, when the provider reports
	// it. Zero-valued (both fields 0) on any error path -- callers only
	// ever read Usage after checking Complete's own error return is nil.
	Usage CompletionUsage
	// CostUSD is this call's own already-computed dollar cost, or nil when
	// no cost could be computed (see the struct-level doc comment above).
	CostUSD *float64
}

// CompletionUsage is one Complete call's own token-usage accounting.
// Provider-agnostic in shape (every major LLM API reports at least an
// input/output token split) even though, as of this port's first real
// implementation (internal/adapters/outbound/llm), only the Anthropic
// adapter populates it.
type CompletionUsage struct {
	// InputTokens is the number of input (prompt) tokens the provider
	// billed for this call.
	InputTokens int64
	// OutputTokens is the number of output (completion) tokens the
	// provider billed for this call.
	OutputTokens int64
}

// CompletionMessage is one turn of CompletionRequest.Messages. Role is
// "user" or "assistant" -- the same two roles every provider's own chat-
// completion API already uses, so no vendor-specific translation is ever
// needed at this port boundary.
type CompletionMessage struct {
	Role    string
	Content string
}

// CompletionRequest is what LLM.Complete needs to produce one structured-
// output completion. Provider/Model are BOTH required, separate, explicit
// dimensions (this port's own "multi-provider by nature" requirement) --
// Model alone is never enough to select an implementation, and Provider is
// never inferred from Model's own naming convention.
type CompletionRequest struct {
	// Provider names which concrete LLM implementation this request
	// targets (e.g. "anthropic") -- resolved against a small provider
	// registry at the wiring layer (cmd/control-plane/main.go or
	// internal/adapters/outbound/llm's own registry.go). An unrecognized
	// Provider is a real, reachable *LLMError{Code: CodeUnsupportedProvider}
	// path, never a silent substitution.
	Provider string
	// Model is the provider-specific model identifier (e.g.
	// "claude-haiku-4-5"). An unrecognized/misconfigured Model string for
	// an otherwise-valid Provider is likewise a real
	// *LLMError{Code: CodeUnsupportedProvider} path -- a misconfigured
	// model choice is just another unsupported-provider-shaped failure,
	// never a silent substitution that keeps reporting success (§18.1).
	Model string
	// System is the system prompt.
	System string
	// Messages is the conversation history to complete against. Most
	// callers (intent classification) pass exactly one user message; the
	// shape supports more for future reuse (§4.3's own "the future model
	// catalog/code-review work also reuses this port").
	Messages []CompletionMessage
	// ResponseSchema is a JSON Schema (see
	// https://json-schema.org/draft/2020-12) constraining the shape of
	// the structured-output completion this request asks for. The raw
	// bytes Complete returns on success validate against this schema.
	ResponseSchema json.RawMessage
}

// LLMErrorCode is LLMError's enumerated, growable classification (§18.1) --
// the EXACT same 5 values ports.FallbackReason enumerates, since every
// LLMError this port returns is what internal/app/intentclassifier maps,
// one-to-one, into the fallback reason it records.
type LLMErrorCode string

const (
	// CodeNoAPIKey means the implementation has no usable credential
	// configured -- reachable without ever calling a real API (a
	// misconfigured/empty-key construction), so every Complete call on
	// that instance fails this way deterministically.
	CodeNoAPIKey LLMErrorCode = "no_api_key"
	// CodeTimeout means the underlying client's own configured request
	// timeout elapsed before a response arrived.
	CodeTimeout LLMErrorCode = "timeout"
	// CodeInvalidOutput means a response WAS received, but it didn't
	// parse as the requested ResponseSchema (malformed JSON, a schema
	// mismatch, or the provider truncating before a complete structured
	// object was produced).
	CodeInvalidOutput LLMErrorCode = "invalid_output"
	// CodeAPIError means the provider's API itself reported a failure
	// (a non-2xx HTTP response, a transport-level failure) that isn't one
	// of the more specific codes above.
	CodeAPIError LLMErrorCode = "api_error"
	// CodeUnsupportedProvider means CompletionRequest.Provider (or
	// .Model, for an otherwise-recognized Provider) does not name a real,
	// configured implementation -- a real, reachable, testable code path
	// (§18's own explicit requirement), never dead code.
	CodeUnsupportedProvider LLMErrorCode = "unsupported_provider"
)

// LLMError is the typed error every LLM implementation returns on
// failure, mirroring ProviderError's own house style (providererror.go):
// a provider-agnostic classification OUTCOME (Code) plus the
// implementation's own opaque debugging context (Provider, Err) — never a
// caller re-parsing a message string to reclassify (§18.1's own explicit
// "never by string-matching").
type LLMError struct {
	// Code is the classification outcome — the ONLY field a caller
	// should switch on.
	Code LLMErrorCode
	// Provider is the CompletionRequest.Provider value this error came
	// from, kept for logging/debugging only.
	Provider string
	// Err is the wrapped underlying error (a transport error, a decode
	// error, the provider's own API error, ...), if any.
	Err error
}

func (e *LLMError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("llm: %s (provider=%s): %v", e.Code, e.Provider, e.Err)
	}
	return fmt.Sprintf("llm: %s (provider=%s)", e.Code, e.Provider)
}

// Unwrap exposes the wrapped underlying error to errors.Is/errors.As.
func (e *LLMError) Unwrap() error { return e.Err }
