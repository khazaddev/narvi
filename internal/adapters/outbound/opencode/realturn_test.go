package opencode

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
)

// These tests need a REAL AI PROVIDER RESPONSE (an actual scripted turn
// against `opencode serve`) -- they skip gracefully (skipIfNoProvider)
// when no provider is configured, and run FOR REAL wherever one is (this
// dev machine, verified during this Step's own design phase; a CI run
// where the user later adds a provider secret).

// TestRealTurn_PlainTextPrompt: a plain-text prompt translates into the
// correct wire event sequence -- token(s) with cumulative text, step_start/
// step_finish with the object-shaped cost.tokens, execution_complete
// {completed}.
func TestRealTurn_PlainTextPrompt(t *testing.T) {
	skipIfNoProvider(t)
	// Deliberately NOT t.Parallel(): these are the tests that make a REAL
	// AI-provider call. Running all three concurrently (on top of every
	// OTHER package's own tests already running in parallel, e.g.
	// internal/sandboxagent/opencodeproc's own real-binary spawn test)
	// was observed to trigger provider-side throttling/rate-limiting
	// during `go test ./...`, producing a spurious "no output" failure --
	// serializing just these three (still parallel to every unrelated
	// test elsewhere in the module) keeps concurrent provider load low
	// without giving up real coverage.

	a := newAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	collector := &eventCollector{}
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: testSessionID, Gen: 1,
		Text: "Reply with exactly the word OK and nothing else.",
	}

	convID, err := a.StartTurn(ctx, cmd, collector.sink, nil)
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	if convID == "" {
		t.Error("StartTurn() returned an empty conversation id")
	}

	events := collector.snapshot()
	if len(events) == 0 {
		t.Fatal("no events observed at all")
	}

	var sawToken, sawStepStart, sawStepFinish bool
	var lastCumulativeText string
	var final *sandboxws.ExecutionComplete

	for _, e := range events {
		switch v := e.Payload.(type) {
		case sandboxws.Token:
			sawToken = true
			lastCumulativeText = v.Text // upsert-by-messageId: later == more complete
			if v.SessionId != testSessionID || v.Gen != 1 {
				t.Errorf("Token SessionId/Gen = %q/%d, want %q/1", v.SessionId, v.Gen, testSessionID)
			}
		case sandboxws.StepStart:
			sawStepStart = true
		case sandboxws.StepFinish:
			sawStepFinish = true
		case sandboxws.ExecutionComplete:
			ec := v
			final = &ec
			if !e.Critical || e.AckID != v.AckId {
				t.Errorf("execution_complete not classified critical with matching ackID: critical=%v ackID=%q", e.Critical, e.AckID)
			}
		}
	}

	if !sawToken {
		t.Error("never observed a token event")
	}
	if lastCumulativeText == "" {
		t.Error("final token text was empty, want non-empty cumulative text")
	}
	if !sawStepStart {
		t.Error("never observed a step_start event")
	}
	if !sawStepFinish {
		t.Error("never observed a step_finish event")
	}
	if final == nil {
		t.Fatal("never observed an execution_complete event")
	}
	if final.Outcome != sandboxws.ExecutionCompleteOutcomeCompleted {
		reason := "<nil>"
		if final.Reason != nil {
			reason = *final.Reason
		}
		t.Errorf("execution_complete outcome = %q, want completed (reason=%s)", final.Outcome, reason)
	}
	if _, ok := events[len(events)-1].Payload.(sandboxws.ExecutionComplete); !ok {
		t.Errorf("last event was %T, want execution_complete to be the final event", events[len(events)-1].Payload)
	}
}

