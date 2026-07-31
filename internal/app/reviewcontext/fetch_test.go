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
// diffOwner/diffRepo/diffNumber/diffToken and prOwner/prRepo/prNumber/
// prToken (audit fix, test-coverage finding) record the exact arguments
// each method was actually called with -- every test below now asserts
// against these, closing a confirmed gap where a swapped-argument
// regression in fetch.go's own two call sites (e.g. owner/repo transposed)
// would previously pass every test in this file undetected, since neither
// method used to consult its own arguments at all.
type fakeFetcher struct {
	diff          string
	diffTruncated bool
	diffErr       error
	diffCalls     int
	diffOwner     string
	diffRepo      string
	diffNumber    int32
	diffToken     string

	pr       githubapi.PullRequest
	prErr    error
	prCalls  int
	prOwner  string
	prRepo   string
	prNumber int32
	prToken  string
}

func (f *fakeFetcher) GetPullRequestDiff(_ context.Context, owner, repo string, number int32, token string) (string, bool, error) {
	f.diffCalls++
	f.diffOwner, f.diffRepo, f.diffNumber, f.diffToken = owner, repo, number, token
	return f.diff, f.diffTruncated, f.diffErr
}

func (f *fakeFetcher) GetPullRequest(_ context.Context, owner, repo string, number int32, token string) (githubapi.PullRequest, error) {
	f.prCalls++
	f.prOwner, f.prRepo, f.prNumber, f.prToken = owner, repo, number, token
	return f.pr, f.prErr
}

// assertDiffArgs/assertPRArgs (audit fix, test-coverage finding) check the
// EXACT (owner, repo, number, token) each fake method was invoked with --
// owner and repo are deliberately distinguishable strings in every test
// below ("acme" vs "widgets"), so a caller-side owner/repo transposition
// in fetch.go's own two call sites would fail these assertions, closing
// the confirmed blind spot where neither fake used to consult its own
// arguments at all.
func assertDiffArgs(t *testing.T, f *fakeFetcher, wantOwner, wantRepo string, wantNumber int32, wantToken string) {
	t.Helper()
	if f.diffOwner != wantOwner || f.diffRepo != wantRepo || f.diffNumber != wantNumber || f.diffToken != wantToken {
		t.Errorf("GetPullRequestDiff args = (%q, %q, %d, %q), want (%q, %q, %d, %q)",
			f.diffOwner, f.diffRepo, f.diffNumber, f.diffToken, wantOwner, wantRepo, wantNumber, wantToken)
	}
}

func assertPRArgs(t *testing.T, f *fakeFetcher, wantOwner, wantRepo string, wantNumber int32, wantToken string) {
	t.Helper()
	if f.prOwner != wantOwner || f.prRepo != wantRepo || f.prNumber != wantNumber || f.prToken != wantToken {
		t.Errorf("GetPullRequest args = (%q, %q, %d, %q), want (%q, %q, %d, %q)",
			f.prOwner, f.prRepo, f.prNumber, f.prToken, wantOwner, wantRepo, wantNumber, wantToken)
	}
}

// TestFetch_DiffSuccess_NoKnownStack_NoStackOnPR proves the ordinary case:
// diff fetched successfully, no knownStack supplied, GetPullRequest called
// once to check for a stack and reports none -- Stack stays nil.
func TestFetch_DiffSuccess_NoKnownStack_NoStackOnPR(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{diff: "diff --git a/x b/x\n", pr: githubapi.PullRequest{HeadRef: "feature-x"}}

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
	if fetcher.diffCalls != 1 {
		t.Errorf("diffCalls = %d, want 1", fetcher.diffCalls)
	}
	if fetcher.prCalls != 1 {
		t.Errorf("prCalls = %d, want 1 (no knownStack supplied, so Fetch must look it up)", fetcher.prCalls)
	}
	assertDiffArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
	assertPRArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
}

// TestFetch_KnownStackShortCircuitsGetPullRequest proves a caller-supplied
// knownStack (the label-retrigger webhook path, which already has GitHub's
// own stack object inline in its payload, §17.6) is used AS-IS with NO
// second GetPullRequest call -- the whole point of threading it through.
func TestFetch_KnownStackShortCircuitsGetPullRequest(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{diff: "d"}
	known := &review.StackContext{Position: 1, Size: 2, UltimateBaseRef: "main", UltimateBaseSHA: "abc123"}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", known)

	if got.Stack != known {
		t.Errorf("Stack = %+v, want the exact knownStack pointer %+v", got.Stack, known)
	}
	if fetcher.prCalls != 0 {
		t.Errorf("prCalls = %d, want 0 (knownStack supplied, GetPullRequest must never be called)", fetcher.prCalls)
	}
	assertDiffArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
}

// TestFetch_StackPresentOnPR proves a fresh GetPullRequest call reporting a
// real stack is converted into review.StackContext correctly.
func TestFetch_StackPresentOnPR(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		diff: "d",
		pr: githubapi.PullRequest{
			HeadRef: "feature-x",
			Stack:   &githubapi.StackInfo{Position: 2, Size: 3, BaseRef: "main", BaseSHA: "deadbeef"},
		},
	}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.Stack == nil {
		t.Fatal("Stack = nil, want non-nil when GetPullRequest reports one")
	}
	want := review.StackContext{Position: 2, Size: 3, UltimateBaseRef: "main", UltimateBaseSHA: "deadbeef"}
	if *got.Stack != want {
		t.Errorf("Stack = %+v, want %+v", *got.Stack, want)
	}
	assertDiffArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
	assertPRArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
}

// TestFetch_DiffFetchFails_DegradesGracefully proves a diff-fetch failure
// never propagates as an error -- Fetch has no error return at all (its own
// doc comment); this test proves the degraded VALUE (empty diff, not
// truncated) rather than a panic or a stuck partial value.
func TestFetch_DiffFetchFails_DegradesGracefully(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{diffErr: errors.New("network exploded"), diffTruncated: true /* must be ignored on error */}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.Diff != "" {
		t.Errorf("Diff = %q, want empty on a fetch failure", got.Diff)
	}
	if got.DiffTruncated {
		t.Error("DiffTruncated = true, want false on a fetch failure (the diffTruncated=true fake field must be ignored once diffErr fired)")
	}
	assertDiffArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
}

// TestFetch_StackLookupFails_DegradesGracefully proves a GetPullRequest
// failure (while resolving stack context) never propagates as an error --
// Stack simply stays nil, diff is unaffected.
func TestFetch_StackLookupFails_DegradesGracefully(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{diff: "d", prErr: errors.New("network exploded")}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if got.Diff != "d" {
		t.Errorf("Diff = %q, want %q (a stack-lookup failure must not affect the already-fetched diff)", got.Diff, "d")
	}
	if got.Stack != nil {
		t.Errorf("Stack = %+v, want nil on a lookup failure", got.Stack)
	}
	assertDiffArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
	assertPRArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
}

// TestFetch_DiffTruncated proves DiffTruncated is carried through verbatim
// on a successful-but-capped fetch.
func TestFetch_DiffTruncated(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{diff: "partial diff...", diffTruncated: true}

	got := reviewcontext.Fetch(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", nil)

	if !got.DiffTruncated {
		t.Error("DiffTruncated = false, want true")
	}
	if got.Diff != "partial diff..." {
		t.Errorf("Diff = %q, want %q", got.Diff, "partial diff...")
	}
	assertDiffArgs(t, fetcher, "acme", "widgets", 42, "gho_bottoken")
}
