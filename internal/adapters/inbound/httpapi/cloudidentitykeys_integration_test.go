//go:build integration

// Integration tests for Step 73a's own ("cloud identity: OIDC issuer,
// bindings, minting", §27.3) admin-triggered signing-key rotation
// endpoint (cloudidentitykeys.go), against a real Postgres instance --
// sharing this package's own testRig (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// --- RBAC (admin only, this Step's own gap-2 resolution row) ---

func TestRotateCloudIdentitySigningKey_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity/signing-keys/rotate", []byte(`{}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (rotation is admin-only, unlike binding CRUD)", status, http.StatusForbidden)
	}
}

func TestRotateCloudIdentitySigningKey_AdminAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var got restdtos.RotateCloudIdentitySigningKeyResponse
	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity/signing-keys/rotate", []byte(`{}`), &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.ActiveKid == "" {
		t.Error("ActiveKid is empty, want a real kid")
	}
	if got.RetiredKid != nil {
		t.Errorf("RetiredKid = %v, want nil on the very first rotation", got.RetiredKid)
	}
}

// TestRotateCloudIdentitySigningKey_IssuerUnset_FailsClosed proves the
// capability-off gate.
func TestRotateCloudIdentitySigningKey_IssuerUnset_FailsClosed(t *testing.T) {
	rig := newTestRig(t, func(r *testRig) { r.cloudIdentityIssuerURL = "" })
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity/signing-keys/rotate", []byte(`{}`), nil, token)
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", status, http.StatusServiceUnavailable)
	}
}

// TestRotateCloudIdentitySigningKey_SecondRotationRetiresFirst proves the
// overlap-window rotation shape (§27.3/§5.2): a second rotation retires
// the previously active key (never leaves it active) and reports its own
// kid/retiredAt/publishableUntil.
func TestRotateCloudIdentitySigningKey_SecondRotationRetiresFirst(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var first restdtos.RotateCloudIdentitySigningKeyResponse
	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity/signing-keys/rotate", []byte(`{}`), &first, token)
	if status != http.StatusOK {
		t.Fatalf("first rotate status = %d, want %d", status, http.StatusOK)
	}

	before := time.Now()
	var second restdtos.RotateCloudIdentitySigningKeyResponse
	status = rig.doJSON(t, http.MethodPost, "/api/cloud-identity/signing-keys/rotate", []byte(`{}`), &second, token)
	after := time.Now()
	if status != http.StatusOK {
		t.Fatalf("second rotate status = %d, want %d", status, http.StatusOK)
	}

	if second.ActiveKid == first.ActiveKid {
		t.Fatalf("second ActiveKid = first ActiveKid (%q) -- rotation did not actually create a new key", first.ActiveKid)
	}
	if second.RetiredKid == nil || *second.RetiredKid != first.ActiveKid {
		t.Fatalf("second.RetiredKid = %v, want %q (the first rotation's own active kid)", second.RetiredKid, first.ActiveKid)
	}
	if second.RetiredAt == nil || second.RetiredAt.Before(before) || second.RetiredAt.After(after) {
		t.Errorf("RetiredAt = %v, want within [%v, %v]", second.RetiredAt, before, after)
	}
	if second.PublishableUntil == nil {
		t.Fatal("PublishableUntil is nil, want retiredAt + overlap window")
	}
	overlap := second.PublishableUntil.Sub(*second.RetiredAt)
	wantOverlap := platform.DefaultTimeouts().CloudIdentitySigningKeyOverlapWindow
	if overlap != wantOverlap {
		t.Errorf("PublishableUntil - RetiredAt = %v, want %v (platform.Timeouts.CloudIdentitySigningKeyOverlapWindow)", overlap, wantOverlap)
	}

	// The JWKS document must now publish BOTH keys -- old (still inside
	// its own overlap window) and new.
	var doc jwksDoc
	status = rig.doJSON(t, http.MethodGet, "/.well-known/jwks.json", nil, &doc, "")
	if status != http.StatusOK {
		t.Fatalf("jwks status = %d, want %d", status, http.StatusOK)
	}
	if len(doc.Keys) != 2 {
		t.Fatalf("len(doc.Keys) = %d, want 2 (old key still inside its overlap window + new active key)", len(doc.Keys))
	}
}

// TestRotateCloudIdentitySigningKey_RecordsAuditLog proves rotation
// itself writes audit_log -- a platform-wide security posture change.
func TestRotateCloudIdentitySigningKey_RecordsAuditLog(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	status := rig.doJSON(t, http.MethodPost, "/api/cloud-identity/signing-keys/rotate", []byte(`{}`), nil, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}

	entries, err := rig.auditLog.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list audit log: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "cloud_identity_signing_key.rotated" && e.ResourceType == "oidc_signing_key" {
			found = true
		}
	}
	if !found {
		t.Errorf("no cloud_identity_signing_key.rotated audit_log entry found among %d entries", len(entries))
	}
}
