package opencode

import (
	"sync"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// turnState is the demux state for one in-flight StartTurn call, keyed by
// its OpenCode sessionID in Adapter.turns (turn.go's own registry, see
// adapter.go). Exactly one turnState is ever registered per OpenCode
// session at a time (Narvi's own turn state machine, §3.3, guarantees
// "exactly one processing per session"), but nothing here assumes that
// beyond what the registry itself enforces (one map entry per key).
type turnState struct {
	cmd  sandboxws.Prompt // for stamping SessionId/Gen on every translated event (see translate.go)
	sink ports.EventSink

	mu sync.Mutex

	// toolCallSent/toolResultSent implement §7's own "dedupe tool states
	// by sid:callID:status" quirk — sid is implicit, since a turnState is
	// already scoped to one OpenCode session. Emit wire tool_call the
	// FIRST time a callID's own input is seen non-empty; emit wire
	// tool_result exactly ONCE, on the first "completed" or "error"
	// status for that callID.
	toolCallSent   map[string]bool
	toolResultSent map[string]bool

	// subtasksOpen tracks every subTaskId this turn has emitted
	// sub_task_start for but not yet sub_task_finish — see finalize's own
	// doc comment (adapter.go) for how these are closed out.
	subtasksOpen map[string]bool

	// assistantMessageIDs tracks which OpenCode message ids belong to an
	// ASSISTANT message, learned from message.updated's own info.role the
	// moment that message is created. VERIFIED LIVE BUG this field fixes:
	// message.part.updated fires for the USER's own message too (its own
	// prompt text, echoed back as a "text" part on the user messageID) --
	// with no role check, that user-authored text was being emitted as a
	// wire token event (indistinguishable from real assistant output to a
	// control-plane consumer, since sandboxws.Token has no role field by
	// design) AND was marking sawText true before the assistant had said
	// anything at all, silently defeating §7's own "treat 'no output' as
	// failure" quirk on the live SSE path (a genuinely empty assistant
	// reply would still read as "completed"). dispatchPart's "text" case
	// now only translates/marks a text part whose MessageID is a KNOWN
	// assistant message id -- an id not yet seen at all is treated the
	// SAME as "not assistant" (fail-closed: skip), which is safe because
	// OpenCode's own real event ordering (verified live) always delivers
	// message.updated for a message before any of that message's own
	// parts.
	assistantMessageIDs map[string]bool

	// lastAssistantError/sawText/sawToolCall feed deriveOutcome
	// (outcome.go) once the turn's own terminal signal (session.idle) or
	// the SSE-inactivity fallback resolves it.
	lastAssistantError *openCodeTaggedError
	sessionError       *openCodeTaggedError
	sawText            bool
	sawToolCall        bool

	lastActivity time.Time
	finalized    bool

	// done is closed exactly once, by Adapter.finalize, once this turn's
	// own execution_complete (and any still-open sub_task_finish events)
	// have been emitted — the signal StartTurn's own wait loop is blocked
	// on.
	done chan struct{}
}

func newTurnState(cmd sandboxws.Prompt, sink ports.EventSink) *turnState {
	return &turnState{
		cmd:                 cmd,
		sink:                sink,
		toolCallSent:        make(map[string]bool),
		toolResultSent:      make(map[string]bool),
		subtasksOpen:        make(map[string]bool),
		assistantMessageIDs: make(map[string]bool),
		lastActivity:        time.Now(),
		done:                make(chan struct{}),
	}
}

// emit populates AgentEvent.Critical/AckID via ports.ClassifyAgentEvent
// (the single shared "which wire types are critical" classification) and
// forwards to the sink.
func (ts *turnState) emit(payload any) {
	critical, ackID := ports.ClassifyAgentEvent(payload)
	ts.sink(ports.AgentEvent{Payload: payload, Critical: critical, AckID: ackID})
}

// touch records that SOME event for this turn's own OpenCode session was
// just dispatched — resetting the SSE-inactivity clock that backs §7's
// own "SSE inactivity timeout (default 120s)" and "final-state fetch
// fallback" quirks (see adapter.go's finalizeByFallback). Called for
// EVERY dispatched event, even ones this adapter otherwise skips
// translating, since any event at all proves the stream is still alive for
// this session.
func (ts *turnState) touch() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.lastActivity = time.Now()
}

