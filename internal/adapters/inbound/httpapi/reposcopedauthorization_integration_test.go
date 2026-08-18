//go:build integration

// Integration tests for fix/repo-scoped-authorization: before this batch,
// each of the four handler families below authorized a repo-scoped request
// with an EMPTY authz.Resource{} -- the URL's own {owner}/{repo} never
// entered the authorization decision at all, so a caller holding the right
// ROLE for one repository passed the identical check for EVERY repository,
// simply by editing the URL. This file proves the fix: resolveKnownRepo
// (reposettings.go) now also confirms the URL's repo is one this
// deployment genuinely knows about (a committed github_pr_sessions row
// exists for it -- see that function's own extended doc comment for the
// full "why this signal" reasoning), and every one of the 15 call sites
// this defect touched now goes through it.
//
// Mirrors this package's own established authz-integration-test style
// (e.g. reposettings_integration_test.go's TestGetRepoSettings_MemberDenied,
// providercredentials_integration_test.go's
// TestUpdateProviderCredential_CrossScope_NotFound) -- table-driven where
// the shape allows, sharing testRig/createUserWithRole/rig.markRepoKnown.
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// repoScopedRouteCase names one of the 15 call sites fix/repo-scoped-
// authorization closes -- role is the role that ALREADY passes this
// route's own §13.3 role check (so a 404 in the unknown-repo case below
// can only be attributed to the repo-known gate, never a role denial
// masquerading as one).
type repoScopedRouteCase struct {
	name   string
	role   sqlcgen.UserRole
	method string
	path   string
	body   []byte
}

// repoScopedRouteCases is every one of the 15 defect sites named in this
// batch's own commit message, one row each.
func repoScopedRouteCases(repo string) []repoScopedRouteCase {
	return []repoScopedRouteCase{
		{"GetRepoSettings", sqlcgen.UserRoleAdmin, http.MethodGet, "/api/repos/" + repo + "/settings", nil},
		{"PutRepoSettings", sqlcgen.UserRoleAdmin, http.MethodPut, "/api/repos/" + repo + "/settings", []byte(`{"blockOnHighRisk":true}`)},
		{"PutAutoApprovalSettings", sqlcgen.UserRoleMaintainer, http.MethodPut, "/api/repos/" + repo + "/auto-approval-settings", []byte(`{"maxAutoApproveFilesChanged":15,"sensitiveBlastRadiusTags":["auth"]}`)},
		{"PutAutoMergeToggle", sqlcgen.UserRoleAdmin, http.MethodPut, "/api/repos/" + repo + "/auto-merge", []byte(`{"enabled":true}`)},
		{"PutAutoRetriggerReviewToggle", sqlcgen.UserRoleAdmin, http.MethodPut, "/api/repos/" + repo + "/auto-retrigger-review", []byte(`{"enabled":true}`)},
		{"PutDescriptionAutofixToggle", sqlcgen.UserRoleAdmin, http.MethodPut, "/api/repos/" + repo + "/description-autofix", []byte(`{"enabled":true}`)},
		{"PutReviewDepthConfig", sqlcgen.UserRoleAdmin, http.MethodPut, "/api/repos/" + repo + "/review-depth", []byte(`{"mode":"always_deep","deepPaths":["internal/billing"]}`)},
		{"PutReviewCostBudget", sqlcgen.UserRoleAdmin, http.MethodPut, "/api/repos/" + repo + "/review-cost-budget", []byte(`{"lightUsd":1.25,"deepUsd":10}`)},
		// GetReviewAnalytics is granted down to viewer (§13.3 row 1) --
		// the WIDEST blast radius of the four families this batch fixes
		// (this batch's own commit message: "any authenticated user reads
		// any repository's review analytics").
		{"GetReviewAnalytics", sqlcgen.UserRoleViewer, http.MethodGet, "/api/repos/" + repo + "/review-analytics", nil},
		{"ListFalsePositivePatterns", sqlcgen.UserRoleMaintainer, http.MethodGet, "/api/repos/" + repo + "/false-positive-patterns", nil},
		{"RetireFalsePositivePattern", sqlcgen.UserRoleMaintainer, http.MethodPost, "/api/repos/" + repo + "/false-positive-patterns/00000000-0000-0000-0000-000000000000/retire", nil},
		{"CreateRepoProviderCredential", sqlcgen.UserRoleMaintainer, http.MethodPost, "/api/repos/" + repo + "/provider-credentials", []byte(`{"provider":"anthropic","value":"sk-should-never-be-stored"}`)},
		{"ListRepoProviderCredentials", sqlcgen.UserRoleMaintainer, http.MethodGet, "/api/repos/" + repo + "/provider-credentials", nil},
		{"UpdateRepoProviderCredentialValue", sqlcgen.UserRoleMaintainer, http.MethodPut, "/api/repos/" + repo + "/provider-credentials/00000000-0000-0000-0000-000000000000", []byte(`{"value":"sk-should-never-apply"}`)},
		{"DeleteRepoProviderCredential", sqlcgen.UserRoleMaintainer, http.MethodDelete, "/api/repos/" + repo + "/provider-credentials/00000000-0000-0000-0000-000000000000", nil},
	}
}

