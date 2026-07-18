package opencode

import "encoding/json"

// This file holds this adapter's own understanding of OpenCode's real
// HTTP+SSE wire shapes — EMPIRICALLY VERIFIED against the real, installed
// OpenCode 1.17.15 binary during this Step's own research pass (a real
// `opencode serve` process was started; real scripted turns — a plain-text
// reply, a bash-tool-invoking prompt, and an aborted turn — were run
// against it, and their real request/response/SSE bodies captured), unless
// a field's own comment below says otherwise (schema-derived only, not
// independently observed live). See package doc.go for the full
// verified-vs-schema-derived-vs-best-effort breakdown. No OpenCode client
// library is vendored here — this adapter is a pure HTTP+SSE client,
// mirroring internal/adapters/outbound/modal's own shape.

// --- sessionResponse: POST /session's response body ------------------------

// sessionResponse is POST /session's response body. VERIFIED live:
// {"id":"ses_...","slug":"...","projectID":"...","directory":"...",
// "cost":0,"tokens":{...},"title":"...","version":"1.17.15","time":{...}}.
// Only the two fields this adapter actually uses are modeled.
type sessionResponse struct {
	ID string `json:"id"`
}

// --- prompt_async request body ---------------------------------------------

// promptAsyncRequest is POST /session/{id}/prompt_async's request body.
// VERIFIED live via /doc (OpenAPI): {"messageID"?, "model"?:
// {"providerID","modelID"}, "agent"?, "parts": [...]}. Only the fields this
// adapter sends are modeled; "agent"/"noReply"/"tools"/"format"/"system"/
// "variant" are real but unused by this Step's own scope.
type promptAsyncRequest struct {
	Model *promptModelRef   `json:"model,omitempty"`
	Parts []promptPartInput `json:"parts"`
}

// promptModelRef is prompt_async's own "model" object shape: {"providerID",
// "modelID"} — VERIFIED live via /doc, both fields required together.
type promptModelRef struct {
	ProviderID string `json:"providerID"`
	ModelID    string `json:"modelID"`
}

// promptPartInput is one element of prompt_async's "parts" array. This
// adapter only ever sends a single {"type":"text","text":"..."} part
// (VERIFIED live: TextPartInput's own schema requires exactly "type" and
// "text") — OpenCode's own schema also allows file/agent/subtask part
// inputs, unused by this Step's own scope (a single free-text prompt per
// turn).
type promptPartInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// --- model catalog (GET /api/model) -----------------------------------------

// modelCatalogResponse is GET /api/model's response body — VERIFIED live:
// {"location":{...},"data":[{"id","providerID",...}, ...]}. This adapter
// only uses Data's own length (§7's "on empty/failed catalog, fall back"
// quirk is a liveness check, not a per-model lookup — see resolveModel's
// own doc comment), so individual entries are not modeled further.
type modelCatalogResponse struct {
	Data []json.RawMessage `json:"data"`
}

// --- the persistent global SSE stream (GET /event) --------------------------

// sseEnvelope is the outer shape of every line on the global GET /event
// SSE stream — VERIFIED live: every line is `data: <json>\n\n`, decoding to
// {"id":"evt_...","type":"<dotted.type>","properties":{...}}.
type sseEnvelope struct {
	Type       string          `json:"type"`
	Properties json.RawMessage `json:"properties"`
}

// messageUpdatedProps is "message.updated"'s own properties shape —
// VERIFIED live: {"sessionID":"ses_...","info":{...}}.
type messageUpdatedProps struct {
	SessionID string              `json:"sessionID"`
	Info      openCodeMessageInfo `json:"info"`
}

// openCodeMessageInfo is the subset of OpenCode's own AssistantMessage/
// UserMessage union this adapter reads — ID and Role are VERIFIED live on
// every message.updated event (both roles, "user" and "assistant"). Error
// is schema-derived (present in the real /doc OpenAPI schema's own
// AssistantMessage.error field) but NOT populated in either successful
// scripted turn run live during this Step's own research pass — it WAS
// observed live, however, on the third scripted turn (an aborted turn),
// confirming the shape: {"error":{"name":"MessageAbortedError",
// "data":{"message":"Aborted"}}}.
type openCodeMessageInfo struct {
	ID    string               `json:"id"`
	Role  string               `json:"role"`
	Error *openCodeTaggedError `json:"error"`
}

// openCodeTaggedError is the tagged-union shape OpenCode uses for both
// session.error's own "error" property and an assistant message's own
// "error" field — VERIFIED live for name="MessageAbortedError" (an aborted
// turn); the other 7 tagged-union member names (ProviderAuthError,
// UnknownError, MessageOutputLengthError, StructuredOutputError,
// ContextOverflowError, ContentFilterError, APIError) are schema-derived
// only (confirmed present in /doc, not independently elicited live — none
// of this Step's own scripted turns produced one).
type openCodeTaggedError struct {
	Name string `json:"name"`
}

// messagePartUpdatedProps is "message.part.updated"'s own properties shape
// — VERIFIED live: {"sessionID":"ses_...","part":{...},"time":...}. Part is
// decoded further by dispatchPart, keyed on its own "type" discriminator
// (see partEnvelope) since Go has no generated union type for it (the same
// "no discriminated-union wrapper" situation
// internal/sandboxagent/wsbridge's own doc.go documents for inbound
// commands).
type messagePartUpdatedProps struct {
	SessionID string          `json:"sessionID"`
	Part      json.RawMessage `json:"part"`
}

