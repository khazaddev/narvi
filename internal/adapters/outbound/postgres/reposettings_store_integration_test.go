//go:build integration

// Integration tests for RepoSettingsStore (§8.2, §21.2's own
// per-repo blockOnHighRisk policy flag) against a real Postgres instance.
package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
)

// TestRepoSettingsStore_Get_MissingRow proves a repo with no settings row
// yet returns pgx.ErrNoRows, unwrapped -- callers (httpapi/reviewverdict.go)
// treat this as "block_on_high_risk defaults to false", never an error
// condition of its own (migrations/000044_repo_settings.up.sql's own doc
// comment).
func TestRepoSettingsStore_Get_MissingRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewRepoSettingsStore(pool)

	_, err := store.Get(ctx, "acme/never-configured")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Get error = %v, want pgx.ErrNoRows", err)
	}
}

// TestRepoSettingsStore_Upsert_CreateThenUpdate proves the create-then-
// update round trip: a fresh Upsert creates the row with the given value,
// and a second Upsert for the SAME repo overwrites it (never a second row,
// never an additive merge) -- exactly the "always writes the full,
// current desired value" contract UpsertRepoSettings' own doc comment
// states.
func TestRepoSettingsStore_Upsert_CreateThenUpdate(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewRepoSettingsStore(pool)

	created, err := store.Upsert(ctx, "acme/toggle-repo", true, true)
	if err != nil {
		t.Fatalf("Upsert (create): %v", err)
	}
	if !created.BlockOnHighRisk {
		t.Errorf("BlockOnHighRisk = false, want true after create")
	}
	if !created.SentinelAutofixEnabled {
		t.Errorf("SentinelAutofixEnabled = false, want true after create")
	}

	got, err := store.Get(ctx, "acme/toggle-repo")
	if err != nil {
		t.Fatalf("Get after create: %v", err)
	}
	if !got.BlockOnHighRisk {
		t.Errorf("Get().BlockOnHighRisk = false, want true")
	}

	updated, err := store.Upsert(ctx, "acme/toggle-repo", false, false)
	if err != nil {
		t.Fatalf("Upsert (update): %v", err)
	}
	if updated.BlockOnHighRisk {
		t.Errorf("BlockOnHighRisk = true after update, want false")
	}
	if updated.SentinelAutofixEnabled {
		t.Errorf("SentinelAutofixEnabled = true after update, want false")
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM repo_settings WHERE repo_full_name = $1`, "acme/toggle-repo").Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want exactly 1 (upsert must never create a second row)", count)
	}
}

// TestRepoSettingsStore_ColumnScopedUpserts_NeverClobberEachOther is the
// C5 regression test (a privilege boundary,
// fixed): UpsertAutoMergeToggle and UpsertAutoApprovalEligibility are
// column-scoped -- a write through EITHER must never overwrite whatever
// the OTHER already set, no matter which one runs last. BEFORE this fix,
// both were the SAME combined UpsertAutoApprovalSettings call, writing
// all three columns together -- an admin's toggle write landing after a
// maintainer's own eligibility-config write (or vice versa) would
// silently revert the earlier write back to whatever THIS caller
// happened to pass through for the columns it didn't actually mean to
// change.
func TestRepoSettingsStore_ColumnScopedUpserts_NeverClobberEachOther(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewRepoSettingsStore(pool)
	const repoFullName = "acme/column-scoped-repo"

	maxFiles := int32(5)
	tagsJSON := []byte(`["auth","migrations"]`)
	if _, err := store.UpsertAutoApprovalEligibility(ctx, repoFullName, &maxFiles, tagsJSON); err != nil {
		t.Fatalf("UpsertAutoApprovalEligibility (initial): %v", err)
	}

	// An admin arms the auto-merge toggle -- must NOT clobber the
	// maintainer's own just-written eligibility config.
	if _, err := store.UpsertAutoMergeToggle(ctx, repoFullName, true); err != nil {
		t.Fatalf("UpsertAutoMergeToggle: %v", err)
	}
	afterToggle, err := store.Get(ctx, repoFullName)
	if err != nil {
		t.Fatalf("Get after toggle: %v", err)
	}
	if !afterToggle.AutoMergeEnabled {
		t.Error("AutoMergeEnabled = false after UpsertAutoMergeToggle(true), want true")
	}
	if afterToggle.MaxAutoApproveFilesChanged == nil || *afterToggle.MaxAutoApproveFilesChanged != 5 {
		t.Errorf("MaxAutoApproveFilesChanged after UpsertAutoMergeToggle = %v, want 5 (the toggle write must never clobber the eligibility config)", afterToggle.MaxAutoApproveFilesChanged)
	}
	// Decoded comparison, not a raw-byte one -- Postgres's own JSONB
	// storage re-serializes with its own canonical spacing (e.g. a space
	// after each comma), so a byte-for-byte match against the ORIGINAL
	// input JSON is the wrong assertion here, independent of this test's
	// own C5 concern.
	var gotTags []string
	if err := json.Unmarshal(afterToggle.SensitiveBlastRadiusTags, &gotTags); err != nil {
		t.Fatalf("unmarshal SensitiveBlastRadiusTags after UpsertAutoMergeToggle: %v", err)
	}
	if !reflect.DeepEqual(gotTags, []string{"auth", "migrations"}) {
		t.Errorf("SensitiveBlastRadiusTags after UpsertAutoMergeToggle = %v, want [auth migrations] unchanged", gotTags)
	}

	// A maintainer changes the eligibility threshold -- must NOT clobber
	// the admin's own just-armed toggle.
	newMaxFiles := int32(50)
	if _, err := store.UpsertAutoApprovalEligibility(ctx, repoFullName, &newMaxFiles, tagsJSON); err != nil {
		t.Fatalf("UpsertAutoApprovalEligibility (second write): %v", err)
	}
	afterEligibility, err := store.Get(ctx, repoFullName)
	if err != nil {
		t.Fatalf("Get after second eligibility write: %v", err)
	}
	if !afterEligibility.AutoMergeEnabled {
		t.Error("AutoMergeEnabled = false after UpsertAutoApprovalEligibility, want true -- the eligibility write must never clobber (revert) the admin's own just-armed toggle")
	}
	if afterEligibility.MaxAutoApproveFilesChanged == nil || *afterEligibility.MaxAutoApproveFilesChanged != 50 {
		t.Errorf("MaxAutoApproveFilesChanged = %v, want 50 (this write's own intended change)", afterEligibility.MaxAutoApproveFilesChanged)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM repo_settings WHERE repo_full_name = $1`, repoFullName).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want exactly 1 (both upserts must target the SAME row, never create a second)", count)
	}
}

