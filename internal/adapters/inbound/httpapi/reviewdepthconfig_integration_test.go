//go:build integration

// Integration tests for §26.3's own (§26.3) per-repo reviewDepth config
// REST route (reposettings.go's own PutReviewDepthConfig), against a real
// Postgres instance -- sharing this package's own testRig (httpapi_
// integration_test.go), mirroring autoretriggerreviewtoggle_integration_
// test.go's/descriptionautofixtoggle_integration_test.go's own identical
// admin-only, column-scoped shape.
//
// Adversarial-review fix, D8: before this fix, this route was not even
// mounted in this test rig (httpapi_integration_test.go), and
// authz.ActionConfigureReviewDepth had zero rows in TestAuthorize_
// ExhaustiveMatrix (internal/domain/authz/authorize_test.go) -- reproduced
// by widening ActionConfigureReviewDepth's own allowed roles from
// admin-only to include maintainer/member/viewer and observing the entire
// test suite stay green.
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestPutReviewDepthConfig_AdminAllowed_RoundTrips proves an admin can
// configure reviewDepth.mode/deepPaths and a subsequent GET reflects it --
// mirrors TestPutAutoRetriggerReviewToggle_AdminAllowed_RoundTrips/
// TestPutDescriptionAutofixToggle_AdminAllowed_RoundTrips exactly.
func TestPutReviewDepthConfig_AdminAllowed_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/review-depth-admin")

	var putResp restdtos.RepoSettings
	body := []byte(`{"mode":"always_deep","deepPaths":["internal/billing"]}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/review-depth-admin/review-depth", body, &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
	}
	if putResp.ReviewDepthMode == nil || *putResp.ReviewDepthMode != "always_deep" {
		t.Errorf("PUT response ReviewDepthMode = %v, want %q", putResp.ReviewDepthMode, "always_deep")
	}
	if putResp.ReviewDepthDeepPaths == nil || len(*putResp.ReviewDepthDeepPaths) != 1 || (*putResp.ReviewDepthDeepPaths)[0] != "internal/billing" {
		t.Errorf("PUT response ReviewDepthDeepPaths = %v, want [internal/billing]", putResp.ReviewDepthDeepPaths)
	}

	var getResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/review-depth-admin/settings", nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if getResp.ReviewDepthMode == nil || *getResp.ReviewDepthMode != "always_deep" {
		t.Errorf("GET response ReviewDepthMode = %v, want %q (must reflect the PUT)", getResp.ReviewDepthMode, "always_deep")
	}
	if getResp.ReviewDepthDeepPaths == nil || len(*getResp.ReviewDepthDeepPaths) != 1 || (*getResp.ReviewDepthDeepPaths)[0] != "internal/billing" {
		t.Errorf("GET response ReviewDepthDeepPaths = %v, want [internal/billing] (must reflect the PUT)", getResp.ReviewDepthDeepPaths)
	}
}

// TestPutReviewDepthConfig_MaintainerDenied proves this is admin-only, no
// maintainer carve-out -- §26.3/§13.3 row 6, the SAME placement as
// ActionToggleSentinelAutoFix/ActionToggleAutoMerge/
// ActionToggleAutoRetriggerReview/ActionToggleDescriptionAutofix.
func TestPutReviewDepthConfig_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	body := []byte(`{"mode":"always_deep","deepPaths":null}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/review-depth-maintainer-denied/review-depth", body, nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/review-depth-maintainer-denied"); err == nil {
		t.Error("repo_settings row exists after a denied maintainer PUT, want no row at all")
	}
}

// TestPutReviewDepthConfig_MemberDenied proves a plain member is also
// denied -- the SAME admin-only gate, no member escape hatch either.
func TestPutReviewDepthConfig_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMember)

	body := []byte(`{"mode":"always_deep","deepPaths":null}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/review-depth-member-denied/review-depth", body, nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/review-depth-member-denied"); err == nil {
		t.Error("repo_settings row exists after a denied member PUT, want no row at all")
	}
}

// TestPutReviewDepthConfig_ViewerDenied proves a viewer is denied too.
func TestPutReviewDepthConfig_ViewerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	body := []byte(`{"mode":"always_deep","deepPaths":null}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/review-depth-viewer-denied/review-depth", body, nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/review-depth-viewer-denied"); err == nil {
		t.Error("repo_settings row exists after a denied viewer PUT, want no row at all")
	}
}

// TestPutReviewDepthConfig_InvalidMode_BadRequest proves an unrecognized
// mode string is rejected 400, mirroring reviewDepthModeString's own doc
// comment (reposettings.go): "rejected with a 400 rather than silently
// stored and silently reinterpreted".
func TestPutReviewDepthConfig_InvalidMode_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/review-depth-invalid-mode")

	body := []byte(`{"mode":"sometimes","deepPaths":null}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/review-depth-invalid-mode/review-depth", body, nil, token)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/review-depth-invalid-mode"); err == nil {
		t.Error("repo_settings row exists after a rejected invalid-mode PUT, want no row at all")
	}
}

// TestPutReviewDepthConfig_PreservesAutoMergeToggle_ColumnScoped proves
// the column-scoped write discipline this endpoint's own doc comment
// describes: arming auto-merge first,
// then separately configuring reviewDepth, must never silently disarm
// auto-merge as a side effect -- and the reverse.
func TestPutReviewDepthConfig_PreservesAutoMergeToggle_ColumnScoped(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	repoFullName := "acme/review-depth-column-scoped"
	rig.markRepoKnown(ctx, t, repoFullName)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/auto-merge", []byte(`{"enabled":true}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("PUT auto-merge status = %d, want %d", status, http.StatusOK)
	}

	var putResp restdtos.RepoSettings
	body := []byte(`{"mode":"always_light","deepPaths":null}`)
	status = rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/review-depth", body, &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT review-depth status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.AutoMergeEnabled {
		t.Errorf("PUT review-depth response AutoMergeEnabled = false, want true (must be preserved, column-scoped write)")
	}
	if putResp.ReviewDepthMode == nil || *putResp.ReviewDepthMode != "always_light" {
		t.Errorf("PUT review-depth response ReviewDepthMode = %v, want %q", putResp.ReviewDepthMode, "always_light")
	}

	settings, err := rig.repoSettings.Get(ctx, repoFullName)
	if err != nil {
		t.Fatalf("get repo settings: %v", err)
	}
	if !settings.AutoMergeEnabled {
		t.Error("repo_settings.auto_merge_enabled = false after an UNRELATED review-depth write, want true (preserved)")
	}
	if settings.ReviewDepthMode == nil || *settings.ReviewDepthMode != "always_light" {
		t.Errorf("repo_settings.review_depth_mode = %v, want %q", settings.ReviewDepthMode, "always_light")
	}
}
