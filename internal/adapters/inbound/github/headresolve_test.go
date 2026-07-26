package github

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
)

// discardLogger is a *slog.Logger that writes nowhere -- every test below
// only cares about resolveIssueCommentHead's own RETURN value, never its
// log output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakePullRequestResolver is a test-only PullRequestResolver -- no real
// HTTP round trip, exactly the point of headresolve.go's own
// PullRequestResolver interface being narrow and locally defined.
type fakePullRequestResolver struct {
	pr        githubapi.PullRequest
	err       error
	gotOwner  string
	gotRepo   string
	gotNumber int32
	gotToken  string
	calls     int
}

func (f *fakePullRequestResolver) GetPullRequest(_ context.Context, owner, repo string, number int32, token string) (githubapi.PullRequest, error) {
	f.calls++
	f.gotOwner = owner
	f.gotRepo = repo
	f.gotNumber = number
	f.gotToken = token
	return f.pr, f.err
}

func baseIssueCommentMention() mention {
	headBranch := (*string)(nil)
	return mention{
		RepoFullName: "acme/widgets",
		RepoName:     "widgets",
		RepoCloneURL: "https://github.com/acme/widgets.git",
		PRNumber:     42,
		HeadBranch:   headBranch,
		CommentBody:  "@narvi-bot please review",
	}
}

// TestResolveIssueCommentHead_Success proves a successful GetPullRequest
// call resolves the mention's REAL head branch AND head repo -- the H5
// audit fix's own headline case: a mention on the Conversation tab of a PR
// whose head branch differs from the base repo's default branch resolves
// to that REAL head branch, not nil/default.
func TestResolveIssueCommentHead_Success(t *testing.T) {
	resolver := &fakePullRequestResolver{
		pr: githubapi.PullRequest{
			HeadRef:          "feature-x",
			HeadRepoName:     "widgets",
			HeadRepoCloneURL: "https://github.com/contributor/widgets.git",
		},
	}

	got := resolveIssueCommentHead(context.Background(), discardLogger(), resolver, "gho_bottoken", baseIssueCommentMention())

	if resolver.calls != 1 {
		t.Fatalf("GetPullRequest call count = %d, want 1", resolver.calls)
	}
	if resolver.gotOwner != "acme" || resolver.gotRepo != "widgets" {
		t.Errorf("GetPullRequest(owner, repo) = (%q, %q), want (%q, %q)", resolver.gotOwner, resolver.gotRepo, "acme", "widgets")
	}
	if resolver.gotNumber != 42 {
		t.Errorf("GetPullRequest number = %d, want 42", resolver.gotNumber)
	}
	if resolver.gotToken != "gho_bottoken" {
		t.Errorf("GetPullRequest token = %q, want %q", resolver.gotToken, "gho_bottoken")
	}

	if got.HeadBranch == nil || *got.HeadBranch != "feature-x" {
		t.Errorf("HeadBranch = %v, want %q (the REAL head branch, not nil/default)", got.HeadBranch, "feature-x")
	}
	if got.RepoName != "widgets" || got.RepoCloneURL != "https://github.com/contributor/widgets.git" {
		t.Errorf("RepoName/RepoCloneURL = %q/%q, want the PR's real head repo %q/%q",
			got.RepoName, got.RepoCloneURL, "widgets", "https://github.com/contributor/widgets.git")
	}
}

// TestResolveIssueCommentHead_APIFailureFallsBack proves a failed
// GetPullRequest call (network error, non-2xx, timeout) falls back to
// today's PRE-fix behavior -- the mention unchanged (HeadBranch nil, base
// repo) -- rather than dropping the mention or failing the whole webhook
// delivery.
func TestResolveIssueCommentHead_APIFailureFallsBack(t *testing.T) {
	resolver := &fakePullRequestResolver{err: errors.New("boom: connection refused")}

	m := baseIssueCommentMention()
	got := resolveIssueCommentHead(context.Background(), discardLogger(), resolver, "gho_bottoken", m)

	if got.HeadBranch != nil {
		t.Errorf("HeadBranch = %v, want nil (API call failed -- fall back to base repo's own default branch)", *got.HeadBranch)
	}
	if got.RepoName != m.RepoName || got.RepoCloneURL != m.RepoCloneURL {
		t.Errorf("RepoName/RepoCloneURL = %q/%q, want unchanged base repo %q/%q", got.RepoName, got.RepoCloneURL, m.RepoName, m.RepoCloneURL)
	}
}

