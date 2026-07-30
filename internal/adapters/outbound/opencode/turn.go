package opencode

import (
	"fmt"
	"log/slog"
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
	// sub_task_start for but not yet sub_task_finish — keyed by whichever
	// id space started it (the legacy, unverified "subtask" part's own
	// "prt_..." id, or — the real, empirically-verified path, see
	// maybeStartTaskSubtask in sse.go — a task tool call's own spawned
	// child session's "ses_..." id; the two never collide). A sub-task
	// closed via the real per-sub-task completion signal
	// (maybeFinishTaskSubtask) is removed here immediately
	// (markSubtaskFinished); anything still left here when the ENCLOSING
	// turn itself finalizes is drained by finalize's own doc comment
	// (adapter.go) as the documented best-effort fallback.
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

	// compacting/compactionAttempted implement §7.2's own compaction-retry
	// guard. compacting is true for the whole window between
	// Adapter.finalizeOrRecoverFromOverflow deciding to attempt a recovery
	// (adapter.go) and either giving up or successfully re-dispatching the
	// prompt — every dispatchEvent case that would otherwise corrupt this
	// turnState's own tracked fields (markAssistantMessageID/
	// setLastAssistantError/dispatchPart/finalize/setSessionError) with
	// compaction-INTERNAL SSE traffic checks isCompacting first and no-ops
	// instead (see dispatchEvent's own doc comment, sse.go, and this
	// Step's own VERIFIED LIVE finding: a synchronous POST /summarize call
	// still produces a full extra wave of message.updated/message.part.
	// updated/session.idle/session.error events for the SAME sessionID on
	// the SAME global SSE stream while it's in flight — see forceCompaction's
	// own doc comment, compact.go, for the full captured event sequence).
	// A single shared boolean guards all four dispatchEvent cases
	// (message.updated, message.part.updated, session.idle, session.error)
	// rather than one per case: simplicity wins here, since every one of
	// them needs to be suppressed for the exact SAME reason and the exact
	// SAME window.
	//
	// compactionAttempted is a SEPARATE, one-way flag (set once, never
	// reset) guarding against a SECOND compaction attempt if the retried
	// prompt ALSO overflows — at most one retry per turn (§7.2 point 3's
	// own infinite-loop guard).
	compacting          bool
	compactionAttempted bool

	lastActivity time.Time

	// finalized is set exactly once, under mu, by tryFinalize below — and
	// read under that SAME mu by emit (Finding 4), so a racing SSE-
	// dispatched emit call and a concurrent Adapter.finalize call can
	// never straddle the moment finalize commits: whichever acquires mu
	// first atomically determines whether the event is delivered or
	// dropped.
	finalized bool

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
// forwards to the sink — UNLESS this turn has already finalized (Finding
// 4): a late-arriving SSE-dispatched event (e.g. a fresh sub_task_start,
// dispatchSubtaskStart below racing Adapter.finalize on a different
// goroutine) must never slip out after execution_complete was already
// sent. The ts.finalized check happens under the SAME ts.mu that
// tryFinalize sets it under, and sink is called WHILE STILL HOLDING that
// lock — never released between the check and the sink call, or the race
// reopens — so whichever of {this emit call} or {finalize's own
// tryFinalize call} acquires ts.mu first correctly and atomically
// determines the outcome; there is no window for a check-then-act race in
// either direction. Every call site here goes through the SSE dispatch
// path (dispatchEvent, dispatchPart, dispatchTool, dispatchSubtaskStart,
// ...) — Adapter.finalize uses the separate emitFinal below instead, which
// is deliberately NOT subject to this same check.
//
// The returned bool reports whether the event was actually sent (true) or
// dropped because this turn had already finalized (false) — a caller with
// its own side effect to perform ONLY when the turn is still genuinely
// live (maybeStartTaskSubtask's own registerSubtaskSession call, sse.go)
// must gate that side effect on this same return value rather than
// performing it unconditionally before/after calling emit: doing it
// unconditionally would reopen exactly the check-then-act race this
// function's own ts.mu-guarded check exists to close, just one level up
// (a task's own "running" event processed after a concurrent finalize has
// already drained this turn's open sub-tasks would otherwise register a
// routing entry nothing will ever remove).
func (ts *turnState) emit(payload any) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if ts.finalized {
		slog.Warn("opencode: dropping event emitted after finalize", "type", fmt.Sprintf("%T", payload))
		return false
	}

	critical, ackID := ports.ClassifyAgentEvent(payload)
	ts.sink(ports.AgentEvent{Payload: payload, Critical: critical, AckID: ackID})
	return true
}

