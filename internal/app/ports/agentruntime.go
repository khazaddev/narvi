package ports

import (
	"context"

	"github.com/narvidev/narvi/contracts/gen/go/sandboxws"
)

// AgentEvent is one wire-ready event produced while an AgentRuntime
// streams a turn's progress. It carries the already fully-populated,
// concrete contracts/gen/go/sandboxws.* payload — mirroring
// internal/sandboxagent/wsbridge.Bridge.SendCritical/SendBestEffort's own
// `msg any` contract (genuinely type-agnostic: wsbridge does not know or
// care which concrete type it is handed, see wsbridge/doc.go) — plus
// enough metadata for the caller to route it correctly without inspecting
// the payload's own Go type a second time.
type AgentEvent struct {
	// Payload is the concrete sandboxws.* event value (e.g.
	// sandboxws.Token{...}, sandboxws.ExecutionComplete{...}), ready to
	// hand directly to wsbridge.Bridge.SendCritical/SendBestEffort.
	Payload any

	// Critical reports whether Payload is one of the 6 critical types
	// (§6.1: execution_complete, error, snapshot_ready, push_complete,
	// push_error, sub_task_finish) requiring the ack protocol
	// (wsbridge.SendCritical) rather than best-effort delivery
	// (wsbridge.SendBestEffort). Use ClassifyAgentEvent to populate this
	// correctly instead of hand-rolling a type switch per adapter — the
	// critical set is a wire-contract fact, not something specific to any
	// one AgentRuntime implementation.
	Critical bool

	// AckID is the deterministic "{type}:{messageId}" ack identifier
	// (§6.1), populated only when Critical is true.
	AckID string
}

// ConversationIDReporter is StartTurn's early-conversation-id-reporting
// callback ("turn recovery", §3.3: "The turn records the OpenCode
// conversation id at turn start... so follow-up prompts on a fresh sandbox
// resume the same conversation — never lazily"). StartTurn's own final
// string return value cannot, by itself, satisfy that requirement: it is
// only available once the ENTIRE call returns, and StartTurn blocks for
// the WHOLE turn (waitForTurn, internal/adapters/outbound/opencode/
// adapter.go) — by which point the turn has, for all practical purposes,
// already ended. The conversation id is actually known much earlier
// though — the moment the implementation resolves/creates it (well before
// any long-blocking wait for the agent engine's own output) — so this
// callback exists purely to let the caller act on it AT THAT EARLIER
// MOMENT: reporting it over the very next heartbeat
// (wsbridge.Bridge.SetConversationID) rather than only once the turn is
// basically already over. Invoked at most once per StartTurn call, with a
// real, non-empty, resolved conversation id — never on a path that never
// resolves one. A nil callback is legal and MUST be tolerated: some
// callers/tests have no need to observe it.
type ConversationIDReporter func(conversationID string)

// EventSink receives each AgentEvent as it is produced, in real time — an
// AgentRuntime implementation MUST NOT buffer a whole turn before invoking
// this (the caller, cmd/sandbox-agent's own commandHandler, forwards each
// one onto the wsbridge live as it arrives; token/tool_call/tool_result
// must stream, not arrive all at once at turn end). Modeled as a plain
// function type, mirroring internal/sandboxagent/services.
// ProgressReporter's own precedent for a single-method streaming callback.
type EventSink func(AgentEvent)