// TestRepoScopedRoutes_UnknownRepo_Refused is the core regression proof,
// table-driven across all 15 call sites: a caller holding the ROLE this
// route requires (never denied on that basis -- every role above is
// deliberately the SAME one this route's own existing *_Allowed/
// *_RoundTrips test already proves succeeds once the repo IS known, see
// e.g. TestGetRepoSettings_MaintainerAllowed_ButPutStillAdminOnly,
// TestPutAutoMergeToggle_AdminAllowed_RoundTrips) still gets 404 "repo not
// found" when the URL names a repository this deployment has never
// actually seen GitHub webhook traffic for -- no github_pr_sessions row
// exists, so resolveKnownRepo's own confirmRepoKnown call rejects it
// before any store call runs. Never seeds rig.markRepoKnown for these
// repos -- that is the entire point of this test.
func TestRepoScopedRoutes_UnknownRepo_Refused(t *testing.T) {
	for _, tc := range repoScopedRouteCases("acme/never-onboarded") {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t)
			ctx := context.Background()
			_, token := createUserWithRole(ctx, t, rig, tc.role)

			status := rig.doJSON(t, tc.method, tc.path, tc.body, nil, token)
			if status != http.StatusNotFound {
				t.Errorf("status = %d, want %d (repo never onboarded, no github_pr_sessions row -- role alone must not be enough)", status, http.StatusNotFound)
			}
		})
	}
}

