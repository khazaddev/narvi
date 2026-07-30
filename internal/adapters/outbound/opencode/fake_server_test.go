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

	mu             sync.Mutex
	current        *fakeSSEConn
	connSeq        int
	rejectEvent    bool
	messages       []messageListEntry
	summarizeCalls []summarizeRequest
	promptCalls    []string // cmd.Text of every POST .../prompt_async body, in order (§7.2 regression test)
	summarizeOK    bool     // whether POST .../summarize succeeds -- false unless armed otherwise (Finding 5)

	// summarizeGate, when non-nil, blocks the /summarize handler until
	// closed -- lets a test deterministically control exactly when a
	// gated forceCompaction call returns, rather than relying on wall-clock
	// sleeps (§7.2 Finding 1's own regression test: proving the
	// SSE-inactivity fallback never fires while a compaction is genuinely
	// still in flight).
	summarizeGate chan struct{}

	// promptAsyncGateFrom/promptAsyncGate let a test block a SPECIFIC,
	// numbered (1-based) POST .../prompt_async call -- e.g. only the
	// RETRY's own re-dispatch, not the turn's original one -- until
	// released, deterministically (§7.2 Finding 3's own regression test:
	// proving a late compaction-tail SSE event arriving while that retry
	// dispatch is still in flight is correctly suppressed). 0 means "gate
	// nothing" (the default).
	promptAsyncGateFrom int
	promptAsyncGate     chan struct{}

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
		var body promptAsyncRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		text := ""
		if len(body.Parts) > 0 {
			text = body.Parts[0].Text
		}
		f.mu.Lock()
		f.promptCalls = append(f.promptCalls, text)
		callIndex := len(f.promptCalls) // 1-based
		gateFrom := f.promptAsyncGateFrom
		gate := f.promptAsyncGate
		f.mu.Unlock()
		if gateFrom != 0 && callIndex >= gateFrom {
			<-gate
		}
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
	case strings.HasSuffix(r.URL.Path, "/summarize"):
		// §7.2's own VERIFIED live request/response shape: a JSON body of
		// {"providerID","modelID"} (both required -- see summarizeRequest,
		// types.go), a bare JSON boolean response on success.
		var body summarizeRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.summarizeCalls = append(f.summarizeCalls, body)
		ok := f.summarizeOK
		gate := f.summarizeGate
		f.mu.Unlock()
		if gate != nil {
			<-gate // Finding 1's own regression test: block until released
		}

		sessionID := summarizeSessionIDFromPath(r.URL.Path)

		// §7.2 Finding 6: broadcast the REAL, empirically-observed
		// compaction-internal SSE wave (compact.go's own forceCompaction doc
		// comment) for the SAME sessionID BEFORE responding -- a genuine
		// POST /summarize call streams its own internal message.updated/
		// message.part.updated/session.idle traffic on success, or still
		// surfaces a session.error for the compaction's own internal agent
		// on failure. This is the ONLY thing that actually exercises every
		// one of dispatchEvent's four isCompacting guards (sse.go) in a
		// fake-server-backed test: before this, the handler never broadcast
		// anything here at all, so those guards were completely unexercised
		// (confirmed empirically -- short-circuiting all four to dead code
		// left every existing test in this package passing unchanged).
		if ok {
			f.broadcastCompactionSuccessWave(sessionID)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(true)
			return
		}
		f.broadcastCompactionErrorEvent(sessionID)
		// Finding 5: a real failed /summarize call returns a non-2xx status
		// (VERIFIED against a real, deliberately-invalid providerID/modelID
		// pair -- OpenCode 1.17.15 replies with an "UnknownError"-tagged
		// JSON body and HTTP 500) -- unlike the OLD fake handler, which
		// always replied HTTP 200 regardless of f.summarizeOK, making
		// setSummarizeOK(false) unable to ever actually exercise
		// forceCompaction's own error branch.
		http.Error(w, `{"name":"UnknownError","data":{"message":"simulated summarize failure"}}`, http.StatusInternalServerError)
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

// setSummarizeOK arms whether POST .../summarize succeeds: true replies with
// a bare JSON boolean `true` (matching OpenCode's real success response --
// VERIFIED live, compact.go's own forceCompaction doc comment); false
// (Finding 5) replies with a real non-2xx error status instead, so
// forceCompaction (compact.go) genuinely observes a failure -- the OLD
// version of this handler always replied HTTP 200 regardless of this flag's
// value, silently making the false branch inert (setSummarizeOK(false)
// could never actually make forceCompaction return an error). Defaults to
// false (the zero value) until called -- tests exercising the success path
// call this with true before triggering the overflow.
func (f *fakeOpenCodeServer) setSummarizeOK(ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.summarizeOK = ok
}

// armSummarizeGate arms the fake server to block the /summarize handler
// (after it has already recorded the call and broadcast whatever
// compaction-internal wave applies) until the returned channel is closed --
// §7.2 Finding 1's own regression test uses this to deterministically hold a
// compaction retry "in flight" for as long as it likes, rather than relying
// on a real, slow model response or a wall-clock sleep racing the SSE-
// inactivity fallback's own ticker.
func (f *fakeOpenCodeServer) armSummarizeGate() chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{})
	f.summarizeGate = ch
	return ch
}

