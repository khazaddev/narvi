package ports

import "encoding/json"

// EventBroadcaster is the port that makes §6.2's "→ broadcast stream"
// real: whenever internal/app/sessionactor durably commits an event to a
// session's append-only event log (via Actor's own appendEvent/
// appendRawEvent, from any current or future command handler), it hands
// that event's raw payload to Broadcast so every currently-subscribed
// browser client for that session receives it live, unsolicited, over its
// own WS connection — sent exactly as stored (§6.2: a browser client
// receiving an unsolicited frame IS the broadcast of one raw stored
// event; there is no separate wrapper envelope for it).
//
// This interface exists specifically so app/sessionactor (which may never
// import internal/adapters/*, per hexagonal architecture — see CLAUDE.md's
// "don't couple a port to a single adapter" and internal/app/ports/doc.go's
// own import-direction rule) can trigger a browser-facing side effect
// without knowing anything about WebSockets, wshub, or any other
// adapter-layer concept. internal/adapters/inbound/wshub's own *Hub type
// (§6.2) is the only implementation today, but nothing about this
// signature is wshub-specific: a future adapter (e.g. a message-bus
// fan-out for cross-pod delivery) could implement it identically.
//
// Broadcast is a best-effort, fire-and-forget signal, not a durable
// delivery guarantee: a session with no currently-subscribed connections,
// or a slow connection whose own buffer is full, simply does not receive
// this particular live push — the event itself is already durably
// persisted (that already happened, in the same transaction, before
// Broadcast is ever called; see Actor.transact's own commit-then-broadcast
// ordering), and any client can always recover a missed live push via
// fetch_history's cursor-paginated replay (§6.2). Implementations MUST
// NOT block the caller (the Actor's own single command-processing
// goroutine) waiting on a slow consumer.
type EventBroadcaster interface {
	// Broadcast delivers payload (the exact raw bytes already persisted as
	// one events-table row) to every connection currently subscribed to
	// sessionID, if any. Implementations must return promptly regardless
	// of how many (if any) subscribers exist or how slow any one of them
	// is to drain.
	Broadcast(sessionID string, payload json.RawMessage)
}
