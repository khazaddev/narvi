package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// ssePollDivisor derives Adapter.pollInterval from the caller-supplied
// sseInactivityTimeout by plain integer division, rather than introducing
// either a dedicated new platform.Timeouts field or a forbidden
// time.Duration unit literal (§5.4/§11's notimeliteral lint forbids
// time.Second/time.Millisecond/etc. selectors anywhere outside
// platform/timeouts.go and _test.go files) — this Step's own instructions
// scope new Timeouts fields narrowly to exactly the two opencodeproc needs.
// 24 keeps the poll granularity comfortably fine relative to the default
// 120s SSEInactivityTimeout (5s) without hammering anything, and scales
// automatically if that default is ever reconfigured.
const ssePollDivisor = 24

// fallbackReconnectGraceMultiplier bounds how many multiples of
// sseInactivityTimeout waitForTurn's own poll loop waits for reconnection to
// recover a dropped connection (Finding 1/2's own liveness disambiguation)
// before finalizing via the fallback anyway -- a plain named int constant,
// not a forbidden time.Duration literal (§5.4/§11's notimeliteral lint
// forbids time.Second/time.Millisecond/etc. selectors outside
// platform/timeouts.go and _test.go files; this multiplies an existing
// duration FIELD, mirroring ssePollDivisor's own precedent above, not a
// fresh unit literal). 2x gives reconnection (now happening every
// platform.Timeouts.OpenCodeSSEReconnectInterval, deliberately much shorter
// than sseInactivityTimeout) a full extra window beyond the normal
// per-turn-idle threshold before concluding the turn's own real outcome is
// unrecoverable -- long enough to be a genuine grace period for a
// same-machine, sandbox-agent-managed process's connection to recover, short
// enough that a turn can never wait unboundedly on a connection that never
// comes back.
const fallbackReconnectGraceMultiplier = 2

