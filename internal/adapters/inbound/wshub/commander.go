package wshub

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/coder/websocket"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// SandboxRegistry is the in-process, session-keyed registry of live
// sandbox WS connections that implements internal/app/ports.
// SandboxCommander (Step 21, "e2e happy path", design decision 4) -- the
// outbound half of what sandbox.go's own doc comment named as
// "actually dispatching outbound sandbox commands" as this Step's job.
//
// Scoped to single-recipient send, not fan-out broadcast, unlike client.go's
// own *Hub: a session has AT MOST ONE live sandbox connection at a time
// (sandboxes.session_id is UNIQUE, §3.2), so there is never more than one
// connection to look up for a given session id.
type SandboxRegistry struct {
	mu      sync.Mutex
	conns   map[string]*websocket.Conn
	timeout platform.Timeouts
}

// NewSandboxRegistry builds an empty SandboxRegistry. timeouts supplies
// platform.Timeouts.SandboxCommandSendTimeout, bounding each individual
// SendCommand's own conn.Write call.
func NewSandboxRegistry(timeouts platform.Timeouts) *SandboxRegistry {
	return &SandboxRegistry{
		conns:   make(map[string]*websocket.Conn),
		timeout: timeouts,
	}
}

// var _ ports.SandboxCommander = (*SandboxRegistry)(nil) makes a
// SandboxCommander signature drift a build error, not a runtime surprise.
var _ ports.SandboxCommander = (*SandboxRegistry)(nil)

// Register records conn as sessionID's current live sandbox connection,
// returning an unregister func the caller must invoke exactly once (via
// defer) when that connection's own read loop exits -- mirrors client.go's
// own Hub.Register/unregister-via-defer convention exactly. Called from
// NewSandboxHandler's own handshake success path, right before entering
// readLoop.
//
// A second Register for the same sessionID (e.g. a stale connection whose
// read loop hasn't yet noticed it should stop, racing a fresh reconnect)
// simply overwrites the map entry -- the newer connection is always what
// SendCommand reaches from that point on. The OLD connection's own
// eventual unregister call is guarded (see unregister's own doc comment)
// so it can never clobber a newer registration that has since replaced
// it.
func (s *SandboxRegistry) Register(sessionID string, conn *websocket.Conn) (unregister func()) {
	s.mu.Lock()
	s.conns[sessionID] = conn
	s.mu.Unlock()

	return func() {
		s.mu.Lock()
		// Only delete if this exact connection is still the one on file --
		// a stale connection's own deferred unregister must never evict a
		// NEWER connection that has since replaced it in the map.
		if cur, ok := s.conns[sessionID]; ok && cur == conn {
			delete(s.conns, sessionID)
		}
		s.mu.Unlock()
	}
}

// SendCommand implements ports.SandboxCommander: looks up sessionID's
// current live connection and writes payload to it directly (no per-
// connection buffered channel/dedicated writer goroutine -- sandbox
// commands are rare and single-recipient, and coder/websocket.Conn's own
// docs state "All methods may be called concurrently except for Reader
// and Read", so this concurrent Write and dispatch.go's own writeAck call
// -- running on the connection's own read-loop goroutine -- are
// documented-safe with no extra synchronization needed here).
//
// Uses context.Background() bounded by a fresh context.WithTimeout(s.
// timeout.SandboxCommandSendTimeout) rather than accepting a caller-
// supplied context: the caller (app/sessionactor's own transact closure)
// has no natural per-write context of its own to hand through cleanly
// across the port boundary (SandboxCommander's own signature takes no
// context, matching EventBroadcaster.Broadcast's identical precedent).
func (s *SandboxRegistry) SendCommand(sessionID string, payload json.RawMessage) error {
	s.mu.Lock()
	conn, ok := s.conns[sessionID]
	s.mu.Unlock()

	if !ok {
		return ports.ErrNoLiveSandboxConnection
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.timeout.SandboxCommandSendTimeout)
	defer cancel()

	return conn.Write(ctx, websocket.MessageText, payload)
}
