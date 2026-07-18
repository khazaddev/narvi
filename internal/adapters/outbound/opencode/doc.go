// Package opencode implements the OpenCode ports.AgentRuntime adapter
// (§7): the anti-corruption layer between Narvi's own wire contract
// (contracts/gen/go/sandboxws) and OpenCode's real HTTP+SSE API. It is a
// pure HTTP+SSE client against an ALREADY-RUNNING `opencode serve` process
// — mirroring internal/adapters/outbound/modal's own shape exactly — and
// does NOT spawn or supervise that process itself; that is
// internal/sandboxagent/opencodeproc's own job (a Spawn call that reuses
// internal/sandboxagent/supervisor, never a bare exec.Command), the same
// separation Modal's adapter already has from Step 13's own supervisor.
//
// See adapter.go's own doc comment on Adapter for the verified-vs-schema-
// derived-vs-best-effort breakdown of the real OpenCode wire shapes this
// package translates, and for the documented reasoning behind this
// adapter's own best-effort sub-task handling (§7.1).
//
// File layout:
//   - adapter.go: the Adapter type, New/Close/Connected, StartTurn/Stop
//     (the ports.AgentRuntime implementation), and the per-turn wait/
//     finalize orchestration.
//   - session.go: OpenCode session resolution (create-vs-resume),
//     the §7 model-catalog-fallback quirk, prompt_async/abort/final-
//     message-fetch HTTP calls.
//   - sse.go: the persistent global GET /event connection and per-event
//     dispatch, including the tool-call/tool-result dedup logic (§7's own
//     "dedupe tool states by sid:callID:status" quirk).
//   - turn.go: turnState, the per-in-flight-turn demux/dedup/outcome-
//     tracking state the SSE loop and StartTurn's own wait loop share.
//   - translate.go: pure OpenCode-part-or-message -> wire-event
//     translation functions.
//   - outcome.go: deriveOutcome, the pure, directly unit-testable
//     execution_complete outcome derivation (§7's "treat 'no output' as
//     failure" quirk and the tagged-error-to-outcome mapping).
//   - types.go: this adapter's own understanding of OpenCode's real wire
//     shapes.
//   - client.go: the low-level JSON HTTP helper every other file's own
//     OpenCode calls go through.
package opencode
