//go:build integration

// Integration tests for Step 67's own (§26.2) description-autofix REST
// route (reposettings.go's own PutDescriptionAutofixToggle), against a
// real Postgres instance -- sharing this package's own testRig
// (httpapi_integration_test.go), mirroring autoretriggerreviewtoggle_
// integration_test.go's own PutAutoRetriggerReviewToggle coverage exactly
// (the SAME admin-only, column-scoped toggle shape).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestPutDescriptionAutofixToggle_AdminAllowed_RoundTrips proves an admin
// can arm the toggle and a subsequent GET reflects it.
func TestPutDescriptionAutofixToggle_AdminAllowed_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/description-autofix-admin")

	var putResp restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/description-autofix-admin/description-autofix", []byte(`{"enabled":true}`), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.DescriptionAutofixEnabled {
		t.Errorf("PUT response DescriptionAutofixEnabled = false, want true")
	}

	var getResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/description-autofix-admin/settings", nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if !getResp.DescriptionAutofixEnabled {
		t.Errorf("GET response DescriptionAutofixEnabled = false, want true (must reflect the PUT)")
	}
}

// TestGetRepoSettings_DescriptionAutofixEnabled_NoRowYet_DefaultsOff
// proves a repo with no repo_settings row at all reads back
// descriptionAutofixEnabled: false -- the table's own established
// fail-closed-on-missing-row precedent, matching every sibling toggle.
func TestGetRepoSettings_DescriptionAutofixEnabled_NoRowYet_DefaultsOff(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/description-autofix-no-row")

	var getResp restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/description-autofix-no-row/settings", nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if getResp.DescriptionAutofixEnabled {
		t.Error("DescriptionAutofixEnabled = true for a repo with no repo_settings row at all, want false (fail-closed default)")
	}
}

// TestPutDescriptionAutofixToggle_MaintainerDenied proves this is
// admin-only, no maintainer carve-out -- §26.2/§13.3 row 6, the SAME
// placement as ActionToggleSentinelAutoFix/ActionToggleAutoMerge/
// ActionToggleAutoRetriggerReview.
func TestPutDescriptionAutofixToggle_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/description-autofix-maintainer-denied/description-autofix", []byte(`{"enabled":true}`), nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/description-autofix-maintainer-denied"); err == nil {
		t.Error("repo_settings row exists after a denied maintainer PUT, want no row at all")
	}
}

// TestPutDescriptionAutofixToggle_MemberDenied proves a plain member is
// also denied -- the SAME admin-only gate, no member escape hatch either.
func TestPutDescriptionAutofixToggle_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/description-autofix-member-denied/description-autofix", []byte(`{"enabled":true}`), nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/description-autofix-member-denied"); err == nil {
		t.Error("repo_settings row exists after a denied member PUT, want no row at all")
	}
}

// TestPutDescriptionAutofixToggle_PreservesAutoMergeToggle_ColumnScoped
// proves the column-scoped write discipline this endpoint's own doc
// comment describes (Step 62 review finding C5's pattern, applied here from
// the start): arming auto-merge first, then separately arming
// description-autofix, must never silently disarm auto-merge as a side
// effect -- and the reverse.
func TestPutDescriptionAutofixToggle_PreservesAutoMergeToggle_ColumnScoped(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	repoFullName := "acme/description-autofix-column-scoped"
	rig.markRepoKnown(ctx, t, repoFullName)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/auto-merge", []byte(`{"enabled":true}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("PUT auto-merge status = %d, want %d", status, http.StatusOK)
	}

	var putResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/description-autofix", []byte(`{"enabled":true}`), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT description-autofix status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.AutoMergeEnabled {
		t.Errorf("PUT description-autofix response AutoMergeEnabled = false, want true (must be preserved, column-scoped write)")
	}
	if !putResp.DescriptionAutofixEnabled {
		t.Errorf("PUT description-autofix response DescriptionAutofixEnabled = false, want true")
	}

	settings, err := rig.repoSettings.Get(ctx, repoFullName)
	if err != nil {
		t.Fatalf("get repo settings: %v", err)
	}
	if !settings.AutoMergeEnabled {
		t.Error("repo_settings.auto_merge_enabled = false after an UNRELATED description-autofix write, want true (preserved)")
	}
	if !settings.DescriptionAutofixEnabled {
		t.Error("repo_settings.description_autofix_enabled = false, want true")
	}
}
