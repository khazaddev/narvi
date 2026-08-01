package github

import (
	"context"
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file is a WHITE-BOX (package github, not github_test) unit test
// file -- no Postgres, no build tag -- covering two of this Step's own
// confirmed re-review findings that are both pure functions/narrow units,
// never needing a real database:
//
//   - parseChangedFilesFromDiff's own rename-blindness fix (a "high"
//     finding): a pure git-rename (100% similarity, zero content change)
//     produces NEITHER a "+++" NOR a "---" line at all, so relying on
//     "+++ " alone left this parser -- and therefore firstNonTestOrDocPath/
//     EvaluateMergeGate, the §17.4 backstop this Step ships as
//     "independent of, and never assuming, §17.2's own spawn-time
//     capability restriction" -- blind to a sentinel-fix session's own
//     bash tool renaming a real production file onto a test/doc-looking
//     path with no content change.
//   - githubMergeGateDataSource.StackRegistered (a "low" finding): must
//     always be a FRESH GetPullRequest.Stack check, never the persisted
//     sentinel_fixes.stack_registered column -- see mergeGateDataSource's
//     own doc comment (pullrequestevent.go) for the full reasoning.
//
// The heavier findings this Step's re-review also confirmed in this SAME
// file (the merge-success branch of handlePullRequestClosed being
// entirely untested) need a real Postgres pool (handlePullRequestClosed
// takes concrete *postgres.SentinelFixStore/RepoSettingsStore/AuditLogStore
// arguments, not interfaces) -- those live in
// pullrequestevent_whitebox_integration_test.go instead.

// pureRenameDiff is a REAL git diff's own exact shape for a 100%-
// similarity rename with ZERO content change (reproduced directly against
// the real git binary during this fix's own verification): no "+++"/"---"
// lines at all.
const pureRenameDiff = `diff --git a/internal/foo/real_impl.go b/internal/foo/real_impl_test.go
similarity index 100%
rename from internal/foo/real_impl.go
rename to internal/foo/real_impl_test.go
`

// renameWithContentChangeDiff is a real git diff's own shape for a rename
// that ALSO changes content -- "rename from"/"rename to" lines coexist
// with ordinary "+++"/"---" hunk headers in this case.
const renameWithContentChangeDiff = `diff --git a/internal/foo/real_impl.go b/internal/foo/real_impl_test.go
similarity index 90%
rename from internal/foo/real_impl.go
rename to internal/foo/real_impl_test.go
index abc123..def456 100644
--- a/internal/foo/real_impl.go
+++ b/internal/foo/real_impl_test.go
@@ -1,3 +1,3 @@
 package foo
-func Old() {}
+func New() {}
`

func containsPath(files []string, path string) bool {
	for _, f := range files {
		if f == path {
			return true
		}
	}
	return false
}

// TestParseChangedFilesFromDiff_PureRename_ReportsBothOldAndNewPath is the
// confirmed-finding regression test: before this fix, parseChangedFilesFromDiff
// returned an EMPTY slice for pureRenameDiff (no "+++ " line exists at
// all), which firstNonTestOrDocPath's own doc comment treats as "every
// file is test/doc, trivially" -- vacuously passing the merge gate's own
// changed-files check for exactly the "rename a real source file onto a
// test-looking path" attack this test reproduces. The OLD path (the real,
// non-test production file) must appear in the result for
// firstNonTestOrDocPath to ever see it and correctly deny -- the NEW path
// alone would read as an innocent test file, which is the whole point of
// the attack.
func TestParseChangedFilesFromDiff_PureRename_ReportsBothOldAndNewPath(t *testing.T) {
	got := parseChangedFilesFromDiff(pureRenameDiff)
	if len(got) == 0 {
		t.Fatal("parseChangedFilesFromDiff(pure rename) returned no files at all -- this is the exact bypass the confirmed finding describes: firstNonTestOrDocPath would treat this as \"every file is test/doc, trivially\"")
	}
	if !containsPath(got, "internal/foo/real_impl.go") {
		t.Errorf("parseChangedFilesFromDiff(pure rename) = %v, want it to include the OLD (real, non-test) path %q so the merge gate can see it", got, "internal/foo/real_impl.go")
	}
	if !containsPath(got, "internal/foo/real_impl_test.go") {
		t.Errorf("parseChangedFilesFromDiff(pure rename) = %v, want it to also include the NEW path %q", got, "internal/foo/real_impl_test.go")
	}
}

// TestParseChangedFilesFromDiff_RenameWithContentChange_StillReportsOldPath
// proves the fix also holds for a rename that ALSO changes content (the
// "+++"/"rename from" signals coexist) -- the old path must still surface.
func TestParseChangedFilesFromDiff_RenameWithContentChange_StillReportsOldPath(t *testing.T) {
	got := parseChangedFilesFromDiff(renameWithContentChangeDiff)
	if !containsPath(got, "internal/foo/real_impl.go") {
		t.Errorf("parseChangedFilesFromDiff(rename+content change) = %v, want it to include the OLD path %q", got, "internal/foo/real_impl.go")
	}
	if !containsPath(got, "internal/foo/real_impl_test.go") {
		t.Errorf("parseChangedFilesFromDiff(rename+content change) = %v, want it to include the NEW path %q", got, "internal/foo/real_impl_test.go")
	}
}

// TestParseChangedFilesFromDiff_OrdinaryAddition_Unaffected is a plain
// regression test for the ORIGINAL, pre-fix behavior this function must
// keep: an ordinary added/modified file (no rename at all) is still
// reported via its "+++ b/<path>" header exactly as before.
func TestParseChangedFilesFromDiff_OrdinaryAddition_Unaffected(t *testing.T) {
	diff := "diff --git a/foo_test.go b/foo_test.go\n" +
		"index abc..def 100644\n" +
		"--- a/foo_test.go\n" +
		"+++ b/foo_test.go\n" +
		"@@ -1,1 +1,2 @@\n" +
		" package foo\n" +
		"+func TestX(t *testing.T) {}\n"
	got := parseChangedFilesFromDiff(diff)
	if len(got) != 1 || got[0] != "foo_test.go" {
		t.Errorf("parseChangedFilesFromDiff(ordinary addition) = %v, want exactly [\"foo_test.go\"]", got)
	}
}

// TestParseChangedFilesFromDiff_DeletedFile_NotDoubleCounted proves the
// existing "+++ /dev/null never counted" defense survives this fix
// unchanged.
func TestParseChangedFilesFromDiff_DeletedFile_NotDoubleCounted(t *testing.T) {
	diff := "diff --git a/gone.go b/gone.go\n" +
		"deleted file mode 100644\n" +
		"index abc..000 100644\n" +
		"--- a/gone.go\n" +
		"+++ /dev/null\n"
	got := parseChangedFilesFromDiff(diff)
	if len(got) != 0 {
		t.Errorf("parseChangedFilesFromDiff(deleted file) = %v, want empty (a deletion's own \"+++\" side is /dev/null, never counted)", got)
	}
}

// fakePullRequestResolverForStack is a minimal test-only PullRequestResolver
// for StackRegistered's own tests below -- distinct from headresolve_test.
// go's own fakePullRequestResolver only in name, to avoid any ambiguity
// about which test exercises which call site; both types are otherwise
// identical in shape and could be unified later if a third caller ever
// needs the same fake.
type fakePullRequestResolverForStack struct {
	pr  githubapi.PullRequest
	err error
}

func (f *fakePullRequestResolverForStack) GetPullRequest(context.Context, string, string, int32, string) (githubapi.PullRequest, error) {
	return f.pr, f.err
}

// TestGithubMergeGateDataSource_StackRegistered_UsesFreshGetPullRequest is
// the confirmed-finding regression test: StackRegistered must be backed by
// a real, fresh PullRequestResolver.GetPullRequest call -- never a
// persisted value -- and must report pr.Stack != nil exactly.
func TestGithubMergeGateDataSource_StackRegistered_UsesFreshGetPullRequest(t *testing.T) {
	tests := []struct {
		name    string
		stack   *githubapi.StackInfo
		want    bool
		wantErr bool
	}{
		{name: "stack present", stack: &githubapi.StackInfo{Size: 2, Position: 2}, want: true},
		{name: "stack absent", stack: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakePullRequestResolverForStack{pr: githubapi.PullRequest{Stack: tt.stack}}
			d := &githubMergeGateDataSource{pullRequests: resolver, timeouts: platform.DefaultTimeouts()}
			got, err := d.StackRegistered(context.Background(), "acme", "widgets", 7)
			if err != nil {
				t.Fatalf("StackRegistered() error = %v, want nil", err)
			}
			if got != tt.want {
				t.Errorf("StackRegistered() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGithubMergeGateDataSource_StackRegistered_PropagatesError proves a
// real GetPullRequest failure is reported as a plain error, never silently
// defaulted to false/true.
func TestGithubMergeGateDataSource_StackRegistered_PropagatesError(t *testing.T) {
	wantErr := errors.New("boom")
	resolver := &fakePullRequestResolverForStack{err: wantErr}
	d := &githubMergeGateDataSource{pullRequests: resolver, timeouts: platform.DefaultTimeouts()}
	if _, err := d.StackRegistered(context.Background(), "acme", "widgets", 7); err == nil {
		t.Fatal("StackRegistered() error = nil, want the underlying GetPullRequest error propagated")
	}
}

// TestGithubMergeGateDataSource_StackRegistered_NilResolver proves a nil
// PullRequestResolver (this package's own handler_test.go, or any other
// minimal wiring) is a plain, honest error -- never a silent false.
func TestGithubMergeGateDataSource_StackRegistered_NilResolver(t *testing.T) {
	d := &githubMergeGateDataSource{timeouts: platform.DefaultTimeouts()}
	if _, err := d.StackRegistered(context.Background(), "acme", "widgets", 7); err == nil {
		t.Fatal("StackRegistered() error = nil, want a real error for a nil PullRequestResolver")
	}
}