// idleFor reports whether this turn has gone at least d since its last
// touch — the poll-based check Adapter.waitForTurn uses instead of a
// Timer.Reset-based scheme (avoids that API's own well-known Stop/Reset
// race entirely, at the cost of poll granularity, acceptable here since
// this bounds only a last-resort fallback path, not the common case).
func (ts *turnState) idleFor(d time.Duration) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return time.Since(ts.lastActivity) >= d
}

func (ts *turnState) setLastAssistantError(err *openCodeTaggedError) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.lastAssistantError = err
}

// markAssistantMessageID records messageID as belonging to an assistant
// message — see assistantMessageIDs' own field comment for why this
// exists.
func (ts *turnState) markAssistantMessageID(messageID string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.assistantMessageIDs[messageID] = true
}

// isAssistantMessage reports whether messageID is a KNOWN assistant
// message id — false for a user message id, and false (fail-closed) for
// an id this turnState has never seen a message.updated for at all.
func (ts *turnState) isAssistantMessage(messageID string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.assistantMessageIDs[messageID]
}

func (ts *turnState) setSessionError(err openCodeTaggedError) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sessionError = &err
}

func (ts *turnState) markSawText() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sawText = true
}

func (ts *turnState) markSawToolCall() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.sawToolCall = true
}

// errorForOutcome returns whichever tagged error was observed for this
// turn, preferring the final assistant message's own "error" field over
// session.error's (ground truth: "Derive... from whichever of these you
// observe" — both were verified to carry the identical shape live on the
// same aborted turn, so preferring one over the other is a tie-break, not
// a correctness concern).
func (ts *turnState) errorForOutcome() *openCodeTaggedError {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.lastAssistantError != nil {
		return ts.lastAssistantError
	}
	return ts.sessionError
}

func (ts *turnState) outcomeInputs() (hasText, hasToolCall bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.sawText, ts.sawToolCall
}

// alreadySentCall/markCallSent/alreadySentResult/markResultSent implement
// the per-callID dedup state described on toolCallSent/toolResultSent
// above.
func (ts *turnState) alreadySentCall(callID string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.toolCallSent[callID]
}

func (ts *turnState) markCallSent(callID string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.toolCallSent[callID] = true
}

func (ts *turnState) alreadySentResult(callID string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.toolResultSent[callID]
}

func (ts *turnState) markResultSent(callID string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.toolResultSent[callID] = true
}

// subtaskAlreadyStarted/markSubtaskStarted guard sub_task_start against
// OpenCode ever repeating the same subtask part id across multiple
// message.part.updated events (parts in general DO repeat with updated
// content, e.g. text/tool — a subtask part's own fields are static once
// created, per its schema, but this guard costs nothing and keeps
// sub_task_start genuinely "first sight only" regardless).
func (ts *turnState) subtaskAlreadyStarted(subTaskID string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.subtasksOpen[subTaskID]
}

func (ts *turnState) markSubtaskStarted(subTaskID string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.subtasksOpen[subTaskID] = true
}

// drainOpenSubtasks returns every subTaskId still open (sub_task_start
// emitted, sub_task_finish not yet) and clears the set — called exactly
// once, by Adapter.finalize, so every subtask this turn ever started is
// guaranteed a matching sub_task_finish (§6.1's own critical-delivery
// rationale for that event type).
func (ts *turnState) drainOpenSubtasks() []string {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ids := make([]string, 0, len(ts.subtasksOpen))
	for id := range ts.subtasksOpen {
		ids = append(ids, id)
	}
	ts.subtasksOpen = make(map[string]bool)
	return ids
}

// tryFinalize marks this turn finalized exactly once, reporting whether
// THIS call was the one that did so (false means some earlier call already
// finalized it — the caller must not emit anything further).
func (ts *turnState) tryFinalize() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.finalized {
		return false
	}
	ts.finalized = true
	return true
}
