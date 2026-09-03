package findingposition

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/domain/review"
	"github.com/narvidev/narvi/internal/domain/reviewpost"
	"github.com/narvidev/narvi/internal/platform"
)

// matchableDiff/matchableSnippet are chosen so reviewpost.MatchPosition
// succeeds on them WITHOUT any relocation fallback -- used to prove
// ResolveAll never calls the LLM when the pure match already succeeded.
// main.go's own new-file lines run 1-3 (package main / the added
// validateBounds call / process(items[i])) -- several tests below rely on
// this exact bound to prove a relocation answer OUTSIDE it gets rejected.
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

// multiFileDiff is matchableDiff's own main.go hunk (new-file lines 1-3)
// PLUS a second file, other.go, whose own hunk covers a completely
// disjoint new-file line range (100-102) -- used to prove a relocation
// answer that names a line number belonging to other.go, while the
// finding is about main.go, gets rejected rather than written verbatim
// (Fix A's own central regression case: a relocation answer landing in
// the WRONG FILE).
const multiFileDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,2 +1,3 @@
 package main
+	validateBounds(i, items)
 	process(items[i])
diff --git a/other.go b/other.go
index 5555555..6666666 100644
--- a/other.go
+++ b/other.go
@@ -98,2 +100,3 @@
 package other
+	doSomething()
 	finish()
`

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
	out := ResolveAll(context.Background(), resolver, findings, "", platform.DefaultTimeouts())

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
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff, platform.DefaultTimeouts())

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

// TestResolveAll_FailedPureMatchTriggersResolver_OutOfBoundsAnswerRejected
// is Fix A's own central regression case (formerly this test asserted the
// out-of-range fake response, (7, 7), got used VERBATIM -- main.go's own
// new-file lines only run 1-3, see matchableDiff's own doc comment, so 7
// was never a real line in this diff at all). ResolveAll must now
// independently verify a relocation answer against the target file's own
// actual diff bounds (reviewpost.FileNewLineBounds) before trusting it,
// exactly like reviewpost.MatchPosition's own filePath argument already
// does on the pure-match path -- an out-of-bounds answer is rejected to
// (0, 0), never written through.
func TestResolveAll_FailedPureMatchTriggersResolver_OutOfBoundsAnswerRejected(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":7,"endLine":7}`)}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{newTestFinding("main.go", unmatchableSnippet)}
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff, platform.DefaultTimeouts())

	if llm.calls != 1 {
		t.Fatalf("Complete called %d times, want exactly 1 (relocation fallback for the one failed-match finding)", llm.calls)
	}
	if out[0].StartLine != 0 || out[0].EndLine != 0 {
		t.Errorf("ResolveAll() = (%d, %d), want (0, 0) -- the relocation answer (7, 7) falls outside main.go's own actual diff bounds (1-3) and must be rejected, never used verbatim", out[0].StartLine, out[0].EndLine)
	}
}

// TestResolveAll_ValidInBoundsRelocationAnswerIsUsed is the companion to
// the rejection test above: a relocation answer that DOES fall inside the
// target file's own actual diff bounds must still be used -- proving the
// new bounds check (Fix A) rejects only genuinely out-of-bounds answers,
// never a legitimately correct one.
func TestResolveAll_ValidInBoundsRelocationAnswerIsUsed(t *testing.T) {
	t.Parallel()

	// main.go's own new-file lines run 1-3 (matchableDiff's own doc
	// comment) -- 2 is a real, in-bounds line.
	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":2,"endLine":2}`)}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{newTestFinding("main.go", unmatchableSnippet)}
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff, platform.DefaultTimeouts())

	if llm.calls != 1 {
		t.Fatalf("Complete called %d times, want exactly 1", llm.calls)
	}
	if out[0].StartLine != 2 || out[0].EndLine != 2 {
		t.Errorf("ResolveAll() = (%d, %d), want (2, 2) -- a genuinely in-bounds relocation answer must be used", out[0].StartLine, out[0].EndLine)
	}
}

// TestResolveAll_RelocationAnswerPointingAtDifferentFile_RejectedToZero is
// Fix A's own multi-file regression proof: a relocation answer that names
// a line number belonging to a DIFFERENT file in the same multi-file diff
// (other.go's own new-file range, 100-102) must be rejected, even though
// the finding is about main.go -- exactly the "wrong file" failure mode
// this fix exists to close. Before this fix, ResolveAll passed the WHOLE
// multi-file diff into resolver.Resolve and assigned its answer verbatim
// with zero bounds checking, so an answer like this would have been
// written straight into the finding as a plausible-looking but entirely
// wrong position.
func TestResolveAll_RelocationAnswerPointingAtDifferentFile_RejectedToZero(t *testing.T) {
	t.Parallel()

	// 101 is a real new-file line in multiFileDiff -- but it belongs to
	// other.go (range 100-102), never to main.go (range 1-3).
	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":101,"endLine":101}`)}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{newTestFinding("main.go", unmatchableSnippet)}
	out := ResolveAll(context.Background(), resolver, findings, multiFileDiff, platform.DefaultTimeouts())

	if llm.calls != 1 {
		t.Fatalf("Complete called %d times, want exactly 1", llm.calls)
	}
	if out[0].StartLine != 0 || out[0].EndLine != 0 {
		t.Errorf("ResolveAll() = (%d, %d), want (0, 0) -- (101, 101) belongs to other.go, not main.go, and must never be written through for a main.go finding", out[0].StartLine, out[0].EndLine)
	}
}

