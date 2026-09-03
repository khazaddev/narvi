//go:build integration

// Integration tests for the M14 audit finding's GitHub half: a comment
// authored by the bot's OWN GitHub identity must never be treated as a
// mention-worthy event -- see handler.go's own filter (this same batch's
// addition) for the full "why", including the two realistic deployment
// shapes it covers (a plain PAT-authenticated bot account whose own login
// matches BotHandle exactly, and a GitHub App installation whose own login
// always carries the fixed "<slug>[bot]" suffix) and the one genuine
// residual gap that filter's own doc comment documents.
package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// issueCommentBodyFromCommenter is issueCommentBody's own twin
// (handler_integration_test.go), with an explicit comment.user.{id,login}
// -- issueCommentBody itself never sets one (every OTHER existing test in
// this package doesn't care who authored the comment), so proving this
// filter needs its own variant that does.
func issueCommentBodyFromCommenter(repoFullName, repoName, cloneURL string, prNumber int, label string, commenterID int64, commenterLogin string) []byte {
	body, err := json.Marshal(map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       prNumber,
			"pull_request": map[string]any{"url": fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repoFullName, prNumber)},
		},
		"comment": map[string]any{
			"body": fmt.Sprintf("@%s please review (%s)", testBotHandleIntegration, label),
			"user": map[string]any{"id": commenterID, "login": commenterLogin},
		},
		"repository": map[string]any{
			"full_name": repoFullName,
			"name":      repoName,
			"clone_url": cloneURL,
		},
	})
	if err != nil {
		panic(err)
	}
	return body
}

// TestGitHubIntegration_SelfComment_MatchingBotHandle_Ignored proves the
// M14 self-comment filter's own mechanism: a comment whose own
// comment.user.login EXACTLY matches the configured BotHandle (the
// "PAT-style bot account" deployment shape) is never treated as a mention,
// even though its own body genuinely satisfies compileMentionPattern -- no
// session/turn is ever created, and the claimed webhook delivery is
// acknowledged (200 -- the SAME "nothing to act on" response the ordinary
// "bot wasn't mentioned" !ok path already gives), never released for a
// pointless retry.
func TestGitHubIntegration_SelfComment_MatchingBotHandle_Ignored(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)

	const repoFullName = "acme/self-comment-repo"
	const prNumber = 707

	// commenterLogin is set to the EXACT SAME value as
	// testBotHandleIntegration (the rig's own configured BotHandle) -- the
	// plain PAT-authenticated bot account shape (see the sibling
	// TestGitHubIntegration_SelfComment_GitHubAppBotSuffix_Ignored below for
	// the OTHER realistic shape, a GitHub App installation's own
	// "[bot]"-suffixed login).
	body := issueCommentBodyFromCommenter(repoFullName, "self-comment-repo", "https://github.com/acme/self-comment-repo.git", prNumber, "self", 999999, testBotHandleIntegration)
	status := postWebhook(t, rig, body, "delivery-self-comment-match")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	var sessionCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE spawn_source = 'github'`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 0 {
		t.Errorf("session count = %d, want 0 (a comment authored by the bot's own matching handle must never be treated as a mention)", sessionCount)
	}
}

// TestGitHubIntegration_SelfComment_GitHubAppBotSuffix_Ignored proves the
// M14 self-comment filter's OTHER realistic deployment shape: a GitHub App
// installation posts its own comments under a comment.user.login of the
// fixed "<configured-handle>[bot]" form (e.g. "narvi-bot[bot]" for a
// configured BotHandle of "narvi-bot"), NOT an exact match against
// BotHandle alone -- this is a standard, well-known GitHub convention for
// every GitHub App, not a vague edge case. Before this filter's own
// "[bot]"-suffix fix, a comment posted this way was NOT recognized as a
// self-comment and would spawn a brand-new session/turn from the bot's own
// comment -- exactly the self-reply loop M14 exists to close, for what is
// the MORE common real-world deployment shape (GitHub Apps are the
// standard way to authenticate a bot like this one, more so than a plain
// PAT-authenticated account). Same assertions as the sibling
// TestGitHubIntegration_SelfComment_MatchingBotHandle_Ignored above: no
// session/turn is ever created, and the claimed webhook delivery is
// acknowledged (200), never released for a pointless retry.
func TestGitHubIntegration_SelfComment_GitHubAppBotSuffix_Ignored(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)

	const repoFullName = "acme/self-comment-app-repo"
	const prNumber = 709

	// commenterLogin is testBotHandleIntegration (the rig's own configured
	// BotHandle) PLUS the literal "[bot]" suffix GitHub always appends for
	// a GitHub App installation's own login.
	body := issueCommentBodyFromCommenter(repoFullName, "self-comment-app-repo", "https://github.com/acme/self-comment-app-repo.git", prNumber, "self-app", 999998, testBotHandleIntegration+"[bot]")
	status := postWebhook(t, rig, body, "delivery-self-comment-app-suffix")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	var sessionCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE spawn_source = 'github'`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 0 {
		t.Errorf("session count = %d, want 0 (a comment authored by the bot's own GitHub App \"[bot]\"-suffixed login must never be treated as a mention)", sessionCount)
	}
}

// TestGitHubIntegration_GenuineCommenter_DifferentLogin_StillProcessed is
// this file's own regression guard: a genuine human commenter whose login
// does NOT match BotHandle must still be processed completely normally --
// proving the filter is scoped to an exact match, never over-broad enough
// to swallow a real mention. commenterID is a genuinely LINKED account
// (batch fix/deny-unlinked-github-actors: an unlinked one would now be
// denied outright, a different property than the self-comment filter this
// test exists to prove).
func TestGitHubIntegration_GenuineCommenter_DifferentLogin_StillProcessed(t *testing.T) {
	ctx := context.Background()
	rig := newTestRig(t)

	const repoFullName = "acme/genuine-commenter-repo"
	const prNumber = 708
	const commenterID = 12345

	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := issueCommentBodyFromCommenter(repoFullName, "genuine-commenter-repo", "https://github.com/acme/genuine-commenter-repo.git", prNumber, "genuine", commenterID, "a-real-human")
	status := postWebhook(t, rig, body, "delivery-genuine-commenter")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	var sessionCount int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE spawn_source = 'github'`).Scan(&sessionCount); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessionCount != 1 {
		t.Errorf("session count = %d, want 1 (a genuine, non-bot commenter must still be processed normally)", sessionCount)
	}
}