// Adapter is the OpenCode ports.AgentRuntime implementation (§7): a pure
// HTTP+SSE client against an ALREADY-RUNNING `opencode serve` process —
// mirroring internal/adapters/outbound/modal's own shape exactly. This
// package does NOT spawn or supervise the OpenCode process itself; that is
// internal/sandboxagent/opencodeproc's job, using
// internal/sandboxagent/supervisor — the same separation Modal's adapter
// already establishes relative to Step 13's supervisor. Build one with New
// from a baseURL an opencodeproc.Spawn call already confirmed healthy.
//
// # Verified vs. schema-derived vs. best-effort
//
// See types.go's own per-type comments for the detailed breakdown; the
// short version: session/prompt_async/abort/message-list shapes and the
// text/step-start/step-finish/tool part progressions (including a genuine
// abort producing session.error{MessageAbortedError}) were all directly
// observed against the real, installed OpenCode 1.17.15 binary during this
// Step's own research pass. The "subtask" part type was schema-derived
// only at the time — a later Step's own investigation DID go on to
// exercise OpenCode's own "task" tool live (see "Sub-task handling" below)
// and found that this part type still never fires; that section is the
// up-to-date, empirically-verified account of this adapter's own §7.1
// sub-task handling.
//
// # Sub-task handling (§7.1): the real correlator, found by live investigation
//
// A later Step's own investigation set out to find OpenCode's real,
// live wire correlator for §7.1's "subTaskId" fan-out (which event tells
// this adapter that a batch of subsequent events belongs to a spawned
// sub-agent, not the turn's own main lane) — starting a real `opencode
// serve` process, scripting a prompt that explicitly delegates via
// OpenCode's own "task" tool, and inspecting the real resulting SSE trace.
// Two things came out of that investigation:
//
//  1. The "subtask" part type (subtaskPart, types.go) — the ONLY signal
//     this adapter originally watched for — never appeared on the wire at
//     all for a real, live-triggered task-tool invocation. It remains
//     schema-present (confirmed in /doc) but is now further confirmed
//     unverified-live even after a genuine attempt to trigger it; kept
//     only as an extra fallback path (dispatchSubtaskStart, sse.go) in
//     case some other OpenCode-internal path emits it.
//  2. The REAL signal observed: an ordinary "tool" part (tool=="task") —
//     going through the SAME dispatchTool machinery every other tool call
//     already does — whose own toolPartState carries a "metadata" object
//     (taskToolMetadata, types.go: {"parentSessionId","sessionId"}) once
//     status reaches "running" (confirmed still present at "completed").
//     sessionId is the newly-spawned sub-agent's own DISTINCT OpenCode
//     session id — every one of ITS OWN inner text/tool/step-start/
//     step-finish parts subsequently arrives on the SAME global /event
//     stream tagged with THAT session id as its own top-level
//     "sessionID", never the enclosing turn's.
//
// This is genuinely more information than §7.1 assumed was available when
// it was written ("OpenCode's own nested-task id" was documented as the
// correlator in the abstract, without having independently confirmed a
// concrete field for it): maybeStartTaskSubtask/maybeFinishTaskSubtask
// (sse.go) use meta.SessionID directly as this sub-task's own subTaskId,
// and Adapter.registerSubtaskSession/resolveEvent route that CHILD
// session's own subsequent events back to the SAME turnState, tagged with
// that subTaskId, via translateToken/translateStepStart/
// translateStepFinish/translateToolCall/translateToolResult (translate.go)
// — each of the six event types Finding 1's own schema change added
// subTaskId to.
//
// This ALSO gives this adapter a genuinely more precise sub_task_finish
// than before: the task tool call's own "completed"/"error" transition
// (maybeFinishTaskSubtask) is a real, per-sub-task completion signal, not
// just the enclosing turn's own eventual outcome. finalize's own
// drainOpenSubtasks (below) is kept as the fallback for the one case that
// signal can't cover: a sub-task whose own task tool call never reaches a
// terminal status before the turn itself ends (e.g. the turn is
// cancelled while the task tool call is still "running") — that sub-task
// is still closed out using the ENCLOSING turn's own outcome, exactly as
// documented before this investigation.
//
// What remains genuinely unverified/best-effort, honestly, after this
// investigation: a sub-agent's own INTERNAL failure (e.g. an assistant
// error inside its own conversation, as opposed to the task tool call
// itself reporting "error") is not separately modeled — only the task
// tool's own reported status governs a sub-task's own outcome, mirroring
// how an ordinary tool_result's own isError already works. The model
// stays flat (§7.1: a sub-task cannot itself spawn a further-nested
// sub-task) — nothing here defends against or specially handles a nested
// "task" tool call occurring INSIDE a sub-agent's own conversation, since
// OpenCode's own real task-tool contract does not appear to allow one.
type Adapter struct {
	baseURL    string
	httpClient *http.Client

	sseInactivityTimeout time.Duration
	pollInterval         time.Duration

	// reconnectInterval is runEventLoop's own reconnect delay after a
	// dropped GET /event connection (platform.Timeouts.
	// OpenCodeSSEReconnectInterval) -- deliberately a SEPARATE field from
	// sseInactivityTimeout (Finding 2: reusing the latter as the
	// reconnect delay made reconnection structurally unable to ever beat
	// the per-turn fallback race).
	reconnectInterval time.Duration

	// requestTimeout bounds every doJSON-routed HTTP call (client.go) via
	// a per-request context.WithTimeout wrap (Finding 3) --
	// platform.Timeouts.OpenCodeRequestTimeout. Deliberately NOT applied
	// as a client-wide http.Client.Timeout: connectAndConsume's own GET
	// /event call uses this SAME httpClient for the intentionally
	// long-lived persistent SSE stream, which a client-wide Timeout would
	// incorrectly kill.
	requestTimeout time.Duration

	mu               sync.Mutex
	currentSessionID string

	// disconnectMu guards lastDisconnectAt -- the adapter-level (not
	// per-turn) record of the last time runEventLoop's own connectAndConsume
	// call observed a GENUINE connection error (a real outage), set BEFORE
	// the reconnect-delay wait so it reflects when the outage STARTED, not
	// when reconnection later succeeds (see recordDisconnect below). Zero
	// value means no disconnect has ever been observed. Deliberately never
	// reset on a successful reconnect: shouldFinalizeByFallback's own
	// disambiguation needs to ask "did a disconnect happen at any point
	// during THIS turn's own current idle episode", which only a value that
	// stays put (compared against a per-turn timestamp) can answer --
	// reconnecting successfully doesn't retroactively prove any given turn
	// is fine, only that reconnection is now technically possible. A
	// separate mutex from mu above (which guards the unrelated
	// currentSessionID) keeps each concern's own lock scoped to exactly
	// what it protects, matching this file's own existing per-concern-
	// locking style.
	disconnectMu     sync.Mutex
	lastDisconnectAt time.Time

	turnsMu sync.Mutex
	turns   map[string]*turnState

	// subtasksMu/subtaskSessions extend the turns registry above to ALSO
	// route a sub-task's own distinct child OpenCode session id back to
	// the SAME enclosing turnState, tagged with that sub-task's own
	// subTaskId — see maybeStartTaskSubtask (sse.go) and this type's own
	// doc comment for the real, empirically-verified §7.1 correlator this
	// implements. A separate mutex/map (rather than folding this into
	// turns/turnsMu itself) keeps "which turn does this MAIN session
	// belong to" and "which turn/sub-task does this CHILD session belong
	// to" independently scoped, matching this file's own existing
	// per-concern-locking style (see disconnectMu above).
	subtasksMu      sync.Mutex
	subtaskSessions map[string]subtaskSession

	connectOnce sync.Once
	connectedCh chan struct{}

	bgCancel context.CancelFunc
	group    errgroup.Group
}

