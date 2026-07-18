package sessionactor

import "encoding/json"

// The 5 named persistent timers §2 lists explicitly: "connecting_deadline,
// liveness_check, inactivity, turn_deadline, terminal_grace." Named
// constants here (rather than bare string literals scattered across the
// package) so a typo in a timer name is a compile error at every call
// site that matters, even though the session_timers.name column itself is
// TEXT, not an enum (see migrations/000009_session_timers.up.sql).
const (
	TimerConnectingDeadline = "connecting_deadline"
	TimerLivenessCheck      = "liveness_check"
	TimerInactivity         = "inactivity"
	TimerTurnDeadline       = "turn_deadline"
	TimerTerminalGrace      = "terminal_grace"
)

// Command is the sum type an Actor's mailbox carries (§2: "one goroutine
// + mailbox (channel of commands) per active session"). isCommand is
// unexported so no type outside this package can implement Command --
// handle's type switch in timerfired.go can therefore treat its default
// case as unreachable dead-code protection, not a real possibility to
// handle.
type Command interface {
	isCommand()
}

// TimerFired is delivered by the timer pump (timerpump.go) when a named
// persistent timer becomes due (§2). Name is one of the 5 constants above
// in practice, but this type does not itself restrict it -- an unknown
// name is handled defensively (logged, ignored) by the dispatch switch in
// timerfired.go, the same deny-list-not-allow-list convention
// internal/domain/sandbox.IsDeadSandboxStatus and internal/domain/turn.
// IsTerminal already use.
type TimerFired struct {
	Name string
}

func (TimerFired) isCommand() {}

// SandboxEvent is delivered by internal/adapters/inbound/wshub's sandbox
// WS read loop (Step 18) for every inbound frame on a sandbox's live
// connection, once that connection's own handshake-time gen has already
// been validated (§6.1: "403 on id/gen mismatch" is enforced at connect
// time; this per-message Gen is the SEPARATE per-message half of §3.2's
// gen-fencing rule -- "stale-gen inputs are rejected and logged" -- the
// same two-layer defense commands.schema.json's own doc comment already
// requires of wsbridge's client-side dispatch, see
// internal/sandboxagent/wsbridge/dispatch.go's checkGen).
//
//   - Type is the wire message's own "type" field (e.g. "ready",
//     "heartbeat", "execution_complete", ...) -- handleSandboxEvent
//     (sandboxevent.go) switches on it for the two in-scope state
//     transitions; every other recognized type is still persisted, just
//     with no transition attempted.
//   - Gen is the wire message's own "gen" field, checked against the
//     sandbox row's CURRENT gen inside handleSandboxEvent's own
//     transaction (never against the connection-level gen the handshake
//     already validated, which may be stale by the time this particular
//     message is actually processed).
//   - Raw is the original, unmodified wire bytes -- persisted verbatim via
//     appendRawEvent (actor.go) so the append-only event log holds exactly
//     what the sandbox sent, not a lossy re-encoding through an
//     intermediate Go struct.
//   - LastBootPhase mirrors sandboxws.Heartbeat.LastBootPhase for a
//     "heartbeat" event (nil/absent for every other type) -- nil
//     specifically means "boot has completed", the signal
//     handleSandboxEvent's Booting->Ready transition watches for.
//   - Reply is how handleSandboxEvent's asynchronous mailbox-processing
//     communicates the outcome back to the wshub read-loop goroutine, which
//     owns the live *websocket.Conn and must write any resulting ack
//     itself. The caller (wshub) constructs it as
//     `make(chan SandboxEventOutcome, 1)` -- buffered so
//     handleSandboxEvent's own send into it (a non-blocking
//     select-with-default) always succeeds immediately, even if the wshub
//     side already gave up waiting on platform.Timeouts.
//     SandboxEventAckTimeout.
type SandboxEvent struct {
	Type          string
	Gen           int
	Raw           json.RawMessage
	LastBootPhase *string
	Reply         chan<- SandboxEventOutcome
}

func (SandboxEvent) isCommand() {}

// SandboxEventOutcome is what handleSandboxEvent (sandboxevent.go) sends
// back on SandboxEvent.Reply once its own transaction has committed (or
// deliberately skipped persisting a stale-gen event, see sandboxevent.go).
//
//   - Persisted reports whether the event was actually appended to the
//     events table (false only for the stale-gen-rejection path).
//   - AckID is the original critical event's own deterministic ackId
//     (contracts/gen/go/sandboxws's own "{type}:{messageId}" convention),
//     non-empty only when the inbound wire message was one of the 6
//     critical types AND it was persisted. wshub echoes this back as a
//     fresh sandboxws.Ack{AckId: outcome.AckID, ...} on the same
//     connection when non-empty; an empty AckID means "do not send an
//     ack for this message" (either it was never a critical type, or it
//     was a stale-gen event that was never persisted at all).
type SandboxEventOutcome struct {
	Persisted bool
	AckID     string
}
