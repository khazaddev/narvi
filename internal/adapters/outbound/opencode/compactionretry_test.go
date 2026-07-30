package opencode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
)

// THE PRIMARY §7.2 regression test: unlike realturn_test.go's own
// skipIfNoProvider-gated tests, this deliberately runs against
// fakeOpenCodeServer (fake_server_test.go) so CI can exercise the whole
// compaction-retry round trip reliably, with no real AI provider needed --
// scripting the EXACT empirically-observed sequence this Step's own live
// research pass captured (see compact.go's own forceCompaction doc
// comment for the full captured event list): a ContextOverflowError on the
// original prompt's own assistant message, followed by session.idle for
// the SAME session, must trigger exactly one POST /summarize call, then
// exactly one retried POST .../prompt_async call with the SAME prompt
// text, then (this scenario) a clean completion.

// waitForCount polls fn until it returns >= n, or fails the test after
// testWait -- shared shape for summarizeCallCount/promptCallCount, which
// this test polls across the background goroutine
// finalizeOrRecoverFromOverflow launches (adapter.go).
func waitForCount(t *testing.T, name string, fn func() int, n int) {
	t.Helper()
	deadline := time.Now().Add(testWait)
	for time.Now().Before(deadline) {
		if fn() >= n {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s never reached %d within %s (last observed: %d)", name, n, testWait, fn())
}

// overflowMessageUpdated/plainAssistantMessageUpdated/assistantTextPart
// build the exact SSE lines this test broadcasts -- mirroring this
// package's own sseLine helper (fake_server_test.go) and the real,
// VERIFIED-live openCodeMessageInfo/textPart shapes (types.go).
func overflowMessageUpdated(t *testing.T, sessionID, messageID string) string {
	t.Helper()
	return sseLine(t, "message.updated", messageUpdatedProps{
		SessionID: sessionID,
		Info: openCodeMessageInfo{
			ID:    messageID,
			Role:  "assistant",
			Error: &openCodeTaggedError{Name: "ContextOverflowError"},
		},
	})
}

func plainAssistantMessageUpdated(t *testing.T, sessionID, messageID string) string {
	t.Helper()
	return sseLine(t, "message.updated", messageUpdatedProps{
		SessionID: sessionID,
		Info:      openCodeMessageInfo{ID: messageID, Role: "assistant"},
	})
}

func assistantTextPart(t *testing.T, sessionID, messageID, partID, text string) string {
	t.Helper()
	// textPart (types.go) has no "type" field of its own (partEnvelope's
	// own "type" discriminator is peeked from the SAME raw bytes
	// separately, dispatchPart, sse.go) -- so the raw JSON on the wire
	// (and here) must carry "type":"text" explicitly alongside id/
	// messageID/text, matching dispatch_test.go's own literal-JSON
	// precedent for the exact same reason.
	part := struct {
		ID        string `json:"id"`
		MessageID string `json:"messageID"`
		Type      string `json:"type"`
		Text      string `json:"text"`
	}{ID: partID, MessageID: messageID, Type: "text", Text: text}
	raw, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("assistantTextPart: %v", err)
	}
	return sseLine(t, "message.part.updated", messagePartUpdatedProps{SessionID: sessionID, Part: raw})
}

func sessionIdleLine(t *testing.T, sessionID string) string {
	t.Helper()
	return sseLine(t, "session.idle", sessionIdleProps{SessionID: sessionID})
}

// TestCompactionRetry_SucceedsAfterOverflow proves the full round trip:
// overflow -> exactly one /summarize call -> exactly one retried
// prompt_async call (same prompt text) -> a clean retry completion ->
// final execution_complete is Completed, never Failed.
func TestCompactionRetry_SucceedsAfterOverflow(t *testing.T) {
	f := newFakeOpenCodeServer(t)
	f.setSummarizeOK(true)

	a := New(f.URL(), testSSEInactivityTimeout, testReconnectInterval, testRequestTimeout, testSummarizeTimeout)
	t.Cleanup(a.Close)

	connCtx, connCancel := context.WithTimeout(context.Background(), testWait)
	defer connCancel()
	if err := a.Connected(connCtx); err != nil {
		t.Fatalf("Connected() error = %v", err)
	}
	waitForConnNumber(t, f, 1)

	collector := &eventCollector{}
	promptText := "do something that will overflow"
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: "sess-1", Gen: 1,
		Text: promptText,
	}

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	var group errgroup.Group
	group.Go(func() error {
		_, err := a.StartTurn(ctx, cmd, collector.sink, nil)
		return err
	})

	ts := waitForTurnRegistered(t, a, "ses_fake")

	// The original turn's own assistant message reports a
	// ContextOverflowError, then the turn goes idle -- the exact
	// empirically-observed trigger sequence (§7.2).
	f.broadcast(overflowMessageUpdated(t, "ses_fake", "msg_original"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	// The overflow must trigger exactly one POST .../summarize call.
	waitForCount(t, "summarizeCallCount", f.summarizeCallCount, 1)

	// ...and, on that call succeeding, exactly one RETRIED prompt_async
	// call for the SAME session with the SAME prompt text (promptCalls[0]
	// is the ORIGINAL dispatch from StartTurn itself; [1] is the retry).
	waitForCount(t, "promptCallCount", f.promptCallCount, 2)
	if got := f.lastPromptText(); got != promptText {
		t.Errorf("retried prompt text = %q, want %q (the EXACT same prompt)", got, promptText)
	}
	if got := f.summarizeCallCount(); got != 1 {
		t.Errorf("summarizeCallCount = %d, want exactly 1", got)
	}

	// A stray compaction-internal message must not have leaked through
	// while ts.compacting was true (see sse.go's dispatchEvent doc
	// comment) -- sanity check via ts's own tracked error state before the
	// retry's own clean completion below.
	if err := ts.errorForOutcome(); err != nil {
		t.Errorf("ts.errorForOutcome() = %+v, want nil (clearErrorsForRetry should have cleared it after a successful compaction)", err)
	}

	// Now script the RETRY's own clean completion: a fresh assistant
	// message, a real text part, then session.idle.
	f.broadcast(plainAssistantMessageUpdated(t, "ses_fake", "msg_retry"))
	f.broadcast(assistantTextPart(t, "ses_fake", "msg_retry", "prt_retry", "all good now"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	if err := group.Wait(); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	final := lastExecutionComplete(t, collector.snapshot())
	if final.Outcome != sandboxws.ExecutionCompleteOutcomeCompleted {
		reason := "<nil>"
		if final.Reason != nil {
			reason = *final.Reason
		}
		t.Errorf("execution_complete.Outcome = %q, want %q (reason=%s)", final.Outcome, sandboxws.ExecutionCompleteOutcomeCompleted, reason)
	}

	// The forced model resolution (cmd.Model is nil here, so
	// resolveModelForced falls back to fallbackModelRef) must have reached
	// the fake server's own /summarize handler correctly.
	f.mu.Lock()
	calls := append([]summarizeRequest(nil), f.summarizeCalls...)
	f.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("summarizeCalls = %+v, want exactly 1", calls)
	}
	wantProviderID, wantModelID, _ := strings.Cut(fallbackModel, "/")
	if calls[0].ProviderID != wantProviderID || calls[0].ModelID != wantModelID {
		t.Errorf("summarize request = %+v, want providerID=%q modelID=%q", calls[0], wantProviderID, wantModelID)
	}

	// §7.2 Finding 6's own mutation-testing proof: the fake server's own
	// /summarize handler (fake_server_test.go) broadcasts a real
	// compaction-internal message.updated for "msg_compaction_ses_fake"
	// WHILE ts.compacting was true, above -- this must NEVER have been
	// recorded as a KNOWN assistant message id, proving dispatchEvent's own
	// isCompacting guard (sse.go, the "message.updated" case) actually fired
	// for it rather than this fake-server-backed test simply never
	// exercising that guard at all (confirmed by temporarily
	// short-circuiting all four dispatchEvent isCompacting guards to dead
	// code and observing this exact assertion fail). Checked here, AFTER
	// the turn's own execution_complete: the compaction wave and the
	// retry's own later completion both travel over the SAME single,
	// strictly-ordered persistent SSE connection, so by the time
	// group.Wait() has returned, the SSE-reader goroutine is guaranteed to
	// have already finished processing (and, if the guard held, discarding)
	// every event broadcast during compaction.
	if ts.isAssistantMessage("msg_compaction_ses_fake") {
		t.Error("compaction-internal message.updated was treated as a real assistant message -- " +
			"dispatchEvent's own isCompacting guard (sse.go) did not suppress it")
	}
}

// TestCompactionRetry_RetryAlsoOverflowsFinalizesFailedExactlyOnce proves
// the infinite-loop guard (§7.2 point 3): when the RETRIED prompt also
// overflows, exactly one /summarize call is EVER made (not two), and the
// final outcome is Failed with a reason that mentions the retry was
// already attempted.
func TestCompactionRetry_RetryAlsoOverflowsFinalizesFailedExactlyOnce(t *testing.T) {
	f := newFakeOpenCodeServer(t)
	f.setSummarizeOK(true)

	a := New(f.URL(), testSSEInactivityTimeout, testReconnectInterval, testRequestTimeout, testSummarizeTimeout)
	t.Cleanup(a.Close)

	connCtx, connCancel := context.WithTimeout(context.Background(), testWait)
	defer connCancel()
	if err := a.Connected(connCtx); err != nil {
		t.Fatalf("Connected() error = %v", err)
	}
	waitForConnNumber(t, f, 1)

	collector := &eventCollector{}
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: "sess-1", Gen: 1,
		Text: "do something that will overflow twice",
	}

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	var group errgroup.Group
	group.Go(func() error {
		_, err := a.StartTurn(ctx, cmd, collector.sink, nil)
		return err
	})

	waitForTurnRegistered(t, a, "ses_fake")

	// First overflow -- triggers the one and only compaction attempt.
	f.broadcast(overflowMessageUpdated(t, "ses_fake", "msg_original"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))
	waitForCount(t, "summarizeCallCount", f.summarizeCallCount, 1)
	waitForCount(t, "promptCallCount", f.promptCallCount, 2)

	// The RETRIED prompt ALSO overflows.
	f.broadcast(overflowMessageUpdated(t, "ses_fake", "msg_retry"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	if err := group.Wait(); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	final := lastExecutionComplete(t, collector.snapshot())
	if final.Outcome != sandboxws.ExecutionCompleteOutcomeFailed {
		t.Errorf("execution_complete.Outcome = %q, want %q", final.Outcome, sandboxws.ExecutionCompleteOutcomeFailed)
	}
	if final.Reason == nil || !strings.Contains(string(*final.Reason), "already attempted") {
		reason := "<nil>"
		if final.Reason != nil {
			reason = string(*final.Reason)
		}
		t.Errorf("execution_complete.Reason = %q, want it to mention a retry was already attempted", reason)
	}

	// Exactly one /summarize call EVER -- the second overflow must NOT
	// trigger a second compaction attempt (§7.2's own infinite-loop
	// guard).
	if got := f.summarizeCallCount(); got != 1 {
		t.Errorf("summarizeCallCount = %d, want exactly 1 (no second compaction attempt)", got)
	}
	// And no THIRD prompt_async call either.
	if got := f.promptCallCount(); got != 2 {
		t.Errorf("promptCallCount = %d, want exactly 2 (original + the one retry, no more)", got)
	}
}

// TestCompactionRetry_ForceCompactionFails is §7.2 Finding 5's own
// regression test: the ONE branch of attemptCompactionRetry (adapter.go)
// that, before this fix, had zero fake-server-driven integration coverage
// at all -- forceCompaction (the POST .../summarize call) itself failing.
// setSummarizeOK(false) (fake_server_test.go) now actually makes the fake
// server's own /summarize handler reply with a real non-2xx status (Finding
// 5's own fix to that handler; it used to always reply HTTP 200 regardless
// of this flag, making the false branch inert), so this exercises the REAL
// integration path: ts.setCompacting(false), enrichReasonForFailedRecovery,
// and a.finalize with the ORIGINAL overflow outcome -- not just the pure
// string-builder outcome_test.go already covered in isolation.
func TestCompactionRetry_ForceCompactionFails(t *testing.T) {
	f := newFakeOpenCodeServer(t)
	f.setSummarizeOK(false)

	a := New(f.URL(), testSSEInactivityTimeout, testReconnectInterval, testRequestTimeout, testSummarizeTimeout)
	t.Cleanup(a.Close)

	connCtx, connCancel := context.WithTimeout(context.Background(), testWait)
	defer connCancel()
	if err := a.Connected(connCtx); err != nil {
		t.Fatalf("Connected() error = %v", err)
	}
	waitForConnNumber(t, f, 1)

	collector := &eventCollector{}
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: "sess-5", Gen: 1,
		Text: "do something that will overflow, but the compaction itself will fail",
	}

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	var group errgroup.Group
	group.Go(func() error {
		_, err := a.StartTurn(ctx, cmd, collector.sink, nil)
		return err
	})

	ts := waitForTurnRegistered(t, a, "ses_fake")

	f.broadcast(overflowMessageUpdated(t, "ses_fake", "msg_original"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	waitForCount(t, "summarizeCallCount", f.summarizeCallCount, 1)

	if err := group.Wait(); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	final := lastExecutionComplete(t, collector.snapshot())
	if final.Outcome != sandboxws.ExecutionCompleteOutcomeFailed {
		t.Errorf("execution_complete.Outcome = %q, want %q", final.Outcome, sandboxws.ExecutionCompleteOutcomeFailed)
	}
	if final.Reason == nil || !strings.Contains(*final.Reason, "compaction retry attempted and failed") {
		reason := "<nil>"
		if final.Reason != nil {
			reason = *final.Reason
		}
		t.Errorf("execution_complete.Reason = %q, want it to mention the compaction retry itself failed", reason)
	}
	if final.Reason == nil || !strings.Contains(*final.Reason, "forceCompaction") {
		reason := "<nil>"
		if final.Reason != nil {
			reason = *final.Reason
		}
		t.Errorf("execution_complete.Reason = %q, want it to name forceCompaction as the failed step", reason)
	}

	// The retry must be correctly short-circuited: no retried prompt_async
	// call was ever made, since forceCompaction itself never succeeded.
	if got := f.promptCallCount(); got != 1 {
		t.Errorf("promptCallCount = %d, want exactly 1 (the original dispatch only -- no retry, forceCompaction failed)", got)
	}

	// ts.compacting must have been reset to false (adapter.go's own doc
	// comment: "so a stray late-arriving event for this session ... is not
	// silently swallowed by a guard that no longer serves a purpose").
	if ts.isCompacting() {
		t.Error("ts.isCompacting() = true after forceCompaction failed and the turn finalized, want false")
	}

	// NOTE: this test's own fake /summarize failure handler
	// (fake_server_test.go) also broadcasts a compaction-internal
	// session.error for realism -- but whether the SSE-reader goroutine has
	// actually finished processing that broadcast by the time this test
	// function reaches this point is a genuine, inherent race against an
	// INDEPENDENT connection (the POST /summarize response vs. the GET
	// /event stream), no different in kind from Finding 3's own root cause.
	// Asserting on ts.sessionError HERE would itself be flaky (confirmed:
	// observed failing roughly 1 run in 3 under `-count=3`) without proving
	// anything about correctness -- this failure branch's own finalize call
	// uses originalOutcome directly, never re-deriving from ts.sessionError,
	// so this particular race is harmless by construction. See
	// TestCompactionRetry_SessionErrorDuringCompactionIsSuppressed below for
	// a DETERMINISTIC proof of the same isCompacting guard (sse.go's
	// "session.error" case), using armSummarizeGate to control the exact
	// ordering instead of racing two independent connections.
}

// TestCompactionRetry_SessionErrorDuringCompactionIsSuppressed
// deterministically proves dispatchEvent's own FOURTH isCompacting-guarded
// case (session.error, sse.go) -- the one case
// TestCompactionRetry_SucceedsAfterOverflow's own compaction-success wave
// never reaches, and TestCompactionRetry_ForceCompactionFails' own
// broadcastCompactionErrorEvent call cannot deterministically prove either
// (see that test's own note above: it races an independent connection).
// Uses armSummarizeGate (fake_server_test.go, §7.2 Finding 1's own
// machinery) to hold ts.compacting genuinely true for as long as this test
// likes, broadcasts a session.error itself while gated (guaranteed to be
// dispatched while compacting is still true), and confirms it never reached
// ts.sessionError -- all ordering controlled explicitly, no timing races.
func TestCompactionRetry_SessionErrorDuringCompactionIsSuppressed(t *testing.T) {
	f := newFakeOpenCodeServer(t)
	f.setSummarizeOK(true)
	gate := f.armSummarizeGate()

	a := New(f.URL(), testSSEInactivityTimeout, testReconnectInterval, testRequestTimeout, testSummarizeTimeout)
	t.Cleanup(a.Close)

	connCtx, connCancel := context.WithTimeout(context.Background(), testWait)
	defer connCancel()
	if err := a.Connected(connCtx); err != nil {
		t.Fatalf("Connected() error = %v", err)
	}
	waitForConnNumber(t, f, 1)

	collector := &eventCollector{}
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: "sess-guard4", Gen: 1,
		Text: "will overflow, compaction gated so we can script a session.error mid-flight",
	}

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	var group errgroup.Group
	group.Go(func() error {
		_, err := a.StartTurn(ctx, cmd, collector.sink, nil)
		return err
	})

	ts := waitForTurnRegistered(t, a, "ses_fake")

	f.broadcast(overflowMessageUpdated(t, "ses_fake", "msg_original"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	waitForCount(t, "summarizeCallCount", f.summarizeCallCount, 1)
	if !ts.isCompacting() {
		t.Fatal("ts.isCompacting() = false right after the overflow triggered a compaction attempt, want true")
	}

	// /summarize is gated (blocked before it would even respond), so
	// ts.compacting is GUARANTEED to still be true right now and to remain
	// true until we close(gate) below -- broadcasting a compaction-internal
	// session.error here has no ordering ambiguity at all.
	before := ts.lastActivityTime()
	f.broadcast(sseLine(t, "session.error", sessionErrorProps{
		SessionID: "ses_fake",
		Error:     openCodeTaggedError{Name: "UnknownError"},
	}))

	// touch() runs unconditionally as the very first thing dispatchEvent's
	// own session.error case does (sse.go), strictly before its
	// isCompacting check, in the SAME synchronous call with no goroutine
	// switch in between -- polling for lastActivityTime to advance past
	// `before` is therefore a deterministic proxy for "this exact broadcast
	// has now been fully dispatched" (guard included), without needing a
	// dedicated test hook.
	deadline := time.Now().Add(testWait)
	for !ts.lastActivityTime().After(before) {
		if time.Now().After(deadline) {
			t.Fatal("the broadcast session.error was never dispatched (ts.lastActivityTime never advanced) within testWait")
		}
		time.Sleep(time.Millisecond)
	}

	if ts.sessionError != nil {
		t.Errorf("ts.sessionError = %+v after a compaction-internal session.error broadcast while gated, want nil -- "+
			"dispatchEvent's own isCompacting guard (sse.go, the \"session.error\" case) did not suppress it", ts.sessionError)
	}
	if !ts.isCompacting() {
		t.Fatal("ts.isCompacting() unexpectedly false while /summarize is still gated")
	}

	close(gate)

	waitForCount(t, "promptCallCount", f.promptCallCount, 2)
	f.broadcast(plainAssistantMessageUpdated(t, "ses_fake", "msg_retry"))
	f.broadcast(assistantTextPart(t, "ses_fake", "msg_retry", "prt_retry", "all good now"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	if err := group.Wait(); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	final := lastExecutionComplete(t, collector.snapshot())
	if final.Outcome != sandboxws.ExecutionCompleteOutcomeCompleted {
		reason := "<nil>"
		if final.Reason != nil {
			reason = *final.Reason
		}
		t.Errorf("execution_complete.Outcome = %q, want %q (reason=%s)", final.Outcome, sandboxws.ExecutionCompleteOutcomeCompleted, reason)
	}
}

// TestTurnState_TryBeginCompactionRetryIsAtomic is §7.2 Finding 2's own
// direct regression test: the OLD "check ts.compactionAlreadyAttempted()
// then separately call ts.markCompactionAttempted()+ts.setCompacting(true)"
// sequence in finalizeOrRecoverFromOverflow (adapter.go) was a classic
// check-then-act race with no lock spanning both steps. Proven here
// directly against turnState (no HTTP/SSE plumbing needed, mirroring
// finalize_race_test.go's own style of testing turnState races directly): a
// large number of goroutines race to call tryBeginCompactionRetry
// concurrently on the SAME shared turnState, released simultaneously via a
// shared channel (not sleeps or timing luck) -- EXACTLY ONE of them must
// ever observe true. Run under `go test -race` (like the whole package) to
// also confirm the underlying compacting/compactionAttempted fields are
// never accessed without ts.mu held.
func TestTurnState_TryBeginCompactionRetryIsAtomic(t *testing.T) {
	t.Parallel()

	collector := &eventCollector{}
	ts := newTurnState(sandboxws.Prompt{}, collector.sink)

	const n = 50
	results := make([]bool, n)
	start := make(chan struct{})
	var group errgroup.Group
	for i := 0; i < n; i++ {
		i := i
		group.Go(func() error {
			<-start
			results[i] = ts.tryBeginCompactionRetry()
			return nil
		})
	}
	close(start)
	_ = group.Wait()

	wins := 0
	for _, r := range results {
		if r {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("tryBeginCompactionRetry() returned true %d times across %d concurrent callers, want exactly 1", wins, n)
	}
	if !ts.compactionAlreadyAttempted() {
		t.Error("compactionAlreadyAttempted() = false after a winning tryBeginCompactionRetry call, want true")
	}
	if !ts.isCompacting() {
		t.Error("isCompacting() = false after a winning tryBeginCompactionRetry call, want true")
	}
}

// TestCompactionRetry_ConcurrentOverflowDetectionAttemptsExactlyOnce is
// §7.2 Finding 2's own integration-level regression test:
// finalizeOrRecoverFromOverflow (adapter.go) is invoked CONCURRENTLY, twice,
// for the exact SAME first-time ContextOverflowError on the SAME turnState
// -- mirroring the real race between dispatchEvent's own "session.idle" live
// path and finalizeByFallback's own SSE-inactivity path, both of which route
// through finalizeOrRecoverFromOverflow and can genuinely race on the exact
// same a.sseInactivityTimeout threshold (adapter.go's own doc comment). Must
// launch the compaction retry EXACTLY ONCE regardless of which goroutine
// "wins".
func TestCompactionRetry_ConcurrentOverflowDetectionAttemptsExactlyOnce(t *testing.T) {
	f := newFakeOpenCodeServer(t)
	f.setSummarizeOK(true)

	a := New(f.URL(), testSSEInactivityTimeout, testReconnectInterval, testRequestTimeout, testSummarizeTimeout)
	t.Cleanup(a.Close)

	connCtx, connCancel := context.WithTimeout(context.Background(), testWait)
	defer connCancel()
	if err := a.Connected(connCtx); err != nil {
		t.Fatalf("Connected() error = %v", err)
	}
	waitForConnNumber(t, f, 1)

	collector := &eventCollector{}
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: "sess-race", Gen: 1,
		Text: "racing overflow detection",
	}
	ts := newTurnState(cmd, collector.sink)
	a.registerTurn("ses_fake", ts)
	t.Cleanup(func() { a.unregisterTurn("ses_fake") })

	overflowErr := &openCodeTaggedError{Name: "ContextOverflowError"}
	outcome := deriveOutcome(overflowErr, false, false)

	const racers = 2
	start := make(chan struct{})
	var group errgroup.Group
	for i := 0; i < racers; i++ {
		group.Go(func() error {
			<-start
			a.finalizeOrRecoverFromOverflow("ses_fake", ts, outcome, overflowErr)
			return nil
		})
	}
	close(start)
	_ = group.Wait()

	waitForCount(t, "summarizeCallCount", f.summarizeCallCount, 1)
	// A generous extra wait to give a SECOND, wrongly-launched compaction
	// attempt a real chance to show up before asserting the negative below
	// -- there is no direct "no more attempts are coming" signal to poll
	// for instead.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if f.summarizeCallCount() > 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := f.summarizeCallCount(); got != 1 {
		t.Errorf("summarizeCallCount = %d, want exactly 1 (two concurrent finalizeOrRecoverFromOverflow "+
			"calls for the SAME first-time overflow must attempt compaction exactly once)", got)
	}
}

// TestCompactionRetry_FallbackDoesNotFinalizeWhileCompacting is §7.2 Finding
// 1's own regression test: finalizeByFallback (adapter.go) used to never
// check ts.isCompacting() at all, unlike every one of dispatchEvent's four
// guarded cases (sse.go) -- so the SSE-inactivity fallback could fire mid-
// compaction and prematurely finalize the turn (via the "already attempted"
// branch of finalizeOrRecoverFromOverflow) while the real background
// compaction-retry goroutine was still genuinely in flight. Deterministically
// forces exactly that timing via a deliberately SHORT SSE-inactivity timeout
// combined with armSummarizeGate (fake_server_test.go), which holds
// forceCompaction's own HTTP call open for as long as the test likes,
// rather than relying on a real slow model response or scheduling luck.
func TestCompactionRetry_FallbackDoesNotFinalizeWhileCompacting(t *testing.T) {
	f := newFakeOpenCodeServer(t)
	f.setSummarizeOK(true)
	gate := f.armSummarizeGate()

	// Deliberately much shorter than testSSEInactivityTimeout -- waitForTurn's
	// own fallback ticker (pollInterval = this / ssePollDivisor) must tick,
	// and shouldFinalizeByFallback must observe ts.idleFor(this)==true, MANY
	// times over, while /summarize is still gated below.
	shortInactivity := 50 * time.Millisecond

	a := New(f.URL(), shortInactivity, testReconnectInterval, testRequestTimeout, testSummarizeTimeout)
	t.Cleanup(a.Close)

	connCtx, connCancel := context.WithTimeout(context.Background(), testWait)
	defer connCancel()
	if err := a.Connected(connCtx); err != nil {
		t.Fatalf("Connected() error = %v", err)
	}
	waitForConnNumber(t, f, 1)

	collector := &eventCollector{}
	promptText := "do something that will overflow, compaction takes a while"
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: "sess-1", Gen: 1,
		Text: promptText,
	}

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	var group errgroup.Group
	group.Go(func() error {
		_, err := a.StartTurn(ctx, cmd, collector.sink, nil)
		return err
	})

	ts := waitForTurnRegistered(t, a, "ses_fake")

	f.broadcast(overflowMessageUpdated(t, "ses_fake", "msg_original"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	waitForCount(t, "summarizeCallCount", f.summarizeCallCount, 1)
	if !ts.isCompacting() {
		t.Fatal("ts.isCompacting() = false right after the overflow triggered a compaction attempt, want true")
	}

	// Let several multiples of the (short) SSE-inactivity timeout elapse
	// while /summarize is still gated -- waitForTurn's own ticker will have
	// fired well over shortInactivity*10/pollInterval times by now. The turn
	// must still be genuinely alive: the real compaction retry is still in
	// flight, and only IT is allowed to decide this turn's own fate right
	// now.
	time.Sleep(shortInactivity * 10)

	select {
	case <-ts.done:
		t.Fatal("turn finalized while a compaction retry was still genuinely in flight " +
			"(forceCompaction still gated) -- finalizeByFallback fired despite ts.isCompacting(), " +
			"Finding 1's own regression")
	default:
	}
	if !ts.isCompacting() {
		t.Fatal("ts.isCompacting() unexpectedly false while /summarize is still gated")
	}

	close(gate) // let the gated /summarize call finally return

	waitForCount(t, "promptCallCount", f.promptCallCount, 2)
	if got := f.lastPromptText(); got != promptText {
		t.Errorf("retried prompt text = %q, want %q", got, promptText)
	}

	// Script the retry's own clean completion, exactly like
	// TestCompactionRetry_SucceedsAfterOverflow.
	f.broadcast(plainAssistantMessageUpdated(t, "ses_fake", "msg_retry"))
	f.broadcast(assistantTextPart(t, "ses_fake", "msg_retry", "prt_retry", "all good now"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	if err := group.Wait(); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	final := lastExecutionComplete(t, collector.snapshot())
	if final.Outcome != sandboxws.ExecutionCompleteOutcomeCompleted {
		reason := "<nil>"
		if final.Reason != nil {
			reason = *final.Reason
		}
		t.Errorf("execution_complete.Outcome = %q, want %q (reason=%s)", final.Outcome, sandboxws.ExecutionCompleteOutcomeCompleted, reason)
	}
}

// TestCompactionRetry_LateCompactionTailEventDuringRetryDispatchIsSuppressed
// is §7.2 Finding 3's own regression test: attemptCompactionRetry
// (adapter.go) used to clear ts.compacting BEFORE dispatching the retry's
// own postPromptAsync call -- widening the window in which a late,
// compaction-tail-shaped SSE event (session.idle carries no field
// distinguishing it from the turn's own real completion -- see
// sessionIdleProps' own doc comment, types.go) arriving in that window could
// sail through the now-false isCompacting guard and get evaluated using the
// turn's STALE pre-overflow ts.sawText (never reset by clearErrorsForRetry,
// deliberately), with no tracked error (just cleared) -- finalizing the
// WHOLE turn as "completed" before the retried prompt was ever actually
// dispatched. Deterministically forces exactly that ordering via
// armPromptAsyncGateForCall (fake_server_test.go), gating ONLY the retry's
// own re-dispatch (call #2), rather than relying on scheduling luck.
func TestCompactionRetry_LateCompactionTailEventDuringRetryDispatchIsSuppressed(t *testing.T) {
	f := newFakeOpenCodeServer(t)
	f.setSummarizeOK(true)
	gate := f.armPromptAsyncGateForCall(2)

	a := New(f.URL(), testSSEInactivityTimeout, testReconnectInterval, testRequestTimeout, testSummarizeTimeout)
	t.Cleanup(a.Close)

	connCtx, connCancel := context.WithTimeout(context.Background(), testWait)
	defer connCancel()
	if err := a.Connected(connCtx); err != nil {
		t.Fatalf("Connected() error = %v", err)
	}
	waitForConnNumber(t, f, 1)

	collector := &eventCollector{}
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: "sess-3", Gen: 1,
		Text: "will overflow after producing some real output first",
	}

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	var group errgroup.Group
	group.Go(func() error {
		_, err := a.StartTurn(ctx, cmd, collector.sink, nil)
		return err
	})

	ts := waitForTurnRegistered(t, a, "ses_fake")

	// Real pre-overflow output, so ts.sawText is ALREADY true before the
	// overflow -- exactly the stale signal Finding 3's failure scenario
	// depends on (clearErrorsForRetry never resets sawText/sawToolCall by
	// design -- see its own doc comment).
	f.broadcast(plainAssistantMessageUpdated(t, "ses_fake", "msg_pre"))
	f.broadcast(assistantTextPart(t, "ses_fake", "msg_pre", "prt_pre", "partial output before overflow"))
	waitForSawText(t, ts)

	f.broadcast(overflowMessageUpdated(t, "ses_fake", "msg_original"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	waitForCount(t, "summarizeCallCount", f.summarizeCallCount, 1)

	// forceCompaction has now succeeded and attemptCompactionRetry is about
	// to (or already did) call postPromptAsync -- which is gated, blocked
	// mid-flight (call #2). A late, compaction-tail-shaped session.idle for
	// the SAME session arrives here -- on the OLD, buggy ordering this would
	// already see ts.isCompacting()==false (cleared before the retry was
	// even dispatched) and wrongly finalize using the stale
	// sawText=true/no-tracked-error state.
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	// Give the SSE reader goroutine ample opportunity to have actually
	// processed that broadcast before asserting -- the gate itself is what
	// makes this assertion meaningful regardless of exactly how long this
	// wait is: postPromptAsync (and therefore any chance of a LEGITIMATE
	// completion) cannot possibly have returned yet, since it is still
	// blocked on gate.
	time.Sleep(100 * time.Millisecond)

	select {
	case <-ts.done:
		t.Fatal("turn finalized while the retry's own postPromptAsync call was still gated/in-flight -- " +
			"a late compaction-tail session.idle was NOT suppressed by ts.isCompacting(), Finding 3's own regression")
	default:
	}

	close(gate) // let the gated retry dispatch finally return

	waitForCount(t, "promptCallCount", f.promptCallCount, 2)

	// Script the retry's own REAL clean completion.
	f.broadcast(plainAssistantMessageUpdated(t, "ses_fake", "msg_retry"))
	f.broadcast(assistantTextPart(t, "ses_fake", "msg_retry", "prt_retry", "all good now"))
	f.broadcast(sessionIdleLine(t, "ses_fake"))

	if err := group.Wait(); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	final := lastExecutionComplete(t, collector.snapshot())
	if final.Outcome != sandboxws.ExecutionCompleteOutcomeCompleted {
		reason := "<nil>"
		if final.Reason != nil {
			reason = *final.Reason
		}
		t.Errorf("execution_complete.Outcome = %q, want %q (reason=%s)", final.Outcome, sandboxws.ExecutionCompleteOutcomeCompleted, reason)
	}
}
