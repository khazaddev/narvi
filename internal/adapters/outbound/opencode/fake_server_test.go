package opencode

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// fakeOpenCodeServer is a minimal, deliberately controllable httptest.Server
// stand-in for a real `opencode serve` process. Unlike this package's own
// established real-binary testing style (helpers_test.go's startServer/
// newAdapter, used by every OTHER test file here for genuine wire-shape
// fidelity against the actual OpenCode 1.17.15 binary), the liveness/
// timeout/cancellation scenarios below need to deterministically construct
// conditions a real, live OpenCode process (and a real, non-deterministic
// AI provider behind it) cannot be coerced into on demand: a connection
// drop mid-turn followed by a reconnect within an exact window, a stream
// that stays alive via heartbeats while one specific turn never gets
// session.idle, a connection that drops and never comes back, a
// deliberately slow HTTP handler. httptest.Server-backed fakes are this
// codebase's own established style for exactly this need — mirroring
// internal/adapters/outbound/modal's own client_test.go/provider_test.go
// precedent (a different outbound-adapter package, same convention: a real
// net/http/httptest.Server, table-driven where it fits, doc-commented
// reasoning alongside each fake behavior).
type fakeOpenCodeServer struct {
	srv *httptest.Server

	mu          sync.Mutex
	current     *fakeSSEConn
	connSeq     int
	rejectEvent bool
	messages    []messageListEntry

	// connected fires once per newly-accepted GET /event connection,
	// carrying that connection's own 1-based sequence number — tests
	// block on this to know precisely when a (re)connect has actually
	// landed, instead of guessing via a sleep.
	connected chan int
}

// fakeSSEConn is one live GET /event connection's own outbound queue and
// kill switch.
type fakeSSEConn struct {
	send  chan string
	close chan struct{}
}

// newFakeOpenCodeServer starts the fake server, registering its own
// teardown via t.Cleanup.
func newFakeOpenCodeServer(t *testing.T) *fakeOpenCodeServer {
	t.Helper()

	f := &fakeOpenCodeServer{connected: make(chan int, 64)}

	mux := http.NewServeMux()
	mux.HandleFunc("/event", f.handleEvent)
	mux.HandleFunc("/session", f.handleCreateSession)
	mux.HandleFunc("/session/", f.handleSessionSubroutes)

	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOpenCodeServer) URL() string { return f.srv.URL }

// handleEvent serves GET /event: an SSE connection whose lines are driven
// entirely by broadcast/dropConnection below, or (once
// rejectAllFutureEventConnections has been called) an immediate error
// response simulating a connection that can never be reestablished.
func (f *fakeOpenCodeServer) handleEvent(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	reject := f.rejectEvent
	f.mu.Unlock()
	if reject {
		http.Error(w, "simulated permanent outage", http.StatusServiceUnavailable)
		return
	}

	flusher, _ := w.(http.Flusher)
	conn := &fakeSSEConn{send: make(chan string, 64), close: make(chan struct{})}

	f.mu.Lock()
	f.connSeq++
	seq := f.connSeq
	f.current = conn
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}

	select {
	case f.connected <- seq:
	default:
	}

	// Mirrors the real OpenCode server's own handshake (types.go's own
	// sseEnvelope doc comment): the very first event on a freshly-accepted
	// /event connection is "server.connected" -- Adapter.Connected (used by
	// several tests below to know the persistent stream is up before
	// proceeding) blocks on exactly this.
	_, _ = io.WriteString(w, sseDataPrefix+`{"type":"server.connected","properties":{}}`+"\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	for {
		select {
		case line := <-conn.send:
			_, _ = io.WriteString(w, line)
			if flusher != nil {
				flusher.Flush()
			}
		case <-conn.close:
			return
		case <-r.Context().Done():
			return
		}
	}
}

func (f *fakeOpenCodeServer) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessionResponse{ID: "ses_fake"})
}

func (f *fakeOpenCodeServer) handleSessionSubroutes(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/prompt_async"):
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(r.URL.Path, "/abort"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(false)
	case strings.HasSuffix(r.URL.Path, "/message"):
		f.mu.Lock()
		entries := f.messages
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if entries == nil {
			entries = []messageListEntry{}
		}
		_ = json.NewEncoder(w).Encode(entries)
	default:
		http.NotFound(w, r)
	}
}