// emitFinal is used ONLY by Adapter.finalize (adapter.go), for its own two
// kinds of terminal emission: the drained sub_task_finish events and the
// final execution_complete itself. Unlike emit above, this does NOT check
// ts.finalized — finalize is the one call path explicitly entitled to emit
// despite having just set that flag via tryFinalize — and needs no locking
// of its own beyond reading ts.sink, which is set once at construction
// (newTurnState) and never mutated by any other method on this type
// afterward: an unlocked read of a value that is written exactly once,
// before this turnState is ever shared across goroutines (registerTurn
// happens after newTurnState returns), and never written again, is safe
// per Go's own memory model.
func (ts *turnState) emitFinal(payload any) {
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

// lastActivityTime returns the raw timestamp idleFor above compares against
// — used by Adapter.shouldFinalizeByFallback (adapter.go) to ask
// Adapter.disconnectedSince whether a genuine connection disconnect
// occurred at any point during THIS turn's own current idle episode, not
// just whether the turn has been idle for some duration in the abstract.
func (ts *turnState) lastActivityTime() time.Time {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.lastActivity
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

// isCompacting/setCompacting implement the compacting guard described on
// this type's own field comment above — read by dispatchEvent's four
// guarded cases (sse.go), set by Adapter.finalizeOrRecoverFromOverflow/
// attemptCompactionRetry (adapter.go).
func (ts *turnState) isCompacting() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.compacting
}

func (ts *turnState) setCompacting(v bool) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.compacting = v
}

// compactionAlreadyAttempted reports the one-shot retry guard described on
// this type's own field comment above — read-only; see tryBeginCompactionRetry
// below for the ONLY path allowed to actually set it.
func (ts *turnState) compactionAlreadyAttempted() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.compactionAttempted
}

// tryBeginCompactionRetry atomically implements finalizeOrRecoverFromOverflow's
// own "first-time ContextOverflowError, not yet attempted" branch (adapter.go)
// as a single locked critical section: check compactionAttempted, and if
// false, mark it true and set compacting true, all under the SAME ts.mu
// acquisition. Returns whether THIS call is the one that gets to launch the
// retry.
//
// §7.2 Finding 2's own fix: the OLD call site did this as two SEPARATE
// method calls (ts.compactionAlreadyAttempted() then, later, ts.
// markCompactionAttempted()+ts.setCompacting(true)) — a classic
// check-then-act race with no lock spanning both steps. The live SSE
// session.idle dispatch (sse.go, on the persistent SSE-reader goroutine) and
// the independent SSE-inactivity fallback (finalizeByFallback, adapter.go,
// on StartTurn's own calling goroutine) can both observe the very first
// ContextOverflowError for the same turn at nearly the same wall-clock
// moment (a plausible near-tie: both are wall-clock-driven, independent
// goroutines racing on the exact same a.sseInactivityTimeout threshold) —
// under the old two-call sequence, both could read compactionAttempted==false
// before either had set it, and both would go on to launch their own
// attemptCompactionRetry goroutine: two concurrent POST /summarize calls and,
// if both succeed, two concurrent retried prompt_async re-dispatches against
// the SAME OpenCode session, violating §3.3's "exactly one processing per
// session" invariant. Folding the check and the two writes into one
// ts.mu-guarded method closes that race exactly the same way tryFinalize
// above already closes the equivalent race for finalization.
func (ts *turnState) tryBeginCompactionRetry() bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.compactionAttempted {
		return false
	}
	ts.compactionAttempted = true
	ts.compacting = true
	return true
}

// clearErrorsForRetry resets this turn's own tracked assistant/session
// errors — called ONLY by Adapter.attemptCompactionRetry (adapter.go),
// immediately after a successful forceCompaction and immediately before
// re-dispatching the same prompt. CRITICAL: without this, a SUCCESSFUL
// retry's own eventual session.idle would still see the STALE original
// ContextOverflowError via errorForOutcome below and incorrectly finalize
// the turn as failed even though the retry itself actually succeeded.
func (ts *turnState) clearErrorsForRetry() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.lastAssistantError = nil
	ts.sessionError = nil
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
// OpenCode ever repeating the same subtask id across multiple updates for
// its own underlying signal (a "subtask" part's own fields are static once
// created, per its schema; a "task" tool call's own "running" state can
// likewise repeat — this guard keeps sub_task_start genuinely "first sight
// only" regardless of which signal or how many times it repeats).
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

// markSubtaskFinished removes subTaskID from the open set — called by
// maybeFinishTaskSubtask (sse.go) once it has ALREADY emitted that
// sub-task's own sub_task_finish via the real, per-sub-task completion
// signal (the task tool call's own "completed"/"error" transition), so
// Adapter.finalize's own drainOpenSubtasks below (still the fallback for a
// sub-task whose own completion signal never arrived before the turn
// itself ended, e.g. cancellation) never double-closes it.
func (ts *turnState) markSubtaskFinished(subTaskID string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	delete(ts.subtasksOpen, subTaskID)
}

// drainOpenSubtasks returns every subTaskId still open (sub_task_start
// emitted, sub_task_finish not yet) and clears the set — called exactly
// once, by Adapter.finalize, so every subtask this turn ever started is
// guaranteed a matching sub_task_finish (§6.1's own critical-delivery
// rationale for that event type). Only ever contains a subtask the real
// per-sub-task completion signal (markSubtaskFinished above) has not
// already closed out.
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