// TestRepoSettingsStore_AutoMergeEnabled_SchemaDefaultsToFalse is the T3
// regression test: migrations/000069_repo_settings_
// auto_approval.up.sql's own `auto_merge_enabled BOOLEAN NOT NULL DEFAULT
// false` is exercised here via TWO real, production-reachable INSERT
// paths that never mention that column at all -- Upsert (block_on_high_
// risk/sentinel_autofix_enabled) and, via the column-scoped split,
// UpsertAutoApprovalEligibility -- proving the
// SCHEMA's own default, not merely the Go-layer AutoMergeEnabled
// function's identical-looking but INDEPENDENT "missing row -> false"
// fallback (config.go), which a mutation to the SQL DEFAULT clause alone
// would never touch. Verified by running the mutation described in this
// test's own doc comment (flip DEFAULT false to DEFAULT true in the
// migration): confirmed to fail without the schema default in place.
func TestRepoSettingsStore_AutoMergeEnabled_SchemaDefaultsToFalse(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewRepoSettingsStore(pool)

	t.Run("via Upsert (block_on_high_risk/sentinel_autofix_enabled path)", func(t *testing.T) {
		const repoFullName = "acme/schema-default-via-upsert"
		created, err := store.Upsert(ctx, repoFullName, true, true)
		if err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		if created.AutoMergeEnabled {
			t.Error("AutoMergeEnabled = true on a brand-new row created via a path that never mentions that column, want false (the SCHEMA's own DEFAULT)")
		}
	})

	t.Run("via UpsertAutoApprovalEligibility (the column-scoped path)", func(t *testing.T) {
		const repoFullName = "acme/schema-default-via-eligibility"
		maxFiles := int32(10)
		created, err := store.UpsertAutoApprovalEligibility(ctx, repoFullName, &maxFiles, nil)
		if err != nil {
			t.Fatalf("UpsertAutoApprovalEligibility: %v", err)
		}
		if created.AutoMergeEnabled {
			t.Error("AutoMergeEnabled = true on a brand-new row created via a path that never mentions that column, want false (the SCHEMA's own DEFAULT)")
		}
	})
}
