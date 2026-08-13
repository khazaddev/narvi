package reviewcontext_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/app/reviewcontext"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/platform"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeFetcher is a test-only reviewcontext.Fetcher -- no real HTTP round
// trip, exactly the point of that interface being narrow and locally
// defined (fetch.go's own doc comment).
//
// §62 review finding C2 (CRITICAL, fixed): GetCompareDiff replaces the
// PREVIOUS GetPullRequestDiff here, and callOrder (below) is this file's
// own NEW assertion surface -- proving GetPullRequest always runs BEFORE
// GetCompareDiff, and that the diff fetch is PINNED to exactly what
// GetPullRequest reported (never an independently-supplied value), the
// two properties the whole fix depends on.
type fakeFetcher struct {
	pr       githubapi.PullRequest
	prErr    error
	prCalls  int
	prOwner  string
	prRepo   string
	prNumber int32
	prToken  string

	diff          string
	diffTruncated bool
	diffErr       error
	diffCalls     int
	diffOwner     string
	diffRepo      string
	diffBase      string
	diffHead      string
	diffToken     string

	// callOrder records each method invoked, in order ("pr", "diff") --
	// asserted directly by TestFetch_Success_CallOrderIsPRThenDiff to pin
	// that GetPullRequest always resolves BEFORE the diff fetch is even
	// attempted (never the reverse, and never interleaved/concurrent).
	callOrder []string
}

func (f *fakeFetcher) GetPullRequest(_ context.Context, owner, repo string, number int32, token string) (githubapi.PullRequest, error) {
	f.prCalls++
	f.prOwner, f.prRepo, f.prNumber, f.prToken = owner, repo, number, token
	f.callOrder = append(f.callOrder, "pr")
	return f.pr, f.prErr
}

func (f *fakeFetcher) GetCompareDiff(_ context.Context, owner, repo, base, head, token string) (string, bool, error) {
	f.diffCalls++
	f.diffOwner, f.diffRepo, f.diffBase, f.diffHead, f.diffToken = owner, repo, base, head, token
	f.callOrder = append(f.callOrder, "diff")
	return f.diff, f.diffTruncated, f.diffErr
}

func assertPRArgs(t *testing.T, f *fakeFetcher, wantOwner, wantRepo string, wantNumber int32, wantToken string) {
	t.Helper()
	if f.prOwner != wantOwner || f.prRepo != wantRepo || f.prNumber != wantNumber || f.prToken != wantToken {
		t.Errorf("GetPullRequest args = (%q, %q, %d, %q), want (%q, %q, %d, %q)",
			f.prOwner, f.prRepo, f.prNumber, f.prToken, wantOwner, wantRepo, wantNumber, wantToken)
	}
}

// assertDiffArgs checks the EXACT (owner, repo, base, head, token)
// GetCompareDiff was invoked with -- wantBase/wantHead are deliberately
// checked here (never hardcoded by a caller) so a regression that pins
// the diff fetch to the WRONG commit (e.g. a stale knownHeadSHA, or the
// wrong PR's own base) fails this assertion specifically.
func assertDiffArgs(t *testing.T, f *fakeFetcher, wantOwner, wantRepo, wantBase, wantHead, wantToken string) {
	t.Helper()
	if f.diffOwner != wantOwner || f.diffRepo != wantRepo || f.diffBase != wantBase || f.diffHead != wantHead || f.diffToken != wantToken {
		t.Errorf("GetCompareDiff args = (%q, %q, base=%q, head=%q, %q), want (%q, %q, base=%q, head=%q, %q)",
			f.diffOwner, f.diffRepo, f.diffBase, f.diffHead, f.diffToken, wantOwner, wantRepo, wantBase, wantHead, wantToken)
	}
}

