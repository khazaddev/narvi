package sessionactor

import (
	"encoding/json"
	"testing"
)

// broadcastCall records one Hub.Broadcast-shaped call fakeBroadcaster
// received.
type broadcastCall struct {
	sessionID string
	payload   json.RawMessage
}

// fakeBroadcaster is a minimal ports.EventBroadcaster test double
// recording every Broadcast call it receives, in order -- used by both
// this file's own pure-unit tests and broadcast_integration_test.go's
// real-Postgres-backed ones (both compile together under the
// "integration" build tag, matching this package's own established
// unit/integration split precedent -- see sandboxevent_test.go vs
// sandboxevent_integration_test.go).
type fakeBroadcaster struct {
	calls []broadcastCall
}

func (f *fakeBroadcaster) Broadcast(sessionID string, payload json.RawMessage) {
	f.calls = append(f.calls, broadcastCall{sessionID: sessionID, payload: payload})
}

// TestActor_BroadcastPending proves broadcastPending's own contract in
// isolation -- no Postgres needed, since broadcastPending itself does no
// I/O: every queued payload is delivered to the broadcaster exactly once,
// in order, with the exact bytes queued, and the queue is reset to nil
// afterward regardless; a nil broadcaster is a safe no-op that still
// resets the queue and never panics; an empty queue calls the broadcaster
// zero times.
func TestActor_BroadcastPending(t *testing.T) {
	t.Parallel()

	sessionID := uuidFromByte(7)

	t.Run("delivers every queued payload in order then resets the queue", func(t *testing.T) {
		t.Parallel()

		fb := &fakeBroadcaster{}
		a := &Actor{
			sessionID:   sessionID,
			broadcaster: fb,
			pendingBroadcast: []json.RawMessage{
				json.RawMessage(`{"type":"a"}`),
				json.RawMessage(`{"type":"b"}`),
			},
		}

		a.broadcastPending()

		if len(fb.calls) != 2 {
			t.Fatalf("Broadcast called %d times, want 2", len(fb.calls))
		}
		if string(fb.calls[0].payload) != `{"type":"a"}` {
			t.Errorf("first Broadcast payload = %s, want {\"type\":\"a\"}", fb.calls[0].payload)
		}
		if string(fb.calls[1].payload) != `{"type":"b"}` {
			t.Errorf("second Broadcast payload = %s, want {\"type\":\"b\"}", fb.calls[1].payload)
		}
		for i, c := range fb.calls {
			if c.sessionID != sessionID.String() {
				t.Errorf("Broadcast call %d sessionID = %q, want %q", i, c.sessionID, sessionID.String())
			}
		}
		if a.pendingBroadcast != nil {
			t.Errorf("pendingBroadcast = %v, want nil after broadcastPending", a.pendingBroadcast)
		}
	})

	t.Run("nil broadcaster never panics and still resets the queue", func(t *testing.T) {
		t.Parallel()

		a := &Actor{
			sessionID:        sessionID,
			broadcaster:      nil,
			pendingBroadcast: []json.RawMessage{json.RawMessage(`{"type":"a"}`)},
		}

		a.broadcastPending() // must not panic

		if a.pendingBroadcast != nil {
			t.Errorf("pendingBroadcast = %v, want nil after broadcastPending even with a nil broadcaster", a.pendingBroadcast)
		}
	})

	t.Run("empty queue is a no-op", func(t *testing.T) {
		t.Parallel()

		fb := &fakeBroadcaster{}
		a := &Actor{sessionID: sessionID, broadcaster: fb}

		a.broadcastPending()

		if len(fb.calls) != 0 {
			t.Errorf("Broadcast called %d times, want 0", len(fb.calls))
		}
	})
}
