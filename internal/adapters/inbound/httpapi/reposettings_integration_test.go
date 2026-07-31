//go:build integration

// Integration tests for Step 47's ("server-side verdict", §8.2/§21.2) own
// admin repo-settings REST routes (reposettings.go), against a real
// Postgres instance -- gated behind the "integration" build tag, sharing
// this package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
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

// TestGetRepoSettings_MaintainerDenied proves even a MAINTAINER is denied
// -- this row is stricter than row 5 (which maintainers DO get), matching
// the sentinel-auto-fix-toggle precedent's own admin-only reasoning.
func TestGetRepoSettings_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/widgets/settings", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
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
