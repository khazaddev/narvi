//go:build integration

// Integration tests for ProviderCredentialStore (Step 53, "provider
// credential injection", §25.1/§25.3) against a real Postgres instance --
// migrations/000056_provider_credentials.up.sql.
package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

func ptr(s string) *string { return &s }

// TestProviderCredentialStore_Get_MissingRow proves a nonexistent id
// returns pgx.ErrNoRows, unwrapped -- same convention every other store in
// this package already establishes.
func TestProviderCredentialStore_Get_MissingRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewProviderCredentialStore(pool)

	var id pgtype.UUID
	if err := id.Scan("11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatalf("scan id: %v", err)
	}
	_, err := store.Get(ctx, id)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Get error = %v, want pgx.ErrNoRows", err)
	}
}

// TestProviderCredentialStore_CreateGetUpdateDelete proves the full CRUD
// round trip for a repo-scoped row, including a REAL EncryptToken/
// DecryptToken round trip (the same mechanism identities.
// access_token_encrypted already uses) -- not a shortcut or a mocked
// cipher.
func TestProviderCredentialStore_CreateGetUpdateDelete(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewProviderCredentialStore(pool)

	key := []byte("01234567890123456789012345678901") // exactly 32 bytes
	plaintext := "sk-real-anthropic-key"
	encrypted, err := platform.EncryptToken(key, []byte(plaintext))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}

	created, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, ptr("acme/widgets"), sqlcgen.ProviderCredentialProviderAnthropic, encrypted)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Scope != sqlcgen.ProviderCredentialScopeRepo {
		t.Errorf("Scope = %q, want repo", created.Scope)
	}
	if created.ScopeTargetID == nil || *created.ScopeTargetID != "acme/widgets" {
		t.Errorf("ScopeTargetID = %v, want \"acme/widgets\"", created.ScopeTargetID)
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

	rotatedPlaintext := "sk-rotated-anthropic-key"
	rotatedEncrypted, err := platform.EncryptToken(key, []byte(rotatedPlaintext))
	if err != nil {
		t.Fatalf("EncryptToken (rotated): %v", err)
	}
	updated, err := store.UpdateValue(ctx, created.ID, rotatedEncrypted)
	if err != nil {
		t.Fatalf("UpdateValue: %v", err)
	}
	if updated.Scope != sqlcgen.ProviderCredentialScopeRepo || updated.ScopeTargetID == nil || *updated.ScopeTargetID != "acme/widgets" {
		t.Errorf("UpdateValue must not change scope/scopeTargetID: got scope=%q scopeTargetID=%v", updated.Scope, updated.ScopeTargetID)
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

// TestProviderCredentialStore_Create_DuplicateScopedRow_Rejected proves the
// partial unique index on (scope, scope_target_id, provider) rejects a
// second repo-scoped row for the SAME repo+provider.
func TestProviderCredentialStore_Create_DuplicateScopedRow_Rejected(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewProviderCredentialStore(pool)

	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, ptr("acme/dup"), sqlcgen.ProviderCredentialProviderOpenai, []byte("ciphertext-1")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, ptr("acme/dup"), sqlcgen.ProviderCredentialProviderOpenai, []byte("ciphertext-2")); err == nil {
		t.Fatal("second Create for the same (scope, scope_target_id, provider) succeeded, want a unique-constraint error")
	}
}

// TestProviderCredentialStore_Create_DuplicateGlobalRow_Rejected proves the
// SEPARATE partial unique index on (provider) WHERE scope_target_id IS
// NULL rejects a second global-scoped row for the same provider -- this is
// the index the scoped one (scope, scope_target_id, provider) cannot
// itself cover, since a plain UNIQUE index never treats two NULLs as
// equal.
func TestProviderCredentialStore_Create_DuplicateGlobalRow_Rejected(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewProviderCredentialStore(pool)

	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil, sqlcgen.ProviderCredentialProviderGoogle, []byte("ciphertext-1")); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil, sqlcgen.ProviderCredentialProviderGoogle, []byte("ciphertext-2")); err == nil {
		t.Fatal("second global Create for the same provider succeeded, want a unique-constraint error")
	}
}

// TestProviderCredentialStore_Create_GlobalWithScopeTargetID_Rejected
// proves the CHECK constraint (scope='global' requires scope_target_id
// NULL) is enforced at the DB layer, not merely by application code that
// happens to always pass nil.
func TestProviderCredentialStore_Create_GlobalWithScopeTargetID_Rejected(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewProviderCredentialStore(pool)

	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, ptr("should-not-be-allowed"), sqlcgen.ProviderCredentialProviderGoogle, []byte("ciphertext")); err == nil {
		t.Fatal("Create with scope=global and a non-nil scope_target_id succeeded, want a CHECK-constraint violation")
	}
}

