package wshub

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// wsConnPair is one real WS connection, both ends: server (what
// SandboxRegistry.Register/SendCommand operate on) and client (what the
// test reads from) -- a real network round trip is needed here (unlike
// Hub's own tests, which use a bare &websocket.Conn{} as a map key)
// because SandboxRegistry.SendCommand actually calls conn.Write, which
// panics on a zero-value Conn.
type wsConnPair struct {
	server  *websocket.Conn
	client  *websocket.Conn
	cleanup func()
}

func newWSConnPair(t *testing.T) wsConnPair {
	t.Helper()

	serverConnCh := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		serverConnCh <- conn
		// Keep the handler alive until the test's own cleanup closes the
		// client connection, so conn.Write calls from SendCommand/writeAck
		// have a live connection to write to for the test's duration.
		for {
			if _, _, err := conn.Read(r.Context()); err != nil {
				return
			}
		}
	}))

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	clientConn, _, err := websocket.Dial(context.Background(), wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	var serverConn *websocket.Conn
	select {
	case serverConn = <-serverConnCh:
	case <-time.After(5 * time.Second):
		t.Fatal("server never accepted the connection")
	}

	return wsConnPair{
		server: serverConn,
		client: clientConn,
		cleanup: func() {
			_ = clientConn.CloseNow()
			server.Close()
		},
	}
}

// TestSandboxRegistry_SendCommand_RegisteredConnectionReaches proves
// Register followed by SendCommand actually reaches the real connection.
func TestSandboxRegistry_SendCommand_RegisteredConnectionReaches(t *testing.T) {
	t.Parallel()

	reg := NewSandboxRegistry(platform.DefaultTimeouts())
	pair := newWSConnPair(t)
	defer pair.cleanup()

	unregister := reg.Register("session-a", pair.server)
	defer unregister()

	payload := json.RawMessage(`{"type":"prompt","messageId":"m1"}`)
	if err := reg.SendCommand("session-a", payload); err != nil {
		t.Fatalf("SendCommand() error = %v, want nil", err)
	}

	_, data, err := pair.client.Read(context.Background())
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(data) != string(payload) {
		t.Errorf("client received %s, want %s", data, payload)
	}
}

// TestSandboxRegistry_SendCommand_Unregistered proves SendCommand for a
// session with no registered connection returns
// ports.ErrNoLiveSandboxConnection, never blocking or panicking.
func TestSandboxRegistry_SendCommand_Unregistered(t *testing.T) {
	t.Parallel()

	reg := NewSandboxRegistry(platform.DefaultTimeouts())

	err := reg.SendCommand("no-such-session", json.RawMessage(`{}`))
	if err != ports.ErrNoLiveSandboxConnection {
		t.Fatalf("SendCommand() error = %v, want ports.ErrNoLiveSandboxConnection", err)
	}
}

// TestSandboxRegistry_Unregister_DoesNotEvictNewerRegistration proves a
// stale connection's own deferred unregister call never clobbers a NEWER
// registration that has since replaced it for the same session id.
func TestSandboxRegistry_Unregister_DoesNotEvictNewerRegistration(t *testing.T) {
	t.Parallel()

	reg := NewSandboxRegistry(platform.DefaultTimeouts())

	oldPair := newWSConnPair(t)
	defer oldPair.cleanup()
	oldUnregister := reg.Register("session-b", oldPair.server)

	newPair := newWSConnPair(t)
	defer newPair.cleanup()
	newUnregister := reg.Register("session-b", newPair.server)
	defer newUnregister()

	// The OLD connection's own unregister call must not evict the NEW one.
	oldUnregister()

	payload := json.RawMessage(`{"type":"prompt","messageId":"m2"}`)
	if err := reg.SendCommand("session-b", payload); err != nil {
		t.Fatalf("SendCommand() error = %v, want nil (newer registration must still be reachable)", err)
	}

	_, data, err := newPair.client.Read(context.Background())
	if err != nil {
		t.Fatalf("new client read: %v", err)
	}
	if string(data) != string(payload) {
		t.Errorf("new client received %s, want %s", data, payload)
	}
}

// TestSandboxRegistry_ConcurrentSendCommandAndAck_NoFrameCorruption
// constructs a concurrent SendCommand write and a concurrent writeAck call
// (dispatch.go's own ack-writing helper) to the SAME connection, proving
// they do not corrupt the frame stream -- coder/websocket.Conn's own docs
// state "All methods may be called concurrently except for Reader and
// Read" (verified directly via `go doc github.com/coder/websocket.Conn`
// during this Step's own design phase); this test constructs the claim
// directly rather than merely citing it.
func TestSandboxRegistry_ConcurrentSendCommandAndAck_NoFrameCorruption(t *testing.T) {
	t.Parallel()

	reg := NewSandboxRegistry(platform.DefaultTimeouts())
	pair := newWSConnPair(t)
	defer pair.cleanup()

	unregister := reg.Register("session-c", pair.server)
	defer unregister()

	const iterations = 200

	received := make(chan []byte, iterations*2)
	var eg errgroup.Group
	eg.Go(func() error {
		for i := 0; i < iterations*2; i++ {
			_, data, err := pair.client.Read(context.Background())
			if err != nil {
				return err
			}
			// Copy: coder/websocket reuses its own internal read buffer
			// across calls.
			cp := make([]byte, len(data))
			copy(cp, data)
			received <- cp
		}
		return nil
	})

	var writers errgroup.Group
	writers.Go(func() error {
		for i := 0; i < iterations; i++ {
			_ = reg.SendCommand("session-c", json.RawMessage(`{"type":"prompt","messageId":"send"}`))
		}
		return nil
	})
	writers.Go(func() error {
		for i := 0; i < iterations; i++ {
			_ = writeAck(context.Background(), pair.server, "session-c", 1, "execution_complete:m1")
		}
		return nil
	})
	_ = writers.Wait()

	// The reader goroutine (eg) reads exactly iterations*2 messages total,
	// matching what the two writers above produce -- it finishes (and
	// stops sending on `received`) once the last one arrives. Waiting for
	// it BEFORE closing the channel avoids a send-on-closed-channel panic;
	// closing first would race the reader's own still-in-flight send.
	if err := eg.Wait(); err != nil {
		t.Fatalf("reader: %v", err)
	}
	close(received)

	gotPrompt, gotAck := 0, 0
	for data := range received {
		var peek struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(data, &peek); err != nil {
			t.Fatalf("corrupted frame (not valid JSON): %s", data)
		}
		switch peek.Type {
		case "prompt":
			gotPrompt++
		case "ack":
			gotAck++
		default:
			t.Fatalf("unexpected frame type %q (%s)", peek.Type, data)
		}
	}
	if gotPrompt != iterations {
		t.Errorf("received %d prompt frames, want %d", gotPrompt, iterations)
	}
	if gotAck != iterations {
		t.Errorf("received %d ack frames, want %d", gotAck, iterations)
	}
}
