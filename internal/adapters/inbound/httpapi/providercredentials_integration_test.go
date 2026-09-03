//go:build integration

// Integration tests for §25.1's own ("provider credential injection",
// §25.1/§25.3) CP-side management CRUD surface (providercredentials.go),
// against a real Postgres instance -- sharing this package's own testRig
// (httpapi_integration_test.go).
package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/platform"
)

// mustParseUUID parses raw as a pgtype.UUID, failing the test on error --
// a small, dependency-free test helper for converting a REST DTO's own
// string id back into the store layer's own UUID type.
func mustParseUUID(t *testing.T, raw string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		t.Fatalf("parse uuid %q: %v", raw, err)
	}
	return id
}

// --- RBAC ---

// TestCreateRepoProviderCredential_MemberDenied proves an ordinary member
// (row 4: admin/maintainer only) is denied.
func TestCreateRepoProviderCredential_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t) // default role: member.

	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/widgets/provider-credentials",
		[]byte(`{"provider":"anthropic","value":"sk-should-never-be-stored"}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestCreateRepoProviderCredential_MaintainerAllowed proves a maintainer
// (row 4's own admin+maintainer split) succeeds.
func TestCreateRepoProviderCredential_MaintainerAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/widgets")

	var got restdtos.ProviderCredential
	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/widgets/provider-credentials",
		[]byte(`{"provider":"anthropic","value":"sk-real-value"}`), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.Scope != restdtos.ProviderCredentialScopeRepo {
		t.Errorf("Scope = %q, want %q", got.Scope, restdtos.ProviderCredentialScopeRepo)
	}
	if got.ScopeTarget == nil || *got.ScopeTarget != "acme/widgets" {
		t.Errorf("ScopeTarget = %v, want %q", got.ScopeTarget, "acme/widgets")
	}
}

// TestCreateEnvironmentProviderCredential_MemberDenied mirrors the repo
// case for the environment-scoped route group.
func TestCreateEnvironmentProviderCredential_MemberDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t)

	status := rig.doJSON(t, http.MethodPost, "/api/environments/11111111-1111-1111-1111-111111111111/provider-credentials",
		[]byte(`{"provider":"openai","value":"sk-should-never-be-stored"}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestCreateGlobalProviderCredential_MaintainerDenied proves the global
// route group is admin-ONLY (row 6), unlike repo/environment's own
// admin+maintainer (row 4) -- mirrors reposettings.go's own identical
// "stricter row" precedent.
func TestCreateGlobalProviderCredential_MaintainerDenied(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/provider-credentials",
		[]byte(`{"provider":"google","value":"should-never-be-stored"}`), nil, token)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (admin only, no maintainer carve-out)", status, http.StatusForbidden)
	}
}

