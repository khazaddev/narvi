package githubapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// TestListOpenPRsForUser_FullScenario exercises ListOpenPRsForUser end to
// end: id->login resolution, both search qualifiers (with an overlapping
// PR deduplicated across them), and full per-PR detail assembly (review
// decision, CI at head SHA via both CI surfaces, changed files, labels,
// assignees/reviewers/teams).
func TestListOpenPRsForUser_FullScenario(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/user/9001":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 9001, "login": "octocat"})

		case r.Method == http.MethodGet && r.URL.Path == "/search/issues":
			q := r.URL.Query().Get("q")
			switch {
			case strings.Contains(q, "assignee:octocat"):
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items": []map[string]any{
						{"number": 1204, "repository_url": "https://api.github.com/repos/acme/widgets"},
					},
				})
			case strings.Contains(q, "review-requested:octocat"):
				// #1204 also shows up here -- must be deduplicated against
				// the assignee query's own result, never built twice.
				_ = json.NewEncoder(w).Encode(map[string]any{
					"items": []map[string]any{
						{"number": 1204, "repository_url": "https://api.github.com/repos/acme/widgets"},
						{"number": 1187, "repository_url": "https://api.github.com/repos/acme/payroll-api"},
					},
				})
			default:
				t.Errorf("unexpected search query: %q", q)
			}

		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/1204":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 1204, "title": "scheduler: exponential backoff", "html_url": "https://github.com/acme/widgets/pull/1204",
				"draft": false, "additions": 40, "deletions": 5,
				"created_at": "2026-08-05T10:00:00Z", "updated_at": "2026-08-05T11:00:00Z",
				"user":                map[string]any{"id": 500, "login": "narvi-bot"},
				"head":                map[string]any{"sha": "headsha1204"},
				"base":                map[string]any{"ref": "main"},
				"labels":              []map[string]any{{"name": "review:low-risk"}},
				"assignees":           []map[string]any{{"id": 9001, "login": "octocat"}},
				"requested_reviewers": []map[string]any{},
				"requested_teams":     []map[string]any{},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/1204/reviews":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"state": "APPROVED"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/commits/headsha1204/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "success"})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/commits/headsha1204/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": []map[string]any{}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/pulls/1204/files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "internal/app/scheduler/backoff.go"}})

		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/payroll-api/pulls/1187":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 1187, "title": "harden webhook signature validation", "html_url": "https://github.com/acme/payroll-api/pull/1187",
				"draft": false, "additions": 120, "deletions": 30,
				"created_at": "2026-08-06T08:00:00Z", "updated_at": "2026-08-06T09:00:00Z",
				"user":                map[string]any{"id": 600, "login": "narvi-bot"},
				"head":                map[string]any{"sha": "headsha1187"},
				"base":                map[string]any{"ref": "main"},
				"labels":              []map[string]any{{"name": "review:medium-risk"}},
				"assignees":           []map[string]any{},
				"requested_reviewers": []map[string]any{{"id": 9001, "login": "octocat"}},
				"requested_teams":     []map[string]any{{"slug": "payroll-team"}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/payroll-api/pulls/1187/reviews":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"state": "CHANGES_REQUESTED"}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/payroll-api/commits/headsha1187/status":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/payroll-api/commits/headsha1187/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": []map[string]any{{"conclusion": "failure"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/payroll-api/pulls/1187/files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"filename": "internal/webhook/verify.go"}})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	prs, truncated, err := adapter.ListOpenPRsForUser(context.Background(), ports.ListOpenPRsForUserSpec{
		GitHubExternalID: "9001", Token: "tok",
	})
	if err != nil {
		t.Fatalf("ListOpenPRsForUser() error = %v, want nil", err)
	}
	if truncated {
		t.Error("truncated = true, want false (both discovery queries succeeded)")
	}
	if len(prs) != 2 {
		t.Fatalf("ListOpenPRsForUser() returned %d PRs, want 2 (deduplicated): %+v", len(prs), prs)
	}

	byNumber := map[int]ports.OpenPR{}
	for _, pr := range prs {
		byNumber[pr.Number] = pr
	}

	pr1204, ok := byNumber[1204]
	if !ok {
		t.Fatal("missing PR #1204")
	}
	if pr1204.HeadSHA != "headsha1204" || !pr1204.HasApprovingReview || pr1204.HasChangesRequested {
		t.Errorf("PR #1204 = %+v, unexpected review/head-sha fields", pr1204)
	}
	if pr1204.CIConclusion != ports.CIConclusionSuccess {
		t.Errorf("PR #1204 CIConclusion = %v, want success", pr1204.CIConclusion)
	}
	if len(pr1204.Assignees) != 1 || pr1204.Assignees[0].ExternalID != "9001" {
		t.Errorf("PR #1204 Assignees = %+v, want [octocat]", pr1204.Assignees)
	}
	if len(pr1204.ChangedFiles) != 1 || pr1204.ChangedFiles[0] != "internal/app/scheduler/backoff.go" {
		t.Errorf("PR #1204 ChangedFiles = %+v", pr1204.ChangedFiles)
	}

	pr1187, ok := byNumber[1187]
	if !ok {
		t.Fatal("missing PR #1187")
	}
	if pr1187.HasApprovingReview || !pr1187.HasChangesRequested {
		t.Errorf("PR #1187 review decision = approving=%v changesRequested=%v, want false/true", pr1187.HasApprovingReview, pr1187.HasChangesRequested)
	}
	if pr1187.CIConclusion != ports.CIConclusionFailure {
		t.Errorf("PR #1187 CIConclusion = %v, want failure (from check-runs, combined status 404)", pr1187.CIConclusion)
	}
	if len(pr1187.RequestedReviewers) != 1 || pr1187.RequestedReviewers[0].Login != "octocat" {
		t.Errorf("PR #1187 RequestedReviewers = %+v", pr1187.RequestedReviewers)
	}
	if len(pr1187.RequestedTeams) != 1 || pr1187.RequestedTeams[0] != "acme/payroll-team" {
		t.Errorf("PR #1187 RequestedTeams = %+v, want [acme/payroll-team]", pr1187.RequestedTeams)
	}
}

