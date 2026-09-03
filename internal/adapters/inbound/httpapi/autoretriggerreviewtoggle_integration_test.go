//go:build integration

// Integration tests for §24's own (§24.5) auto-retrigger-review REST
// route (reposettings.go's own PutAutoRetriggerReviewToggle), against a
// real Postgres instance -- sharing this package's own testRig
// (httpapi_integration_test.go), mirroring autoapprovalsettings_
// integration_test.go's own PutAutoMergeToggle coverage exactly (the
// SAME admin-only, column-scoped toggle shape).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestPutAutoRetriggerReviewToggle_AdminAllowed_RoundTrips proves an
// admin can arm the toggle and a subsequent GET reflects it.
func TestPutAutoRetriggerReviewToggle_AdminAllowed_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/auto-retrigger-review-admin")

	var putResp restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/auto-retrigger-review-admin/auto-retrigger-review", []byte(`{"enabled":true}`), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.AutoRetriggerReviewEnabled {
		t.Errorf("PUT response AutoRetriggerReviewEnabled = false, want true")
	}

	var getResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/auto-retrigger-review-admin/settings", nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if !getResp.AutoRetriggerReviewEnabled {
		t.Errorf("GET response AutoRetriggerReviewEnabled = false, want true (must reflect the PUT)")
	}
}

// TestPutAutoRetriggerReviewToggle_MaintainerDenied proves this is
// admin-only, no maintainer carve-out -- §24.5/§13.3 row 6, the SAME
// placement as ActionToggleSentinelAutoFix/ActionToggleAutoMerge.
func TestPutAutoRetriggerReviewToggle_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/auto-retrigger-review-maintainer-denied/auto-retrigger-review", []byte(`{"enabled":true}`), nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/auto-retrigger-review-maintainer-denied"); err == nil {
		t.Error("repo_settings row exists after a denied maintainer PUT, want no row at all")
	}
}

// TestPutAutoRetriggerReviewToggle_PreservesAutoMergeToggle_ColumnScoped
// proves the column-scoped write discipline this endpoint's own doc
// comment describes: arming auto-merge first, then separately arming
// auto-retrigger-review, must never silently disarm auto-merge as a side
// effect -- and the reverse.
func TestPutAutoRetriggerReviewToggle_PreservesAutoMergeToggle_ColumnScoped(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	repoFullName := "acme/auto-retrigger-review-column-scoped"
	rig.markRepoKnown(ctx, t, repoFullName)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/auto-merge", []byte(`{"enabled":true}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("PUT auto-merge status = %d, want %d", status, http.StatusOK)
	}

	var putResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/auto-retrigger-review", []byte(`{"enabled":true}`), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT auto-retrigger-review status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.AutoMergeEnabled {
		t.Errorf("PUT auto-retrigger-review response AutoMergeEnabled = false, want true (must be preserved, column-scoped write)")
	}
	if !putResp.AutoRetriggerReviewEnabled {
		t.Errorf("PUT auto-retrigger-review response AutoRetriggerReviewEnabled = false, want true")
	}

	settings, err := rig.repoSettings.Get(ctx, repoFullName)
	if err != nil {
		t.Fatalf("get repo settings: %v", err)
	}
	if !settings.AutoMergeEnabled {
		t.Error("repo_settings.auto_merge_enabled = false after an UNRELATED auto-retrigger-review write, want true (preserved)")
	}
	if !settings.AutoRetriggerReviewEnabled {
		t.Error("repo_settings.auto_retrigger_review_enabled = false, want true")
	}
}
