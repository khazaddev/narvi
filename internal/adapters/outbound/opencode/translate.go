package opencode

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
)

// This file holds pure translation functions: one OpenCode part/message/
// outcome shape (types.go) in, one wire sandboxws.* event out. Every
// function stamps SessionId/Gen from cmd (Narvi's OWN session id and spawn
// generation, sandboxws.Prompt's own fields — NOT to be confused with
// OpenCode's own "ses_..." session id, which only ever appears as the wire
// conversationId, never as a wire event's sessionId/gen).
//
// messageId convention (§7's own "correlate by generated
// lexicographically-ascending message IDs" quirk): translateToken is the
// ONE exception that uses the part's own id ("prt_...") as the wire
// messageId — every other part-derived event (step_start, step_finish,
// tool_call, tool_result, and sub_task_start's own parentMessageId) uses
// the ENCLOSING OpenCode message's id ("msg_...", p.MessageID) instead,
// with the part's own id carried separately in the field that is each
// event's real per-instance correlator (StepId for step_start/step_finish,
// CallId for tool_call/tool_result). This split is deliberate: messageId
// answers "which conversational message did this happen under" (useful
// for grouping/ordering across an entire turn), while Step/CallId answers
// "which specific instance of a step/tool this is" (needed for the dedup
// logic in sse.go's dispatchTool) — collapsing both onto the same id would
// lose one of those two distinct pieces of information. Adapter-
// SYNTHESIZED events with no single originating OpenCode id
// (execution_complete, sub_task_start, sub_task_finish) mint a fresh
// uuid instead, via newEventID — mirroring
// internal/sandboxagent/wsbridge.Bridge's own newMessageID precedent for
// bridge-originated events.

func newEventID() string { return uuid.NewString() }

func translateToken(cmd sandboxws.Prompt, p textPart) sandboxws.Token {
	return sandboxws.Token{
		Type:      "token",
		MessageId: p.ID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		Text:      p.Text,
	}
}

func translateStepStart(cmd sandboxws.Prompt, p partEnvelope) sandboxws.StepStart {
	return sandboxws.StepStart{
		Type:      "step_start",
		MessageId: p.MessageID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		StepId:    p.ID,
	}
}

// translateStepFinish maps OpenCode's own step-finish token breakdown onto
// wire StepFinish.Cost — THE schema §6.1/§9.1 dedicate their own explicit
// warning to: cost.tokens MUST be an object, never a bare number.
// StepFinishCostTokens.Cached is populated from tokens.cache.read
// (per this Step's own instructions: "cost.tokens = {input, output,
// cached: tokens.cache.read}").
func translateStepFinish(cmd sandboxws.Prompt, p stepFinishPart) sandboxws.StepFinish {
	cached := int(p.Tokens.Cache.Read)
	usd := p.Cost
	return sandboxws.StepFinish{
		Type:      "step_finish",
		MessageId: p.MessageID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		StepId:    p.ID,
		Cost: sandboxws.StepFinishCost{
			Tokens: sandboxws.StepFinishCostTokens{
				Input:  int(p.Tokens.Input),
				Output: int(p.Tokens.Output),
				Cached: &cached,
			},
			Usd: &usd,
		},
	}
}

// decodeToolInput unmarshals a tool part's own "input" object (always
// present, possibly "{}") into wire ToolCall's own freeform map shape.
func decodeToolInput(raw json.RawMessage) (sandboxws.ToolCallInput, error) {
	m, err := decodeToolObject(raw)
	if err != nil {
		return nil, err
	}
	return sandboxws.ToolCallInput(m), nil
}