// AgentRuntime is the anti-corruption-layer port every agent-execution
// engine adapter implements (§4.2, §7). OpenCode
// (internal/adapters/outbound/opencode) implements it first; per CLAUDE.md
// ("the agent runtime... is expected to gain a second adapter"), nothing
// OpenCode-specific — or specific to any other single engine — may leak
// into this signature. It intentionally references
// contracts/gen/go/sandboxws types directly: these are canonical WIRE
// types (the same abstraction level ports.CreateSpec already uses for
// contracts/gen/go/sessionconfig), not adapter-specific ones.
//
// The technical plan's own §4.2 sketch names StartTurn/ResumeConversation/
// Stop as three separate methods; this interface merges the first two into
// one StartTurn, keyed by whether cmd.ConversationId is set --
// commands.schema.json's own Prompt.conversationId doc comment: "Absent or
// null both mean 'start a fresh conversation'" (§3.3) — a single inbound
// "prompt" command already carries everything either case needs, and
// cmd/sandbox-agent's own wsbridge.CommandHandler.HandlePrompt has exactly
// one command value to dispatch from, not two.
type AgentRuntime interface {
	// StartTurn dispatches cmd (already gen-checked by wsbridge before ever
	// reaching here) as a turn against the agent engine, streaming every
	// translated event to sink as it happens, and returns once the turn
	// has reached a terminal outcome or ctx is canceled.
	//
	// When cmd.ConversationId is nil/absent, the implementation starts a
	// brand-new conversation; when set, the implementation resumes that
	// exact conversation. Either way, the moment the implementation
	// resolves the conversation id it will use — BEFORE doing anything
	// else that could block for the turn's own duration — it invokes
	// onConversationID with it (if onConversationID is non-nil), and ALSO
	// returns that same id as its own final conversationID return value
	// once the whole call eventually completes (§3.3: "The turn records
	// the OpenCode conversation id at turn start... so follow-up prompts
	// on a fresh sandbox resume the same conversation — never lazily";
	// see ConversationIDReporter's own doc comment for why the final
	// return value ALONE cannot satisfy that "at turn start" requirement,
	// and onConversationID exists specifically to close that gap). The
	// caller is expected to thread whichever value it observes first
	// (onConversationID's callback, in practice — it always fires no
	// later than the return value does) into
	// wsbridge.Bridge.SetConversationID so the next heartbeat reports it
	// (§6.1: Heartbeat.conversationId).
	//
	// StartTurn always attempts to deliver exactly one execution_complete-
	// shaped terminal event via sink before returning — even when the
	// agent engine could never be reached at all (§3.3: "Stop/failure
	// paths emit a synthetic execution_complete event so clients always
	// see one terminal event per turn") — so the caller never needs its
	// own separate synthetic-failure path. A non-nil returned error means
	// ctx was canceled before that could happen (an ordinary shutdown, not
	// a wire-reportable failure); the caller may log it, but owes sink
	// nothing further.
	StartTurn(ctx context.Context, cmd sandboxws.Prompt, sink EventSink, onConversationID ConversationIDReporter) (conversationID string, err error)

	// Stop aborts whichever turn is currently in flight (§3.3: a
	// Stop-initiated cancellation surfaces as an
	// execution_complete{outcome: "cancelled"} event through the
	// in-flight StartTurn call's own sink — Stop itself never emits
	// anything to a sink directly, it only requests the abort). A Stop
	// with nothing in flight is a safe no-op, not an error.
	Stop(ctx context.Context, cmd sandboxws.Stop) error
}

// ClassifyAgentEvent reports whether payload is one of the 6 critical
// wire event types (§6.1) and, if so, its own already-populated ackId —
// a plain Go type switch, not a duplicate of wsbridge's own dispatch-by-
// type-field pattern (dispatch.go handles the INBOUND command side; this
// is the OUTBOUND event side, and the payload here is already a concrete
// Go struct, not raw JSON needing a discriminator peek). Any AgentRuntime
// adapter should use this instead of hand-rolling its own switch, so the
// critical set is asserted in exactly one place.
func ClassifyAgentEvent(payload any) (critical bool, ackID string) {
	switch v := payload.(type) {
	case sandboxws.ExecutionComplete:
		return true, v.AckId
	case sandboxws.SandboxErrorEvent:
		return true, v.AckId
	case sandboxws.SnapshotReady:
		return true, v.AckId
	case sandboxws.PushComplete:
		return true, v.AckId
	case sandboxws.PushError:
		return true, v.AckId
	case sandboxws.SubTaskFinish:
		return true, v.AckId
	default:
		return false, ""
	}
}
