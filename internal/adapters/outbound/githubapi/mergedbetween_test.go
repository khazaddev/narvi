package githubapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// TestListMergedBetween_FullScenario exercises ListMergedBetween end to
// end against a fake GitHub server standing in for every real endpoint
// it calls: compare (discovering constituent PRs from both the merge-
// commit and squash-merge message shapes), per-PR detail/reviews/files/
// commits, branch protection (admin-override inference), CI status +
// check-runs at the merge SHA, and the batch-wide revert search.
func TestListMergedBetween_FullScenario(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/compare/main...release-1.0":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_commits": 2,
				"commits": []map[string]any{
					// Merge-commit strategy: PR #10.
					{"commit": map[string]any{"message": "Merge pull request #10 from acme/widgets/fix-thing\n\nFix thing"}},
					// Squash-merge strategy: PR #11.
					{"commit": map[string]any{"message": "Add feature (#11)"}},
				},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/10":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 10, "title": "Fix thing", "merged": true,
				"merged_at": "2024-01-01T00:00:00Z", "merge_commit_sha": "sha10",
				"base":   map[string]any{"ref": "main"},
				"labels": []map[string]any{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/11":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 11, "title": "Add feature", "merged": true,
				"merged_at": "2024-01-02T00:00:00Z", "merge_commit_sha": "sha11",
				"base":   map[string]any{"ref": "main"},
				"labels": []map[string]any{{"name": "review:high-risk"}},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/10/reviews":
			// No approving review at all -- combined with branch
			// protection requiring one (below), this is the confirmed
			// admin-override case.
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"state": "COMMENTED"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/11/reviews":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"state": "CHANGES_REQUESTED"},
				{"state": "APPROVED"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/99/reviews":
			// The revert PR's own review state: unreviewed.
			_ = json.NewEncoder(w).Encode([]map[string]any{})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/branches/main/protection":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"required_pull_request_reviews": map[string]any{"required_approving_review_count": 1},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/commits/sha10/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "failure"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/commits/sha11/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "success"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/check-runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": []map[string]any{}})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/10/files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "internal/domain/review/x.go"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/11/files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "internal/domain/review/y.go"}})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/10/commits":
			// Single-parent commits only -- no manual conflict resolution.
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"parents": []map[string]any{{"sha": "p1"}}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/11/commits":
			// A 2-parent commit -- the manual-conflict-resolution heuristic.
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"parents": []map[string]any{{"sha": "p1"}, {"sha": "p2"}}},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{
					{"number": 99, "title": `Revert "Fix thing"`, "closed_at": "2024-01-01T02:00:00Z"},
				},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	merged, err := adapter.ListMergedBetween(context.Background(), ports.ListMergedBetweenSpec{
		Owner: "acme", Repo: "widgets", BaseRef: "main", HeadRef: "release-1.0", Token: "tok",
	})
	if err != nil {
		t.Fatalf("ListMergedBetween() error = %v", err)
	}
	if len(merged) != 2 {
		t.Fatalf("got %d merged PRs, want 2: %+v", len(merged), merged)
	}

	pr10, pr11 := merged[0], merged[1]
	if pr10.Number != 10 {
		t.Fatalf("merged[0].Number = %d, want 10 (compare's own commit order must be preserved)", pr10.Number)
	}
	if pr11.Number != 11 {
		t.Fatalf("merged[1].Number = %d, want 11", pr11.Number)
	}

	if pr10.HasApprovingReview {
		t.Error("PR 10: HasApprovingReview = true, want false (only a COMMENTED review present)")
	}
	if !pr10.MergedViaAdminOverride {
		t.Error("PR 10: MergedViaAdminOverride = false, want true (branch requires review, none present)")
	}
	if pr10.CIConclusionAtMergeSHA != ports.CIConclusionFailure {
		t.Errorf("PR 10: CIConclusionAtMergeSHA = %s, want %s", pr10.CIConclusionAtMergeSHA, ports.CIConclusionFailure)
	}
	if pr10.HadManualConflictResolution {
		t.Error("PR 10: HadManualConflictResolution = true, want false (single-parent commit only)")
	}
	if !pr10.WasReverted {
		t.Error("PR 10: WasReverted = false, want true (a matching revert PR was found)")
	}
	if pr10.RevertReviewed {
		t.Error("PR 10: RevertReviewed = true, want false (the revert PR carried no approving review)")
	}
	wantRevertedAt := time.Date(2024, 1, 1, 2, 0, 0, 0, time.UTC)
	if pr10.RevertedAt == nil || !pr10.RevertedAt.Equal(wantRevertedAt) {
		t.Errorf("PR 10: RevertedAt = %v, want %v", pr10.RevertedAt, wantRevertedAt)
	}
	if len(pr10.ChangedPathPrefixes) != 1 || pr10.ChangedPathPrefixes[0] != "internal/domain" {
		t.Errorf("PR 10: ChangedPathPrefixes = %v, want [internal/domain]", pr10.ChangedPathPrefixes)
	}

	if !pr11.HasApprovingReview {
		t.Error("PR 11: HasApprovingReview = false, want true (an APPROVED review is present)")
	}
	if pr11.MergedViaAdminOverride {
		t.Error("PR 11: MergedViaAdminOverride = true, want false (it has an approving review)")
	}
	if pr11.CIConclusionAtMergeSHA != ports.CIConclusionSuccess {
		t.Errorf("PR 11: CIConclusionAtMergeSHA = %s, want %s", pr11.CIConclusionAtMergeSHA, ports.CIConclusionSuccess)
	}
	if !pr11.HadManualConflictResolution {
		t.Error("PR 11: HadManualConflictResolution = false, want true (a 2-parent commit is present)")
	}
	if pr11.WasReverted {
		t.Error("PR 11: WasReverted = true, want false (no matching revert PR)")
	}
	if len(pr11.Labels) != 1 || pr11.Labels[0] != "review:high-risk" {
		t.Errorf("PR 11: Labels = %v, want [review:high-risk]", pr11.Labels)
	}
}