func TestResolveAll_FailedPureMatchAndFailedRelocationStaysZero(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{err: errTransientLLM}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{newTestFinding("main.go", unmatchableSnippet)}
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff, platform.DefaultTimeouts())

	if out[0].StartLine != 0 || out[0].EndLine != 0 {
		t.Errorf("ResolveAll() = (%d, %d), want (0, 0) when both the pure match and the relocation fallback fail", out[0].StartLine, out[0].EndLine)
	}
}

func TestResolveAll_NilResolverLeavesFailedMatchesUnanchoredWithoutPanic(t *testing.T) {
	t.Parallel()

	findings := []reviewpost.Finding{newTestFinding("main.go", unmatchableSnippet)}
	out := ResolveAll(context.Background(), nil, findings, matchableDiff, platform.DefaultTimeouts())

	if out[0].StartLine != 0 || out[0].EndLine != 0 {
		t.Errorf("ResolveAll() with nil resolver = (%d, %d), want (0, 0)", out[0].StartLine, out[0].EndLine)
	}
}

// TestResolveAll_MultipleFindingsResolvedIndependently_OutOfBoundsAnswerRejected
// mirrors the single-finding rejection test above in a multi-finding
// batch: the second finding's own relocation answer (50, 50) falls
// outside main.go's own actual diff bounds (1-3) and must be rejected to
// (0, 0), never written through as 50 -- proving the bounds check applies
// independently to every finding in the batch, not just a single-element
// slice.
func TestResolveAll_MultipleFindingsResolvedIndependently_OutOfBoundsAnswerRejected(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":50,"endLine":50}`)}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	findings := []reviewpost.Finding{
		newTestFinding("main.go", matchableSnippet),   // pure match succeeds
		newTestFinding("main.go", unmatchableSnippet), // pure match fails -> relocation
	}
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff, platform.DefaultTimeouts())

	if out[0].StartLine == 0 {
		t.Errorf("first finding (pure-matchable) startLine = 0, want anchored")
	}
	if out[1].StartLine != 0 || out[1].EndLine != 0 {
		t.Errorf("second finding (relocation-only) = (%d, %d), want (0, 0) -- 50 falls outside main.go's own actual diff bounds (1-3) and must be rejected", out[1].StartLine, out[1].EndLine)
	}
	if llm.calls != 1 {
		t.Errorf("Complete called %d times, want exactly 1 (only the second finding needed relocation)", llm.calls)
	}
}

// TestResolveAll_ExhaustedAggregateBudgetSkipsRelocationCalls is Fix F's
// own regression proof: timeouts.FindingPositionResolveAllTimeout bounds
// the WHOLE relocation-fallback loop, not any single call -- an already-
// exhausted aggregate budget (deliberately negative here, so the derived
// context is guaranteed already expired at creation, never a timing-
// dependent race) must skip the relocation call ENTIRELY, leaving that
// finding at its own honest (0, 0) pure-match result rather than blocking
// on (or ever even attempting) a doomed LLM call. The pure-match step
// itself (no I/O) is unaffected: the first, pure-matchable finding is
// still anchored normally.
func TestResolveAll_ExhaustedAggregateBudgetSkipsRelocationCalls(t *testing.T) {
	t.Parallel()

	llm := &fakeLLM{response: json.RawMessage(`{"found":true,"startLine":2,"endLine":2}`)}
	resolver := New(llm, "anthropic", "claude-haiku-4-5")

	timeouts := platform.DefaultTimeouts()
	timeouts.FindingPositionResolveAllTimeout = -1 * time.Second

	findings := []reviewpost.Finding{
		newTestFinding("main.go", matchableSnippet),   // pure match succeeds regardless of the relocation budget
		newTestFinding("main.go", unmatchableSnippet), // pure match fails; relocation would be needed, but the budget is already exhausted
	}
	out := ResolveAll(context.Background(), resolver, findings, matchableDiff, timeouts)

	if out[0].StartLine == 0 {
		t.Errorf("first finding (pure-matchable) startLine = 0, want anchored -- the pure-match step must be unaffected by an exhausted relocation budget")
	}
	if out[1].StartLine != 0 || out[1].EndLine != 0 {
		t.Errorf("second finding = (%d, %d), want (0, 0) -- the aggregate relocation budget was already exhausted, so relocation must never even be attempted", out[1].StartLine, out[1].EndLine)
	}
	if llm.calls != 0 {
		t.Errorf("Complete called %d times, want 0 -- an already-exhausted aggregate budget must skip the relocation call entirely, not attempt and let it fail", llm.calls)
	}
}

// errTransientLLM is a small stand-in typed error mirroring
// *ports.LLMError's own shape for tests that only care that Complete
// returned SOME error.
var errTransientLLM = &llmErrStub{}

type llmErrStub struct{}

func (e *llmErrStub) Error() string { return "stub transient llm error" }
