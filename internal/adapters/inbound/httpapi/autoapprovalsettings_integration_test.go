//go:build integration

// Integration tests for §21's own (§21.2) auto-approval-settings/
// auto-merge REST routes (reposettings.go's own PutAutoApprovalSettings/
// PutAutoMergeToggle), against a real Postgres instance -- sharing this
// package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestPutAutoApprovalSettings_MaintainerAllowed_RoundTrips is THE
// property this Step's own endpoint split exists to prove: a maintainer
// (§13.3 row 5, "auto-approval eligibility config") can configure the
// eligibility criteria WITHOUT holding any admin-only permission --
// unlike PUT /settings, which demands ActionConfigureBlockOnHighRisk/
// ActionToggleSentinelAutoFix (admin only) unconditionally.
func TestPutAutoApprovalSettings_MaintainerAllowed_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/auto-approval-maintainer")

	var putResp restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/auto-approval-maintainer/auto-approval-settings",
		[]byte(`{"maxAutoApproveFilesChanged":15,"sensitiveBlastRadiusTags":["auth","migrations"]}`), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
	}
	if putResp.MaxAutoApproveFilesChanged == nil || *putResp.MaxAutoApproveFilesChanged != 15 {
		t.Errorf("MaxAutoApproveFilesChanged = %v, want 15", putResp.MaxAutoApproveFilesChanged)
	}
	if putResp.SensitiveBlastRadiusTags == nil || len(*putResp.SensitiveBlastRadiusTags) != 2 {
		t.Errorf("SensitiveBlastRadiusTags = %v, want 2 tags", putResp.SensitiveBlastRadiusTags)
	}

	var getResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/auto-approval-maintainer/settings", nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if getResp.MaxAutoApproveFilesChanged == nil || *getResp.MaxAutoApproveFilesChanged != 15 {
		t.Errorf("GET MaxAutoApproveFilesChanged = %v, want 15 (must reflect the PUT)", getResp.MaxAutoApproveFilesChanged)
	}
}

// TestPutAutoApprovalSettings_MemberDenied proves an ordinary member
// still cannot reach this row-5 endpoint.
func TestPutAutoApprovalSettings_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/auto-approval-member/auto-approval-settings",
		[]byte(`{"maxAutoApproveFilesChanged":15,"sensitiveBlastRadiusTags":null}`), nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestPutAutoMergeToggle_AdminAllowed_RoundTrips proves an admin can arm
// the toggle and a subsequent GET reflects it.
func TestPutAutoMergeToggle_AdminAllowed_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/auto-merge-admin")

	var putResp restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/auto-merge-admin/auto-merge", []byte(`{"enabled":true}`), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.AutoMergeEnabled {
		t.Errorf("PUT response AutoMergeEnabled = false, want true")
	}

	var getResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/auto-merge-admin/settings", nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if !getResp.AutoMergeEnabled {
		t.Errorf("GET response AutoMergeEnabled = false, want true (must reflect the PUT)")
	}
}

// TestPutAutoMergeToggle_MaintainerDenied is the OTHER half of the
// property TestPutAutoApprovalSettings_MaintainerAllowed_RoundTrips
// proves: the SAME maintainer who CAN configure eligibility criteria
// must NOT be able to arm the unattended-merge toggle -- §13.3's own
// explicit row 5 vs row 6 split (auto-approval config is maintainer+,
// the auto-merge toggle is admin only, "since it ends in an unattended
// merge... not a human Merge click").
func TestPutAutoMergeToggle_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/auto-merge-maintainer-denied/auto-merge", []byte(`{"enabled":true}`), nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/auto-merge-maintainer-denied"); err == nil {
		t.Error("repo_settings row exists after a denied maintainer PUT, want no row at all")
	}
}

// TestPutAutoApprovalSettings_PreservesAutoMergeToggle_ReadModifyWrite
// proves the read-modify-write discipline PutAutoApprovalSettings' own
// doc comment describes: arming auto-merge (admin), then a maintainer
// separately tuning the diff-size threshold, must never silently
// disarm auto-merge as a side effect.
func TestPutAutoApprovalSettings_PreservesAutoMergeToggle_ReadModifyWrite(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	const repoFullName = "acme/rmw-preserve-automerge"
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	_, maintainerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, repoFullName)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/auto-merge", []byte(`{"enabled":true}`), nil, adminToken)
	if status != http.StatusOK {
		t.Fatalf("arm auto-merge status = %d, want %d", status, http.StatusOK)
	}

	var putResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/auto-approval-settings",
		[]byte(`{"maxAutoApproveFilesChanged":42,"sensitiveBlastRadiusTags":null}`), &putResp, maintainerToken)
	if status != http.StatusOK {
		t.Fatalf("configure eligibility status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.AutoMergeEnabled {
		t.Error("AutoMergeEnabled = false after a maintainer-only eligibility-config PUT, want true (must be preserved, never a side effect of an unrelated field's own write)")
	}
}

// TestPutAutoMergeToggle_PreservesEligibilityConfig_ReadModifyWrite is
// the mirror image: arming auto-merge must never reset an
// already-configured diff-size threshold/sensitive-tag list back to
// "unconfigured".
func TestPutAutoMergeToggle_PreservesEligibilityConfig_ReadModifyWrite(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	const repoFullName = "acme/rmw-preserve-eligibility"
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	_, maintainerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, repoFullName)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/auto-approval-settings",
		[]byte(`{"maxAutoApproveFilesChanged":7,"sensitiveBlastRadiusTags":["secrets"]}`), nil, maintainerToken)
	if status != http.StatusOK {
		t.Fatalf("configure eligibility status = %d, want %d", status, http.StatusOK)
	}

	var putResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/auto-merge", []byte(`{"enabled":true}`), &putResp, adminToken)
	if status != http.StatusOK {
		t.Fatalf("arm auto-merge status = %d, want %d", status, http.StatusOK)
	}
	if putResp.MaxAutoApproveFilesChanged == nil || *putResp.MaxAutoApproveFilesChanged != 7 {
		t.Errorf("MaxAutoApproveFilesChanged = %v after arming auto-merge, want 7 (must be preserved)", putResp.MaxAutoApproveFilesChanged)
	}
	if putResp.SensitiveBlastRadiusTags == nil || len(*putResp.SensitiveBlastRadiusTags) != 1 {
		t.Errorf("SensitiveBlastRadiusTags = %v after arming auto-merge, want 1 tag preserved", putResp.SensitiveBlastRadiusTags)
	}
}

// TestGetRepoSettings_ContradictionRate_NotYetComputed_DistinctFromZero
// is §21's own explicitly-pinned mutation test: "the not-yet-computed
// sentinel vs a real zero", applied here to the contradiction-rate
// calibration read model's own wire rendering.
func TestGetRepoSettings_ContradictionRate_NotYetComputed_DistinctFromZero(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/no-outcomes-yet")

	var got restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/no-outcomes-yet/settings", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.ContradictionRateComputed {
		t.Error("ContradictionRateComputed = true, want false (no auto-approval outcome has ever been recorded for this repo)")
	}
	if got.ContradictionRatePercent != nil {
		t.Errorf("ContradictionRatePercent = %v, want nil (never a fabricated zero standing in for 'no data')", *got.ContradictionRatePercent)
	}
	if got.ContradictionSampleSize != 0 {
		t.Errorf("ContradictionSampleSize = %d, want 0", got.ContradictionSampleSize)
	}
}
