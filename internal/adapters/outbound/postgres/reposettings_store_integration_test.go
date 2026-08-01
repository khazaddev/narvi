//go:build integration

// Integration tests for RepoSettingsStore (§8.2/Step 47, §21.2's own
// per-repo blockOnHighRisk policy flag) against a real Postgres instance.
package postgres_test

import (
	"context"
	"errors"
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
