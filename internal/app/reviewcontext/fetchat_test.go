package reviewcontext_test

import (
	"context"
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/githubapi"
	"github.com/narvidev/narvi/internal/app/reviewcontext"
	"github.com/narvidev/narvi/internal/platform"
)

// TestFetchDiffAt_PinnedToGivenHeadSHA_NotPRCurrentHead is this file's own
// central proof: FetchDiffAt must pin the compare-diff fetch to the
// CALLER-SUPPLIED headSHA (the review turn's own historical
// turns.review_head_sha), never to pr.HeadSHA -- even when a fresh
// GetPullRequest call reports the PR has since moved on to a DIFFERENT,
// newer head. Without this, a verdict posted after a mid-review push
// would silently anchor findings against the WRONG diff.
func TestFetchDiffAt_PinnedToGivenHeadSHA_NotPRCurrentHead(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		// The PR's CURRENT head (as of this fresh GetPullRequest call) has
		// already moved on -- a later push landed after the review turn
		// that produced this verdict was dispatched.
		pr:   githubapi.PullRequest{HeadSHA: "brand-new-head-since-review-ran", BaseRef: "main"},
		diff: "diff --git a/x b/x\n",
	}

	diff, ok := reviewcontext.FetchDiffAt(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", "the-sha-the-review-turn-actually-saw")
	if !ok {
		t.Fatalf("FetchDiffAt() ok = false, want true")
	}
	if diff != "diff --git a/x b/x\n" {
		t.Errorf("diff = %q, want the fetched diff", diff)
	}

	assertDiffArgs(t, fetcher, "acme", "widgets", "main", "the-sha-the-review-turn-actually-saw", "gho_bottoken")
	if fetcher.diffHead == "brand-new-head-since-review-ran" {
		t.Fatal("GetCompareDiff was pinned to the PR's CURRENT head, not the caller-supplied historical headSHA")
	}
}

func TestFetchDiffAt_EmptyHeadSHA_ReturnsFalseWithoutCallingFetcher(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{pr: githubapi.PullRequest{HeadSHA: "sha", BaseRef: "main"}, diff: "d"}

	diff, ok := reviewcontext.FetchDiffAt(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", "")
	if ok {
		t.Fatalf("FetchDiffAt() ok = true, want false for an empty headSHA")
	}
	if diff != "" {
		t.Errorf("diff = %q, want empty", diff)
	}
	if fetcher.prCalls != 0 || fetcher.diffCalls != 0 {
		t.Errorf("prCalls=%d diffCalls=%d, want 0/0 -- an empty headSHA must short-circuit before any network call", fetcher.prCalls, fetcher.diffCalls)
	}
}

func TestFetchDiffAt_GetPullRequestFails_ReturnsFalse(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{prErr: errors.New("network exploded")}

	diff, ok := reviewcontext.FetchDiffAt(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", "some-sha")
	if ok {
		t.Fatalf("FetchDiffAt() ok = true, want false when GetPullRequest fails")
	}
	if diff != "" {
		t.Errorf("diff = %q, want empty", diff)
	}
	if fetcher.diffCalls != 0 {
		t.Errorf("diffCalls = %d, want 0 -- GetCompareDiff must never be attempted with no confirmed base ref", fetcher.diffCalls)
	}
}

func TestFetchDiffAt_GetCompareDiffFails_ReturnsFalse(t *testing.T) {
	t.Parallel()

	fetcher := &fakeFetcher{
		pr:      githubapi.PullRequest{HeadSHA: "current-head", BaseRef: "main"},
		diffErr: errors.New("network exploded"),
	}

	diff, ok := reviewcontext.FetchDiffAt(context.Background(), discardLogger(), fetcher, platform.DefaultTimeouts(), "acme", "widgets", 42, "gho_bottoken", "some-sha")
	if ok {
		t.Fatalf("FetchDiffAt() ok = true, want false when GetCompareDiff fails")
	}
	if diff != "" {
		t.Errorf("diff = %q, want empty", diff)
	}
}