func TestListOpenPRsForUser_OneQueryFailingDoesNotBlankTheOther(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/user/1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "login": "octocat"})
		case r.URL.Path == "/search/issues" && strings.Contains(r.URL.Query().Get("q"), "assignee:octocat"):
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/search/issues" && strings.Contains(r.URL.Query().Get("q"), "review-requested:octocat"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]any{{"number": 5, "repository_url": "https://api.github.com/repos/acme/widgets"}},
			})
		case r.URL.Path == "/repos/acme/widgets/pulls/5":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 5, "title": "x", "html_url": "u", "head": map[string]any{"sha": "s"}, "base": map[string]any{"ref": "main"},
			})
		case r.URL.Path == "/repos/acme/widgets/pulls/5/reviews":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case r.URL.Path == "/repos/acme/widgets/commits/s/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "success"})
		case r.URL.Path == "/repos/acme/widgets/commits/s/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": []map[string]any{}})
		case r.URL.Path == "/repos/acme/widgets/pulls/5/files":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	prs, truncated, err := adapter.ListOpenPRsForUser(context.Background(), ports.ListOpenPRsForUserSpec{GitHubExternalID: "1", Token: "tok"})
	if err != nil {
		t.Fatalf("ListOpenPRsForUser() error = %v, want nil (one query failing is best-effort)", err)
	}
	if len(prs) != 1 || prs[0].Number != 5 {
		t.Errorf("ListOpenPRsForUser() = %+v, want exactly PR #5 from the surviving query", prs)
	}
	// §60 review finding C1: the surviving query's own result is still
	// returned in full (never blanked out), but truncated must still be
	// true -- this result is a known-incomplete picture (the assignee:
	// query's own failure means any PR only discoverable THAT way is
	// silently missing), which a caller must not cache/present as
	// confirmed-complete.
	if !truncated {
		t.Error("truncated = false, want true (the assignee: query failed)")
	}
}