var _ ports.AgentRuntime = (*Adapter)(nil)

// New constructs an Adapter against baseURL (an already-running OpenCode
// server — see internal/sandboxagent/opencodeproc.Spawn) and immediately
// starts ONE persistent connection to OpenCode's own global GET /event
// stream, opened here once for this Adapter's whole lifetime rather than
// per-turn (§7: "filter the global event stream by session id" — a single
// shared stream is what makes that filtering necessary and meaningful in
// the first place; opening a fresh stream per turn would also risk a
// subscribe-before-first-event race this design avoids entirely), via
// this Adapter's own zero-value errgroup.Group.Go call — the same
// "background work started from within a constructor/Spawn-style call,
// via the type's own errgroup field, never a bare `go` statement"
// pattern internal/sandboxagent/supervisor.Supervisor.Spawn already
// establishes for its own reap goroutine.
//
// sseInactivityTimeout, reconnectInterval, and requestTimeout are, in
// production, platform.Timeouts.SSEInactivityTimeout,
// OpenCodeSSEReconnectInterval, and OpenCodeRequestTimeout respectively:
// sseInactivityTimeout is the per-turn silence threshold both waitForTurn's
// own fallback and its own disconnect-during-this-idle-episode
// disambiguation (disconnectedSince) share; reconnectInterval is how long
// runEventLoop waits before retrying a dropped GET /event connection
// (deliberately much shorter than sseInactivityTimeout — Finding 2 — so
// reconnection has a real chance to win against the fallback); requestTimeout
// bounds every doJSON-routed HTTP call (Finding 3), applied per-request in
// client.go, never as a client-wide http.Client.Timeout (which would also
// incorrectly bound the persistent SSE connection below).
func New(baseURL string, sseInactivityTimeout, reconnectInterval, requestTimeout time.Duration) *Adapter {
	bgCtx, cancel := context.WithCancel(context.Background())

	a := &Adapter{
		baseURL:              strings.TrimSuffix(baseURL, "/"),
		httpClient:           &http.Client{},
		sseInactivityTimeout: sseInactivityTimeout,
		reconnectInterval:    reconnectInterval,
		requestTimeout:       requestTimeout,
		pollInterval:         sseInactivityTimeout / ssePollDivisor,
		turns:                make(map[string]*turnState),
		subtaskSessions:      make(map[string]subtaskSession),
		connectedCh:          make(chan struct{}),
		bgCancel:             cancel,
	}

	a.group.Go(func() error {
		return a.runEventLoop(bgCtx)
	})

	return a
}

