//go:build integration

// Integration coverage for Step 50's own ("release PR review", §15)
// end-to-end wiring: a real webhook POST, through the real
// coalescer.CreateOrJoin (a real Postgres session-creation transaction),
// through triggerReleaseManifestCheckBestEffort, into a REAL
// *postgres.OutboxStore -- proving the whole chain actually inserts a
// ports.NotificationKindReleaseManifest row, not just that its individual
// pieces compile against each other (already proven by this package's
// own non-integration releasemanifest_test.go, which uses fakes
// throughout). GitHub itself is still faked (SourceControl/PullRequests)
// -- no real outbound GitHub call is possible in this test environment --
// mirrors this package's own established fakePullRequestResolver
// precedent (handler_integration_test.go) for the identical reason.
package github_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	githubingress "github.com/khazaddev/narvi/internal/adapters/inbound/github"
	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// fakeReleaseManifestSourceControl is a test-only releasereview.
// MergedPRLister -- no real HTTP round trip, mirroring this package's
// own fakePullRequestResolver/fakeCommentPoster precedent exactly.
type fakeReleaseManifestSourceControl struct {
	merged []ports.MergedPR
}

func (f *fakeReleaseManifestSourceControl) ListMergedBetween(context.Context, ports.ListMergedBetweenSpec) ([]ports.MergedPR, error) {
	return f.merged, nil
}

// TestGitHubIntegration_ReleasePRDetected_EnqueuesRealOutboxRow proves
// the full chain: a brand-new review session created on a PR whose head
// branch matches the configured release pattern ends with a REAL row in
// Postgres's own outbox table, kind='release_manifest', carrying the
// correctly-identified owner/repo/PR number and a non-empty rendered
// comment body.
func TestGitHubIntegration_ReleasePRDetected_EnqueuesRealOutboxRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	outboxStore := narvipg.NewOutboxStore(pool)

	sourceControl := &fakeReleaseManifestSourceControl{merged: []ports.MergedPR{
		{Number: 201, Title: "fix: something", HasApprovingReview: true, CIConclusionAtMergeSHA: ports.CIConclusionSuccess},
		{Number: 202, Title: "feat: another thing", HasApprovingReview: false},
	}}

	rig := newTestRig(t, func(cfg *githubingress.Config) {
		cfg.PullRequests = &fakePullRequestResolver{pr: githubapi.PullRequest{HeadRef: "release/9.9", BaseRef: "main"}}
		cfg.SourceControl = sourceControl
		cfg.Outbox = outboxStore
		cfg.ReleaseLabel = "release"
		cfg.ReleaseBranchPattern = "release/*"
		cfg.Timeouts = platform.DefaultTimeouts()
	})

	const commenterID = 80000301
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := issueCommentBodyWithCommenter("acme/release-repo", "release-repo", "https://github.com/acme/release-repo.git", 301, "release-check", commenterID, "release-check-user")

	status := postWebhook(t, rig, body, "delivery-release-manifest-1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	var sessionID string
	if err := rig.pool.QueryRow(ctx,
		`SELECT id FROM sessions WHERE spawn_source = 'github' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&sessionID); err != nil {
		t.Fatalf("query session: %v", err)
	}

	var outboxSessionID, kind, payloadText string
	if err := rig.pool.QueryRow(ctx,
		`SELECT session_id::text, kind, payload::text FROM outbox WHERE kind = 'release_manifest' ORDER BY created_at DESC LIMIT 1`,
	).Scan(&outboxSessionID, &kind, &payloadText); err != nil {
		t.Fatalf("query outbox: %v (no release_manifest row was ever enqueued)", err)
	}

	if outboxSessionID != sessionID {
		t.Errorf("outbox row session_id = %q, want %q (the just-created release-review session)", outboxSessionID, sessionID)
	}
	if kind != string(ports.NotificationKindReleaseManifest) {
		t.Errorf("outbox row kind = %q, want %q", kind, ports.NotificationKindReleaseManifest)
	}

	var payload githubapi.ReleaseManifestPayload
	if err := json.Unmarshal([]byte(payloadText), &payload); err != nil {
		t.Fatalf("decode outbox payload: %v", err)
	}
	if payload.Owner != "acme" || payload.Repo != "release-repo" || payload.PRNumber != 301 {
		t.Errorf("payload identity = %+v, want owner=acme repo=release-repo pr_number=301", payload)
	}
	if !strings.Contains(payload.Body, "Release manifest check") {
		t.Errorf("payload.Body missing the expected rendered heading:\n%s", payload.Body)
	}
	if !strings.Contains(payload.Body, "PR #202") {
		t.Errorf("payload.Body missing the expected unreviewed-merge finding for PR #202:\n%s", payload.Body)
	}
}

// TestGitHubIntegration_OrdinaryPR_NeverEnqueuesReleaseManifestRow proves
// an ordinary (non-release) PR's own brand-new session never enqueues a
// release_manifest outbox row at all -- the negative-case sibling to the
// test above, over the SAME real Postgres wiring.
func TestGitHubIntegration_OrdinaryPR_NeverEnqueuesReleaseManifestRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	outboxStore := narvipg.NewOutboxStore(pool)
	sourceControl := &fakeReleaseManifestSourceControl{}

	rig := newTestRig(t, func(cfg *githubingress.Config) {
		cfg.PullRequests = &fakePullRequestResolver{pr: githubapi.PullRequest{HeadRef: "feature/ordinary-thing", BaseRef: "main"}}
		cfg.SourceControl = sourceControl
		cfg.Outbox = outboxStore
		cfg.ReleaseLabel = "release"
		cfg.ReleaseBranchPattern = "release/*"
		cfg.Timeouts = platform.DefaultTimeouts()
	})

	const commenterID = 80000302
	createLinkedGitHubUser(ctx, t, rig.users, rig.identities, commenterID, sqlcgen.UserRoleMaintainer)

	body := issueCommentBodyWithCommenter("acme/ordinary-repo", "ordinary-repo", "https://github.com/acme/ordinary-repo.git", 302, "ordinary-check", commenterID, "ordinary-check-user")

	status := postWebhook(t, rig, body, "delivery-release-manifest-2")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	var count int
	if err := rig.pool.QueryRow(ctx, `SELECT count(*) FROM outbox WHERE kind = 'release_manifest'`).Scan(&count); err != nil {
		t.Fatalf("query outbox: %v", err)
	}
	if count != 0 {
		t.Errorf("release_manifest outbox row count = %d, want 0 (not a release PR)", count)
	}
}