// TestCreateGlobalProviderCredential_AdminAllowed is the positive
// counterpart.
func TestCreateGlobalProviderCredential_AdminAllowed(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	var got restdtos.ProviderCredential
	status := rig.doJSON(t, http.MethodPost, "/api/provider-credentials",
		[]byte(`{"provider":"google","value":"real-google-key"}`), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.Scope != restdtos.ProviderCredentialScopeGlobal {
		t.Errorf("Scope = %q, want %q", got.Scope, restdtos.ProviderCredentialScopeGlobal)
	}
	if got.ScopeTarget != nil {
		t.Errorf("ScopeTarget = %v, want nil (global)", got.ScopeTarget)
	}
}

// --- Write-only value / masking ---

// TestCreateProviderCredential_ValueNeverReturned proves the raw response
// body of a successful create never contains the plaintext value, only
// the fixed masked placeholder.
func TestCreateProviderCredential_ValueNeverReturned(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	const secret = "sk-super-secret-value-must-never-leak"
	var got restdtos.ProviderCredential
	status := rig.doJSON(t, http.MethodPost, "/api/provider-credentials",
		[]byte(fmt.Sprintf(`{"provider":"openai","value":%q}`, secret)), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}
	if got.MaskedValue == secret {
		t.Fatal("MaskedValue equals the real plaintext secret -- must never happen")
	}
	if got.MaskedValue == "" {
		t.Error("MaskedValue is empty, want a non-empty masked placeholder")
	}
}

// TestProviderCredentialStore_ValueEncryptedAtRest proves the value stored
// in Postgres is real ciphertext, decryptable via the SAME
// tokenEncryptionKey the rig's handler uses -- not plaintext, and not some
// other encoding.
func TestProviderCredentialStore_ValueEncryptedAtRest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)

	const secret = "sk-at-rest-round-trip"
	var got restdtos.ProviderCredential
	status := rig.doJSON(t, http.MethodPost, "/api/provider-credentials",
		[]byte(fmt.Sprintf(`{"provider":"anthropic","value":%q}`, secret)), &got, token)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want %d", status, http.StatusCreated)
	}

	rows, err := rig.providerCredentials.ListByScope(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil)
	if err != nil {
		t.Fatalf("ListByScope: %v", err)
	}
	var stored *sqlcgen.ProviderCredential
	for i := range rows {
		if rows[i].ID.String() == got.Id {
			stored = &rows[i]
		}
	}
	if stored == nil {
		t.Fatalf("no stored row found for id %s", got.Id)
	}
	if string(stored.ValueEncrypted) == secret {
		t.Fatal("value_encrypted equals the plaintext secret -- stored unencrypted")
	}
	decrypted, err := platform.DecryptToken(rig.tokenEncryptionKey, stored.ValueEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken: %v", err)
	}
	if string(decrypted) != secret {
		t.Errorf("decrypted = %q, want %q", decrypted, secret)
	}
}

// --- List / Update / Delete round trip ---

// TestProviderCredentials_RepoScoped_FullRoundTrip proves create -> list
// -> update (rotate) -> delete -> list-empty, all via the real REST
// routes, for the repo-scoped route group.
func TestProviderCredentials_RepoScoped_FullRoundTrip(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/roundtrip")

	var created restdtos.ProviderCredential
	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/roundtrip/provider-credentials",
		[]byte(`{"provider":"openai","value":"sk-initial"}`), &created, token)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}

	var listed restdtos.ListProviderCredentialsResponse
	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/roundtrip/provider-credentials", nil, &listed, token)
	if status != http.StatusOK {
		t.Fatalf("list status = %d, want %d", status, http.StatusOK)
	}
	if len(listed.ProviderCredentials) != 1 {
		t.Fatalf("len(listed) = %d, want 1", len(listed.ProviderCredentials))
	}

	var updated restdtos.ProviderCredential
	status = rig.doJSON(t, http.MethodPut, "/api/repos/acme/roundtrip/provider-credentials/"+created.Id,
		[]byte(`{"value":"sk-rotated"}`), &updated, token)
	if status != http.StatusOK {
		t.Fatalf("update status = %d, want %d", status, http.StatusOK)
	}
	if updated.Scope != restdtos.ProviderCredentialScopeRepo || updated.ScopeTarget == nil || *updated.ScopeTarget != "acme/roundtrip" {
		t.Errorf("update must not change scope/scopeTarget: got scope=%q scopeTarget=%v", updated.Scope, updated.ScopeTarget)
	}

	stored, err := rig.providerCredentials.Get(ctx, mustParseUUID(t, created.Id))
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	decrypted, err := platform.DecryptToken(rig.tokenEncryptionKey, stored.ValueEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken after update: %v", err)
	}
	if string(decrypted) != "sk-rotated" {
		t.Errorf("decrypted after update = %q, want %q", decrypted, "sk-rotated")
	}

	status = rig.doJSON(t, http.MethodDelete, "/api/repos/acme/roundtrip/provider-credentials/"+created.Id, nil, nil, token)
	if status != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d", status, http.StatusNoContent)
	}

	status = rig.doJSON(t, http.MethodGet, "/api/repos/acme/roundtrip/provider-credentials", nil, &listed, token)
	if status != http.StatusOK {
		t.Fatalf("list after delete status = %d, want %d", status, http.StatusOK)
	}
	if len(listed.ProviderCredentials) != 0 {
		t.Errorf("len(listed after delete) = %d, want 0", len(listed.ProviderCredentials))
	}
}