// TestFetch_Success_DiffPinnedToExactlyWhatGetPullRequestReported is the
// C2 regression test (§62 review, CRITICAL, fixed) at the unit level: the
// core atomicity property this whole fix exists to provide -- the diff
// fetch (GetCompareDiff) is parametrized by EXACTLY pr.BaseRef/pr.HeadSHA,
// the SAME values this call returns as HeadSHA, never a second,
// independently-suppliable value that could disagree.
func TestFetch_Success_DiffPinnedToExactlyWhatGetPullRequestReported(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		pr:   githubapi.PullRequest{HeadRef: "feature-x", HeadSHA: "resolved-head-sha", BaseRef: "main", Title: "Fix the retry loop", Body: "Retries now back off exponentially."},
		diff: "diff --git a/x b/x\n",
	}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.Diff != "diff --git a/x b/x\n" {
		t.Errorf("Diff = %q, want the fetched diff", got.Diff)
	}
	if got.DiffTruncated {
		t.Error("DiffTruncated = true, want false")
	}
	if got.Stack != nil {
		t.Errorf("Stack = %+v, want nil (PR reported no stack)", got.Stack)
	}
	if got.HeadSHA != "resolved-head-sha" {
		t.Errorf("HeadSHA = %q, want %q", got.HeadSHA, "resolved-head-sha")
	}
	if got.Title != "Fix the retry loop" {
		t.Errorf("Title = %q, want %q", got.Title, "Fix the retry loop")
	}
	if got.Body != "Retries now back off exponentially." {
		t.Errorf("Body = %q, want %q", got.Body, "Retries now back off exponentially.")
	}
	if fetcher.prCalls != 1 {
		t.Errorf("prCalls = %d, want 1", fetcher.prCalls)
	}
	if fetcher.diffCalls != 1 {
		t.Errorf("diffCalls = %d, want 1", fetcher.diffCalls)
	}
	assertPRArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
	// THE core assertion: GetCompareDiff's own base/head args are EXACTLY
	// pr.BaseRef/pr.HeadSHA -- proving the diff is pinned to what
	// GetPullRequest reported, never an independent value.
	assertDiffArgs(t, fetcher, "acme", "widgets", "main", "resolved-head-sha", "gho_bottoken")
}

// TestFetch_Success_CallOrderIsPRThenDiff pins that GetPullRequest always
// resolves BEFORE GetCompareDiff is even attempted -- the ordering the
// whole atomicity fix depends on (fetch.go's own doc comment: "resolve
// pr.HeadSHA ... FIRST, then fetch the diff ... PINNED to that exact
// pair").
func TestFetch_Success_CallOrderIsPRThenDiff(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{pr: githubapi.PullRequest{HeadSHA: "sha", BaseRef: "main"}, diff: "d"}
	reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	want := []string{"pr", "diff"}
	if len(fetcher.callOrder) != len(want) {
		t.Fatalf("callOrder = %v, want %v", fetcher.callOrder, want)
	}
	for i := range want {
		if fetcher.callOrder[i] != want[i] {
			t.Errorf("callOrder = %v, want %v", fetcher.callOrder, want)
			break
		}
	}
}

// TestFetch_GetPullRequestAlwaysCalled_EvenWithKnownStack is the
// deliberate-tradeoff regression test named in fetch.go's own doc
// comment: UNLIKE the previous version of this function, a caller-
// supplied knownStack no longer skips the GetPullRequest call --
// correctness (a provably-pinned diff) now requires it unconditionally,
// since pr.BaseRef was never available from that old shortcut anyway.
func TestFetch_GetPullRequestAlwaysCalled_EvenWithKnownStack(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{pr: githubapi.PullRequest{HeadSHA: "sha", BaseRef: "main"}, diff: "d"}
	known := &review.StackContext{Position: 1, Size: 2, UltimateBaseRef: "main", UltimateBaseSHA: "abc123"}

	reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", known)

	if fetcher.prCalls != 1 {
		t.Errorf("prCalls = %d, want 1 -- GetPullRequest must run even when knownStack is already supplied (this function's own doc comment: correctness now requires this call unconditionally)", fetcher.prCalls)
	}
}

