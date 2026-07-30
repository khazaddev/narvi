package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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

// TestRealTurn_SummarizeEndpointExists is §7.2's own MANDATORY CI contract
// test (§7.2 point 5, mirroring §7's own real-binary contract-test
// precedent): asserts POST /session/{id}/summarize genuinely exists and
// returns 200 on the pinned OpenCode binary, a real session, and a real
// resolvable model -- so an OpenCode version bump that drops or renames
// this endpoint fails CI outright, never surfaces as a silent production
// regression discovered only when a real turn actually overflows. This is
// deliberately the ONLY thing this test proves; TestCompactionRetry_*
// (compactionretry_test.go, fake-server-backed, no real provider needed)
// is what proves the actual retry LOGIC works end to end.
//
// §7.2 Finding 4: deliberately does NOT call skipIfNoProvider, unlike its
// three siblings above -- §7.2 point 5 itself calls this check "mandatory,
// not optional" specifically so that "an OpenCode version bump that drops
// or renames /summarize must fail CI, never surface as a silent production
// regression". A skipIfNoProvider-gated version of this test would silently
// t.Skip() on the real CI pipeline (.github/workflows/ci.yml's own comment
// documents that no OPENAI_API_KEY/ANTHROPIC_API_KEY/opencode-auth
// credential is ever configured there, by deliberate choice) -- exactly the
// silent-non-coverage outcome point 5 says must never happen. This test
// does not actually need a real, user-configured AI-provider credential at
// all: freeCatalogModelRef below resolves a model from OpenCode's own
// bundled, no-signup-required "opencode" catalog provider (VERIFIED LIVE --
// see that helper's own doc comment), and a real POST /summarize call
// against it genuinely succeeds end to end with zero credentials configured
// anywhere. That makes this test run -- and matter -- on EVERY CI run
// unconditionally.
func TestRealTurn_SummarizeEndpointExists(t *testing.T) {
	// Deliberately NOT t.Parallel() -- matches every other test in this
	// file's own throttling rationale (see their own doc comments above):
	// even though this test needs no PAID provider credential, it still
	// spawns a real `opencode serve` process and makes a real, if free,
	// model-backed HTTP call, and running many such calls concurrently
	// alongside this package's OTHER real-binary tests was observed to
	// starve CPU under `go test ./...`.

	a := newAdapter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	sessionID, err := a.resolveSession(ctx, sandboxws.Prompt{})
	if err != nil {
		t.Fatalf("resolveSession() error = %v", err)
	}

	// Deliberately NOT resolveModelForced's own fallbackModelRef() here:
	// that fixed "anthropic/claude-sonnet-4-5" slug (§7.2's own pinned,
	// last-resort default, chosen to survive an upgrade dropping the
	// catalog entirely) names a SPECIFIC provider this test cannot assume
	// is actually configured/working anywhere it runs -- this test's only
	// job is confirming /summarize's own continued existence/shape, which
	// needs a GENUINELY, unconditionally resolvable providerID/modelID
	// pair. freeCatalogModelRef below picks OpenCode's own bundled
	// credential-free catalog entry instead -- guaranteed present and
	// functional on ANY machine, with or without a real provider
	// configured (Finding 4).
	model := freeCatalogModelRef(ctx, t, a)

	if err := a.forceCompaction(ctx, sessionID, model); err != nil {
		t.Fatalf("forceCompaction() error = %v -- POST /session/{id}/summarize may have been "+
			"dropped or renamed by an OpenCode version bump (§7.2 point 5)", err)
	}
}

// freeCatalogModelRef fetches GET /api/model directly (the SAME endpoint
// resolveProviderModel's own catalog-liveness check uses, session.go) and
// returns the first entry whose providerID is "opencode" -- OpenCode's own
// bundled, no-signup-required "zen" free tier.
//
// VERIFIED LIVE (§7.2 Finding 4's own research pass): spawning a real
// `opencode serve` process with a deliberately EMPTY, isolated
// HOME/XDG_DATA_HOME (no auth.json, no OPENAI_API_KEY/ANTHROPIC_API_KEY --
// i.e. realProviderConfigured() would report false) still serves a
// non-empty GET /api/model catalog whose entries include at least one
// providerID=="opencode" model (e.g. "ling-3.0-flash-free") carrying its
// own {"apiKey":"public"} request metadata -- and a real POST
// /session/{id}/summarize call against that exact model genuinely
// completes successfully end to end, with zero credentials configured
// anywhere. This is what makes TestRealTurn_SummarizeEndpointExists
// genuinely mandatory rather than provider-gated (Finding 4): unlike
// realturn_test.go's other three tests (which need a REAL scripted AI
// conversation and so must skip gracefully without a real credential),
// confirming /summarize's own continued existence/shape needs nothing
// more than SOME genuinely resolvable, genuinely working providerID/modelID
// pair -- and OpenCode's own bundled free tier is unconditionally that,
// regardless of whatever else may or may not be configured on the machine
// running this test. Picking specifically THIS provider (rather than
// whatever happens to sort first in the catalog) also keeps this test's
// own pass/fail independent of any OTHER provider a given dev/CI machine
// happens to have configured (e.g. an expired or misconfigured credential
// for some other provider must never make this test flaky).
//
// Polls for up to testWait: a freshly-spawned real `opencode serve`
// process (startServer/newAdapter, helpers_test.go) was observed live to
// report GET /api/health healthy a full 1-2s BEFORE its own model catalog
// (an async, presumably network-fetched population step internal to
// OpenCode itself) is actually non-empty -- calling GET /api/model too
// early in that window genuinely returns {"data":[]}, not an error, so a
// single unretried call here would be a flaky false negative unrelated to
// this test's own real purpose (confirming /summarize's continued
// existence/shape).
func freeCatalogModelRef(ctx context.Context, t *testing.T, a *Adapter) *promptModelRef {
	t.Helper()

	deadline := time.Now().Add(testWait)
	for {
		var catalog modelCatalogResponse
		if err := a.doJSON(ctx, http.MethodGet, "/api/model", nil, &catalog); err != nil {
			t.Fatalf("GET /api/model error = %v", err)
		}
		for _, raw := range catalog.Data {
			var entry struct {
				ID         string `json:"id"`
				ProviderID string `json:"providerID"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil {
				continue
			}
			if entry.ProviderID == "opencode" {
				return &promptModelRef{ProviderID: entry.ProviderID, ModelID: entry.ID}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET /api/model never reported a providerID==\"opencode\" (free-tier) entry within %s -- "+
				"cannot pick a credential-free model to summarize with (catalog=%d entries)", testWait, len(catalog.Data))
		}
		time.Sleep(testReadinessPollInterval)
	}
}
