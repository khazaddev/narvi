//go:build integration

// Integration tests for Step 63's own §22.2 capture command against a
// real Postgres instance -- gated behind the "integration" build tag,
// reusing newTestPool/testWebhookSecret/testBotHandleIntegration/sign/
// postWebhook/createLinkedGitHubUser from handler_integration_test.go
// (same package, same build tag). Run via `make test-integration`.
package github_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	githubingress "github.com/khazaddev/narvi/internal/adapters/inbound/github"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// falsePositiveCommentBody builds a synthetic, real-shaped "issue_comment"
// webhook payload carrying commentID/commenterID/commenterLogin and a
// custom body -- mirrors issueCommentBodyWithCommenter (identity_
// integration_test.go) but ALSO sets comment.id (needed for §22.2's own
// "keyed on the triggering comment id" idempotency) and accepts an
// arbitrary body string instead of a fixed "@mention" template.
func falsePositiveCommentBody(repoFullName, repoName, cloneURL string, prNumber int, commentID, commenterID int64, commenterLogin, commentBody string) []byte {
	body, err := json.Marshal(map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       prNumber,
			"pull_request": map[string]any{"url": fmt.Sprintf("https://api.github.com/repos/%s/pulls/%d", repoFullName, prNumber)},
		},
		"comment": map[string]any{
			"id":   commentID,
			"body": commentBody,
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

// newFalsePositiveTestRig builds a rig with a real
// *postgres.FalsePositivePatternStore wired as BOTH
// Config.FalsePositivePatternCapture and Config.FalsePositivePatterns --
// mirroring cmd/control-plane/main.go's own "one store, two interfaces"
// production wiring exactly.
func newFalsePositiveTestRig(t *testing.T) (testRig, *narvipg.FalsePositivePatternStore) {
	t.Helper()
	patterns := narvipg.NewFalsePositivePatternStore(newTestPool(t))
	rig := newTestRig(t, func(cfg *githubingress.Config) {
		cfg.FalsePositivePatternCapture = patterns
		cfg.FalsePositivePatterns = patterns
	})
	return rig, patterns
}

func countFalsePositivePatterns(ctx context.Context, t *testing.T, rig testRig, repoFullName string) int {
	t.Helper()
	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM review_false_positive_patterns WHERE repo_full_name = $1`, repoFullName).Scan(&count); err != nil {
		t.Fatalf("count review_false_positive_patterns: %v", err)
	}
	return count
}

func countSessionsForRepo(ctx context.Context, t *testing.T, rig testRig, repoNameFragment string) int {
	t.Helper()
	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE spawn_source = 'github' AND repos::text LIKE '%'||$1||'%'`, repoNameFragment).Scan(&count); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	return count
}

// TestGitHubIntegration_FalsePositiveCapture_MaintainerTeaches_PatternCreated
// is this Step's own core happy-path proof: a maintainer's `false
// positive: <reason>` comment on a PR creates exactly one
// review_false_positive_patterns row, correctly attributed, and creates
// NO session/turn at all -- dispatch-before-router, never reaching the
// ordinary mention pipeline.
func TestGitHubIntegration_FalsePositiveCapture_MaintainerTeaches_PatternCreated(t *testing.T) {
	ctx := context.Background()
	rig, patterns := newFalsePositiveTestRig(t)

	const repoFullName = "acme/fp-maintainer-repo"
	const commenterID = 80080001
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := falsePositiveCommentBody(repoFullName, "fp-maintainer-repo", "https://github.com/acme/fp-maintainer-repo.git", 1, 700001, commenterID, "maintainer-user", "false positive: this logger call is intentionally unchecked")
	status := postWebhook(t, rig, body, "delivery-fp-maintainer-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	if got := countFalsePositivePatterns(ctx, t, rig, repoFullName); got != 1 {
		t.Fatalf("pattern count = %d, want 1", got)
	}

	rows, err := patterns.ListActive(ctx, repoFullName)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("ListActive returned %d rows, want 1", len(rows))
	}
	if rows[0].Reason != "this logger call is intentionally unchecked" {
		t.Errorf("Reason = %q, want %q", rows[0].Reason, "this logger call is intentionally unchecked")
	}
	if rows[0].CommentID != 700001 {
		t.Errorf("CommentID = %d, want %d", rows[0].CommentID, 700001)
	}
	if !rows[0].CreatedBy.Valid {
		t.Error("CreatedBy.Valid = false, want true (attributed to the real maintainer)")
	}

	if got := countSessionsForRepo(ctx, t, rig, "fp-maintainer-repo"); got != 0 {
		t.Errorf("session count = %d, want 0 (dispatch-before-router: a capture command must never spawn a review session)", got)
	}
}

