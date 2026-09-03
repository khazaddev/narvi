//go:build integration

// Integration tests for SandboxSecretStore ("sandbox secrets &
// opencode config", §27.1) against a real Postgres instance --
// migrations/000090_sandbox_secrets.up.sql. Mirrors
// providercredential_store_integration_test.go's own test shapes,
// adapted to sandbox_secrets' name-keyed (not provider-ENUM-keyed) rows
// and its 3-scope (not 4-scope) resolution query.
package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/platform"
)

// TestSandboxSecretStore_Get_MissingRow proves a nonexistent id returns
// pgx.ErrNoRows, unwrapped -- same convention every other store in this
// package already establishes.
func TestSandboxSecretStore_Get_MissingRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSandboxSecretStore(pool)

	var id pgtype.UUID
	if err := id.Scan("11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("scan id: %v", err)
	}
	_, err := store.Get(ctx, id)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Get error = %v, want pgx.ErrNoRows", err)
	}
}

// TestSandboxSecretStore_CreateGetUpdateDelete proves the full CRUD round
// trip for a repo-scoped row, including a REAL EncryptToken/DecryptToken
// round trip -- not a shortcut or a mocked cipher.
func TestSandboxSecretStore_CreateGetUpdateDelete(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSandboxSecretStore(pool)

	key := []byte("01234567890123456789012345678901") // exactly 32 bytes
	plaintext := "very-secret-value"
	encrypted, err := platform.EncryptToken(key, []byte(plaintext))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}

	created, err := store.Create(ctx, sqlcgen.SandboxSecretScopeRepo, ptr("acme/widgets"), "MY_SECRET", encrypted)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Scope != sqlcgen.SandboxSecretScopeRepo {
		t.Errorf("Scope = %q, want repo", created.Scope)
	}
	if created.ScopeTargetID == nil || *created.ScopeTargetID != "acme/widgets" {
		t.Errorf("ScopeTargetID = %v, want \"acme/widgets\"", created.ScopeTargetID)
	}
	if created.Name != "MY_SECRET" {
		t.Errorf("Name = %q, want %q", created.Name, "MY_SECRET")
	}

	got, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	decrypted, err := platform.DecryptToken(key, got.ValueEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken: %v", err)
	}
	if string(decrypted) != plaintext {
		t.Errorf("decrypted = %q, want %q", decrypted, plaintext)
	}

	rotatedPlaintext := "rotated-secret-value"
	rotatedEncrypted, err := platform.EncryptToken(key, []byte(rotatedPlaintext))
	if err != nil {
		t.Fatalf("EncryptToken (rotated): %v", err)
	}
	updated, err := store.UpdateValue(ctx, created.ID, rotatedEncrypted)
	if err != nil {
		t.Fatalf("UpdateValue: %v", err)
	}
	if updated.Scope != sqlcgen.SandboxSecretScopeRepo || updated.ScopeTargetID == nil || *updated.ScopeTargetID != "acme/widgets" || updated.Name != "MY_SECRET" {
		t.Errorf("UpdateValue must not change scope/scopeTargetID/name: got scope=%q scopeTargetID=%v name=%q", updated.Scope, updated.ScopeTargetID, updated.Name)
	}
	redecrypted, err := platform.DecryptToken(key, updated.ValueEncrypted)
	if err != nil {
		t.Fatalf("DecryptToken (rotated): %v", err)
	}
	if string(redecrypted) != rotatedPlaintext {
		t.Errorf("redecrypted = %q, want %q", redecrypted, rotatedPlaintext)
	}

	rowsAffected, err := store.Delete(ctx, created.ID)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if rowsAffected != 1 {
		t.Errorf("Delete rowsAffected = %d, want 1", rowsAffected)
	}
	if _, err := store.Get(ctx, created.ID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("Get after delete error = %v, want pgx.ErrNoRows", err)
	}
}

// TestSandboxSecretStore_Create_DuplicateScopedRow_Rejected proves the
// partial unique index on (scope, scope_target_id, name) rejects a
// second repo-scoped row for the SAME repo+name.
func TestSandboxSecretStore_Create_DuplicateScopedRow_Rejected(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSandboxSecretStore(pool)

	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeRepo, ptr("acme/dup"), "DUP_SECRET", []byte("ciphertext-1")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeRepo, ptr("acme/dup"), "DUP_SECRET", []byte("ciphertext-2")); err == nil {
		t.Fatal("second Create for the same (scope, scope_target_id, name) succeeded, want a unique-constraint error")
	}
}

// TestSandboxSecretStore_Create_DuplicateGlobalRow_Rejected proves the
// SEPARATE partial unique index on (name) WHERE scope_target_id IS NULL
// rejects a second global-scoped row for the same name.
func TestSandboxSecretStore_Create_DuplicateGlobalRow_Rejected(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSandboxSecretStore(pool)

	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "GLOBAL_DUP", []byte("ciphertext-1")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "GLOBAL_DUP", []byte("ciphertext-2")); err == nil {
		t.Fatal("second global Create for the same name succeeded, want a unique-constraint error")
	}
}