// TestRealTurn_ToolInvokingPrompt: a tool-invoking prompt produces exactly
// one tool_call and exactly one tool_result for that callID despite
// OpenCode's own multiple "running" updates in between -- the single most
// important correctness property in this Step (§7's own explicit "dedupe
// tool states" quirk).
func TestRealTurn_ToolInvokingPrompt(t *testing.T) {
	skipIfNoProvider(t)
	// Deliberately NOT t.Parallel(): these are the tests that make a REAL
	// AI-provider call. Running all three concurrently (on top of every
	// OTHER package's own tests already running in parallel, e.g.
	// internal/sandboxagent/opencodeproc's own real-binary spawn test)
	// was observed to trigger provider-side throttling/rate-limiting
	// during `go test ./...`, producing a spurious "no output" failure --
	// serializing just these three (still parallel to every unrelated
	// test elsewhere in the module) keeps concurrent provider load low
	// without giving up real coverage.

	a := newAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	collector := &eventCollector{}
	marker := "NARVI_ADAPTER_TEST_MARKER"
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: testSessionID, Gen: 1,
		Text: fmt.Sprintf("Use the bash tool to run exactly this command: echo %s", marker),
	}

	if _, err := a.StartTurn(ctx, cmd, collector.sink, nil); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	events := collector.snapshot()

	callCounts := map[string]int{}
	resultCounts := map[string]int{}
	sawMarker := false

	for _, e := range events {
		switch v := e.Payload.(type) {
		case sandboxws.ToolCall:
			callCounts[v.CallId]++
		case sandboxws.ToolResult:
			resultCounts[v.CallId]++
			if out, ok := v.Output["output"].(string); ok && strings.Contains(out, marker) {
				sawMarker = true
			}
			if e.Critical {
				t.Error("tool_result classified as critical, want non-critical (not one of the 6 critical types)")
			}
		}
	}

	if len(callCounts) == 0 {
		t.Fatal("never observed any tool_call event")
	}
	for callID, n := range callCounts {
		if n != 1 {
			t.Errorf("tool_call for callID %q emitted %d times, want exactly 1 (dedup)", callID, n)
		}
		if resultCounts[callID] != 1 {
			t.Errorf("tool_result for callID %q emitted %d times, want exactly 1 (dedup)", callID, resultCounts[callID])
		}
	}
	for callID := range resultCounts {
		if callCounts[callID] == 0 {
			t.Errorf("tool_result for callID %q with no matching tool_call", callID)
		}
	}
	if !sawMarker {
		t.Error("never observed the marker text in any tool_result output")
	}
}

// TestRealTurn_Aborted: starting a slow-ish prompt and calling Stop
// shortly after produces execution_complete{cancelled}.
func TestRealTurn_Aborted(t *testing.T) {
	skipIfNoProvider(t)
	// Deliberately NOT t.Parallel(): these are the tests that make a REAL
	// AI-provider call. Running all three concurrently (on top of every
	// OTHER package's own tests already running in parallel, e.g.
	// internal/sandboxagent/opencodeproc's own real-binary spawn test)
	// was observed to trigger provider-side throttling/rate-limiting
	// during `go test ./...`, producing a spurious "no output" failure --
	// serializing just these three (still parallel to every unrelated
	// test elsewhere in the module) keeps concurrent provider load low
	// without giving up real coverage.

	a := newAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	collector := &eventCollector{}
	cmd := sandboxws.Prompt{
		Type: "prompt", MessageId: "m1", SessionId: testSessionID, Gen: 1,
		Text: "Use the bash tool to run exactly this command: sleep 20 && echo done",
	}

	var group errgroup.Group
	group.Go(func() error {
		_, err := a.StartTurn(ctx, cmd, collector.sink, nil)
		return err
	})

	// Give the turn time to actually dispatch and start the bash tool call
	// before aborting it.
	time.Sleep(3 * time.Second)

	stop := sandboxws.Stop{Type: "stop", MessageId: "m2", SessionId: testSessionID, Gen: 1}
	if err := a.Stop(ctx, stop); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if err := group.Wait(); err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}

	var final *sandboxws.ExecutionComplete
	for _, e := range collector.snapshot() {
		if ec, ok := e.Payload.(sandboxws.ExecutionComplete); ok {
			v := ec
			final = &v
		}
	}
	if final == nil {
		t.Fatal("never observed an execution_complete event")
	}
	if final.Outcome != sandboxws.ExecutionCompleteOutcomeCancelled {
		reason := "<nil>"
		if final.Reason != nil {
			reason = *final.Reason
		}
		t.Errorf("execution_complete outcome = %q, want cancelled (reason=%s)", final.Outcome, reason)
	}
}
