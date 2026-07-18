package opencode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// sseDataPrefix is the one SSE field this adapter cares about — VERIFIED
// live: every line on the global /event stream is `data: <json>\n\n`. Any
// other SSE field (event:, id:, retry:, comments) or a blank separator
// line is silently ignored by processSSELine.
const sseDataPrefix = "data: "

// runEventLoop maintains ONE persistent connection to the global GET
// /event SSE stream for the adapter's whole lifetime, opened once here
// rather than per-turn (§7: "filter the global event stream by session
// id"), reconnecting on any failure until ctx is canceled
// (Adapter.Close). Every parsed event is dispatched to whichever turnState
// is currently registered for its OpenCode sessionID, if any (dispatchEvent
// below) — an event for a session with no registered turn is silently
// dropped; this can only happen for a session this adapter never started
// a turn for, or one whose turn already finalized and unregistered.
// StartTurn always registers a turnState BEFORE POSTing prompt_async (see
// adapter.go), so no live turn's own events can ever race this.
//
// Reconnect backoff reuses a.sseInactivityTimeout (already available, no
// dedicated new value) rather than a fresh platform.Timeouts field or a
// forbidden time.Duration unit literal (§5.4/§11, enforced by
// tools/lint/narvichecks/notimeliteral) — this connection is to a
// same-machine, sandbox-agent-managed process (internal/sandboxagent/
// opencodeproc), so a drop is expected to be rare/fatal-ish rather than
// something needing a tuned backoff curve of its own.
func (a *Adapter) runEventLoop(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := a.connectAndConsume(ctx); err != nil && ctx.Err() == nil {
			// A failure that coincides with ctx already being canceled
			// (Adapter.Close, or the process shutting down) is an
			// ordinary, expected disconnect -- not worth a warning.
			slog.Warn("opencode: event stream connection lost, reconnecting", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(a.sseInactivityTimeout):
		}
	}
}

// connectAndConsume performs one GET /event handshake and reads it line by
// line until the connection ends or ctx is canceled. Uses bufio.Reader.
// ReadString rather than bufio.Scanner specifically because Scanner's
// default token buffer has a fixed cap that a long tool-output-carrying
// SSE line could exceed; ReadString has no such limit.
func (a *Adapter) connectAndConsume(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.baseURL+"/event", nil)
	if err != nil {
		return fmt.Errorf("opencode: build GET /event request: %w", err)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("opencode: GET /event: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode: GET /event: http %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			a.processSSELine(line)
		}
		if err != nil {
			return err
		}
	}
}

// processSSELine parses one line of the SSE stream, dispatching it if it
// is a "data:" line carrying a well-formed sseEnvelope. A malformed line
// is logged and skipped — never torn down the whole connection over one
// bad frame: a single corrupt/unexpected SSE line from OpenCode must not
// take down every other in-flight turn sharing this one persistent
// connection.
func (a *Adapter) processSSELine(line string) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return
	}

	data, ok := strings.CutPrefix(line, sseDataPrefix)
	if !ok {
		return
	}

	var env sseEnvelope
	if err := json.Unmarshal([]byte(data), &env); err != nil {
		slog.Warn("opencode: malformed SSE event, skipping", "error", err)
		return
	}

	a.dispatchEvent(env)
}

// dispatchEvent routes one parsed SSE envelope by its own "type" field —
// the concrete shapes and verification status of each are documented on
// their own properties struct in types.go.
func (a *Adapter) dispatchEvent(env sseEnvelope) {
	switch env.Type {
	case "server.connected":
		a.connectOnce.Do(func() { close(a.connectedCh) })

	case "message.updated":
		var props messageUpdatedProps
		if err := json.Unmarshal(env.Properties, &props); err != nil {
			slog.Warn("opencode: malformed message.updated event, skipping", "error", err)
			return
		}
		ts := a.lookupTurn(props.SessionID)
		if ts == nil {
			return
		}
		ts.touch()
		if props.Info.Role == "assistant" {
			ts.setLastAssistantError(props.Info.Error)
			ts.markAssistantMessageID(props.Info.ID)
		}

	case "message.part.updated":
		var props messagePartUpdatedProps
		if err := json.Unmarshal(env.Properties, &props); err != nil {
			slog.Warn("opencode: malformed message.part.updated event, skipping", "error", err)
			return
		}
		ts := a.lookupTurn(props.SessionID)
		if ts == nil {
			return
		}
		ts.touch()
		a.dispatchPart(ts, props.Part)

	case "session.idle":
		var props sessionIdleProps
		if err := json.Unmarshal(env.Properties, &props); err != nil {
			slog.Warn("opencode: malformed session.idle event, skipping", "error", err)
			return
		}
		ts := a.lookupTurn(props.SessionID)
		if ts == nil {
			return
		}
		ts.touch()
		hasText, hasToolCall := ts.outcomeInputs()
		a.finalize(ts, deriveOutcome(ts.errorForOutcome(), hasText, hasToolCall))

	case "session.error":
		var props sessionErrorProps
		if err := json.Unmarshal(env.Properties, &props); err != nil {
			slog.Warn("opencode: malformed session.error event, skipping", "error", err)
			return
		}
		ts := a.lookupTurn(props.SessionID)
		if ts == nil {
			return
		}
		ts.touch()
		ts.setSessionError(props.Error)

	default:
		// server.heartbeat, session.updated, session.status, session.diff,
		// message.part.delta (a redundant delta view of the SAME text
		// message.part.updated already reports cumulatively -- translating
		// it too would double-report the same content), catalog.updated,
		// bare busy/idle, plugin.*, integration.*, reference.updated, and
		// anything else a future OpenCode version adds: silently ignored,
		// exactly matching §7's own "no wire event exists for it, this is
		// normal/expected" bucket -- OpenCode's own event surface is far
		// wider than Narvi's own wire contract has slots for.
	}
}