// TestFetch_KnownStackPreferredOverPRStack proves knownStack, when
// supplied, is used AS-IS (exact pointer) rather than re-derived from
// this call's own pr.Stack -- even when pr.Stack reports a DIFFERENT
// stack, proving preference, not a coincidental match.
func TestFetch_KnownStackPreferredOverPRStack(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		pr: githubapi.PullRequest{
			HeadSHA: "sha", BaseRef: "main",
			Stack: &githubapi.StackInfo{Position: 9, Size: 9, BaseRef: "other", BaseSHA: "other-sha"},
		},
		diff: "d",
	}
	known := &review.StackContext{Position: 1, Size: 2, UltimateBaseRef: "main", UltimateBaseSHA: "abc123"}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", known)

	if got.Stack != known {
		t.Errorf("Stack = %+v, want the exact knownStack pointer %+v (never overwritten by pr.Stack)", got.Stack, known)
	}
}

// TestFetch_NoKnownStack_DerivesFromPRStack proves a fresh GetPullRequest
// call reporting a real stack is converted into review.StackContext
// correctly when no knownStack was supplied.
func TestFetch_NoKnownStack_DerivesFromPRStack(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		pr: githubapi.PullRequest{
			HeadSHA: "sha", BaseRef: "main",
			Stack: &githubapi.StackInfo{Position: 2, Size: 3, BaseRef: "main", BaseSHA: "deadbeef"},
		},
		diff: "d",
	}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.Stack == nil {
		t.Fatal("Stack = nil, want non-nil when GetPullRequest reports one")
	}
	want := review.StackContext{Position: 2, Size: 3, UltimateBaseRef: "main", UltimateBaseSHA: "deadbeef"}
	if *got.Stack != want {
		t.Errorf("Stack = %+v, want %+v", *got.Stack, want)
	}
}

// TestFetch_NoKnownStack_NoPRStack_StaysNil proves the ordinary,
// non-stacked-PR case: neither knownStack nor pr.Stack present, Stack
// stays nil.
func TestFetch_NoKnownStack_NoPRStack_StaysNil(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{pr: githubapi.PullRequest{HeadSHA: "sha", BaseRef: "main"}, diff: "d"}
	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.Stack != nil {
		t.Errorf("Stack = %+v, want nil", got.Stack)
	}
}

// TestFetch_GetPullRequestFails_DiffNeverAttempted_OnlyKnownStackSurvives
// proves a GetPullRequest failure degrades gracefully -- Diff/HeadSHA
// both stay empty (nothing safe to pin a diff fetch to, nothing safe to
// persist as review_verdicts.head_sha), GetCompareDiff is NEVER even
// attempted (there is no head sha to pin it to), and knownStack -- when
// the caller already had it, at zero cost -- still survives.
func TestFetch_GetPullRequestFails_DiffNeverAttempted_OnlyKnownStackSurvives(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{prErr: errors.New("network exploded")}
	known := &review.StackContext{Position: 1, Size: 2, UltimateBaseRef: "main", UltimateBaseSHA: "abc123"}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", known)

	if got.Diff != "" {
		t.Errorf("Diff = %q, want empty on a GetPullRequest failure", got.Diff)
	}
	if got.DiffTruncated {
		t.Error("DiffTruncated = true, want false")
	}
	if got.HeadSHA != "" {
		t.Errorf("HeadSHA = %q, want empty on a GetPullRequest failure", got.HeadSHA)
	}
	if got.Stack != known {
		t.Errorf("Stack = %+v, want the exact knownStack pointer %+v (a caller-supplied value costs nothing to keep)", got.Stack, known)
	}
	if fetcher.diffCalls != 0 {
		t.Errorf("diffCalls = %d, want 0 -- GetCompareDiff must never be attempted with no confirmed head sha to pin it to", fetcher.diffCalls)
	}
}

