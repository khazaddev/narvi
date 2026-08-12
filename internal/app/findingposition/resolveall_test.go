package findingposition

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// matchableDiff/matchableSnippet are chosen so reviewpost.MatchPosition
// succeeds on them WITHOUT any relocation fallback -- used to prove
// ResolveAll never calls the LLM when the pure match already succeeded.
const matchableDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+	validateBounds(i, items)
 	process(items[i])
`

const matchableSnippet = "validateBounds(i, items) return value not checked"

// unmatchableSnippet shares no real vocabulary with matchableDiff at all
// -- reviewpost.MatchPosition is guaranteed to fail on it, triggering
// ResolveAll's own relocation fallback.
const unmatchableSnippet = "completely unrelated prose about quarterly revenue projections"

func newTestFinding(filePath, description string) reviewpost.Finding {
	return reviewpost.BuildFinding(reviewpost.FindingInput{
		Severity:    review.RiskLevelLow,
		FilePath:    filePath,
		Description: description,
	})
}

func TestResolveAll_EmptyDiffLeavesFindingsUnanchored(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":1,"endLine":1}`)}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{newTestFinding("main.go", matchableSnippet)}
	out := ResolveAll(context.Background(), resolver, findings, "")

	if out[0].StartLine != 0 || out[0].EndLine != 0 {
		t.Errorf("ResolveAll() with empty diff = (%d, %d), want (0, 0)", out[0].StartLine, out[0].EndLine)
	}
	if llm.calls != 0 {
		t.Errorf("Complete called %d times with an empty diff, want 0 (never even attempted)", llm.calls)
	}
}

func TestResolveAll_SuccessfulPureMatchNeverCallsResolver(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":999,"endLine":999}`)}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{newTestFinding("main.go", matchableSnippet)}
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff)

	if out[0].StartLine == 0 {
		t.Fatalf("ResolveAll() startLine = 0, want a real anchored line from the pure match")
	}
	if out[0].StartLine == 999 {
		t.Errorf("ResolveAll() used the RESOLVER's fake line (999) even though the pure match should have already succeeded")
	}
	if llm.calls != 0 {
		t.Errorf("Complete called %d times, want 0 -- relocation must only fire on a FAILED pure match", llm.calls)
	}
}

func TestResolveAll_FailedPureMatchTriggersResolver(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":7,"endLine":7}`)}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{newTestFinding("main.go", unmatchableSnippet)}
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff)

	if llm.calls != 1 {
		t.Fatalf("Complete called %d times, want exactly 1 (relocation fallback for the one failed-match finding)", llm.calls)
	}
	if out[0].StartLine != 7 || out[0].EndLine != 7 {
		t.Errorf("ResolveAll() = (%d, %d), want (7, 7) from the relocation fallback", out[0].StartLine, out[0].EndLine)
	}
}

func TestResolveAll_FailedPureMatchAndFailedRelocationStaysZero(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{err: errTransientLLM}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{newTestFinding("main.go", unmatchableSnippet)}
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff)

	if out[0].StartLine != 0 || out[0].EndLine != 0 {
		t.Errorf("ResolveAll() = (%d, %d), want (0, 0) when both the pure match and the relocation fallback fail", out[0].StartLine, out[0].EndLine)
	}
}

func TestResolveAll_NilResolverLeavesFailedMatchesUnanchoredWithoutPanic(t *testing.T) {
	t.Parallel()

	findings := []reviewpost.Finding{newTestFinding("main.go", unmatchableSnippet)}
	out := ResolveAll(context.Background(), nil, findings, matchableDiff)

	if out[0].StartLine != 0 || out[0].EndLine != 0 {
		t.Errorf("ResolveAll() with nil resolver = (%d, %d), want (0, 0)", out[0].StartLine, out[0].EndLine)
	}
}

func TestResolveAll_MultipleFindingsResolvedIndependently(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":50,"endLine":50}`)}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{
		newTestFinding("main.go", matchableSnippet),   // pure match succeeds
		newTestFinding("main.go", unmatchableSnippet), // pure match fails -> relocation
	}
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff)

	if out[0].StartLine == 0 {
		t.Errorf("first finding (pure-matchable) startLine = 0, want anchored")
	}
	if out[1].StartLine != 50 {
		t.Errorf("second finding (relocation-only) startLine = %d, want 50 (from the fake resolver)", out[1].StartLine)
	}
	if llm.calls != 1 {
		t.Errorf("Complete called %d times, want exactly 1 (only the second finding needed relocation)", llm.calls)
	}
}

// errTransientLLM is a small stand-in typed error mirroring
// *ports.LLMError's own shape for tests that only care that Complete
// returned SOME error.
var errTransientLLM = &llmErrStub{}

type llmErrStub struct{}

func (e *llmErrStub) Error() string { return "stub transient llm error" }
