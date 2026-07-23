// Package llm holds the Anthropic LLM adapter implementing ports.LLM
// (§4.3, §8.3/§18) -- Step 36's own real, first implementation. A future
// OpenAI adapter (PR-50, §8, §8.8: "Models: Anthropic + OpenAI/Codex")
// remains an untouched stub in its own sibling package,
// internal/adapters/outbound/openai, mirroring internal/adapters/outbound/
// rwx's and gitlabapi's own "second-adapter, not-yet-implemented" house
// style exactly -- see that package's own doc.go.
//
// New (registry.go) is this Step's own small provider registry/factory:
// it resolves a configured provider name against the concrete
// implementations this codebase ships, returning a ports.LLM that NEVER
// fails to construct. An unrecognized provider name (or, once resolved to
// this package's own Anthropic implementation, an unrecognized model
// string) is a REAL, REACHABLE runtime code path — every Complete call on
// the returned value then deterministically fails with
// *ports.LLMError{Code: ports.CodeUnsupportedProvider} — never a silent
// substitution that keeps reporting success, and never a process-boot
// crash over what is, at worst, a misconfigured internal classification
// feature (§18.1).
//
// anthropic.go implements the real Anthropic adapter against
// github.com/anthropics/anthropic-sdk-go's structured-output feature
// (output_config.format, a JSON schema forcing the exact IntentDecision-
// shaped output) rather than free-text JSON + hand-parsing. Extended
// thinking is deliberately never enabled — intent classification is a
// fast, low-complexity, high-volume, latency-sensitive call, not a
// "remotely complicated" reasoning task (§18.1). errors.go classifies
// every failure into the FIXED, 5-value FallbackReason taxonomy purely by
// typed/structural signals (an *anthropic.Error's own StatusCode,
// errors.Is against context.DeadlineExceeded for the SDK's own configured
// request-timeout abort, ...) — never by string-matching a human-readable
// message, mirroring ports.ProviderError's own house rule.
package llm
