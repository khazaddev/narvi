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

	// t is held so broadcast can FAIL the test when it cannot deliver,
	// instead of silently dropping the line -- see broadcast's own doc
	// comment for the CI failure that motivated this.
	t *testing.T

	mu             sync.Mutex
	current        *fakeSSEConn
	connSeq        int
	rejectEvent    bool
	messages       []messageListEntry
	summarizeCalls []summarizeRequest
	promptCalls    []string // cmd.Text of every POST .../prompt_async body, in order (§7.2 regression test)
	abortCalls     int      // count of every POST .../abort call -- a LATER audit's own round-2 Finding 1 regression test
	summarizeOK    bool     // whether POST .../summarize succeeds -- false unless armed otherwise (Finding 5)

	// messageCalls counts every GET /session/{id}/message call, incremented
	// BEFORE the handler waits on messageGate below -- a LATER audit's own
	// test-adversarial finding: unlike promptCallCount/summarizeCallCount
	// (which already have waitForCount-compatible counters a test can poll
	// deterministically), no such counter existed for GET /message before
	// this field, leaving tests that need to know "the fallback's own fetch
	// has definitely reached and blocked on messageGate" with nothing better
	// than a fixed wall-clock sleep to approximate it -- a sleep that can
	// under-wait on a machine under heavy concurrent load, silently letting
	// a test pass without ever having forced the race it exists to prove.
	// messageCallCount below, paired with the existing waitForCount helper
	// (compactionretry_test.go), closes that gap the same way
	// promptCallCount/summarizeCallCount already do for their own endpoints.
	messageCalls int

	// promptAsyncFail, when true, makes EVERY SUBSEQUENT POST
	// .../prompt_async call reply with a real non-2xx status instead of 200
	// -- a LATER audit's own test-gap finding: attemptCompactionRetry's own
	// THIRD documented failure branch (the retried postPromptAsync dispatch
	// itself failing, adapter.go) had no fake-server-driven coverage at all
	// before this field existed, mirroring summarizeOK/setSummarizeOK's own
	// Finding 5 precedent exactly. Zero value (false) preserves this
	// handler's own original, unconditional-200 behavior for every OTHER
	// test in this package that never calls setPromptAsyncOK at all --
	// checked at request time, so a test must call setPromptAsyncOK(false)
	// AFTER the turn's own ORIGINAL dispatch has already happened (that
	// first call must keep succeeding, or the overflow scenario a test like
	// this needs could never even get triggered in the first place).
	promptAsyncFail bool

	// messageGate, when non-nil, blocks the GET /session/{id}/message
	// handler (the SSE-inactivity fallback's own final-state fetch,
	// fetchFinalMessages/finalizeByFallback, adapter.go) until closed --
	// lets a test deterministically hold that fetch "in flight" for as
	// long as it likes, mirroring summarizeGate/promptAsyncGate's own
	// precedent, so a LATER audit's own Finding 2 (the fallback's
	// check-then-act race across that exact fetch) can be exercised
	// without racing wall-clock timing against a real HTTP round trip.
	messageGate chan struct{}

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

	f := &fakeOpenCodeServer{
		t:         t,
		connected: make(chan int, 64),
	}

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
		fail := f.promptAsyncFail
		f.mu.Unlock()
		if gateFrom != 0 && callIndex >= gateFrom {
			<-gate
		}
		if fail {
			// Finding 5's own precedent, applied to prompt_async: a real
			// non-2xx status, not just an inert flag -- see
			// promptAsyncFail's own field comment for why this branch
			// exists at all.
			http.Error(w, `{"name":"UnknownError","data":{"message":"simulated prompt_async failure"}}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	case strings.HasSuffix(r.URL.Path, "/abort"):
		f.mu.Lock()
		f.abortCalls++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(false)
	case strings.HasSuffix(r.URL.Path, "/message"):
		f.mu.Lock()
		entries := f.messages
		gate := f.messageGate
		f.messageCalls++ // recorded BEFORE the gate wait -- see messageCalls' own field comment
		f.mu.Unlock()
		if gate != nil {
			<-gate // §7.2 Finding 2's own regression test: block until released
		}
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

// setPromptAsyncOK arms whether POST .../prompt_async succeeds from this
// point forward: true (also the zero value / default) replies 200 OK
// exactly as this handler always has; false makes every SUBSEQUENT call
// reply with a real non-2xx status instead -- mirroring setSummarizeOK's
// own Finding 5 precedent, so a test can deterministically exercise
// attemptCompactionRetry's own THIRD documented failure branch: compaction
// succeeds, but the RETRIED postPromptAsync dispatch itself fails. Call
// this AFTER the turn's own ORIGINAL dispatch has already happened -- see
// promptAsyncFail's own field comment for why.
func (f *fakeOpenCodeServer) setPromptAsyncOK(ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.promptAsyncFail = !ok
}

// armMessageGate arms the fake server to block the GET
// /session/{id}/message handler (the SSE-inactivity fallback's own
// final-state fetch) until the returned channel is closed -- see
// messageGate's own field comment for why a LATER audit's own Finding 2
// regression test needs this.
func (f *fakeOpenCodeServer) armMessageGate() chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{})
	f.messageGate = ch
	return ch
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

	// NOTE: a LATER audit's own test-gap finding (this wave's ORIGINAL
	// "text"-only shape leaves message.part.updated's own isCompacting
	// guard, sse.go, mutation-survivable, since dispatchPart's own
	// isAssistantMessage gate already drops an unknown-message-id "text"
	// part regardless) is proven by a DEDICATED test instead of by adding a
	// step-start part HERE: TestCompactionRetry_StepStartDuringCompactionIsSuppressed
	// (compactionretry_test.go) uses armSummarizeGate to broadcast a
	// step-start part itself, deterministically, while /summarize is still
	// gated (so ts.compacting is PROVABLY still true, no race). Adding a
	// 4th queued event to THIS shared wave was tried and reverted: it
	// measurably widened the pre-existing, explicitly-disclaimed cross-
	// goroutine race attemptCompactionRetry's own doc comment describes
	// (Finding 3) -- an in-process fake server's forceCompaction+
	// postPromptAsync round trip can complete, and ts.compacting can flip
	// false, before the SSE-reader goroutine has drained every event this
	// handler just queued, especially under full-package `-race` load —
	// occasionally letting THIS wave's own tail (not the real retry's) be
	// misread as the turn's real completion. Keeping this wave exactly as
	// small as the real, empirically-observed shape needs keeps that
	// already-accepted risk at its original size instead of growing it.
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

// messageCallCount reports how many GET /session/{id}/message calls have
// been recorded so far -- incremented BEFORE the handler waits on
// messageGate (messageCalls' own field comment), so a test can poll this via
// waitForCount to know DETERMINISTICALLY that a gated fetch has reached and
// is now blocked on the gate, rather than assuming so via a fixed wall-clock
// sleep (a LATER audit's own test-adversarial finding).
func (f *fakeOpenCodeServer) messageCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.messageCalls
}

func (f *fakeOpenCodeServer) lastPromptText() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.promptCalls) == 0 {
		return ""
	}
	return f.promptCalls[len(f.promptCalls)-1]
}

// abortCallCount reports how many POST .../abort calls the fake server has
// actually received -- a LATER audit's own round-2 Finding 1 regression
// test uses this to prove attemptCompactionRetry's own late-stillLive()
// check explicitly aborts a retry prompt that was already dispatched by
// the time a Stop landed, on top of Adapter.Stop's own always-present
// abort call.
func (f *fakeOpenCodeServer) abortCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.abortCalls
}

// broadcast sends one raw SSE line (already "data: ...\n\n"-shaped, see
// sseLine below) to whichever /event connection is CURRENTLY live -- a
// no-op if none is connected. Callers are expected to have already
// consumed at least one value from f.connected first.
// broadcast delivers line on the CURRENT /event connection, waiting for one
// to exist (and re-waiting if a reconnect races the send) rather than
// dropping the line when f.current happens to be nil.
//
// It used to return silently in that case, and that is exactly how CI lost
// TestCompactionRetry_SharesOneShotBudgetWithTransientRetry and
// TestCompactionRetry_RetryAlsoOverflowsFinalizesFailedExactlyOnce: the
// adapter's own SSE reconnect fires mid-test on a loaded runner (the failing
// runs' logs show "event stream connection lost, reconnecting" in the same
// second as the failure), so an error-carrying message.updated vanished,
// only the following session.idle landed, and the turn finalized as
// completed with a nil reason -- surfacing as a wrong-outcome assertion two
// layers from its cause, on tests that pass every time locally where no
// reconnect happens.
//
// t.Errorf rather than t.Fatalf: this is reachable from a non-test goroutine,
// where FailNow is not allowed.
func (f *fakeOpenCodeServer) broadcast(line string) {
	deadline := time.After(testWait)
	for {
		f.mu.Lock()
		conn := f.current
		f.mu.Unlock()
		if conn != nil {
			select {
			case conn.send <- line:
				return
			case <-conn.close:
				// A reconnect raced us between reading f.current
				// and sending; wait for the replacement.
			case <-deadline:
				f.t.Errorf("fake server: no live /event connection accepted a broadcast within %s", testWait)
				return
			}
			continue
		}
		select {
		case <-time.After(time.Millisecond):
		case <-deadline:
			f.t.Errorf("fake server: never observed a live /event connection to broadcast on within %s", testWait)
			return
		}
	}
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

// waitForNotCompacting polls ts.isCompacting() until it reads false, or
// fails the test after testWait -- a LATER audit's own round-2 Finding 3
// regression fix: a test that releases a gated retry postPromptAsync call
// and then immediately broadcasts that retry's own completion events (via
// f.broadcast, bypassing the real client-side HTTP round trip entirely)
// must not race attemptCompactionRetry's own ts.setCompacting(false) call
// (adapter.go, deliberately run only AFTER postPromptAsync returns, §7.2
// Finding 3) -- broadcasting those events before compacting has actually
// flipped false would have them silently dropped by dispatchEvent's own
// isCompacting guard (sse.go), exactly the same hazard
// TestCompactionRetry_LateCompactionTailEventDuringRetryDispatchIsSuppressed
// deliberately controls via armPromptAsyncGateForCall. Waiting for this
// deterministically, rather than a sleep, closes that window for good.
func waitForNotCompacting(t *testing.T, ts *turnState) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if !ts.isCompacting() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("ts.isCompacting() never became false within testWait")
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
