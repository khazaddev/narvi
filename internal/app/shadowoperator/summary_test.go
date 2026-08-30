package shadowoperator

import (
	"testing"
	"time"
)

func TestCategoryForSCMOperation(t *testing.T) {
	cases := map[string]string{
		"create_pr":                       CategoryPullRequests,
		"update_pr_body":                  CategoryPullRequests,
		"register_pr_stack":               CategoryPullRequests,
		"create_branch":                   CategoryBranches,
		"merge_pr":                        CategoryMerges,
		"update_file_content":             CategoryFileContentWrites,
		"push":                            CategoryPushes,
		"sentinel_auto_fix":               CategorySentinelAutoFix,
		"scm_credential_mint_refused":     CategoryCredentialSubst,
		"scm_credential_substituted":      CategoryCredentialSubst,
		"slack_post_ack":                  CategorySlackActivity,
		"slack_post_ephemeral":            CategorySlackActivity,
		"slack_post_identity_link_notice": CategorySlackActivity,
		"slack_update_message":            CategorySlackActivity,
		"slack_open_view":                 CategorySlackActivity,
		"linear_create_thought_activity":  CategoryLinearActivity,
		"linear_create_response_activity": CategoryLinearActivity,
		"http_post":                       CategoryGitHubNotices,
		"http_patch":                      CategoryGitHubNotices,
		"something_unforeseen":            CategoryOtherSCMWrite,
	}
	for op, want := range cases {
		if got := categoryForSCMOperation(op); got != want {
			t.Errorf("categoryForSCMOperation(%q) = %q, want %q", op, got, want)
		}
	}
}

func TestCategoryForOutboxKind(t *testing.T) {
	cases := map[string]string{
		"github_verdict":             CategoryGitHubNotices,
		"github_description_autofix": CategoryGitHubNotices,
		"slack_digest":               CategorySlackActivity,
		"slack_plan_approval":        CategorySlackActivity,
		"linear_progress":            CategoryLinearActivity,
		"linear_workflow_decision":   CategoryLinearActivity,
		"release_manifest":           CategoryOtherNotification,
		"handoff_sentinel":           CategoryOtherNotification,
	}
	for kind, want := range cases {
		if got := categoryForOutboxKind(kind); got != want {
			t.Errorf("categoryForOutboxKind(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestSummarizeCategories_OrdersByDescendingCountThenLabel(t *testing.T) {
	entries := []Entry{
		{Category: CategoryPullRequests},
		{Category: CategoryBranches},
		{Category: CategoryBranches},
		{Category: CategoryMerges},
		{Category: CategoryMerges},
	}
	got := summarizeCategories(entries)
	want := []Category{
		{Label: CategoryBranches, Count: 2},
		{Label: CategoryMerges, Count: 2},
		{Label: CategoryPullRequests, Count: 1},
	}
	if len(got) != len(want) {
		t.Fatalf("summarizeCategories() = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("summarizeCategories()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestSummarizeCategories_EmptyInputYieldsNoCategories(t *testing.T) {
	if got := summarizeCategories(nil); len(got) != 0 {
		t.Errorf("summarizeCategories(nil) = %+v, want empty", got)
	}
}

func TestSortEntriesNewestFirst(t *testing.T) {
	now := time.Now()
	entries := []Entry{
		{Operation: "oldest", CreatedAt: now.Add(-2 * time.Hour)},
		{Operation: "newest", CreatedAt: now},
		{Operation: "middle", CreatedAt: now.Add(-1 * time.Hour)},
	}
	sortEntriesNewestFirst(entries)
	want := []string{"newest", "middle", "oldest"}
	for i, w := range want {
		if entries[i].Operation != w {
			t.Errorf("entries[%d].Operation = %q, want %q", i, entries[i].Operation, w)
		}
	}
}
