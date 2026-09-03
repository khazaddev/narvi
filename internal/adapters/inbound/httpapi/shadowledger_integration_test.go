//go:build integration

// Integration tests for the shadow-operator surface's own REST routes
// (shadowledger.go), against a real Postgres instance -- gated behind the
// "integration" build tag, sharing this package's own testRig
// (httpapi_integration_test.go). Proves the RBAC wiring itself (§30.6's
// admin-only pair, authz.ActionViewShadowLedger/ActionActivateShadowLedger)
// end to end through the real router -- internal/app/shadowoperator's own
// integration suite already proves BuildSummary/Activate's own business
// logic against Postgres directly, so this file's own job is the HTTP
// layer around it: role gates, the unknown-repo 404, and the 409
// quarantine refusal reaching the wire correctly.
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// createShadowLedgerTestSession creates a session naming EXACTLY one repo
// -- the shape postgres.OutboxStore.Create's own §30.8 per-repo
// resolution requires (outbox_shadow.go's own doc comment) -- mirroring
// internal/app/shadowoperator's own createTestSessionWithRepo (this
// codebase's established per-package test-helper convention).
func createShadowLedgerTestSession(ctx context.Context, t *testing.T, r testRig, repoFullName string) sqlcgen.Session {
	t.Helper()
	reposJSON, err := json.Marshal([]map[string]any{
		{"name": repoFullName, "url": "https://github.com/" + repoFullName, "branch": nil},
	})
	if err != nil {
		t.Fatalf("marshal test repos: %v", err)
	}
	session, err := r.sessions.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
		Repos:       reposJSON,
	})
	if err != nil {
		t.Fatalf("create shadow-ledger test session: %v", err)
	}
	return session
}

// TestGetShadowLedger_MemberDenied proves an ordinary member is denied
// (403) -- authz.ActionViewShadowLedger is admin only, no maintainer/
// member carve-out at all (this ledger exposes strictly more than even
// the admin-only Members audit-log row, action.go's own doc comment).
func TestGetShadowLedger_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.
	rig.markRepoKnown(ctx, t, "acme/shadow-ledger-member-denied")

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/shadow-ledger-member-denied/shadow-ledger", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestGetShadowLedger_MaintainerDenied proves this ledger is admin-only
// even for a maintainer, unlike GetRepoSettings' own authorizeAny
// widening (reposettings_integration_test.go's own
// TestGetRepoSettings_MaintainerAllowed_ButPutStillAdminOnly) -- there is
// no read-side carve-out here at all.
func TestGetShadowLedger_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/shadow-ledger-maintainer-denied")

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/shadow-ledger-maintainer-denied/shadow-ledger", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}
}

// TestGetShadowLedger_UnknownRepo404 proves the resolveKnownRepo gate
// this route shares with every other repo-scoped route in this package
// still applies -- an admin naming a repo this deployment has never seen
// a committed github_pr_sessions row for gets 404, never a leak of "this
// repo exists but you can't see it" via a different status.
func TestGetShadowLedger_UnknownRepo404(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/shadow-ledger-never-known/shadow-ledger", nil, nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestGetShadowLedger_AdminAllowed_EmptyForUnusedRepo proves an admin can
// read the ledger, and a repo with no suppressed activity at all renders
// an EMPTY, well-formed summary -- never a 404 or a 500 -- mirroring
// GetRepoSettings' own "no row yet is not an error condition" precedent.
func TestGetShadowLedger_AdminAllowed_EmptyForUnusedRepo(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	rig.markRepoKnown(ctx, t, "acme/shadow-ledger-empty")

	var got restdtos.ShadowLedgerSummary
	status := rig.doJSON(t, http.MethodGet, "/api/repos/acme/shadow-ledger-empty/shadow-ledger", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.RepoFullName != "acme/shadow-ledger-empty" {
		t.Errorf("RepoFullName = %q, want %q", got.RepoFullName, "acme/shadow-ledger-empty")
	}
	if got.LiveEgressEnabled {
		t.Errorf("LiveEgressEnabled = true, want false (an unenrolled repo defaults to shadow)")
	}
	if got.TotalCount != 0 || len(got.Entries) != 0 {
		t.Errorf("TotalCount/Entries = %d/%v, want 0/empty", got.TotalCount, got.Entries)
	}
	if got.LlmSpendComputed {
		t.Errorf("LlmSpendComputed = true, want false (no priced turn recorded)")
	}
}

// TestPostActivateShadowLedger_MemberDenied proves the Activate route is
// gated identically to the GET (403 for a plain member).
func TestPostActivateShadowLedger_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.
	rig.markRepoKnown(ctx, t, "acme/shadow-ledger-activate-member-denied")

	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/shadow-ledger-activate-member-denied/shadow-ledger/activate", nil, nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestPostActivateShadowLedger_AdminActivatesWithNoPendingRows proves the
// happy path over the wire: an admin activates a repo with nothing
// unresolved, gets 200 with the freshly-rebuilt summary showing
// liveEgressEnabled=true, and repo_settings itself is durably flipped.
func TestPostActivateShadowLedger_AdminActivatesWithNoPendingRows(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	const repoFullName = "acme/shadow-ledger-activate-ok"
	rig.markRepoKnown(ctx, t, repoFullName)

	// Decoded into the real generated DTO, deliberately: a genuine
	// (non-null) liveEgressPromotedAt is the exact shape whose Go-side
	// decode used to fail outright (go-jsonschema's defined-pointer-type
	// output -- see tools/contractspatch's package comment), which forced
	// this test's first version to decode a raw map[string]any instead.
	// contractstest's TestShadowLedgerSummaryRoundTrip pins the fixed
	// shape in isolation; this decode pins it end to end through the real
	// handler and router.
	var got restdtos.ShadowLedgerSummary
	status := rig.doJSON(t, http.MethodPost, "/api/repos/"+repoFullName+"/shadow-ledger/activate", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if !got.LiveEgressEnabled {
		t.Errorf("response liveEgressEnabled = false, want true")
	}
	if got.LiveEgressPromotedAt == nil {
		t.Errorf("response liveEgressPromotedAt = nil, want a fresh promotion timestamp")
	}

	settings, err := rig.repoSettings.Get(ctx, repoFullName)
	if err != nil {
		t.Fatalf("read back repo settings: %v", err)
	}
	if !settings.LiveEgressEnabled {
		t.Errorf("repo_settings.LiveEgressEnabled = false after a successful Activate, want true")
	}
}

// TestPostActivateShadowLedger_RefusesWithPendingRows proves §30.8's own
// shadow-era artifact quarantine reaches the wire as a 409, and that
// repo_settings is left completely untouched.
func TestPostActivateShadowLedger_RefusesWithPendingRows(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	const repoFullName = "acme/shadow-ledger-activate-pending"
	rig.markRepoKnown(ctx, t, repoFullName)

	session := createShadowLedgerTestSession(ctx, t, rig, repoFullName)
	if _, err := rig.outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: session.ID,
		Kind:      "github_verdict",
		Payload:   []byte(`{}`),
	}); err != nil {
		t.Fatalf("create pending outbox row: %v", err)
	}

	status := rig.doJSON(t, http.MethodPost, "/api/repos/"+repoFullName+"/shadow-ledger/activate", nil, nil, token)
	if status != http.StatusConflict {
		t.Errorf("status = %d, want %d", status, http.StatusConflict)
	}

	settings, err := rig.repoSettings.Get(ctx, repoFullName)
	if err == nil && settings.LiveEgressEnabled {
		t.Errorf("repo_settings.LiveEgressEnabled = true after a refused Activate, want untouched")
	}
}