func decodeToolObject(raw json.RawMessage) (map[string]interface{}, error) {
	if len(raw) == 0 {
		return map[string]interface{}{}, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if m == nil {
		m = map[string]interface{}{}
	}
	return m, nil
}

// toolInputEmpty reports whether a tool part's own "input" is the
// empty-object placeholder OpenCode sends while a call is still "pending"
// (§7's own "skip the empty-input pending state -- it has nothing real to
// report yet" instruction). Malformed input is treated as empty (nothing
// real to report yet either) rather than erroring here — translateToolCall
// surfaces a genuine decode error separately, for the case a NON-empty but
// malformed input sneaks through.
func toolInputEmpty(raw json.RawMessage) bool {
	m, err := decodeToolObject(raw)
	if err != nil {
		return true
	}
	return len(m) == 0
}

func translateToolCall(cmd sandboxws.Prompt, p toolPart) (sandboxws.ToolCall, error) {
	input, err := decodeToolInput(p.State.Input)
	if err != nil {
		return sandboxws.ToolCall{}, err
	}
	return sandboxws.ToolCall{
		Type:      "tool_call",
		MessageId: p.MessageID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		CallId:    p.CallID,
		ToolName:  p.Tool,
		Input:     input,
	}, nil
}

// translateToolResult wraps OpenCode's own STRING-shaped completed/error
// output (VERIFIED live: ToolStateCompleted.output and ToolStateError.error
// are both plain strings, never objects) into wire ToolResult.Output's own
// object shape ({"output": "..."} or {"error": "..."} respectively) — this
// adapter's own, documented convention for satisfying the wire contract's
// "freeform, tool-specific output" object requirement from a tool state
// that is itself just a string.
func translateToolResult(cmd sandboxws.Prompt, p toolPart) sandboxws.ToolResult {
	isError := p.State.Status == "error"

	output := sandboxws.ToolResultOutput{}
	switch {
	case isError && p.State.Error != nil:
		output["error"] = *p.State.Error
	case p.State.Output != nil:
		output["output"] = *p.State.Output
	}

	return sandboxws.ToolResult{
		Type:      "tool_result",
		MessageId: p.MessageID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		CallId:    p.CallID,
		Output:    output,
		IsError:   isError,
	}
}

// translateCompactionOverflow maps an "overflow" compaction (§7's own
// "handle compaction events" quirk — the context window genuinely ran out
// of room and had to be force-compacted mid-turn) onto a wire warning
// event -- there is no dedicated wire event for compaction itself, and
// warning is the closest existing slot for an operationally meaningful,
// non-fatal degradation signal. A non-overflow (ordinary/auto) compaction
// is NOT translated at all — see dispatchPart's "compaction" case
// (sse.go) for that deliberate asymmetry.
func translateCompactionOverflow(cmd sandboxws.Prompt, p compactionPart) sandboxws.Warning {
	return sandboxws.Warning{
		Type:      "warning",
		MessageId: p.MessageID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		Message:   "opencode: context window overflow forced a mid-turn compaction",
	}
}

func translateSubTaskStart(cmd sandboxws.Prompt, p subtaskPart) sandboxws.SubTaskStart {
	return sandboxws.SubTaskStart{
		Type:            "sub_task_start",
		MessageId:       newEventID(),
		SessionId:       cmd.SessionId,
		Gen:             cmd.Gen,
		SubTaskId:       p.ID,
		Label:           p.Description,
		ParentMessageId: p.MessageID,
	}
}

func translateSubTaskFinish(cmd sandboxws.Prompt, subTaskID string, outcome sandboxws.ExecutionCompleteOutcome) sandboxws.SubTaskFinish {
	messageID := newEventID()
	return sandboxws.SubTaskFinish{
		Type:      "sub_task_finish",
		MessageId: messageID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		AckId:     "sub_task_finish:" + messageID,
		SubTaskId: subTaskID,
		Outcome:   subTaskOutcome(outcome),
	}
}

func translateExecutionComplete(cmd sandboxws.Prompt, out turnOutcome) sandboxws.ExecutionComplete {
	messageID := newEventID()
	return sandboxws.ExecutionComplete{
		Type:      "execution_complete",
		MessageId: messageID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		AckId:     "execution_complete:" + messageID,
		Outcome:   out.Outcome,
		Reason:    sandboxws.ExecutionCompleteReason(out.Reason),
	}
}