// TestGitHubIntegration_FalsePositiveCapture_MemberDenied proves §13.3's
// own maintainer+ gate (authz.ActionTeachFalsePositivePattern): a linked
// `member` is denied -- no pattern row created, still acknowledged 200.
func TestGitHubIntegration_FalsePositiveCapture_MemberDenied(t *testing.T) {
	ctx := context.Background()
	rig, _ := newFalsePositiveTestRig(t)

	const repoFullName = "acme/fp-member-repo"
	const commenterID = 80080002
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMember)

	body := falsePositiveCommentBody(repoFullName, "fp-member-repo", "https://github.com/acme/fp-member-repo.git", 1, 700002, commenterID, "member-user", "false positive: not for you to teach")
	status := postWebhook(t, rig, body, "delivery-fp-member-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (denied, but still acknowledged)", status, http.StatusOK)
	}

	if got := countFalsePositivePatterns(ctx, t, rig, repoFullName); got != 0 {
		t.Errorf("pattern count = %d, want 0 (a member must be denied)", got)
	}
}

// TestGitHubIntegration_FalsePositiveCapture_ViewerDenied mirrors the
// member case for a `viewer`.
func TestGitHubIntegration_FalsePositiveCapture_ViewerDenied(t *testing.T) {
	ctx := context.Background()
	rig, _ := newFalsePositiveTestRig(t)

	const repoFullName = "acme/fp-viewer-repo"
	const commenterID = 80080003
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleViewer)

	body := falsePositiveCommentBody(repoFullName, "fp-viewer-repo", "https://github.com/acme/fp-viewer-repo.git", 1, 700003, commenterID, "viewer-user", "false positive: not for you either")
	status := postWebhook(t, rig, body, "delivery-fp-viewer-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	if got := countFalsePositivePatterns(ctx, t, rig, repoFullName); got != 0 {
		t.Errorf("pattern count = %d, want 0 (a viewer must be denied)", got)
	}
}

// TestGitHubIntegration_FalsePositiveCapture_AdminAllowed proves admin
// (the role above maintainer) is ALSO allowed -- maintainer+ means both.
func TestGitHubIntegration_FalsePositiveCapture_AdminAllowed(t *testing.T) {
	ctx := context.Background()
	rig, _ := newFalsePositiveTestRig(t)

	const repoFullName = "acme/fp-admin-repo"
	const commenterID = 80080004
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleAdmin)

	body := falsePositiveCommentBody(repoFullName, "fp-admin-repo", "https://github.com/acme/fp-admin-repo.git", 1, 700004, commenterID, "admin-user", "false positive: admin-taught pattern")
	status := postWebhook(t, rig, body, "delivery-fp-admin-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	if got := countFalsePositivePatterns(ctx, t, rig, repoFullName); got != 1 {
		t.Errorf("pattern count = %d, want 1 (an admin must be allowed)", got)
	}
}

// TestGitHubIntegration_FalsePositiveCapture_UnlinkedCommenterDenied
// proves an unresolved (never signed into Narvi) commenter is denied,
// exactly like every other state-changing GitHub actor command
// (actorauthz.AuthorizeLinkedActor's own `!Valid -> false` short-circuit).
func TestGitHubIntegration_FalsePositiveCapture_UnlinkedCommenterDenied(t *testing.T) {
	ctx := context.Background()
	rig, _ := newFalsePositiveTestRig(t)

	const repoFullName = "acme/fp-unlinked-repo"
	// commenterID 80080005 is never linked via createLinkedGitHubUser.
	body := falsePositiveCommentBody(repoFullName, "fp-unlinked-repo", "https://github.com/acme/fp-unlinked-repo.git", 1, 700005, 80080005, "unlinked-user", "false positive: should never land")
	status := postWebhook(t, rig, body, "delivery-fp-unlinked-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	if got := countFalsePositivePatterns(ctx, t, rig, repoFullName); got != 0 {
		t.Errorf("pattern count = %d, want 0 (an unlinked commenter must be denied)", got)
	}
}