// Close stops the persistent SSE connection and waits for it to actually
// exit. Not part of ports.AgentRuntime (the port declares no lifecycle-
// teardown method — see agentruntime.go's own doc comment on why nothing
// engine-specific belongs there); an extra capability on the concrete
// type, called by cmd/sandbox-agent/main.go's own shutdown sequence and by
// this package's own tests.
func (a *Adapter) Close() {
	a.bgCancel()
	_ = a.group.Wait()
}

// Connected blocks until the persistent SSE stream has observed at least
// one server.connected handshake, or ctx is done — used by this package's
// own tests to prove the stream came up; not required for StartTurn's own
// correctness (a turn's prompt_async POST and its own SSE-inactivity
// fallback are safe even if called before the very first server.connected
// arrives).
func (a *Adapter) Connected(ctx context.Context) error {
	select {
	case <-a.connectedCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *Adapter) getCurrentSession() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.currentSessionID
}

func (a *Adapter) setCurrentSession(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.currentSessionID = id
}

func (a *Adapter) registerTurn(sessionID string, ts *turnState) {
	a.turnsMu.Lock()
	defer a.turnsMu.Unlock()
	a.turns[sessionID] = ts
}

func (a *Adapter) unregisterTurn(sessionID string) {
	a.turnsMu.Lock()
	defer a.turnsMu.Unlock()
	delete(a.turns, sessionID)
}

func (a *Adapter) lookupTurn(sessionID string) *turnState {
	a.turnsMu.Lock()
	defer a.turnsMu.Unlock()
	return a.turns[sessionID]
}

// subtaskSession is one registered sub-task's own routing entry:
// register/unregisterSubtaskSession/lookupSubtaskSession below.
type subtaskSession struct {
	ts        *turnState
	subTaskID string
}

// registerSubtaskSession/unregisterSubtaskSession/lookupSubtaskSession
// implement the child-session routing this type's own subtaskSessions
// field documents — called by maybeStartTaskSubtask/maybeFinishTaskSubtask
// (sse.go) and by finalize's own drainOpenSubtasks loop below.
func (a *Adapter) registerSubtaskSession(childSessionID string, ts *turnState, subTaskID string) {
	a.subtasksMu.Lock()
	defer a.subtasksMu.Unlock()
	a.subtaskSessions[childSessionID] = subtaskSession{ts: ts, subTaskID: subTaskID}
}

func (a *Adapter) unregisterSubtaskSession(childSessionID string) {
	a.subtasksMu.Lock()
	defer a.subtasksMu.Unlock()
	delete(a.subtaskSessions, childSessionID)
}

func (a *Adapter) lookupSubtaskSession(childSessionID string) (*turnState, string, bool) {
	a.subtasksMu.Lock()
	defer a.subtasksMu.Unlock()
	sub, ok := a.subtaskSessions[childSessionID]
	return sub.ts, sub.subTaskID, ok
}

// resolveEvent resolves sessionID to whichever turnState it belongs to —
// the turn's own main OpenCode session (subTaskID == "", the overwhelming
// common case) or a sub-task's own distinct child session (subTaskID !=
// "", see registerSubtaskSession above) — for dispatchEvent (sse.go) to
// route by uniformly, regardless of which lane an incoming SSE event's own
// top-level sessionID belongs to. ok is false when sessionID matches
// neither — the existing "no registered turn for this session, silently
// drop" case dispatchEvent's own doc comment already documents.
func (a *Adapter) resolveEvent(sessionID string) (ts *turnState, subTaskID string, ok bool) {
	if ts := a.lookupTurn(sessionID); ts != nil {
		return ts, "", true
	}
	return a.lookupSubtaskSession(sessionID)
}

