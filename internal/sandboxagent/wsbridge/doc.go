// Package wsbridge implements sandbox-agent's half of the sandbox WS
// protocol (§6.1): dialing control-plane's own `wss://…/sessions/{id}/ws
// ?type=sandbox` endpoint, the ack/resend-on-reconnect guarantee for the 5
// critical event types, 30s heartbeats carrying the tracked boot phase,
// boot_progress translation, and inbound command dispatch (with per-message
// gen-fencing) to a pluggable CommandHandler.
//
// # The real, merged contract has 5 critical types, not 6
//
// Technical plan §6.1's own prose lists `sub_task_finish` as a 6th critical/
// ackable event type alongside execution_complete, error, snapshot_ready,
// push_complete, and push_error. That is not what the ALREADY-MERGED wire
// contract this package implements against actually says:
// contracts/sandbox-ws/v1/events.schema.json's own top-level description
// names exactly 5 CRITICAL types, and its `oneOf` has no
// sub_task_start/sub_task_finish variant at all -- there is no
// contracts/gen/go/sandboxws.SubTaskStart or SubTaskFinish type to even
// construct. §7.1's own "Phasing" note assigns sub-task adapter-side
// tagging to Step 17 (the OpenCode adapter); whoever implements that Step is
// the one who must first EXTEND events.schema.json with a real
// sub_task_start/sub_task_finish variant before either could become a real,
// ackable wire type. This package therefore implements the ack protocol
// against exactly the 5 types the schema and contracts/gen/go/sandboxws
// actually define: ExecutionComplete, SandboxErrorEvent (named to avoid
// shadowing the builtin `error`, per that type's own doc comment),
// SnapshotReady, PushComplete, PushError. SendCritical does not know or
// care which concrete type it is handed, so this package needs no changes
// if/when a future Step extends the schema.
//
// # Outbound ack-protocol buffer: the eviction policy is the single most
// important design decision in this package
//
// §6.1 states the guarantee tersely: "sender buffers (1000 events, evict
// oldest non-critical) and re-sends on reconnect until acked". Read
// literally, "evict oldest non-critical" is not merely a tie-breaking rule
// among equally-evictable candidates -- it is an explicit exemption: a
// critical (unacked) entry is NEVER evicted, full stop, regardless of how
// full the buffer gets. The 1000 cap (outboundBufferCap) is therefore a
// SOFT target enforced only by evicting non-critical entries; a non-critical
// (best-effort) entry is always safe to drop because the receiver's own
// upsert-by-messageId dedupe (§6.1) makes redelivery-or-not equally correct
// for it. A critical entry is exactly the opposite: it exists BECAUSE
// at-least-once delivery must be guaranteed (execution_complete/error/
// snapshot_ready/push_complete/push_error each close out state the control
// plane and UI treat as authoritative -- a turn's terminal outcome, a
// snapshot becoming restorable, a push's result). Evicting one to make room
// under memory pressure would silently convert a guaranteed-delivery
// protocol into a best-effort one at precisely the moment it matters most --
// exactly the failure class this whole ack protocol exists to prevent. So:
//
//   - If the buffer is under cap, or every existing entry the new one would
//     have to make room for is itself critical, nothing is evicted -- the
//     buffer is allowed to grow past 1000 in that (extreme, unlikely)
//     scenario rather than silently drop a guaranteed event.
//   - Otherwise, exactly the OLDEST non-critical entry is evicted to make
//     room -- oldest, not merely "some non-critical one", so eviction order
//     is deterministic and fair (a busy sender doesn't starve out
//     best-effort events sent early in a session in favor of ones sent
//     later).
//
// This decision lives in its own small, pure, deterministic function
// (evictionDecision, buffer.go) precisely so it can be exhaustively
// table-tested without any WS/network machinery in the loop.
//
// # Dispatch-by-type-field is this package's own necessary pattern
//
// contracts/gen/go/sandboxws has no generated discriminated-union wrapper
// type for commands.schema.json's own `oneOf` (go-jsonschema does not
// synthesize one) -- each command shape is its own independent Go struct
// with no shared marker beyond a `Type string` field. This package peeks
// that field via a tiny envelope struct to decide which concrete
// contracts/gen/go/sandboxws.{Prompt, Stop, Push, Snapshot, Shutdown, Ack,
// GitSyncComplete} to unmarshal the same raw bytes into. An unrecognized
// type value is logged and skipped, never fatal -- forward-compatible with
// a command type a future Step adds that this package doesn't know about
// yet.
//
// # Per-message gen-fencing
//
// commands.schema.json's own description states the invariant this package
// must enforce: "per-message gen-fencing (§3.2 'stale-gen inputs are
// rejected') does not rely solely on the connection-level X-Sandbox-Gen
// header". Every inbound command except ack (handled internally, never
// dispatched to CommandHandler at all) has its own Gen field checked
// against this Bridge's own session Gen before being acted on; a mismatch
// is logged and the command is silently ignored -- this applies to
// `shutdown` exactly as it does to the 5 business commands, since a stale
// connection's delayed shutdown must never tear down a bridge that has
// already moved on to a newer generation.
//
// # Honest gaps this package documents rather than papers over
//
// New's own sandboxID parameter is this Step's own invented value for the
// X-Sandbox-ID header -- no Step yet wires a real provider-assigned
// sandbox-instance id into the sandbox's own environment (see
// internal/sandboxagent/boot.Config.SandboxID's own doc comment), matching
// Step 13's NARVI_IMAGE_DIGEST gap exactly. Heartbeat.ConversationId is
// always nil for this Step: no OpenCode adapter exists yet (Step 17) to
// ever start a real conversation.
package wsbridge
