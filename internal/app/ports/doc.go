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
// SandboxProvider (§4.1) is the first port implemented, this Step,
// against two adapters: internal/adapters/outbound/modal (this Step,
// real) and internal/adapters/outbound/rwx (Step 48, still a stub as of
// this Step — same interface, later implementation). See
// sandboxprovider.go, capabilities.go, createspec.go, refs.go, and
// providererror.go for the interface and its supporting value types.
//
// The other ports §4.3 names — AgentRuntime (§4.2, the OpenCode
// anti-corruption layer), SourceControl, Notifier, IntentClassifier, LLM,
// BlobStore, SessionStore/TurnStore/SandboxStore, Outbox, TimerScheduler,
// Clock — are out of scope for this Step and land in their own later
// Steps, each adding its own interface file here without touching
// SandboxProvider's.
package ports
