package ports

import (
	"encoding/json"
	"errors"
)

// ErrNoLiveSandboxConnection is returned by SandboxCommander.SendCommand
// when no live sandbox WS connection is currently registered for the given
// session id -- a sentinel a caller can errors.Is against, mirroring
// pgx.ErrNoRows-style sentinels already used throughout this codebase.
// Design decision 3b (§9.3, "e2e happy path"): a caller that gets this
// back MUST treat it as "the prompt genuinely never reached a live
// connection". As of this Step's own review-driven fix
// (internal/app/sessionactor/dispatch.go's own top comment has the full
// story), the caller does NOT roll back a turn-dispatch transaction on
// this error -- SendCommand is deliberately called OUTSIDE any Postgres
// transaction now (a real WS frame write must never run while a
// transact's own FOR UPDATE lock on the session row is held), by which
// point the turn has already been committed Processing. This error (and
// every other SendCommand failure) is instead handled by failing the
// turn forward, Processing->Failed, with an honest reason -- never by
// reverting it to Pending (domain/turn's transition table has no reverse
// edge for that).
var ErrNoLiveSandboxConnection = errors.New("ports: no live sandbox connection for this session")

// SandboxCommander is the port app/sessionactor uses to push an outbound
// command frame (prompt/stop/push/snapshot/shutdown/git_sync_complete,
// §6.1) to a session's live sandbox WS connection, without knowing
// anything about WebSockets, wshub, or any other adapter-layer concept --
// mirrors EventBroadcaster's own doc-comment rigor and "implemented by the
// inbound wshub adapter, app depends on the port, never the adapter"
// direction exactly (see eventbroadcaster.go).
//
// internal/adapters/inbound/wshub's own SandboxRegistry (Step 21) is the
// only implementation today: a session has AT MOST ONE live sandbox
// connection at a time (unlike the client-hub's own potentially-many-
// browser-tabs case EventBroadcaster.Broadcast fans out to), so this is a
// single-recipient send, not a broadcast.
type SandboxCommander interface {
	// SendCommand delivers payload (an already-marshaled wire command
	// frame, e.g. a sandboxws.Prompt) to sessionID's live sandbox
	// connection, if one is currently registered. Returns
	// ErrNoLiveSandboxConnection if none is; any other error is a genuine
	// send failure (e.g. the write itself failed or timed out). Must
	// return promptly -- bounded by the implementation's own configured
	// send timeout (platform.Timeouts.SandboxCommandSendTimeout) -- never
	// block the caller indefinitely.
	SendCommand(sessionID string, payload json.RawMessage) error
}
