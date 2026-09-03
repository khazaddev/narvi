//go:build integration

// Integration test for SessionStore.UpdateIntentDecisionIfNull (§18.4's
// write-once guarded UPDATE -- "UPDATE sessions SET intent_decision = ...
// WHERE intent_decision IS NULL", never read-then-write) against a REAL
// Postgres instance under genuine concurrent transactions -- the M10
// audit fix (observability/consolidation batch).
//
// Before this fix, the only concurrency test for this write-once contract
// (internal/app/intentclassifier/concurrency_test.go's own
// TestService_RecordDecision_ConcurrentDoubleWrite_OnlyOneWins) exercised
// an in-memory Go-mutex-guarded fake reimplementing "first caller wins"
// semantics itself, never the real guarded UPDATE below real concurrent
// Postgres transactions -- confirmed by direct search that zero references
// to UpdateIntentDecisionIfNull/UpdateSessionIntentDecisionIfNull existed
// anywhere in this package's own test files. Mirrors
// webhookdelivery_store_integration_test.go's own
// TestWebhookDeliveryStore_Claim_ConcurrentSameIdentity_ExactlyOneWinner
// errgroup-based concurrency-test shape exactly, adapted to this store's
// own UPDATE ... WHERE ... IS NULL guard rather than an INSERT ... ON
// CONFLICT one.
package postgres_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"golang.org/x/sync/errgroup"

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
)

// decodeJSONObject unmarshals raw into a generic map for semantic
// (key-order- and whitespace-independent) comparison -- Postgres's own
// jsonb column type re-serializes stored JSON in its own canonical key
// order, so comparing the raw bytes coming back from a SELECT against the
// raw bytes originally sent to an INSERT/UPDATE is never reliable, even
// when the two are logically identical.
func decodeJSONObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return m
}

// TestSessionStore_UpdateIntentDecisionIfNull_ConcurrentSameSession_ExactlyOneWinner
// proves the write-once guard is genuinely concurrency-safe at the real-SQL
// level (-race, real testcontainers Postgres, N real goroutines): N
// concurrent UpdateIntentDecisionIfNull calls for the SAME session id must
// yield exactly one won=true, and the row's own intent_decision column
// must end up holding that ONE winner's payload, never a later caller's
// (the guarded UPDATE's WHERE intent_decision IS NULL clause means every
// loser's own UPDATE genuinely touches zero rows, rather than overwriting
// the winner's).
func TestSessionStore_UpdateIntentDecisionIfNull_ConcurrentSameSession_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)
	store := narvipg.NewSessionStore(pool)

	const n = 20

	// start gates every goroutine's own UpdateIntentDecisionIfNull call so
	// they all arrive at the guarded UPDATE roughly together -- proving
	// genuine concurrency, not an accidental sequential ordering. Mirrors
	// githubprsession_store_integration_test.go's own identical
	// close(start)-as-broadcast convention.
	start := make(chan struct{})

	results := make([]bool, n)
	payloads := make([][]byte, n)
	for i := 0; i < n; i++ {
		payloads[i] = []byte(fmt.Sprintf(
			`{"surface":"github","source":"classifier","target":"review","mode":"build","decided_at":"2026-01-01T00:00:00Z","decided_at_stage":"create","n":%d}`, i))
	}

	var g errgroup.Group
	for i := 0; i < n; i++ {
		idx := i
		g.Go(func() error {
			<-start
			won, err := store.UpdateIntentDecisionIfNull(ctx, sessionID, payloads[idx])
			results[idx] = won
			return err
		})
	}

	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent UpdateIntentDecisionIfNull: %v", err)
	}

	wins := 0
	winnerIdx := -1
	for i, won := range results {
		if won {
			wins++
			winnerIdx = i
		}
	}
	if wins != 1 {
		t.Fatalf("wins = %d across %d concurrent UpdateIntentDecisionIfNull calls for the SAME session id, want exactly 1", wins, n)
	}

	var stored []byte
	if err := pool.QueryRow(ctx,
		`SELECT intent_decision FROM sessions WHERE id = $1`, sessionID,
	).Scan(&stored); err != nil {
		t.Fatalf("query stored intent_decision: %v", err)
	}
	if got, want := decodeJSONObject(t, stored), decodeJSONObject(t, payloads[winnerIdx]); !reflect.DeepEqual(got, want) {
		t.Errorf("stored intent_decision = %s, want the single winner's own payload %s", stored, payloads[winnerIdx])
	}
}

// TestSessionStore_UpdateIntentDecisionIfNull_SecondCallNeverOverwrites
// proves the sequential (non-concurrent) half of the same guarantee: a
// second call for a session that already has a decision recorded reports
// won=false and leaves the FIRST decision's payload untouched -- "first
// decision wins" against real Postgres, not just the in-memory fake.
func TestSessionStore_UpdateIntentDecisionIfNull_SecondCallNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)
	store := narvipg.NewSessionStore(pool)

	first := []byte(`{"surface":"slack","source":"classifier","target":"review","mode":"build","decided_at":"2026-01-01T00:00:00Z","decided_at_stage":"first_prompt"}`)
	second := []byte(`{"surface":"slack","source":"classifier","target":"request","mode":"plan","decided_at":"2026-01-01T00:00:01Z","decided_at_stage":"first_prompt"}`)

	won1, err := store.UpdateIntentDecisionIfNull(ctx, sessionID, first)
	if err != nil {
		t.Fatalf("first UpdateIntentDecisionIfNull: %v", err)
	}
	if !won1 {
		t.Fatal("first UpdateIntentDecisionIfNull won = false, want true")
	}

	won2, err := store.UpdateIntentDecisionIfNull(ctx, sessionID, second)
	if err != nil {
		t.Fatalf("second UpdateIntentDecisionIfNull: %v", err)
	}
	if won2 {
		t.Error("second UpdateIntentDecisionIfNull won = true, want false (first decision wins)")
	}

	var stored []byte
	if err := pool.QueryRow(ctx,
		`SELECT intent_decision FROM sessions WHERE id = $1`, sessionID,
	).Scan(&stored); err != nil {
		t.Fatalf("query stored intent_decision: %v", err)
	}
	if got, want := decodeJSONObject(t, stored), decodeJSONObject(t, first); !reflect.DeepEqual(got, want) {
		t.Errorf("stored intent_decision = %s, want the FIRST call's own payload %s (never overwritten)", stored, first)
	}
}
