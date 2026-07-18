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
// Step's own research pass. The "subtask" part type is schema-derived only
// (confirmed present in the real, live /doc OpenAPI spec, not independently
// elicited — that requires the model to invoke OpenCode's own "task" tool,
// non-deterministic and not exercised here).
//
// # Sub-task handling is a documented best-effort simplification (§7.1)
//
// This adapter emits sub_task_start the first time it sees a "subtask"
// part. OpenCode's own nested-completion signal for a sub-task
// specifically — as opposed to the enclosing turn's own session.idle — is
// NOT independently verified (ground truth explicitly flags this exact
// piece as unverified territory to treat as best-effort). Rather than
// build unverified nested-session-tracking logic on top of an already-
// uncertain schema, this adapter guarantees the CRITICAL sub_task_finish
// event is never leaked (§6.1's own rationale: a dropped, never-
// redelivered sub_task_finish would leave a UI sub-lane stuck active
// forever) by closing every still-open sub-task using the ENCLOSING turn's
// own outcome at the moment that turn itself finalizes (see finalize
// below) — at the cost of a sub-task's own reported outcome potentially
// reflecting the whole turn's fate rather than a truly isolated one. This
// trade-off, and the reasoning behind it, is intentional and documented
// here rather than silently guessed at.
type Adapter struct {
	baseURL    string
	httpClient *http.Client

	sseInactivityTimeout time.Duration
	pollInterval         time.Duration

	mu               sync.Mutex
	currentSessionID string

	turnsMu sync.Mutex
	turns   map[string]*turnState

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
func New(baseURL string, sseInactivityTimeout time.Duration) *Adapter {
	bgCtx, cancel := context.WithCancel(context.Background())

	a := &Adapter{
		baseURL:              strings.TrimSuffix(baseURL, "/"),
		httpClient:           &http.Client{},
		sseInactivityTimeout: sseInactivityTimeout,
		pollInterval:         sseInactivityTimeout / ssePollDivisor,
		turns:                make(map[string]*turnState),
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

// StartTurn implements ports.AgentRuntime — see that interface's own doc
// comment for the full contract. Dispatches cmd as a turn, streams every
// translated event to sink live, and always attempts exactly one
// execution_complete-shaped terminal event before returning.
func (a *Adapter) StartTurn(ctx context.Context, cmd sandboxws.Prompt, sink ports.EventSink) (string, error) {
	sessionID, err := a.resolveSession(ctx, cmd)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		reason := "opencode: could not start a conversation"
		ts := newTurnState(cmd, sink)
		a.finalize(ts, turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeFailed, Reason: &reason})
		return "", nil
	}

	a.setCurrentSession(sessionID)

	ts := newTurnState(cmd, sink)
	a.registerTurn(sessionID, ts)
	defer a.unregisterTurn(sessionID)

	model := a.resolveModel(ctx, (*string)(cmd.Model))
	if err := a.postPromptAsync(ctx, sessionID, cmd, model); err != nil {
		if ctx.Err() != nil {
			return sessionID, ctx.Err()
		}
		reason := "opencode: could not dispatch prompt"
		a.finalize(ts, turnOutcome{Outcome: sandboxws.ExecutionCompleteOutcomeFailed, Reason: &reason})
		return sessionID, nil
	}

	a.waitForTurn(ctx, sessionID, ts)
	return sessionID, nil
}

// waitForTurn blocks until ts.done is closed (the SSE loop's own
// dispatchEvent already finalized and emitted the turn's terminal event
// via session.idle/session.error), ctx is canceled, or this turn's own
// SSE-inactivity fallback fires — polling ts.idleFor rather than a
// Timer.Reset-based scheme (see turnState.idleFor's own doc comment for
// why).
func (a *Adapter) waitForTurn(ctx context.Context, sessionID string, ts *turnState) {
	ticker := time.NewTicker(a.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ts.done:
			return
		case <-ticker.C:
			if ts.idleFor(a.sseInactivityTimeout) {
				a.finalizeByFallback(ctx, sessionID, ts)
				return
			}
		}
	}
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
// package's own doc comment on Adapter for the documented best-effort
// reasoning) before the turn's own execution_complete, then closes
// ts.done so waitForTurn's own poll loop returns.
func (a *Adapter) finalize(ts *turnState, outcome turnOutcome) {
	if !ts.tryFinalize() {
		return
	}

	for _, subTaskID := range ts.drainOpenSubtasks() {
		ts.emit(translateSubTaskFinish(ts.cmd, subTaskID, outcome.Outcome))
	}

	ts.emit(translateExecutionComplete(ts.cmd, outcome))

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
