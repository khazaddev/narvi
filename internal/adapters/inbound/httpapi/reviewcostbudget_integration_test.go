//go:build integration

// Integration tests for Step 69's own (§26.7) per-repo cost-budget config
// REST route (reposettings.go's own PutReviewCostBudget), against a real
// Postgres instance -- sharing this package's own testRig (httpapi_
// integration_test.go), mirroring reviewdepthconfig_integration_test.go's
// own identical admin-only, column-scoped shape.
//
// B9 fix: before this fix, this route was not even mounted in this test
// rig (httpapi_integration_test.go) -- the SAME gap review-depth had
// before its own D8 fix (reviewdepthconfig_integration_test.go's own doc
// comment) -- and the handler accepted an explicit lightUsd/deepUsd of 0,
// which silently collides with internal/domain/reviewtriage.CostBudget's
// own "zero means no ceiling configured" sentinel (ShouldSkipOptionalPass,
// costbudget.go) and resolves to UNLIMITED spend, the opposite of an
// explicit-zero operator's likely intent.
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestPutReviewCostBudget_AdminAllowed_RoundTrips proves an admin can
// configure reviewCostBudget.light/deep and a subsequent GET reflects it --
// mirrors TestPutReviewDepthConfig_AdminAllowed_RoundTrips exactly.
func TestPutReviewCostBudget_AdminAllowed_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/cost-budget-admin")

	var putResp restdtos.RepoSettings
	body := []byte(`{"lightUsd":1.25,"deepUsd":10}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/cost-budget-admin/review-cost-budget", body, &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
	}
	if putResp.ReviewCostBudgetLightUsd == nil || *putResp.ReviewCostBudgetLightUsd != 1.25 {
		t.Errorf("PUT response ReviewCostBudgetLightUsd = %v, want 1.25", putResp.ReviewCostBudgetLightUsd)
	}
	if putResp.ReviewCostBudgetDeepUsd == nil || *putResp.ReviewCostBudgetDeepUsd != 10 {
		t.Errorf("PUT response ReviewCostBudgetDeepUsd = %v, want 10", putResp.ReviewCostBudgetDeepUsd)
	}

	var getResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/cost-budget-admin/settings", nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if getResp.ReviewCostBudgetLightUsd == nil || *getResp.ReviewCostBudgetLightUsd != 1.25 {
		t.Errorf("GET response ReviewCostBudgetLightUsd = %v, want 1.25 (must reflect the PUT)", getResp.ReviewCostBudgetLightUsd)
	}
	if getResp.ReviewCostBudgetDeepUsd == nil || *getResp.ReviewCostBudgetDeepUsd != 10 {
		t.Errorf("GET response ReviewCostBudgetDeepUsd = %v, want 10 (must reflect the PUT)", getResp.ReviewCostBudgetDeepUsd)
	}
}

// TestPutReviewCostBudget_MaintainerDenied proves this is admin-only, no
// maintainer carve-out -- §26.7/§13.3 row 6, the SAME placement as
// ActionConfigureReviewDepth.
func TestPutReviewCostBudget_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	body := []byte(`{"lightUsd":1,"deepUsd":null}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/cost-budget-maintainer-denied/review-cost-budget", body, nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/cost-budget-maintainer-denied"); err == nil {
		t.Error("repo_settings row exists after a denied maintainer PUT, want no row at all")
	}
}

// TestPutReviewCostBudget_ViewerDenied proves a viewer is denied too.
func TestPutReviewCostBudget_ViewerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleViewer)

	body := []byte(`{"lightUsd":1,"deepUsd":null}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/cost-budget-viewer-denied/review-cost-budget", body, nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/cost-budget-viewer-denied"); err == nil {
		t.Error("repo_settings row exists after a denied viewer PUT, want no row at all")
	}
}

// TestPutReviewCostBudget_NegativeRejected_BadRequest proves a negative
// lightUsd/deepUsd is rejected 400 -- the pre-existing half of this
// validation, unaffected by the B9 fix.
func TestPutReviewCostBudget_NegativeRejected_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/cost-budget-negative")

	body := []byte(`{"lightUsd":-0.01,"deepUsd":null}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/cost-budget-negative/review-cost-budget", body, nil, token)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", status, http.StatusBadRequest)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/cost-budget-negative"); err == nil {
		t.Error("repo_settings row exists after a rejected negative-lightUsd PUT, want no row at all")
	}
}