// TestResolveIssueCommentHead_NilResolverFallsBack proves a nil resolver
// (no SourceControl wired -- e.g. handler_test.go's own minimal Config)
// falls back silently, with no call attempted at all.
func TestResolveIssueCommentHead_NilResolverFallsBack(t *testing.T) {
	m := baseIssueCommentMention()
	got := resolveIssueCommentHead(context.Background(), discardLogger(), nil, "gho_bottoken", m)

	if got.HeadBranch != nil {
		t.Errorf("HeadBranch = %v, want nil (resolver == nil)", *got.HeadBranch)
	}
	if got.RepoName != m.RepoName || got.RepoCloneURL != m.RepoCloneURL {
		t.Errorf("RepoName/RepoCloneURL changed with a nil resolver, want unchanged")
	}
}

// TestResolveIssueCommentHead_EmptyHeadRefFallsBack proves a successful
// but degenerate GetPullRequest response (empty head ref -- should never
// happen for a real, open GitHub PR, but not assumed away) falls back
// rather than setting HeadBranch to an empty string.
func TestResolveIssueCommentHead_EmptyHeadRefFallsBack(t *testing.T) {
	resolver := &fakePullRequestResolver{pr: githubapi.PullRequest{HeadRef: ""}}

	m := baseIssueCommentMention()
	got := resolveIssueCommentHead(context.Background(), discardLogger(), resolver, "gho_bottoken", m)

	if got.HeadBranch != nil {
		t.Errorf("HeadBranch = %v, want nil (empty head ref reported)", *got.HeadBranch)
	}
}

// TestResolveIssueCommentHead_NullHeadRepoKeepsBaseRepo proves the L15
// "deleted fork" case is handled on this path too: a successful
// GetPullRequest call that reports a real head ref but an empty head repo
// (GitHub's own head.repo was null) still resolves HeadBranch, but leaves
// RepoName/RepoCloneURL as the base repo parseIssueComment already set --
// never an empty repo spec.
func TestResolveIssueCommentHead_NullHeadRepoKeepsBaseRepo(t *testing.T) {
	resolver := &fakePullRequestResolver{pr: githubapi.PullRequest{HeadRef: "feature-x"}}

	m := baseIssueCommentMention()
	got := resolveIssueCommentHead(context.Background(), discardLogger(), resolver, "gho_bottoken", m)

	if got.HeadBranch == nil || *got.HeadBranch != "feature-x" {
		t.Errorf("HeadBranch = %v, want %q", got.HeadBranch, "feature-x")
	}
	if got.RepoName != m.RepoName || got.RepoCloneURL != m.RepoCloneURL {
		t.Errorf("RepoName/RepoCloneURL = %q/%q, want unchanged base repo %q/%q (head.repo was null)",
			got.RepoName, got.RepoCloneURL, m.RepoName, m.RepoCloneURL)
	}
}

// TestResolveIssueCommentHead_UnsplittableRepoFullNameFallsBack proves a
// defensively-handled, never-expected-from-real-GitHub malformed
// RepoFullName (no "/") falls back rather than panicking or calling
// GetPullRequest with garbage owner/repo.
func TestResolveIssueCommentHead_UnsplittableRepoFullNameFallsBack(t *testing.T) {
	resolver := &fakePullRequestResolver{pr: githubapi.PullRequest{HeadRef: "feature-x"}}

	m := baseIssueCommentMention()
	m.RepoFullName = "not-a-valid-full-name"
	got := resolveIssueCommentHead(context.Background(), discardLogger(), resolver, "gho_bottoken", m)

	if resolver.calls != 0 {
		t.Errorf("GetPullRequest call count = %d, want 0 (never called with an unsplittable repo_full_name)", resolver.calls)
	}
	if got.HeadBranch != nil {
		t.Errorf("HeadBranch = %v, want nil", *got.HeadBranch)
	}
}

func TestSplitOwnerRepo(t *testing.T) {
	tests := []struct {
		name      string
		fullName  string
		wantOwner string
		wantRepo  string
		wantOK    bool
	}{
		{name: "well-formed", fullName: "acme/widgets", wantOwner: "acme", wantRepo: "widgets", wantOK: true},
		{name: "no slash", fullName: "widgets", wantOK: false},
		{name: "empty owner", fullName: "/widgets", wantOK: false},
		{name: "empty repo", fullName: "acme/", wantOK: false},
		{name: "empty string", fullName: "", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, ok := splitOwnerRepo(tc.fullName)
			if ok != tc.wantOK {
				t.Fatalf("splitOwnerRepo(%q) ok = %v, want %v", tc.fullName, ok, tc.wantOK)
			}
			if ok && (owner != tc.wantOwner || repo != tc.wantRepo) {
				t.Errorf("splitOwnerRepo(%q) = (%q, %q), want (%q, %q)", tc.fullName, owner, repo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}
