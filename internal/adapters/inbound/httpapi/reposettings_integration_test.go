//go:build integration

// Integration tests for §8.2's ("server-side verdict", §8.2/§21.2) own
// admin repo-settings REST routes (reposettings.go), against a real
// Postgres instance -- gated behind the "integration" build tag, sharing
// this package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestGetRepoSettings_MemberDenied proves an ordinary member is denied
// (403) -- §13.3 row 6 is admin only, no maintainer/member carve-out at
// all, mirroring ActionToggleSentinelAutoFix's own identical placement.
func TestGetRepoSettings_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/widgets/settings", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestGetRepoSettings_MaintainerAllowed_ButPutStillAdminOnly is §21's
// own update to what was TestGetRepoSettings_MaintainerDenied: a
// maintainer is now ALLOWED to read settings (they hold
// authz.ActionConfigureAutoApprove, §13.3 row 5, and GetRepoSettings'
// own gate is authorizeAny across every action any part of this resource
// needs, reposettings.go's own doc comment) -- but PUT /settings itself
// (blockOnHighRisk/sentinelAutofixEnabled, both admin-only row 6) is
// UNCHANGED and still denies them, proving this is a READ-side widening
// only, never a write-side one.
func TestGetRepoSettings_MaintainerAllowed_ButPutStillAdminOnly(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/widgets/settings", nil, nil, token)
	if status != http.StatusOK {
		t.Errorf("GET status = %d, want %d (a maintainer holds authz.ActionConfigureAutoApprove, §13.3 row 5, sufficient to READ this endpoint)", status, http.StatusOK)
	}

	status = rig.doJSON(t, http.MethodPut, "/api/repos/acme/widgets/settings", []byte(`{"blockOnHighRisk":true}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("PUT status = %d, want %d (blockOnHighRisk/sentinelAutofixEnabled remain admin-only, row 6, unaffected by the read-side widening)", status, http.StatusForbidden)
	}
}

// TestGetRepoSettings_MemberStillDenied proves an ordinary member (who
// holds NEITHER row-5 nor row-6 actions) is still denied read access --
// the authorizeAny widening above is scoped to exactly the two roles
// §13.3 actually grants SOME action on this resource to, never a general
// "any authenticated user" opening.
func TestGetRepoSettings_MemberStillDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/widgets/settings", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (a plain member holds none of ActionConfigureBlockOnHighRisk/ActionConfigureAutoApprove/ActionToggleAutoMerge)", status, http.StatusForbidden)
	}
}

// TestGetRepoSettings_NoRowYet_DefaultsFalse proves a repo with no
// repo_settings row at all renders {blockOnHighRisk: false}, never a 404
// -- "no row yet" is not an error condition for a policy flag that always
// has a well-defined default (migrations/000044_repo_settings.up.sql).
func TestGetRepoSettings_NoRowYet_DefaultsFalse(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/never-configured")

	var got restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/never-configured/settings", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.RepoFullName != "acme/never-configured" {
		t.Errorf("RepoFullName = %q, want %q", got.RepoFullName, "acme/never-configured")
	}
	if got.BlockOnHighRisk {
		t.Errorf("BlockOnHighRisk = true, want false (no row yet)")
	}
}

// TestPutRepoSettings_AdminAllowed_RoundTrips proves an admin can set
// blockOnHighRisk, and a subsequent GET reflects it.
func TestPutRepoSettings_AdminAllowed_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/toggle-repo")

	var putResp restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/toggle-repo/settings", []byte(`{"blockOnHighRisk":true}`), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.BlockOnHighRisk {
		t.Errorf("PUT response BlockOnHighRisk = false, want true")
	}

	var getResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/toggle-repo/settings", nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if !getResp.BlockOnHighRisk {
		t.Errorf("GET response BlockOnHighRisk = false, want true (must reflect the PUT)")
	}

	// Flip it back off -- proves this is a real update, not an insert-only path.
	status = rig.doJSON(t, http.MethodPut, "/api/repos/acme/toggle-repo/settings", []byte(`{"blockOnHighRisk":false}`), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("second PUT status = %d, want %d", status, http.StatusOK)
	}
	if putResp.BlockOnHighRisk {
		t.Errorf("second PUT response BlockOnHighRisk = true, want false")
	}
}

// TestPutRepoSettings_MemberDenied_NeverWrites proves a denied member's
// call never mutates the row.
func TestPutRepoSettings_MemberDenied_NeverWrites(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/denied-write/settings", []byte(`{"blockOnHighRisk":true}`), nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/denied-write"); err == nil {
		t.Error("repo_settings row exists after a denied PUT, want no row at all")
	}
}

// TestGetRepoSettings_SentinelAutofixEnabled_NoRowYet_DefaultsOff is
// §17.1's own explicitly required test: the sentinel-auto-fix admin toggle
// (§17.1) defaults to OFF for a repo with no settings row at all --
// migrations/000048_repo_settings_sentinel_autofix.up.sql's own documented
// safe default.
func TestGetRepoSettings_SentinelAutofixEnabled_NoRowYet_DefaultsOff(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/never-configured-sentinel")

	var got restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/never-configured-sentinel/settings", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.SentinelAutofixEnabled {
		t.Errorf("SentinelAutofixEnabled = true, want false (no row yet -- must default off)")
	}
}

// TestPutRepoSettings_SentinelAutofixEnabled_RoundTrips proves an admin
// can arm the sentinel-auto-fix toggle, independently of blockOnHighRisk,
// and a subsequent GET reflects it.
func TestPutRepoSettings_SentinelAutofixEnabled_RoundTrips(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/sentinel-toggle-repo")

	var putResp restdtos.RepoSettings
	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/sentinel-toggle-repo/settings", []byte(`{"blockOnHighRisk":false,"sentinelAutofixEnabled":true}`), &putResp, token)
	if status != http.StatusOK {
		t.Fatalf("PUT status = %d, want %d", status, http.StatusOK)
	}
	if !putResp.SentinelAutofixEnabled {
		t.Errorf("PUT response SentinelAutofixEnabled = false, want true")
	}
	if putResp.BlockOnHighRisk {
		t.Errorf("PUT response BlockOnHighRisk = true, want false (independent of sentinelAutofixEnabled)")
	}

	var getResp restdtos.RepoSettings
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/sentinel-toggle-repo/settings", nil, &getResp, token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d, want %d", status, http.StatusOK)
	}
	if !getResp.SentinelAutofixEnabled {
		t.Errorf("GET response SentinelAutofixEnabled = false, want true (must reflect the PUT)")
	}
}

// TestPutRepoSettings_MaintainerDenied_SentinelAutofixNeverArmed proves a
// maintainer (who CAN edit review verdicts, row 5) cannot arm this
// stricter, admin-only row-6 toggle.
func TestPutRepoSettings_MaintainerDenied_SentinelAutofixNeverArmed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/maintainer-denied-sentinel/settings", []byte(`{"blockOnHighRisk":false,"sentinelAutofixEnabled":true}`), nil, token)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}

	if _, err := rig.repoSettings.Get(ctx, "acme/maintainer-denied-sentinel"); err == nil {
		t.Error("repo_settings row exists after a denied maintainer PUT, want no row at all")
	}
}
