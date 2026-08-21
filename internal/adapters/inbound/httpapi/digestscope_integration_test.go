//go:build integration

// Integration tests for GET /api/repos/{owner}/{repo}/digest-scope
// (digestscope.go's own GetRepoDigestScope), against a real Postgres
// instance -- sharing this package's own testRig
// (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestGetRepoDigestScope_ViewerAllowed_UnknownRepo404s proves TWO things
// at once: (1) authz.ActionViewAnalytics is genuinely a §13.3 row 1
// action -- a viewer may read this endpoint, unlike the admin-only
// environments/prompt-template surfaces this same Step adds; (2) the
// SAME resolveKnownRepo 404 every other repo-scoped route in this package
// enforces applies here too -- a repo this deployment has never seen a
// review session for is refused, never silently rendered as "zero
// channels".
func TestGetRepoDigestScope_ViewerAllowed_UnknownRepo404s(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/never-seen/digest-scope", nil, nil, token)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestGetRepoDigestScope_KnownRepo_NoThreadedSessionsRendersEmpty proves
// a repo that IS known to this deployment (github_pr_sessions has claimed
// it) but has never threaded a review session through Slack or Linear
// renders empty channel/organization lists -- never a 500, never a
// fabricated entry.
func TestGetRepoDigestScope_KnownRepo_NoThreadedSessionsRendersEmpty(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)
	rig.markRepoKnown(ctx, t, "acme/digest-scope-empty")

	var resp restdtos.RepoDigestScope
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/digest-scope-empty/digest-scope", nil, &resp, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if resp.RepoFullName != "acme/digest-scope-empty" {
		t.Errorf("RepoFullName = %q, want %q", resp.RepoFullName, "acme/digest-scope-empty")
	}
	if len(resp.SlackChannelIds) != 0 {
		t.Errorf("SlackChannelIds = %v, want empty", resp.SlackChannelIds)
	}
	if len(resp.LinearOrganizationIds) != 0 {
		t.Errorf("LinearOrganizationIds = %v, want empty", resp.LinearOrganizationIds)
	}
	if resp.LookbackDays <= 0 {
		t.Errorf("LookbackDays = %d, want > 0 (platform.Timeouts.DigestChannelDiscoveryLookback)", resp.LookbackDays)
	}
}
