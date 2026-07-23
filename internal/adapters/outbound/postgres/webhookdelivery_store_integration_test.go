//go:build integration

// Integration test for WebhookDeliveryStore.Claim (§5.1's "Dedupe/
// coalescing (webhook events, concurrent PR @mentions) via
// INSERT ... ON CONFLICT atomic claims" -- Step 31, "webhook toolkit").
// Kept in its own file, mirroring
// sandbox_upsertforspawn_integration_test.go's own precedent of a
// focused file per query rather than growing
// postgres_integration_test.go's single pipeline test.
package postgres_test

import (
	"context"
	"sync"
	"testing"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
)

// TestWebhookDeliveryStore_Claim_FirstClaimInserted proves the first
// claim for a given (provider, delivery_id) is reported as freshly
// inserted, with received_at populated.
func TestWebhookDeliveryStore_Claim_FirstClaimInserted(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewWebhookDeliveryStore(pool)

	row, err := store.Claim(ctx, "github", "delivery-abc-123")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !row.Inserted {
		t.Error("Inserted = false, want true (first claim for this identity)")
	}
	if row.Provider != "github" {
		t.Errorf("Provider = %q, want %q", row.Provider, "github")
	}
	if row.DeliveryID != "delivery-abc-123" {
		t.Errorf("DeliveryID = %q, want %q", row.DeliveryID, "delivery-abc-123")
	}
	if !row.ReceivedAt.Valid {
		t.Error("ReceivedAt is not valid, want a populated timestamp")
	}
}

// TestWebhookDeliveryStore_Claim_SecondClaimIsDuplicate proves a second
// claim for the SAME (provider, delivery_id) -- a real webhook
// redelivery -- is detected as a duplicate (Inserted == false), not
// double-inserted, and that the row's own received_at stays pinned to
// the FIRST claim's value (the self-referential no-op update in
// ClaimWebhookDelivery's own generated doc comment: "received_at is set
// back to its own current value").
func TestWebhookDeliveryStore_Claim_SecondClaimIsDuplicate(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewWebhookDeliveryStore(pool)

	first, err := store.Claim(ctx, "slack", "Ev0123ABC")
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if !first.Inserted {
		t.Fatal("first Claim: Inserted = false, want true")
	}

	second, err := store.Claim(ctx, "slack", "Ev0123ABC")
	if err != nil {
		t.Fatalf("second Claim (redelivery): %v", err)
	}
	if second.Inserted {
		t.Error("second Claim: Inserted = true, want false (a redelivery of the same identity must be detected as a duplicate)")
	}
	if !second.ReceivedAt.Time.Equal(first.ReceivedAt.Time) {
		t.Errorf("second Claim ReceivedAt = %v, want unchanged from the first claim's %v", second.ReceivedAt.Time, first.ReceivedAt.Time)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = $1 AND delivery_id = $2`,
		"slack", "Ev0123ABC",
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (never double-inserted)", count)
	}
}

// TestWebhookDeliveryStore_Claim_DifferentProviderSameDeliveryID proves
// the dedupe identity is genuinely (provider, delivery_id) TOGETHER --
// the same delivery_id string from a DIFFERENT provider is its own,
// independent claim, not a collision.
func TestWebhookDeliveryStore_Claim_DifferentProviderSameDeliveryID(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewWebhookDeliveryStore(pool)

	ghRow, err := store.Claim(ctx, "github", "shared-id-1")
	if err != nil {
		t.Fatalf("github Claim: %v", err)
	}
	if !ghRow.Inserted {
		t.Error("github Claim: Inserted = false, want true")
	}

	linearRow, err := store.Claim(ctx, "linear", "shared-id-1")
	if err != nil {
		t.Fatalf("linear Claim: %v", err)
	}
	if !linearRow.Inserted {
		t.Error("linear Claim: Inserted = false, want true (different provider, independent identity)")
	}
}

// TestWebhookDeliveryStore_Claim_ConcurrentSameIdentity_ExactlyOneWinner
// proves the claim is genuinely concurrent-safe -- INSERT ON CONFLICT,
// not a read-then-write race: N goroutines racing to claim the exact
// same (provider, delivery_id) must yield exactly one Inserted == true
// and N-1 Inserted == false, never two winners and never a lost/duplicate
// row.
func TestWebhookDeliveryStore_Claim_ConcurrentSameIdentity_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewWebhookDeliveryStore(pool)

	const n = 20
	var wg sync.WaitGroup
	results := make([]bool, n)
	errs := make([]error, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			row, err := store.Claim(ctx, "github", "concurrent-delivery-xyz")
			results[idx] = row.Inserted
			errs[idx] = err
		}(i)
	}
	wg.Wait()

	insertedCount := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Claim[%d]: %v", i, err)
		}
		if results[i] {
			insertedCount++
		}
	}
	if insertedCount != 1 {
		t.Errorf("insertedCount = %d, want exactly 1 winner across %d concurrent claims of the same identity", insertedCount, n)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_deliveries WHERE provider = $1 AND delivery_id = $2`,
		"github", "concurrent-delivery-xyz",
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want exactly 1 (no duplicate row from the race)", count)
	}
}