// TestPutReviewCostBudget_ExplicitZeroRejected_BadRequest is the B9
// regression test: an explicit lightUsd/deepUsd of 0 must be rejected 400,
// never silently stored -- internal/domain/reviewtriage.CostBudget's own
// zero value means "no ceiling configured" (ShouldSkipOptionalPass,
// costbudget.go: "a zero ceilingUSD ... NEVER skips"), so a stored 0 here
// would collide with that sentinel and resolve to UNLIMITED spend on this
// path, the opposite of what an operator explicitly setting 0 almost
// certainly intends. Before the B9 fix, the handler's own validation used
// `< 0` (rejecting only negative values) and this exact request round-
// tripped as "success", silently storing the ceiling-defeating value.
func TestPutReviewCostBudget_ExplicitZeroRejected_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/cost-budget-zero-light")
	rig.markRepoKnown(ctx, t, "acme/cost-budget-zero-deep")

	body := []byte(`{"lightUsd":0,"deepUsd":null}`)
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/cost-budget-zero-light/review-cost-budget", body, nil, token)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (an explicit 0 must never silently mean 'unlimited')", status, http.StatusBadRequest)
	}
	if _, err := rig.repoSettings.Get(ctx, "acme/cost-budget-zero-light"); err == nil {
		t.Error("repo_settings row exists after a rejected lightUsd=0 PUT, want no row at all")
	}

	body = []byte(`{"lightUsd":null,"deepUsd":0}`)
	status = rig.doJSON(t, http.MethodPut, "/api/repos/acme/cost-budget-zero-deep/review-cost-budget", body, nil, token)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (deepUsd=0 must be rejected the same way)", status, http.StatusBadRequest)
	}
	if _, err := rig.repoSettings.Get(ctx, "acme/cost-budget-zero-deep"); err == nil {
		t.Error("repo_settings row exists after a rejected deepUsd=0 PUT, want no row at all")
	}
}

// TestPutReviewCostBudget_PreservesAutoMergeToggle_ColumnScoped proves the
// column-scoped write discipline this endpoint's own doc comment describes
// (Step 62 review finding C5's pattern): arming auto-merge first, then
// separately configuring the cost budget, must never silently disarm
// auto-merge as a side effect -- mirrors
// TestPutReviewDepthConfig_PreservesAutoMergeToggle_ColumnScoped exactly.
func TestPutReviewCostBudget_PreservesAutoMergeToggle_ColumnScoped(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	repoFullName := "acme/cost-budget-column-scoped"
	rig.markRepoKnown(ctx, t, repoFullName)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/auto-merge", []byte(`{"enabled":true}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("PUT auto-merge status = %d, want %d", status, http.StatusOK)
	}

	var putResp restdtos.RepoSettings
	body := []byte(`{"lightUsd":2,"deepUsd":null}`)
	status = rig.doJSON(t, http.MethodPut, "/api/repos/"+repoFullName+"/review-cost-budget", body, &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT review-cost-budget status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.AutoMergeEnabled {
		t.Errorf("PUT review-cost-budget response AutoMergeEnabled = false, want true (must be preserved, column-scoped write)")
	}
	if putResp.ReviewCostBudgetLightUsd == nil || *putResp.ReviewCostBudgetLightUsd != 2 {
		t.Errorf("PUT review-cost-budget response ReviewCostBudgetLightUsd = %v, want 2", putResp.ReviewCostBudgetLightUsd)
	}

	settings, err := rig.repoSettings.Get(ctx, repoFullName)
	if err != nil {
		t.Fatalf("get repo settings: %v", err)
	}
	if !settings.AutoMergeEnabled {
		t.Error("repo_settings.auto_merge_enabled = false after an UNRELATED review-cost-budget write, want true (preserved)")
	}
}
