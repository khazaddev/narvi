//go:build integration

// Integration tests for OpenCodeConfigStore ("sandbox secrets &
// opencode config", §27.2) against a real Postgres instance --
// migrations/000091_opencode_configs.up.sql.
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

// jsonEqual compares two JSON documents SEMANTICALLY, not byte-for-byte
// -- Postgres's own JSONB storage re-serializes with its own canonical
// whitespace (e.g. a space after every ":"/","), so a round-tripped
// document is never byte-identical to what was inserted even though it
// carries the exact same data. Every test below that compares a stored
// Document uses this helper rather than a raw string/[]byte comparison
// for that reason.
func jsonEqual(t *testing.T, got, want []byte) bool {
	t.Helper()
	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("jsonEqual: unmarshal got %s: %v", got, err)
	}
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("jsonEqual: unmarshal want %s: %v", want, err)
	}
	return reflect.DeepEqual(gotVal, wantVal)
}

// TestOpenCodeConfigStore_GetEnvironment_MissingRow proves an
// unconfigured Environment returns pgx.ErrNoRows, unwrapped -- an
// ordinary, expected state (§27.2's own "at most one... per
// environment"), never an error condition, per this store's own doc
// comment.
func TestOpenCodeConfigStore_GetEnvironment_MissingRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewOpenCodeConfigStore(pool)

	_, err := store.GetEnvironment(ctx, "11111111-1111-1111-1111-111111111111")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetEnvironment error = %v, want pgx.ErrNoRows", err)
	}
}

// TestOpenCodeConfigStore_GetGlobal_MissingRow mirrors
// TestOpenCodeConfigStore_GetEnvironment_MissingRow for the global scope.
func TestOpenCodeConfigStore_GetGlobal_MissingRow(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewOpenCodeConfigStore(pool)

	_, err := store.GetGlobal(ctx)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetGlobal error = %v, want pgx.ErrNoRows", err)
	}
}

// TestOpenCodeConfigStore_Environment_UpsertGetDelete proves the full
// create-or-replace round trip for an environment-scoped row: create,
// re-upsert (replace), get, delete.
func TestOpenCodeConfigStore_Environment_UpsertGetDelete(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewOpenCodeConfigStore(pool)

	environmentID := "22222222-2222-2222-2222-222222222222"
	doc1 := []byte(`{"model":"anthropic/claude"}`)

	created, err := store.UpsertEnvironment(ctx, environmentID, doc1)
	if err != nil {
		t.Fatalf("UpsertEnvironment (create): %v", err)
	}
	if !jsonEqual(t, created.Document, doc1) {
		t.Errorf("Document = %s, want %s", created.Document, doc1)
	}

	got, err := store.GetEnvironment(ctx, environmentID)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if !jsonEqual(t, got.Document, doc1) {
		t.Errorf("GetEnvironment().Document = %s, want %s", got.Document, doc1)
	}

	// Re-upsert (replace) -- proves this is a genuine upsert, not a
	// second, conflicting row: a SECOND row at the SAME environmentID
	// would violate opencode_configs_scoped_uniq if Upsert ever degraded
	// into a plain INSERT.
	doc2 := []byte(`{"model":"openai/gpt"}`)
	replaced, err := store.UpsertEnvironment(ctx, environmentID, doc2)
	if err != nil {
		t.Fatalf("UpsertEnvironment (replace): %v", err)
	}
	if !jsonEqual(t, replaced.Document, doc2) {
		t.Errorf("replaced Document = %s, want %s", replaced.Document, doc2)
	}
	if replaced.ID != created.ID {
		t.Errorf("replaced.ID = %v, want the SAME id as the original row (%v) -- an upsert must replace in place, never create a second row", replaced.ID, created.ID)
	}

	affected, err := store.DeleteEnvironment(ctx, environmentID)
	if err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if affected != 1 {
		t.Errorf("DeleteEnvironment affected = %d, want 1", affected)
	}
	if _, err := store.GetEnvironment(ctx, environmentID); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetEnvironment after delete error = %v, want pgx.ErrNoRows", err)
	}
}