// TestCreateProviderCredential_Duplicate_Conflict proves a second create
// for the SAME (scope, scopeTarget, provider) is rejected 409, matching
// the table's own partial unique index.
func TestCreateProviderCredential_Duplicate_Conflict(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/dup")

	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/dup/provider-credentials",
		[]byte(`{"provider":"anthropic","value":"sk-first"}`), nil, token)
	if status != http.StatusCreated {
		t.Fatalf("first create status = %d, want %d", status, http.StatusCreated)
	}

	status = rig.doJSON(t, http.MethodPost, "/api/repos/acme/dup/provider-credentials",
		[]byte(`{"provider":"anthropic","value":"sk-second"}`), nil, token)
	if status != http.StatusConflict {
		t.Errorf("second create status = %d, want %d", status, http.StatusConflict)
	}
}

// TestUpdateProviderCredential_CrossScope_NotFound proves a maintainer
// cannot rotate a GLOBAL credential merely by hitting the repo-scoped PUT
// route with its id -- the row's own persisted scope must match the URL's
// implied scope, or it is treated as not found at all (never a
// distinguishing error), closing the IDOR-shaped cross-scope confusion
// risk providercredentials.go's own top doc comment names.
func TestUpdateProviderCredential_CrossScope_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	_, maintainerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var globalCred restdtos.ProviderCredential
	status := rig.doJSON(t, http.MethodPost, "/api/provider-credentials",
		[]byte(`{"provider":"google","value":"real-google-key"}`), &globalCred, adminToken)
	if status != http.StatusCreated {
		t.Fatalf("create global status = %d, want %d", status, http.StatusCreated)
	}

	// A maintainer (who holds ActionManageRepoSecrets but NOT
	// ActionManageGlobalSecrets) hits the repo-scoped PUT with the
	// GLOBAL credential's own real id.
	status = rig.doJSON(t, http.MethodPut, "/api/repos/acme/cross-scope/provider-credentials/"+globalCred.Id,
		[]byte(`{"value":"should-never-apply"}`), nil, maintainerToken)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d (cross-scope id must read as not-found, never succeed)", status, http.StatusNotFound)
	}

	// Proves the global row was genuinely untouched.
	stored, err := rig.providerCredentials.Get(ctx, mustParseUUID(t, globalCred.Id))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	decrypted, err := platform.DecryptToken(rig.tokenEncryptionKey, stored.ValueEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken: %v", err)
	}
	if string(decrypted) != "real-google-key" {
		t.Errorf("global credential value changed via cross-scope PUT: decrypted = %q", decrypted)
	}
}

// TestDeleteProviderCredential_CrossScope_NotFound is
// TestUpdateProviderCredential_CrossScope_NotFound's DELETE counterpart.
func TestDeleteProviderCredential_CrossScope_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, adminToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleAdmin)
	_, maintainerToken := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	var globalCred restdtos.ProviderCredential
	status := rig.doJSON(t, http.MethodPost, "/api/provider-credentials",
		[]byte(`{"provider":"openai","value":"real-openai-key"}`), &globalCred, adminToken)
	if status != http.StatusCreated {
		t.Fatalf("create global status = %d, want %d", status, http.StatusCreated)
	}

	status = rig.doJSON(t, http.MethodDelete, "/api/repos/acme/cross-scope-delete/provider-credentials/"+globalCred.Id, nil, nil, maintainerToken)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d (cross-scope id must read as not-found, never succeed)", status, http.StatusNotFound)
	}

	if _, err := rig.providerCredentials.Get(ctx, mustParseUUID(t, globalCred.Id)); err != nil {
		t.Errorf("global credential was deleted via cross-scope DELETE: Get error = %v", err)
	}
}