// TestRepoScopedRoutes_KnownRepo_RoleStillGoverns is
// TestRepoScopedRoutes_UnknownRepo_Refused's positive counterpart for the
// subset of the 15 call sites that need no pre-existing resource (repo
// settings/provider-credential/false-positive-pattern reads, and the two
// simplest writes) -- proves the fix is additive, not a new blanket denial:
// once rig.markRepoKnown seeds a real github_pr_sessions row, the SAME
// role that was refused above now succeeds, and the ordinary §13.3 role
// gate is still the ONLY other thing governing the outcome. The remaining
// call sites (PutRepoSettings, PutAutoApprovalSettings,
// PutAutoRetriggerReviewToggle, PutDescriptionAutofixToggle,
// PutReviewDepthConfig, PutReviewCostBudget, RetireFalsePositivePattern,
// UpdateRepoProviderCredentialValue, DeleteRepoProviderCredential) get
// this SAME "known repo succeeds" proof from their own pre-existing
// round-trip tests (reposettings_integration_test.go,
// autoapprovalsettings_integration_test.go,
// autoretriggerreviewtoggle_integration_test.go,
// descriptionautofixtoggle_integration_test.go,
// reviewdepthconfig_integration_test.go, reviewcostbudget_integration_test.go,
// falsepositivepatterns_integration_test.go,
// providercredentials_integration_test.go) -- this batch added
// rig.markRepoKnown seeding to every one of them (see this batch's own
// commit message), so their continuing to pass 200/201 IS this same proof,
// not merely unaffected.
func TestRepoScopedRoutes_KnownRepo_RoleStillGoverns(t *testing.T) {
	tests := []struct {
		name       string
		role       sqlcgen.UserRole
		method     string
		path       string
		body       []byte
		wantStatus int
	}{
		{"GetRepoSettings", sqlcgen.UserRoleAdmin, http.MethodGet, "/api/repos/acme/known-repo-settings/settings", nil, http.StatusOK},
		{"PutAutoMergeToggle", sqlcgen.UserRoleAdmin, http.MethodPut, "/api/repos/acme/known-repo-auto-merge/auto-merge", []byte(`{"enabled":true}`), http.StatusOK},
		{"GetReviewAnalytics", sqlcgen.UserRoleViewer, http.MethodGet, "/api/repos/acme/known-repo-analytics/review-analytics", nil, http.StatusOK},
		{"ListFalsePositivePatterns", sqlcgen.UserRoleMaintainer, http.MethodGet, "/api/repos/acme/known-repo-fp-list/false-positive-patterns", nil, http.StatusOK},
		{"CreateRepoProviderCredential", sqlcgen.UserRoleMaintainer, http.MethodPost, "/api/repos/acme/known-repo-cred-create/provider-credentials", []byte(`{"provider":"anthropic","value":"sk-real"}`), http.StatusCreated},
		{"ListRepoProviderCredentials", sqlcgen.UserRoleMaintainer, http.MethodGet, "/api/repos/acme/known-repo-cred-list/provider-credentials", nil, http.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t)
			ctx := context.Background()
			_, token := createUserWithRole(ctx, t, rig, tc.role)

			// Extract "acme/<suffix>" from the path -- every path above
			// has it as the third segment.
			repo := repoFromPath(t, tc.path)
			rig.markRepoKnown(ctx, t, repo)

			status := rig.doJSON(t, tc.method, tc.path, tc.body, nil, token)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d (repo IS known -- the ordinary role gate is the only thing left to deny on)", status, tc.wantStatus)
			}
		})
	}
}

// repoFromPath extracts the "{owner}/{repo}" segment from a
// "/api/repos/{owner}/{repo}/..." path -- a small test-only helper so
// TestRepoScopedRoutes_KnownRepo_RoleStillGoverns's own table doesn't need
// to spell the repo out a second time, independently of path.
func repoFromPath(t *testing.T, path string) string {
	t.Helper()
	const prefix = "/api/repos/"
	if len(path) <= len(prefix) || path[:len(prefix)] != prefix {
		t.Fatalf("repoFromPath: %q does not start with %q", path, prefix)
	}
	rest := path[len(prefix):]
	slashes := 0
	for i, c := range rest {
		if c == '/' {
			slashes++
			if slashes == 2 {
				return rest[:i]
			}
		}
	}
	t.Fatalf("repoFromPath: %q has no owner/repo/... shape", path)
	return ""
}

// TestRetireFalsePositivePattern_UnknownRepo_NoAuditLogRow proves the
// "audit behaviour" half of this batch's own requirement: a request
// refused for naming an unknown repo writes NO audit_log row at all --
// mirroring this codebase's own established convention that a role-based
// authz REFUSAL is never audit-logged either (helpers.go's own authorize,
// ErrForbidden branch: a plain 403, no audit_log row -- grepped every
// recordAuditLog call site in this package before writing this test, see
// resolveKnownRepo's own logUnknownRepoRefusal doc comment for the full
// "why" this mirrors coalesce.go's Warn-only precedent instead). Contrasts
// directly with TestRetireFalsePositivePattern_MaintainerAllowed (same
// file), which DOES write a "false_positive_pattern.retire" row on an
// actual success -- proving this isn't "this endpoint never audits
// anything", only "a refusal specifically never does".
func TestRetireFalsePositivePattern_UnknownRepo_NoAuditLogRow(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	before, err := rig.auditLog.List(ctx, 200, 0)
	if err != nil {
		t.Fatalf("List (before): %v", err)
	}

	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/audit-unknown-repo/false-positive-patterns/00000000-0000-0000-0000-000000000000/retire", nil, nil, token)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", status, http.StatusNotFound)
	}

	after, err := rig.auditLog.List(ctx, 200, 0)
	if err != nil {
		t.Fatalf("List (after): %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("audit_log grew from %d to %d rows after a refused (unknown-repo) retire attempt, want unchanged", len(before), len(after))
	}
}