// dispatchPart decodes one message.part.updated's own "part" object by its
// "type" discriminator (partEnvelope) and translates it, when a wire event
// slot exists for that part type.
func (a *Adapter) dispatchPart(ts *turnState, raw json.RawMessage) {
	var env partEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		slog.Warn("opencode: malformed part envelope, skipping", "error", err)
		return
	}

	switch env.Type {
	case "text":
		var p textPart
		if err := json.Unmarshal(raw, &p); err != nil {
			slog.Warn("opencode: malformed text part, skipping", "error", err)
			return
		}
		// VERIFIED LIVE: message.part.updated fires for the USER's own
		// message too (its prompt echoed back as a "text" part), not just
		// the assistant's reply. Only an assistant-owned text part may be
		// translated/counted -- see assistantMessageIDs' own field comment
		// (turn.go) for the full story of the bug this check fixes.
		if !ts.isAssistantMessage(p.MessageID) {
			return
		}
		if p.Text != "" {
			ts.markSawText()
		}
		ts.emit(translateToken(ts.cmd, p))

	case "step-start":
		ts.emit(translateStepStart(ts.cmd, env))

	case "step-finish":
		var p stepFinishPart
		if err := json.Unmarshal(raw, &p); err != nil {
			slog.Warn("opencode: malformed step-finish part, skipping", "error", err)
			return
		}
		ts.emit(translateStepFinish(ts.cmd, p))

	case "tool":
		var p toolPart
		if err := json.Unmarshal(raw, &p); err != nil {
			slog.Warn("opencode: malformed tool part, skipping", "error", err)
			return
		}
		a.dispatchTool(ts, p)

	case "subtask":
		var p subtaskPart
		if err := json.Unmarshal(raw, &p); err != nil {
			slog.Warn("opencode: malformed subtask part, skipping", "error", err)
			return
		}
		a.dispatchSubtaskStart(ts, p)

	case "compaction":
		// §7's own "handle compaction events" quirk. An ordinary/automatic
		// compaction (Auto true, Overflow false) is routine housekeeping —
		// no wire-event slot exists for it and none is warranted. An
		// OVERFLOW compaction (the context window genuinely ran out of
		// room mid-turn) is an operationally meaningful degradation this
		// adapter surfaces as a wire warning (translateCompactionOverflow)
		// — the closest existing slot, since the wire contract has no
		// dedicated compaction event.
		var p compactionPart
		if err := json.Unmarshal(raw, &p); err != nil {
			slog.Warn("opencode: malformed compaction part, skipping", "error", err)
			return
		}
		if p.Overflow {
			ts.emit(translateCompactionOverflow(ts.cmd, p))
		}

	default:
		// reasoning, patch, snapshot, file, agent, retry, and any future
		// part type: no wire-event slot exists for these (§7); silently
		// skipped, not an error.
	}
}

// dispatchTool implements §7's own "dedupe tool states by sid:callID:
// status" quirk — the single most important correctness property in this
// Step: emit tool_call the FIRST time a callID's own input is seen
// non-empty (skipping the empty-input "pending" state), and tool_result
// exactly ONCE, on the first "completed" or "error" status for that
// callID, ignoring every intermediate "running" update in between.
func (a *Adapter) dispatchTool(ts *turnState, p toolPart) {
	switch p.State.Status {
	case "pending":
		// Nothing real to report yet (§7: empty input).

	case "running":
		if ts.alreadySentCall(p.CallID) || toolInputEmpty(p.State.Input) {
			return
		}
		a.emitToolCall(ts, p)

	case "completed", "error":
		if !ts.alreadySentCall(p.CallID) {
			// This call resolved so fast we never saw a non-empty
			// "running" state first -- still owe a tool_call (the wire
			// contract pairs call+result) using this same final state's
			// own input.
			a.emitToolCall(ts, p)
		}
		if ts.alreadySentResult(p.CallID) {
			return
		}
		ts.markResultSent(p.CallID)
		ts.emit(translateToolResult(ts.cmd, p))

	default:
		slog.Warn("opencode: unrecognized tool state status, skipping",
			"status", p.State.Status, "callID", p.CallID)
	}
}

func (a *Adapter) emitToolCall(ts *turnState, p toolPart) {
	event, err := translateToolCall(ts.cmd, p)
	if err != nil {
		slog.Warn("opencode: malformed tool input, skipping tool_call", "callID", p.CallID, "error", err)
		return
	}
	ts.markCallSent(p.CallID)
	ts.markSawToolCall()
	ts.emit(event)
}

// dispatchSubtaskStart implements §7.1's own sub-task fan-out — see
// adapter.go's own package doc comment for the honest, documented
// best-effort limits of this adapter's sub-task handling.
func (a *Adapter) dispatchSubtaskStart(ts *turnState, p subtaskPart) {
	if ts.subtaskAlreadyStarted(p.ID) {
		return
	}
	ts.markSubtaskStarted(p.ID)
	ts.emit(translateSubTaskStart(ts.cmd, p))
}