// TestUpdateProviderCredential_Nonexistent_NotFound proves an id that
// simply does not exist at all -> 404 (the ordinary, non-cross-scope
// case).
func TestUpdateProviderCredential_Nonexistent_NotFound(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPut, "/api/repos/acme/nonexistent/provider-credentials/11111111-1111-1111-1111-111111111111",
		[]byte(`{"value":"anything"}`), nil, token)
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestCreateProviderCredential_MalformedEnvironmentID_BadRequest proves a
// malformed environmentID path segment is rejected 400, not silently
// stored as a bogus scope_target_id.
func TestCreateProviderCredential_MalformedEnvironmentID_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)

	status := rig.doJSON(t, http.MethodPost, "/api/environments/not-a-uuid/provider-credentials",
		[]byte(`{"provider":"openai","value":"sk-x"}`), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// TestCreateProviderCredential_InvalidProvider_BadRequest proves a
// provider outside the closed 3-value enum is rejected 400.
func TestCreateProviderCredential_InvalidProvider_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/invalid-provider")

	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/invalid-provider/provider-credentials",
		[]byte(`{"provider":"mistral","value":"sk-x"}`), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}
}

// --- NUL-byte credential value rejection ---
//
// A value containing an embedded NUL byte (\u0000 -- a plausible
// accident: binary-ish copy-paste artifact, UTF-16 residue, a key read
// from a file with a stray byte) must be rejected 400 at BOTH write
// sites, before platform.EncryptToken ever runs -- see containsNULByte's
// own doc comment (providercredentials.go) for why: an accepted
// NUL-bearing value would later break os/exec at spawn time
// (cmd/sandbox-agent/main.go's own fetchProviderCredentialSpawnEnv builds
// cmd.Env entries as name+"="+value, and os/exec rejects any env entry
// containing a NUL byte before fork), killing sandbox boot on every
// respawn until the row is rotated with a clean value. Request bodies
// below are built as raw, valid JSON literals (never Go's own %q, which
// emits a \x00 escape -- not valid JSON) so the decoded req.Value
// genuinely contains a NUL byte, the same way a real client's request
// would.
func TestCreateProviderCredential_NULByteValue_BadRequest(t *testing.T) {
	tests := []struct {
		name      string
		jsonValue string // a valid JSON string literal that decodes to a Go string containing an embedded NUL byte
	}{
		{"NUL in the middle", `"sk-abc\u0000def"`},
		{"NUL at the start", `"\u0000sk-leading-nul"`},
		{"NUL at the end", `"sk-trailing-nul\u0000"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t)
			ctx := context.Background()
			_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
			rig.markRepoKnown(ctx, t, "acme/nul-byte-create")

			body := []byte(fmt.Sprintf(`{"provider":"anthropic","value":%s}`, tc.jsonValue))
			status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/nul-byte-create/provider-credentials", body, nil, token)
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
			}
		})
	}
}

// TestUpdateProviderCredentialValue_NULByteValue_BadRequest is
// TestCreateProviderCredential_NULByteValue_BadRequest's PUT (rotate)
// counterpart -- also proves the credential's stored value is left
// genuinely untouched by a rejected rotation attempt.
func TestUpdateProviderCredentialValue_NULByteValue_BadRequest(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	_, token := createUserWithRole(ctx, t, rig, sqlcgen.UserRoleMaintainer)
	rig.markRepoKnown(ctx, t, "acme/nul-byte-update")

	var created restdtos.ProviderCredential
	status := rig.doJSON(t, http.MethodPost, "/api/repos/acme/nul-byte-update/provider-credentials",
		[]byte(`{"provider":"openai","value":"sk-clean-initial"}`), &created, token)
	if status != http.StatusCreated {
		t.Fatalf("create status = %d, want %d", status, http.StatusCreated)
	}

	status = rig.doJSON(t, http.MethodPut, "/api/repos/acme/nul-byte-update/provider-credentials/"+created.Id,
		[]byte(`{"value":"sk-tainted\u0000value"}`), nil, token)
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", status, http.StatusBadRequest)
	}

	stored, err := rig.providerCredentials.Get(ctx, mustParseUUID(t, created.Id))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	decrypted, err := platform.DecryptToken(rig.tokenEncryptionKey, stored.ValueEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken: %v", err)
	}
	if string(decrypted) != "sk-clean-initial" {
		t.Errorf("decrypted = %q, want %q (rejected NUL-byte update must not change the stored value)", decrypted, "sk-clean-initial")
	}
}
