// Package openai will hold the OpenAI/Codex LLM adapter implementing
// ports.LLM -- implemented in PR-50 (§8: "Models: Anthropic +
// OpenAI/Codex (ChatGPT OAuth plugin)"). Mirrors internal/adapters/
// outbound/rwx's and gitlabapi's own "second-adapter, not-yet-
// implemented" house style exactly: §8.3 ships exactly ONE
// real, fully-working ports.LLM adapter (internal/adapters/outbound/llm,
// Anthropic), with this package left as an untouched stub for the second.
//
// Adding the real implementation here later must require changes ONLY
// inside this package + one new registry-registration line
// (internal/adapters/outbound/llm's own New factory, registry.go) + one
// new config value -- ZERO changes to ports.LLM, internal/domain/intent,
// or internal/app/intentclassifier (§8's own "genuinely multi-provider by
// nature" bar).
package openai