// recordDisconnect records that runEventLoop's own connectAndConsume call
// just observed a GENUINE connection error (a real outage), called BEFORE
// the reconnect-delay wait so this reflects the moment the outage STARTED,
// not when reconnection later succeeds — Finding 1/2's own disambiguation
// (see shouldFinalizeByFallback below) depends on this being the outage's
// own start time, not a point-in-time "is the stream fresh right now"
// snapshot.
func (a *Adapter) recordDisconnect() {
	a.disconnectMu.Lock()
	defer a.disconnectMu.Unlock()
	a.lastDisconnectAt = time.Now()
}

// disconnectedSince reports whether a genuine disconnect (recordDisconnect
// above) was observed strictly after t — shouldFinalizeByFallback's own
// disambiguation passes a turn's own lastActivityTime as t, to ask "did a
// real outage occur at any point during THIS turn's current idle episode",
// as opposed to merely asking whether the stream looks fresh RIGHT NOW (a
// point-in-time snapshot that reconnecting alone flips back to "fresh"
// without proving anything about what happened during the turn's own
// silence). The zero value of lastDisconnectAt (no disconnect ever
// observed) is never After any real t, so this correctly reports false
// until the first genuine disconnect.
func (a *Adapter) disconnectedSince(t time.Time) bool {
	a.disconnectMu.Lock()
	defer a.disconnectMu.Unlock()
	return a.lastDisconnectAt.After(t)
}

// StartTurn implements ports.AgentRuntime — see that interface's own doc
// comment for the full contract. Dispatches cmd as a turn, streams every
// translated event to sink live, and always attempts exactly one
// execution_complete-shaped terminal event before returning — including on
// EVERY early-return path below, not just the common case: a ctx
// cancellation before resolveSession/postPromptAsync ever completes, or
// while waitForTurn is still blocked, now correctly finalizes via
// finalizeCanceled instead of returning silently (Finding 5 — the three
// gaps this promise used to have).
//
// Step 28 ("turn recovery") adds onConversationID: invoked immediately
// after resolveSession succeeds below — BEFORE registerTurn/
// postPromptAsync/waitForTurn, i.e. well before this call's own long
// block for the rest of the turn — with the real, resolved sessionID.
// Guarded against nil (some callers/tests have no need for early
// reporting) and never called on the resolveSession failure path (no real
// id was ever resolved there — see ConversationIDReporter's own doc
// comment: it fires "at most once... with a real, non-empty, resolved
// conversation id").
func (a *Adapter) StartTurn(ctx context.Context, cmd sandboxws.Prompt, sink ports.EventSink, onConversationID ports.ConversationIDReporter) (string, error) {
	// Created up front, before resolveSession is ever called, so EVERY
	// subsequent return path below — including the very first one — has
	// a valid turnState to finalize through. This does not register it in
	// a.turns any earlier than today: nothing dispatches SSE events for a
	// session that doesn't exist yet, so only its own local existence
	// needs to move up (registerTurn below is unchanged).
	ts := newTurnState(cmd, sink)

	sessionID, err := a.resolveSession(ctx, cmd)
	if err != nil {
		if ctx.Err() != nil {
			a.finalizeCanceled(ts)
			return "", ctx.Err()
		}
		reason := "opencode: could not start a conversation"
		a.finalize(ts, turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeFailed, Reason: &reason})
		return "", nil
	}

	// §3.3 "at turn start... never lazily": report the resolved
	// conversation id THE MOMENT it's known, not once this whole
	// (possibly minutes-long) call eventually returns it — see this
	// method's own doc comment above.
	if onConversationID != nil {
		onConversationID(sessionID)
	}

	a.setCurrentSession(sessionID)

	a.registerTurn(sessionID, ts)
	defer a.unregisterTurn(sessionID)

	model := a.resolveModel(ctx, (*string)(cmd.Model))
	if err := a.postPromptAsync(ctx, sessionID, cmd, model); err != nil {
		if ctx.Err() != nil {
			a.finalizeCanceled(ts)
			return sessionID, ctx.Err()
		}
		reason := "opencode: could not dispatch prompt"
		a.finalize(ts, turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeFailed, Reason: &reason})
		return sessionID, nil
	}

	a.waitForTurn(ctx, sessionID, ts)
	return sessionID, nil
}