// TestOpenCodeConfigStore_Global_UpsertGetDelete mirrors
// TestOpenCodeConfigStore_Environment_UpsertGetDelete for the global
// scope -- including the SAME "at most one global row" property, this
// time enforced by opencode_configs_global_uniq.
func TestOpenCodeConfigStore_Global_UpsertGetDelete(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewOpenCodeConfigStore(pool)

	doc1 := []byte(`{"autoupdate":true}`)
	created, err := store.UpsertGlobal(ctx, doc1)
	if err != nil {
		t.Fatalf("UpsertGlobal (create): %v", err)
	}

	doc2 := []byte(`{"autoupdate":false}`)
	replaced, err := store.UpsertGlobal(ctx, doc2)
	if err != nil {
		t.Fatalf("UpsertGlobal (replace): %v", err)
	}
	if replaced.ID != created.ID {
		t.Errorf("replaced.ID = %v, want the SAME id as the original row (%v)", replaced.ID, created.ID)
	}

	got, err := store.GetGlobal(ctx)
	if err != nil {
		t.Fatalf("GetGlobal: %v", err)
	}
	if !jsonEqual(t, got.Document, doc2) {
		t.Errorf("GetGlobal().Document = %s, want %s", got.Document, doc2)
	}

	affected, err := store.DeleteGlobal(ctx)
	if err != nil {
		t.Fatalf("DeleteGlobal: %v", err)
	}
	if affected != 1 {
		t.Errorf("DeleteGlobal affected = %d, want 1", affected)
	}
}

// TestOpenCodeConfigStore_Environment_IndependentAcrossEnvironments
// proves two DIFFERENT environments can each have their own row
// simultaneously -- the unique index is scoped per environment, never a
// singleton across the whole table.
func TestOpenCodeConfigStore_Environment_IndependentAcrossEnvironments(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewOpenCodeConfigStore(pool)

	envA := "33333333-3333-3333-3333-333333333333"
	envB := "44444444-4444-4444-4444-444444444444"

	if _, err := store.UpsertEnvironment(ctx, envA, []byte(`{"a":true}`)); err != nil {
		t.Fatalf("UpsertEnvironment A: %v", err)
	}
	if _, err := store.UpsertEnvironment(ctx, envB, []byte(`{"b":true}`)); err != nil {
		t.Fatalf("UpsertEnvironment B: %v", err)
	}

	gotA, err := store.GetEnvironment(ctx, envA)
	if err != nil {
		t.Fatalf("GetEnvironment A: %v", err)
	}
	gotB, err := store.GetEnvironment(ctx, envB)
	if err != nil {
		t.Fatalf("GetEnvironment B: %v", err)
	}
	if !jsonEqual(t, gotA.Document, []byte(`{"a":true}`)) || !jsonEqual(t, gotB.Document, []byte(`{"b":true}`)) {
		t.Errorf("gotA.Document=%s gotB.Document=%s, want independent documents", gotA.Document, gotB.Document)
	}
}

// TestOpenCodeConfigStore_ListForDelivery_BothScopesAtOnce proves the
// delivery read returns BOTH the global row and this session's own
// environment row together, in one call -- §27.2's own "delivered at
// boot... both scopes at once", never a Resolve-style single winner.
func TestOpenCodeConfigStore_ListForDelivery_BothScopesAtOnce(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewOpenCodeConfigStore(pool)

	environmentID := "55555555-5555-5555-5555-555555555555"
	otherEnvironmentID := "66666666-6666-6666-6666-666666666666"

	if _, err := store.UpsertGlobal(ctx, []byte(`{"global":true}`)); err != nil {
		t.Fatalf("UpsertGlobal: %v", err)
	}
	if _, err := store.UpsertEnvironment(ctx, environmentID, []byte(`{"env":true}`)); err != nil {
		t.Fatalf("UpsertEnvironment: %v", err)
	}
	if _, err := store.UpsertEnvironment(ctx, otherEnvironmentID, []byte(`{"other":true}`)); err != nil {
		t.Fatalf("UpsertEnvironment (other): %v", err)
	}

	got, err := store.ListForDelivery(ctx, &environmentID)
	if err != nil {
		t.Fatalf("ListForDelivery: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2 (global + the named environment, never the OTHER environment)", len(got))
	}

	// No environment named at all -- only the global row comes back.
	globalOnly, err := store.ListForDelivery(ctx, nil)
	if err != nil {
		t.Fatalf("ListForDelivery (nil environmentID): %v", err)
	}
	if len(globalOnly) != 1 {
		t.Fatalf("len(globalOnly) = %d, want 1 (only the global row)", len(globalOnly))
	}
}
