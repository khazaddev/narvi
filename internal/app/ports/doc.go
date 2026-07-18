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
// The remaining §4.3 ports — SourceControl, Notifier, IntentClassifier,
// LLM, BlobStore, SessionStore/TurnStore/SandboxStore, Outbox,
// TimerScheduler, Clock — are out of scope for this Step and land in their
// own later Steps, each adding its own interface file here without
// touching SandboxProvider's or AgentRuntime's.
package ports