// TestListMergedBetween_UnmergedCandidateExcluded proves a candidate PR
// number extracted from a commit message but reported by GitHub as NOT
// actually merged is silently excluded from the manifest -- never an
// error, never a fabricated entry.
func TestListMergedBetween_UnmergedCandidateExcluded(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/compare/main...release-1.0":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"commits": []map[string]any{
					{"commit": map[string]any{"message": "Merge pull request #10 from acme/widgets/fix-thing"}},
				},
			})
		case "/repos/acme/widgets/pulls/10":
			// merged: false -- e.g. a coincidental "(#10)" match, or a PR
			// later force-pushed/closed without merging.
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 10, "merged": false})
		case "/search/issues":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	merged, err := adapter.ListMergedBetween(context.Background(), ports.ListMergedBetweenSpec{
		Owner: "acme", Repo: "widgets", BaseRef: "main", HeadRef: "release-1.0", Token: "tok",
	})
	if err != nil {
		t.Fatalf("ListMergedBetween() error = %v", err)
	}
	if len(merged) != 0 {
		t.Fatalf("got %d merged PRs, want 0 (the one candidate was never actually merged): %+v", len(merged), merged)
	}
}

// TestListMergedBetween_CompareFailurePropagatesError proves a genuine
// failure on the top-level compare call is a real, propagated error --
// this is the one call ListMergedBetween cannot degrade gracefully from
// (there is nothing to build a manifest over at all).
func TestListMergedBetween_CompareFailurePropagatesError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	_, err := adapter.ListMergedBetween(context.Background(), ports.ListMergedBetweenSpec{
		Owner: "acme", Repo: "widgets", BaseRef: "main", HeadRef: "release-1.0", Token: "tok",
	})
	if err == nil {
		t.Fatal("ListMergedBetween() error = nil, want a real error on a compare-call failure")
	}
}

// TestListMergedBetween_NoCandidates proves a range with no merge/squash-
// shaped commit messages at all (e.g. every commit was rebase-merged --
// this file's own documented, accepted limitation) returns an empty,
// non-nil-error manifest, never a spurious failure.
func TestListMergedBetween_NoCandidates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/compare/main...release-1.0":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"commits": []map[string]any{
					{"commit": map[string]any{"message": "Fix a typo"}},
				},
			})
		case "/search/issues":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	merged, err := adapter.ListMergedBetween(context.Background(), ports.ListMergedBetweenSpec{
		Owner: "acme", Repo: "widgets", BaseRef: "main", HeadRef: "release-1.0", Token: "tok",
	})
	if err != nil {
		t.Fatalf("ListMergedBetween() error = %v", err)
	}
	if len(merged) != 0 {
		t.Fatalf("got %d merged PRs, want 0: %+v", len(merged), merged)
	}
}