// setMessages configures GET /session/{id}/message's own response body --
// the SSE-inactivity fallback's final-state fetch target.
func (f *fakeOpenCodeServer) setMessages(entries []messageListEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messages = entries
}

// broadcast sends one raw SSE line (already "data: ...\n\n"-shaped, see
// sseLine below) to whichever /event connection is CURRENTLY live -- a
// no-op if none is connected. Callers are expected to have already
// consumed at least one value from f.connected first.
func (f *fakeOpenCodeServer) broadcast(line string) {
	f.mu.Lock()
	conn := f.current
	f.mu.Unlock()
	if conn == nil {
		return
	}
	conn.send <- line
}

// dropConnection force-closes whichever /event connection is currently
// live, simulating a real dropped persistent connection: the adapter's own
// connectAndConsume (sse.go) observes this as a read error and
// runEventLoop reconnects after a.reconnectInterval.
func (f *fakeOpenCodeServer) dropConnection() {
	f.mu.Lock()
	conn := f.current
	f.current = nil
	f.mu.Unlock()
	if conn != nil {
		close(conn.close)
	}
}

// rejectAllFutureEventConnections arms a permanent failure mode: every
// SUBSEQUENT GET /event handshake (including any reconnect attempt) fails
// immediately with a non-200 status, simulating a connection that drops and
// never comes back. Does not affect any connection already established --
// pair with dropConnection to also kill the current one.
func (f *fakeOpenCodeServer) rejectAllFutureEventConnections() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectEvent = true
}

// sseLine builds one raw "data: <json>\n\n" line matching sseEnvelope's own
// wire shape (types.go), for a given event type and JSON-marshalable
// properties value.
func sseLine(t *testing.T, eventType string, props any) string {
	t.Helper()
	encodedProps, err := json.Marshal(props)
	if err != nil {
		t.Fatalf("sseLine: marshal properties: %v", err)
	}
	env := struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}{Type: eventType, Properties: encodedProps}
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("sseLine: marshal envelope: %v", err)
	}
	return sseDataPrefix + string(body) + "\n\n"
}

// waitForConnNumber blocks until f.connected has delivered a value >= n, or
// fails the test after testWait -- used to know precisely when the Nth
// /event connection (1-based) has been accepted, e.g. after a
// dropConnection-triggered reconnect.
func waitForConnNumber(t *testing.T, f *fakeOpenCodeServer, n int) {
	t.Helper()
	deadline := time.After(testWait)
	for {
		select {
		case seq := <-f.connected:
			if seq >= n {
				return
			}
		case <-deadline:
			t.Fatalf("never observed /event connection #%d within %s", n, testWait)
		}
	}
}

// waitForTurnRegistered polls until a.lookupTurn(sessionID) is non-nil (and
// returns it), or fails the test after testWait -- an SSE event broadcast
// for a session with no registered turn yet is silently dropped (sse.go's
// own dispatchEvent doc comment), so tests that broadcast events for a
// just-started turn must wait for registration first rather than racing
// StartTurn's own resolveSession/registerTurn sequence.
func waitForTurnRegistered(t *testing.T, a *Adapter, sessionID string) *turnState {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if ts := a.lookupTurn(sessionID); ts != nil {
			return ts
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("turn for session %q was never registered within %s", sessionID, testWait)
	return nil
}

// waitForSawText polls ts.outcomeInputs() until hasText is true, or fails
// the test after testWait -- used to confirm the adapter has actually
// FINISHED PROCESSING a broadcast assistant text part before a test goes on
// to (e.g.) drop the connection, rather than racing the fake server's own
// asynchronous relay of already-queued SSE lines against a subsequent
// dropConnection call.
func waitForSawText(t *testing.T, ts *turnState) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if hasText, _ := ts.outcomeInputs(); hasText {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("turn never observed the broadcast assistant text part within testWait")
}

// lastExecutionComplete asserts events' own last entry is a
// sandboxws.ExecutionComplete and returns it -- the common "did the turn
// actually terminate, and with what" assertion shared by every liveness
// test below.
func lastExecutionComplete(t *testing.T, events []ports.AgentEvent) sandboxws.ExecutionComplete {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no events observed at all")
	}
	final, ok := events[len(events)-1].Payload.(sandboxws.ExecutionComplete)
	if !ok {
		t.Fatalf("last event = %T, want execution_complete to be the final event", events[len(events)-1].Payload)
	}
	return final
}
