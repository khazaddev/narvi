// Package ports declares the application's port interfaces — the
// boundary between the app layer and the outside world. Ports are pure
// contracts; adapters (internal/adapters/*) implement them, never the
// other way around (CLAUDE.md: "Don't couple a port to a single adapter.
// Interfaces in /internal/app/ports must hold for more than one
// implementation").
//
// This package is deliberately restricted to interface + value-type
// declarations: no I/O, no time.Now(), no randomness. It may import
// contracts/gen/go/* (generated wire-contract types — e.g.
// sessionconfig.SessionConfig, which CreateSpec carries) and the standard
// library only. It MUST NOT import internal/adapters/* — doing so would
// invert the dependency direction hexagonal architecture exists to
// enforce: adapters depend on ports, ports never depend on adapters.
//
// SandboxProvider (§4.1) is the first port implemented, at Step 12,
// against two adapters: internal/adapters/outbound/modal (Step 12, real)
// and internal/adapters/outbound/rwx (Step 48, still a stub as of this
// Step — same interface, later implementation). See sandboxprovider.go,
// capabilities.go, createspec.go, refs.go, and providererror.go for the
// interface and its supporting value types.
//
// AgentRuntime (§4.2, the OpenCode anti-corruption layer) is the SECOND
// port, added this Step (17), against internal/adapters/outbound/opencode
// (this Step, real) — CLAUDE.md's "the agent runtime... is expected to
// gain a second adapter" line applies to this interface exactly as it
// already did to SandboxProvider: nothing OpenCode-specific may leak into
// agentruntime.go's signature, even though OpenCode is (for now) its only
// implementation. See agentruntime.go for the interface, its AgentEvent/
// EventSink supporting types, and ClassifyAgentEvent (the shared "which
// wire event types are critical" classification every AgentRuntime
// adapter should reuse rather than reimplement).
//
// SandboxCommander (sandboxcommander.go) and SourceControl (sourcecontrol.go)
// are the THIRD and FOURTH ports, both added at Step 21 ("e2e happy
// path"): SandboxCommander lets app/sessionactor push an outbound command
// to a session's live sandbox WS connection (internal/adapters/inbound/
// wshub's own SandboxRegistry is the implementation) without importing
// wshub itself; SourceControl creates a pull request
// (internal/adapters/outbound/githubapi is the first real implementation,
// internal/adapters/outbound/gitlabapi remains an untouched stub for a
// future Step).
//
// Notifier (notifier.go) is the FIFTH port, added at Step 35 ("outbox
// delivery", §5.1/§5.4): a single Deliver(ctx, Notification) method,
// implemented by THREE real adapters (internal/adapters/outbound/
// slackapi, linearapi, githubapi) -- see notifier.go's own doc comment for
// why one interface with three implementations is the right shape here,
// even though (unlike SandboxProvider/SourceControl, where two adapters
// genuinely implement the same operation against different providers)
// each of these three is only ever asked to Deliver its own matching
// NotificationKind in practice, by internal/app/outboxworker's own
// kind->Notifier routing.
//
// LLM (llm.go) and IntentClassifier (intentclassifier.go) are the SIXTH
// and SEVENTH ports, both added this Step (36, §8.3/§18): LLM is a
// genuinely reusable, provider-agnostic structured-output text-completion
// port (internal/adapters/outbound/llm's Anthropic adapter is the first
// real implementation this Step; a future internal/adapters/outbound/
// openai remains an untouched stub, PR-50) — nothing Anthropic- or
// OpenAI-specific may leak into either port's signature. IntentClassifier
// is the never-throw classification port internal/app/intentclassifier
// implements against LLM.
//
// The remaining §4.3 ports — BlobStore, SessionStore/TurnStore/
// SandboxStore, Outbox, TimerScheduler, Clock — are out of scope for this
// Step and land in their own later Steps, each adding its own interface
// file here without touching any existing one.
package ports