// TestProviderCredentialStore_ListByScope_Global proves ListByScope's own
// nil-scopeTargetID/IS-NOT-DISTINCT-FROM handling actually matches
// global-scoped rows (a plain "=" comparison would never match NULL).
func TestProviderCredentialStore_ListByScope_Global(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewProviderCredentialStore(pool)

	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil, sqlcgen.ProviderCredentialProviderAnthropic, []byte("global-anthropic")); err != nil {
		t.Fatalf("Create global anthropic: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil, sqlcgen.ProviderCredentialProviderOpenai, []byte("global-openai")); err != nil {
		t.Fatalf("Create global openai: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, ptr("acme/other"), sqlcgen.ProviderCredentialProviderAnthropic, []byte("repo-anthropic")); err != nil {
		t.Fatalf("Create repo anthropic: %v", err)
	}

	got, err := store.ListByScope(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil)
	if err != nil {
		t.Fatalf("ListByScope: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (only the 2 global rows, never the repo-scoped one)", len(got))
	}
}

// TestProviderCredentialStore_ListForResolution proves the resolution
// query returns exactly the candidate rows that could apply to a given
// session shape: the global row for every provider, the repo-scoped row
// for a NAMED repo, the environment-scoped row for a NAMED environment id,
// and (Step 59, §29.4) the user-scoped row for a NAMED creator userID --
// and nothing for an UNNAMED repo/environment/user.
func TestProviderCredentialStore_ListForResolution(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewProviderCredentialStore(pool)

	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil, sqlcgen.ProviderCredentialProviderAnthropic, []byte("global-anthropic")); err != nil {
		t.Fatalf("Create global: %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, ptr("acme/in-session"), sqlcgen.ProviderCredentialProviderAnthropic, []byte("repo-anthropic")); err != nil {
		t.Fatalf("Create repo (in session): %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, ptr("acme/not-in-session"), sqlcgen.ProviderCredentialProviderAnthropic, []byte("other-repo-anthropic")); err != nil {
		t.Fatalf("Create repo (not in session): %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeEnvironment, ptr("env-in-session"), sqlcgen.ProviderCredentialProviderAnthropic, []byte("env-anthropic")); err != nil {
		t.Fatalf("Create environment (in session): %v", err)
	}
	if _, err := store.Create(ctx, sqlcgen.ProviderCredentialScopeEnvironment, ptr("env-not-in-session"), sqlcgen.ProviderCredentialProviderAnthropic, []byte("other-env-anthropic")); err != nil {
		t.Fatalf("Create environment (not in session): %v", err)
	}
	// scope_target_id is a polymorphic TEXT column with no FK to users(id)
	// (unlike chatgpt_link_attempts.user_id) -- see migrations/
	// 000056_provider_credentials.up.sql's own doc comment -- so, mirroring
	// this test's own existing repo/environment fixtures immediately above
	// (plain literal strings, no real repo_settings/environments row
	// either), a plain literal UUID-shaped string is enough here too.
	inSessionUserID := "11111111-1111-1111-1111-111111111111"
	notInSessionUserID := "22222222-2222-2222-2222-222222222222"
	if _, err := store.UpsertOAuth(ctx, inSessionUserID, sqlcgen.ProviderCredentialProviderOpenai, []byte(`{"access":"a","refresh":"r","expires_ms":1,"account_id":"acct"}`), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("UpsertOAuth (in session creator): %v", err)
	}
	if _, err := store.UpsertOAuth(ctx, notInSessionUserID, sqlcgen.ProviderCredentialProviderOpenai, []byte(`{"access":"a","refresh":"r","expires_ms":1,"account_id":"acct"}`), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("UpsertOAuth (not in session, other user): %v", err)
	}

	envID := "env-in-session"
	got, err := store.ListForResolution(ctx, []string{"acme/in-session"}, &envID, &inSessionUserID)
	if err != nil {
		t.Fatalf("ListForResolution: %v", err)
	}

	var scopes []string
	for _, row := range got {
		scopes = append(scopes, string(row.Scope)+":"+derefOrEmpty(row.ScopeTargetID))
	}
	if len(got) != 4 {
		t.Fatalf("len(got) = %d, want 4 (global + the named repo + the named environment + the named creator's own user-scope row); got scopes %v", len(got), scopes)
	}

	// No session repos/environment/creator named at all -- only the global
	// row should ever come back.
	onlyGlobal, err := store.ListForResolution(ctx, nil, nil, nil)
	if err != nil {
		t.Fatalf("ListForResolution (no repos/environment/user): %v", err)
	}
	if len(onlyGlobal) != 1 || onlyGlobal[0].Scope != sqlcgen.ProviderCredentialScopeGlobal {
		t.Fatalf("ListForResolution (no repos/environment/user) = %+v, want exactly the 1 global row", onlyGlobal)
	}
}

// TestProviderCredentialStore_ListForResolution_ExcludesNeedsRelink proves
// Step 59's own resolution-query addition (§29.5: "the row stops being
// served"): a user-scope oauth row marked oauth_needs_relink is excluded
// from the candidate set entirely, even though it is otherwise a perfect
// match for the named creator userID.
func TestProviderCredentialStore_ListForResolution_ExcludesNeedsRelink(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewProviderCredentialStore(pool)

	userID := "33333333-3333-3333-3333-333333333333"
	row, err := store.UpsertOAuth(ctx, userID, sqlcgen.ProviderCredentialProviderOpenai, []byte(`{"access":"a","refresh":"r","expires_ms":1,"account_id":"acct"}`), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("UpsertOAuth: %v", err)
	}

	// Sanity: present before marking needs-relink.
	before, err := store.ListForResolution(ctx, nil, nil, &userID)
	if err != nil {
		t.Fatalf("ListForResolution (before): %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("ListForResolution (before) = %d rows, want 1", len(before))
	}

	if _, err := store.MarkNeedsRelink(ctx, row.ID); err != nil {
		t.Fatalf("MarkNeedsRelink: %v", err)
	}

	after, err := store.ListForResolution(ctx, nil, nil, &userID)
	if err != nil {
		t.Fatalf("ListForResolution (after): %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("ListForResolution (after MarkNeedsRelink) = %d rows, want 0 (row must stop being served)", len(after))
	}
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