// TestGitHubIntegration_FalsePositiveCapture_EmptyReasonNotCaptured proves
// falsepositive.ValidateReason's own rejection reaches all the way through:
// a bare "false positive:" with nothing after it captures nothing.
func TestGitHubIntegration_FalsePositiveCapture_EmptyReasonNotCaptured(t *testing.T) {
	ctx := context.Background()
	rig, _ := newFalsePositiveTestRig(t)

	const repoFullName = "acme/fp-empty-reason-repo"
	const commenterID = 80080006
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := falsePositiveCommentBody(repoFullName, "fp-empty-reason-repo", "https://github.com/acme/fp-empty-reason-repo.git", 1, 700006, commenterID, "maintainer-user", "false positive:")
	status := postWebhook(t, rig, body, "delivery-fp-empty-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	if got := countFalsePositivePatterns(ctx, t, rig, repoFullName); got != 0 {
		t.Errorf("pattern count = %d, want 0 (an empty reason must not be captured)", got)
	}
}

// TestGitHubIntegration_FalsePositiveCapture_RedeliveredComment_Idempotent
// is §22.2's own central proof: a REDELIVERED (or retried) capture
// command for the SAME comment id -- a DIFFERENT webhook delivery id, so
// the OUTER webhook_deliveries dedupe claim does not itself prevent
// reprocessing -- must never double-insert. The UpsertFalsePositivePattern
// ON CONFLICT clause is what provides idempotency here, not the outer
// claim.
func TestGitHubIntegration_FalsePositiveCapture_RedeliveredComment_Idempotent(t *testing.T) {
	ctx := context.Background()
	rig, patterns := newFalsePositiveTestRig(t)

	const repoFullName = "acme/fp-redelivered-repo"
	const commenterID = 80080007
	const commentID = 700007
	maintainer := createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := falsePositiveCommentBody(repoFullName, "fp-redelivered-repo", "https://github.com/acme/fp-redelivered-repo.git", 1, commentID, commenterID, "maintainer-user", "false positive: redelivered pattern")

	first := postWebhook(t, rig, body, "delivery-fp-redelivered-first")
	if first != http.StatusOK {
		t.Fatalf("first delivery status = %d, want %d", first, http.StatusOK)
	}
	if got := countFalsePositivePatterns(ctx, t, rig, repoFullName); got != 1 {
		t.Fatalf("pattern count after first delivery = %d, want 1", got)
	}

	// A DIFFERENT delivery id, SAME comment id -- simulates a human-
	// triggered GitHub "Redeliver" for the same underlying comment event.
	second := postWebhook(t, rig, body, "delivery-fp-redelivered-second")
	if second != http.StatusOK {
		t.Fatalf("second delivery status = %d, want %d", second, http.StatusOK)
	}

	if got := countFalsePositivePatterns(ctx, t, rig, repoFullName); got != 1 {
		t.Fatalf("pattern count after redelivered comment = %d, want STILL 1 (idempotent upsert keyed on comment id)", got)
	}

	// The row's own lifecycle state must be UNTOUCHED by the redelivery --
	// retire it, then redeliver a THIRD time, and confirm it is still
	// retired (never silently reset back to active by the re-observed
	// comment).
	rows, err := patterns.List(ctx, repoFullName, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("List: rows=%v err=%v", rows, err)
	}
	if _, err := patterns.Retire(ctx, rows[0].ID, maintainer.ID); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	third := postWebhook(t, rig, body, "delivery-fp-redelivered-third")
	if third != http.StatusOK {
		t.Fatalf("third delivery status = %d, want %d", third, http.StatusOK)
	}

	after, err := patterns.Get(ctx, rows[0].ID)
	if err != nil {
		t.Fatalf("Get after third delivery: %v", err)
	}
	if !after.RetiredAt.Valid {
		t.Error("RetiredAt.Valid = false after a redelivered comment -- the upsert must never resurrect a retired pattern")
	}
}