// armPromptAsyncGateForCall arms the fake server to block the Nth (1-based)
// POST .../prompt_async call until the returned channel is closed -- lets a
// test deterministically control exactly when a SPECIFIC prompt_async call
// (e.g. n=2, the compaction retry's own re-dispatch, leaving the turn's
// ORIGINAL dispatch at n=1 unaffected) returns to its caller, rather than
// relying on wall-clock sleeps (§7.2 Finding 3's own regression test).
func (f *fakeOpenCodeServer) armPromptAsyncGateForCall(n int) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promptAsyncGateFrom = n
	ch := make(chan struct{})
	f.promptAsyncGate = ch
	return ch
}

// summarizeSessionIDFromPath extracts "{id}" out of a
// "/session/{id}/summarize" request path -- used by the /summarize handler
// to know which session's own SSE stream to broadcast the compaction-
// internal wave onto (Finding 6).
func summarizeSessionIDFromPath(path string) string {
	return strings.TrimSuffix(strings.TrimPrefix(path, "/session/"), "/summarize")
}

// buildSSELine is sseLine's own non-test-fixture core (see sseLine below for
// the test-goroutine-facing wrapper): building one raw "data: <json>\n\n"
// line does not itself need *testing.T, only sseLine's own t.Fatalf-on-
// failure convenience does. fakeOpenCodeServer's own /summarize handler
// (broadcastCompactionSuccessWave/broadcastCompactionErrorEvent below) runs
// on an httptest.Server-managed goroutine, NOT the test's own goroutine --
// calling t.Fatalf there would be unsafe (t.Fatalf calls runtime.Goexit,
// which only unwinds the CALLING goroutine, not the test's own; a
// mis-marshaled SSE line here would silently hang the test instead of
// failing it cleanly) -- so those callers use this error-returning core
// directly and simply skip broadcasting on the (practically impossible, for
// these fixed-shape values) marshal-error case instead.
func buildSSELine(eventType string, props any) (string, error) {
	encodedProps, err := json.Marshal(props)
	if err != nil {
		return "", err
	}
	env := struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}{Type: eventType, Properties: encodedProps}
	body, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return sseDataPrefix + string(body) + "\n\n", nil
}

// broadcastCompactionSuccessWave scripts the real, empirically-observed
// message.updated (a new assistant message, compaction's own internal
// summary)/message.part.updated (its own "text" part)/session.idle wave a
// genuine successful POST /summarize call streams on the SAME global
// /event stream for sessionID WHILE it is still in flight (compact.go's own
// forceCompaction doc comment) -- called from the /summarize handler itself
// (Finding 6), BEFORE it responds, exactly mirroring the real ordering.
// Every event broadcast here is dispatched while the caller (adapter.go's
// Adapter.attemptCompactionRetry, via finalizeOrRecoverFromOverflow) has
// already set ts.compacting=true and keeps it true for the whole span this
// handler runs within -- so a correctly-guarded dispatchEvent (sse.go)
// silently drops every one of these, and a test can assert that (e.g. the
// compaction message's own id never becomes a KNOWN assistant message id)
// to prove the guard actually fired.
func (f *fakeOpenCodeServer) broadcastCompactionSuccessWave(sessionID string) {
	msgID := "msg_compaction_" + sessionID
	if line, err := buildSSELine("message.updated", messageUpdatedProps{
		SessionID: sessionID,
		Info:      openCodeMessageInfo{ID: msgID, Role: "assistant"},
	}); err == nil {
		f.broadcast(line)
	}

	part := struct {
		ID        string `json:"id"`
		MessageID string `json:"messageID"`
		Type      string `json:"type"`
		Text      string `json:"text"`
	}{ID: "prt_compaction_" + sessionID, MessageID: msgID, Type: "text", Text: "compaction summary"}
	raw, err := json.Marshal(part)
	if err == nil {
		if line, err := buildSSELine("message.part.updated", messagePartUpdatedProps{SessionID: sessionID, Part: raw}); err == nil {
			f.broadcast(line)
		}
	}

	if line, err := buildSSELine("session.idle", sessionIdleProps{SessionID: sessionID}); err == nil {
		f.broadcast(line)
	}
}

// broadcastCompactionErrorEvent scripts a compaction-internal session.error
// for sessionID -- the failure-path counterpart to
// broadcastCompactionSuccessWave above (Finding 6), exercising
// dispatchEvent's own fourth isCompacting-guarded case (session.error,
// sse.go) that the success-only wave never reaches. Called from the
// /summarize handler's own failure branch, BEFORE it replies with the
// simulated non-2xx status (Finding 5).
func (f *fakeOpenCodeServer) broadcastCompactionErrorEvent(sessionID string) {
	if line, err := buildSSELine("session.error", sessionErrorProps{
		SessionID: sessionID,
		Error:     openCodeTaggedError{Name: "UnknownError"},
	}); err == nil {
		f.broadcast(line)
	}
}

// summarizeCallCount/promptCallCount/lastPromptText let a test assert how
// many times each endpoint was actually called (§7.2's own primary
// regression test: exactly one /summarize call, exactly one retried
// prompt_async call, both scripted assertions this fake server's own
// counters exist for).
func (f *fakeOpenCodeServer) summarizeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.summarizeCalls)
}

func (f *fakeOpenCodeServer) promptCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.promptCalls)
}

func (f *fakeOpenCodeServer) lastPromptText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.promptCalls) == 0 {
		return ""
	}
	return f.promptCalls[len(f.promptCalls)-1]
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
// properties value -- the test-goroutine-facing wrapper around buildSSELine
// above, which does the actual encoding work.
func sseLine(t *testing.T, eventType string, props any) string {
	t.Helper()
	line, err := buildSSELine(eventType, props)
	if err != nil {
		t.Fatalf("sseLine: %v", err)
	}
	return line
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