// finalizeCanceled finalizes ts with a Cancelled outcome and an honest,
// fixed reason string — used by every one of StartTurn/waitForTurn's own
// ctx-cancellation early-return paths (Finding 5). Cancelled (rather than
// Failed) is the correct outcome for an externally-imposed interruption
// like this, not a genuine execution failure — the same precedent
// deriveOutcome (outcome.go) already follows for a real
// MessageAbortedError. Safe to call even if ts is already finalized
// (finalize's own tryFinalize guard makes every finalize call idempotent),
// so no caller here needs to reason about whether some OTHER goroutine
// (e.g. the SSE loop's own session.idle/session.error dispatch) might have
// already finalized ts first.
func (a *Adapter) finalizeCanceled(ts *turnState) {
	reason := "opencode: turn context canceled before completion"
	a.finalize(ts, turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeCancelled, Reason: &reason})
}

// waitForTurn blocks until ts.done is closed (the SSE loop's own
// dispatchEvent already finalized and emitted the turn's terminal event
// via session.idle/session.error), ctx is canceled, or this turn's own
// SSE-inactivity fallback fires — polling ts.idleFor rather than a
// Timer.Reset-based scheme (see turnState.idleFor's own doc comment for
// why). A ctx cancellation now finalizes ts (Cancelled) before returning
// (Finding 5) rather than leaving the turn's own execution_complete
// promise unfulfilled — safe even if some OTHER goroutine (the SSE loop,
// or this turn's own fallback path racing on a near-simultaneous tick)
// already finalized ts first, since finalizeCanceled's underlying
// a.finalize call is idempotent.
func (a *Adapter) waitForTurn(ctx context.Context, sessionID string, ts *turnState) {
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.finalizeCanceled(ts)
			return
		case <-ts.done:
			return
		case <-ticker.C:
			if a.shouldFinalizeByFallback(ts) {
				a.finalizeByFallback(ctx, sessionID, ts)
				return
			}
		}
	}
}

// shouldFinalizeByFallback implements Finding 1/2's own liveness
// disambiguation: ts.idleFor(a.sseInactivityTimeout) alone (the ORIGINAL
// fallback trigger) cannot tell "this turn is genuinely stuck" apart from
// "the whole SSE connection dropped, and this turn just went silent as a
// side effect" — the latter might still resolve normally once runEventLoop
// reconnects (now happening every a.reconnectInterval, deliberately much
// shorter than a.sseInactivityTimeout so it has a real chance to win this
// race), letting the turn's own real session.idle/session.error arrive as
// usual.
//
// This asks "did a genuine disconnect happen at any point DURING this
// turn's own current idle episode" (a.disconnectedSince(ts.
// lastActivityTime())), NOT "does the stream look fresh RIGHT NOW" — a
// point-in-time snapshot a bare reconnect handshake flips back to "fresh"
// on the very next poll tick, without the turn itself having had any
// chance yet to receive its own completion event. Reconnecting only proves
// reconnection is now technically possible, never that THIS turn's own
// outcome is ready.
//
//   - Turn idle, no disconnect ever observed during this turn's own
//     silence (!a.disconnectedSince(...)): this turn alone stalled on a
//     continuously healthy connection — the fallback's own original reason
//     for existing, unchanged. Finalize now.
//   - Turn idle AND a genuine disconnect occurred at some point during
//     this turn's own current silence (a.disconnectedSince(...)): do NOT
//     finalize yet — keep polling, giving the turn's own real outcome a
//     real chance to arrive normally (whether the stream has already
//     reconnected or not). Bounded: once the turn has been silent for
//     fallbackReconnectGraceMultiplier times the normal threshold (a full
//     extra window beyond the point reconnection should have already
//     recovered, were it going to), finalize via the fallback anyway —
//     this wait must never be unbounded.
func (a *Adapter) shouldFinalizeByFallback(ts *turnState) bool {
	if !ts.idleFor(a.sseInactivityTimeout) {
		return false
	}
	if a.disconnectedSince(ts.lastActivityTime()) {
		return ts.idleFor(fallbackReconnectGraceMultiplier * a.sseInactivityTimeout)
	}
	return true
}