// TestSandboxSecretStore_Create_DifferentScopesSameName_BothAllowed proves
// the SAME name is allowed at multiple DIFFERENT scopes simultaneously
// (e.g. a repo-scoped AND a global-scoped row both named "MY_VAR") -- the
// unique indexes are scoped to (scope, scope_target_id, name), never to
// name alone.
func TestSandboxSecretStore_Create_DifferentScopesSameName_BothAllowed(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSandboxSecretStore(pool)

	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "SHARED_NAME", []byte("global-value")); err != nil {
		t.Fatalf("Create global: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeRepo, ptr("acme/repo"), "SHARED_NAME", []byte("repo-value")); err != nil {
		t.Fatalf("Create repo with the SAME name at a different scope: %v", err)
	}
}

// TestSandboxSecretStore_Create_GlobalWithScopeTargetID_Rejected proves
// the CHECK constraint (scope='global' requires scope_target_id NULL) is
// enforced at the DB layer, not merely by application code that happens
// to always pass nil.
func TestSandboxSecretStore_Create_GlobalWithScopeTargetID_Rejected(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSandboxSecretStore(pool)

	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, ptr("should-not-be-allowed"), "SOME_NAME", []byte("ciphertext")); err == nil {
		t.Fatal("Create with scope=global and a non-nil scope_target_id succeeded, want a CHECK-constraint violation")
	}
}

// TestSandboxSecretStore_Create_RepoWithoutScopeTargetID_Rejected proves
// the SAME CHECK constraint's other direction: scope<>'global' requires a
// non-empty scope_target_id.
func TestSandboxSecretStore_Create_RepoWithoutScopeTargetID_Rejected(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSandboxSecretStore(pool)

	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeRepo, nil, "SOME_NAME", []byte("ciphertext")); err == nil {
		t.Fatal("Create with scope=repo and a nil scope_target_id succeeded, want a CHECK-constraint violation")
	}
}

// TestSandboxSecretStore_ListByScope_Global proves ListByScope's own
// nil-scopeTargetID/IS-NOT-DISTINCT-FROM handling actually matches
// global-scoped rows.
func TestSandboxSecretStore_ListByScope_Global(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSandboxSecretStore(pool)

	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "GLOBAL_ONE", []byte("v1")); err != nil {
		t.Fatalf("Create global one: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "GLOBAL_TWO", []byte("v2")); err != nil {
		t.Fatalf("Create global two: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeRepo, ptr("acme/other"), "GLOBAL_ONE", []byte("v3")); err != nil {
		t.Fatalf("Create repo (different scope, same name): %v", err)
	}

	got, err := store.ListByScope(ctx, sqlcgen.SandboxSecretScopeGlobal, nil)
	if err != nil {
		t.Fatalf("ListByScope: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (only the 2 global rows, never the repo-scoped one)", len(got))
	}
}

// TestSandboxSecretStore_ListForResolution proves the resolution query
// returns exactly the candidate rows that could apply to a given session
// shape: the global row for every name, the repo-scoped row for a NAMED
// repo, the environment-scoped row for a NAMED environment id -- and
// NOTHING for an UNNAMED repo/environment, and nothing at all for an
// automation-scoped row (§27.1's own schema-only carve-out -- there is no
// automationID parameter for this query to even accept).
func TestSandboxSecretStore_ListForResolution(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSandboxSecretStore(pool)

	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "SHARED", []byte("global-value")); err != nil {
		t.Fatalf("Create global: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeRepo, ptr("acme/in-session"), "SHARED", []byte("repo-value")); err != nil {
		t.Fatalf("Create repo (in session): %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeRepo, ptr("acme/not-in-session"), "SHARED", []byte("other-repo-value")); err != nil {
		t.Fatalf("Create repo (not in session): %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeEnvironment, ptr("env-in-session"), "SHARED", []byte("env-value")); err != nil {
		t.Fatalf("Create environment (in session): %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.SandboxSecretScopeEnvironment, ptr("env-not-in-session"), "SHARED", []byte("other-env-value")); err != nil {
		t.Fatalf("Create environment (not in session): %v", err)
	}

	envID := "env-in-session"
	got, err := store.ListForResolution(ctx, []string{"acme/in-session"}, &envID)
	if err != nil {
		t.Fatalf("ListForResolution: %v", err)
	}
	var scopes []string
	for _, row := range got {
		scopes = append(scopes, string(row.Scope)+":"+derefOrEmpty(row.ScopeTargetID))
	}
	if len(got) != 3 {
		t.Fatalf("len(got) = %d, want 3 (global + the named repo + the named environment); got scopes %v", len(got), scopes)
	}

	// No session repos/environment named at all -- only the global row
	// should ever come back.
	onlyGlobal, err := store.ListForResolution(ctx, nil, nil)
	if err != nil {
		t.Fatalf("ListForResolution (no repos/environment): %v", err)
	}
	if len(onlyGlobal) != 1 || onlyGlobal[0].Scope != sqlcgen.SandboxSecretScopeGlobal {
		t.Fatalf("ListForResolution (no repos/environment) = %+v, want exactly the 1 global row", onlyGlobal)
	}
}