// partEnvelope is the common shape every OpenCode message-part type
// shares, peeked first to decide which concrete type to decode the same
// raw bytes into — VERIFIED live for text/step-start/step-finish/tool;
// schema-derived only for subtask (see subtaskPart's own comment).
type partEnvelope struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	Type      string `json:"type"`
}

// compactionPart is a `"type":"compaction"` part — SCHEMA-DERIVED only
// (confirmed present in the real /doc OpenAPI schema's CompactionPart def;
// not independently elicited live during this Step's own research pass —
// manually triggering one via POST /session/{id}/compact returned
// "Session compact is not available yet" on this OpenCode version, and
// organically eliciting one needs a context window large enough to force
// it). Auto distinguishes an automatic (context-window-driven) compaction
// from a manually-requested one; Overflow specifically signals the
// context window genuinely ran out of room mid-turn — an operationally
// meaningful degradation worth surfacing, see dispatchPart's "compaction"
// case (sse.go) and §7's own "handle compaction events" quirk.
type compactionPart struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	Auto      bool   `json:"auto"`
	Overflow  bool   `json:"overflow"`
}

// textPart is a `"type":"text"` part — VERIFIED live: Text is the FULL
// CUMULATIVE text so far for this part id (a later update replaces an
// earlier, shorter/empty one), matching wire Token's own cumulative
// contract exactly.
type textPart struct {
	ID        string `json:"id"`
	MessageID string `json:"messageID"`
	Text      string `json:"text"`
}

// stepFinishPart is a `"type":"step-finish"` part — VERIFIED live:
// {"id","reason","messageID","type":"step-finish","tokens":{"total",
// "input","output","reasoning","cache":{"read","write"}},"cost"}.
type stepFinishPart struct {
	ID        string           `json:"id"`
	MessageID string           `json:"messageID"`
	Tokens    stepFinishTokens `json:"tokens"`
	Cost      float64          `json:"cost"`
}

type stepFinishTokens struct {
	Input  float64         `json:"input"`
	Output float64         `json:"output"`
	Cache  stepFinishCache `json:"cache"`
}

type stepFinishCache struct {
	Read float64 `json:"read"`
}

// toolPart is a `"type":"tool"` part — VERIFIED live across the full
// pending -> running (xN) -> completed state progression, and separately
// for an aborted call (still reaches "completed", never "error", in the
// one abort scripted live — "error" itself is schema-derived only, not
// independently observed live).
type toolPart struct {
	ID        string        `json:"id"`
	MessageID string        `json:"messageID"`
	Tool      string        `json:"tool"`
	CallID    string        `json:"callID"`
	State     toolPartState `json:"state"`
}

// toolPartState is toolPart's own "state" object — VERIFIED live: Status
// is always present; Input is always an object (possibly empty, `{}`, in
// the "pending" state); Output is a STRING (not an object!) present only
// once Status is "completed" — schema-confirmed AND live-verified;
// Error is a STRING present only once Status is "error" — schema-derived
// only (ToolStateError's own real /doc shape), not independently observed
// live (no scripted turn produced a genuine tool error).
type toolPartState struct {
	Status string          `json:"status"`
	Input  json.RawMessage `json:"input"`
	Output *string         `json:"output"`
	Error  *string         `json:"error"`
}

// subtaskPart is a `"type":"subtask"` part — SCHEMA-DERIVED ONLY (confirmed
// present in the real, live /doc OpenAPI schema's own SubtaskPart
// definition), NOT independently observed live: eliciting one requires the
// model to actually invoke OpenCode's own "task" tool, which this Step's
// own research pass did not exercise (non-deterministic, model-dependent).
// See turn.go's own dispatchSubtaskStart and adapter.go's own package doc
// comment for how this adapter's own sub-task handling is, in turn,
// clearly-documented best-effort on TOP of this already-uncertain layer.
type subtaskPart struct {
	ID          string `json:"id"`
	MessageID   string `json:"messageID"`
	Description string `json:"description"`
}

// sessionIdleProps is "session.idle"'s own properties shape — VERIFIED
// live: {"sessionID":"ses_..."}. THE signal a turn (or, best-effort, a
// sub-task) has concluded.
type sessionIdleProps struct {
	SessionID string `json:"sessionID"`
}

// sessionErrorProps is "session.error"'s own properties shape — VERIFIED
// live (the aborted-turn scripted run): {"sessionID":"ses_...","error":
// {"name":"MessageAbortedError","data":{"message":"Aborted"}}}.
type sessionErrorProps struct {
	SessionID string              `json:"sessionID"`
	Error     openCodeTaggedError `json:"error"`
}

// --- final-state fetch fallback (GET /session/{id}/message) ----------------

// messageListEntry is one element of GET /session/{id}/message's response
// array — VERIFIED live: [{"info":{...},"parts":[...]}, ...]. Used only by
// the SSE-inactivity final-state fallback (§7).
type messageListEntry struct {
	Info  openCodeMessageInfo `json:"info"`
	Parts []json.RawMessage   `json:"parts"`
}