// TestListOpenPRsForUser_QueuedOrCancelledCheckIsNotGreen proves
// fetchCIConclusionLive's own STRICT departure from fetchCIConclusion's
// lenient "any confirmed success, no confirmed failure" rule (§60 review
// finding A2): a live, pre-merge read must never report CIConclusionSuccess
// while a required check is still queued/in_progress (Conclusion == nil)
// or was cancelled -- "some check finished green" must never stand in for
// "the whole suite is green" while other checks are still outstanding.
func TestListOpenPRsForUser_QueuedOrCancelledCheckIsNotGreen(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		checkRuns []map[string]any
		want      ports.CIConclusion
	}{
		{
			name: "one success, one still queued",
			checkRuns: []map[string]any{
				{"conclusion": "success"},
				{"conclusion": nil},
			},
			want: ports.CIConclusionUnknown,
		},
		{
			name: "one success, one cancelled",
			checkRuns: []map[string]any{
				{"conclusion": "success"},
				{"conclusion": "cancelled"},
			},
			want: ports.CIConclusionUnknown,
		},
		{
			name: "one failure, one still queued -- failure wins",
			checkRuns: []map[string]any{
				{"conclusion": "failure"},
				{"conclusion": nil},
			},
			want: ports.CIConclusionFailure,
		},
		{
			name: "every check concluded success",
			checkRuns: []map[string]any{
				{"conclusion": "success"},
				{"conclusion": "neutral"},
			},
			want: ports.CIConclusionSuccess,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/user/1":
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "login": "octocat"})
				case r.URL.Path == "/search/issues" && strings.Contains(r.URL.Query().Get("q"), "assignee:octocat"):
					_ = json.NewEncoder(w).Encode(map[string]any{
						"items": []map[string]any{{"number": 5, "repository_url": "https://api.github.com/repos/acme/widgets"}},
					})
				case r.URL.Path == "/search/issues" && strings.Contains(r.URL.Query().Get("q"), "review-requested:octocat"):
					_ = json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}})
				case r.URL.Path == "/repos/acme/widgets/pulls/5":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"number": 5, "title": "x", "html_url": "u", "head": map[string]any{"sha": "s"}, "base": map[string]any{"ref": "main"},
					})
				case r.URL.Path == "/repos/acme/widgets/pulls/5/reviews":
					_ = json.NewEncoder(w).Encode([]map[string]any{})
				case r.URL.Path == "/repos/acme/widgets/commits/s/status":
					// No legacy combined-status signal at all -- this test
					// isolates the Checks API surface.
					w.WriteHeader(http.StatusNotFound)
				case r.URL.Path == "/repos/acme/widgets/commits/s/check-runs":
					_ = json.NewEncoder(w).Encode(map[string]any{"check_runs": tc.checkRuns})
				case r.URL.Path == "/repos/acme/widgets/pulls/5/files":
					_ = json.NewEncoder(w).Encode([]map[string]any{})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			adapter := githubapi.New(server.Client(), server.URL)

			prs, _, err := adapter.ListOpenPRsForUser(context.Background(), ports.ListOpenPRsForUserSpec{GitHubExternalID: "1", Token: "tok"})
			if err != nil {
				t.Fatalf("ListOpenPRsForUser() error = %v, want nil", err)
			}
			if len(prs) != 1 {
				t.Fatalf("ListOpenPRsForUser() returned %d PRs, want 1", len(prs))
			}
			if prs[0].CIConclusion != tc.want {
				t.Errorf("CIConclusion = %v, want %v", prs[0].CIConclusion, tc.want)
			}
		})
	}
}