// finalizeByFallback implements §7's own "final-state fetch fallback"
// quirk: the SSE stream went quiet for longer than sseInactivityTimeout
// without session.idle/session.error ever arriving, so this determines the
// final state directly via GET /session/{id}/message instead of hanging
// forever waiting for an event that may never come.
func (a *Adapter) finalizeByFallback(ctx context.Context, sessionID string, ts *turnState) {
	entries, err := a.fetchFinalMessages(ctx, sessionID)
	if err != nil || len(entries) == 0 {
		reason := "opencode: SSE stream went inactive and the final-state fallback fetch also failed"
		a.finalize(ts, turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeFailed, Reason: &reason})
		return
	}

	last := entries[len(entries)-1]
	hasText, hasToolCall := partsHaveOutput(last.Parts)
	a.finalize(ts, deriveOutcome(last.Info.Error, hasText, hasToolCall))
}

// finalize is the single path that ever emits a turn's own terminal
// events: guarded by ts.tryFinalize so a stray duplicate session.idle (or
// one arriving after the inactivity fallback already ran) can never
// double-emit. Closes out every still-open sub-task (§7.1, see this
// package's own doc comment on Adapter for the full sub-task-fidelity
// writeup) before the turn's own execution_complete, unregistering each
// one's own child-session routing entry too (a subtask closed via the
// real per-sub-task completion signal, maybeFinishTaskSubtask, already
// unregistered and removed itself before ever reaching here — see
// markSubtaskFinished's own doc comment, turn.go — so this loop only ever
// runs for a sub-task whose own completion was never observed, e.g. the
// turn was cancelled while its task tool call was still running), then
// closes ts.done so waitForTurn's own poll loop returns.
//
// Uses ts.emitFinal, NOT ts.emit, for both kinds of terminal emission
// below (Finding 4): tryFinalize above has already set ts.finalized=true
// under ts.mu, and emit's own finalized check (turn.go) — reading that SAME
// flag under that SAME lock — would unconditionally drop anything finalize
// itself tries to emit here. emitFinal is the one path explicitly entitled
// to emit despite ts.finalized already being true, since finalize is the
// only caller of it.
func (a *Adapter) finalize(ts *turnState, outcome turnOutcome) {
	if !ts.tryFinalize() {
		return
	}

	for _, subTaskID := range ts.drainOpenSubtasks() {
		a.unregisterSubtaskSession(subTaskID)
		ts.emitFinal(translateSubTaskFinish(ts.cmd, subTaskID, outcome.Outcome))
	}

	ts.emitFinal(translateExecutionComplete(ts.cmd, outcome, ""))

	close(ts.done)
}

// Stop implements ports.AgentRuntime — aborts whatever OpenCode session is
// currently "current" for this adapter (there is exactly one live
// conversation per sandbox-agent process at a time, §3.3: "Exactly one
// processing per session"); cmd itself carries no OpenCode session
// reference of its own (commands.schema.json's own Stop has no
// conversationId field). A Stop before any session has ever been created
// is a safe local no-op (no HTTP call at all, so it can never fail).
func (a *Adapter) Stop(ctx context.Context, _ sandboxws.Stop) error {
	sessionID := a.getCurrentSession()
	if sessionID == "" {
		return nil
	}
	return a.postAbort(ctx, sessionID)
}

// partsHaveOutput scans a message's own parts (as returned by
// GET /session/{id}/message) for a non-empty text part or any tool part —
// the same "genuinely non-empty output" signal outcomeInputs derives
// incrementally from the live SSE stream, computed instead from the
// final-state fallback's own already-complete snapshot.
func partsHaveOutput(parts []json.RawMessage) (hasText, hasToolCall bool) {
	for _, raw := range parts {
		var env partEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		switch env.Type {
		case "text":
			var p textPart
			if err := json.Unmarshal(raw, &p); err == nil && p.Text != "" {
				hasText = true
			}
		case "tool":
			hasToolCall = true
		}
	}
	return hasText, hasToolCall
}