// TestFetch_DiffFetchFails_HeadSHAStillReported proves the diff fetch
// failing independently of the (already-succeeded) GetPullRequest call:
// Diff/DiffTruncated degrade to their own zero value, but HeadSHA (and
// Stack) are UNAFFECTED -- pr.HeadSHA is still an honest fact about the
// PR's real head even when the diff transfer itself failed (fetch.go's
// own doc comment: "HeadSHA is reported here regardless of whether the
// diff fetch above itself succeeded").
func TestFetch_DiffFetchFails_HeadSHAStillReported(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		pr:      githubapi.PullRequest{HeadSHA: "resolved-head-sha", BaseRef: "main"},
		diffErr: errors.New("network exploded"),
	}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.Diff != "" {
		t.Errorf("Diff = %q, want empty on a diff-fetch failure", got.Diff)
	}
	if got.DiffTruncated {
		t.Error("DiffTruncated = true, want false")
	}
	if got.HeadSHA != "resolved-head-sha" {
		t.Errorf("HeadSHA = %q, want %q -- a diff-fetch failure must not erase an already-confirmed head sha", got.HeadSHA, "resolved-head-sha")
	}
}

// TestFetch_TitleBodyThreadedThrough_EvenWhenDiffFetchFails is the
// adversarial-review fix's own regression test (§26.2/Step 67's own
// follow-up, review.PreFetchedContext.Title's own doc comment): Title/Body
// come from the SAME already-succeeded GetPullRequest call HeadSHA itself
// is resolved from, so a LATER diff-fetch failure must not erase them --
// mirrors TestFetch_DiffFetchFails_HeadSHAStillReported's own identical
// "HeadSHA/Title/Body are independent of the diff fetch's own outcome"
// property.
func TestFetch_TitleBodyThreadedThrough_EvenWhenDiffFetchFails(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		pr:      githubapi.PullRequest{HeadSHA: "resolved-head-sha", BaseRef: "main", Title: "Fix the retry loop", Body: "Retries now back off exponentially."},
		diffErr: errors.New("network exploded"),
	}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.Title != "Fix the retry loop" {
		t.Errorf("Title = %q, want %q -- a diff-fetch failure must not erase an already-fetched title", got.Title, "Fix the retry loop")
	}
	if got.Body != "Retries now back off exponentially." {
		t.Errorf("Body = %q, want %q -- a diff-fetch failure must not erase an already-fetched body", got.Body, "Retries now back off exponentially.")
	}
}

// TestFetch_GetPullRequestFails_TitleBodyStayEmpty proves the SAME
// graceful-degradation precedent Diff/HeadSHA already establish
// (TestFetch_GetPullRequestFails_DiffNeverAttempted_OnlyKnownStackSurvives)
// extends to Title/Body: a GetPullRequest failure leaves them at their own
// honest empty zero value, never a stale or fabricated value.
func TestFetch_GetPullRequestFails_TitleBodyStayEmpty(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{prErr: errors.New("network exploded")}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.Title != "" {
		t.Errorf("Title = %q, want empty on a GetPullRequest failure", got.Title)
	}
	if got.Body != "" {
		t.Errorf("Body = %q, want empty on a GetPullRequest failure", got.Body)
	}
}

// TestFetch_DiffTruncated proves DiffTruncated is carried through verbatim
// on a successful-but-capped fetch.
func TestFetch_DiffTruncated(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		pr:            githubapi.PullRequest{HeadSHA: "sha", BaseRef: "main"},
		diff:          "partial diff...",
		diffTruncated: true,
	}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if !got.DiffTruncated {
		t.Error("DiffTruncated = false, want true")
	}
	if got.Diff != "partial diff..." {
		t.Errorf("Diff = %q, want %q", got.Diff, "partial diff...")
	}
}

// TestFetch_DiffFetchFails_TruncatedIgnored proves diffTruncated is
// ignored (never surfaced) once diffErr fired -- mirrors this function's
// own pre-existing "an error return's OTHER values carry no signal"
// discipline.
func TestFetch_DiffFetchFails_TruncatedIgnored(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		pr:            githubapi.PullRequest{HeadSHA: "sha", BaseRef: "main"},
		diffErr:       errors.New("network exploded"),
		diffTruncated: true, // must be ignored on error
	}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.DiffTruncated {
		t.Error("DiffTruncated = true, want false (the diffTruncated=true fake field must be ignored once diffErr fired)")
	}
}
