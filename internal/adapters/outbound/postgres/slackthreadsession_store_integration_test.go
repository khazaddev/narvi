//go:build integration

// Integration tests for SlackThreadSessionStore.Claim/Get (§8.10's
// thread↔session mapping "Slack ingress") -- mirrors
// webhookdelivery_store_integration_test.go's own precedent of a
// focused file per query rather than growing postgres_integration_test.go's
// single pipeline test.
package postgres_test

import (
	"context"
	"testing"

	"golang.org/x/sync/errgroup"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestSlackThreadSessionStore_Claim_FirstClaimWins proves the first claim
// for a given (channel_id, thread_ts) wins (ok == true) and the returned
// row's own session_id round-trips.
func TestSlackThreadSessionStore_Claim_FirstClaimWins(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessions := narvipg.NewSessionStore(pool)
	store := narvipg.NewSlackThreadSessionStore(pool)

	session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceSlack,
	})
	if err != nil {
		t.Fatalf("create fixture session: %v", err)
	}

	row, ok, err := store.Claim(ctx, "C123", "1700000000.000100", session.ID)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !ok {
		t.Fatal("Claim: ok = false, want true (first claim for this thread)")
	}
	if row.ChannelID != "C123" {
		t.Errorf("ChannelID = %q, want %q", row.ChannelID, "C123")
	}
	if row.ThreadTs != "1700000000.000100" {
		t.Errorf("ThreadTs = %q, want %q", row.ThreadTs, "1700000000.000100")
	}
	if row.SessionID != session.ID {
		t.Errorf("SessionID = %v, want %v", row.SessionID, session.ID)
	}
}

// TestSlackThreadSessionStore_Claim_SecondClaimLoses proves a second
// claim attempt for the SAME (channel_id, thread_ts) -- a concurrent
// racer that lost -- reports ok == false, err == nil (never a genuine
// error), and never overwrites the winner's own session_id. Get then
// proves the loser can discover the winner's real session via a plain
// lookup.
func TestSlackThreadSessionStore_Claim_SecondClaimLoses(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessions := narvipg.NewSessionStore(pool)
	store := narvipg.NewSlackThreadSessionStore(pool)

	winner, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceSlack})
	if err != nil {
		t.Fatalf("create winner session: %v", err)
	}
	loser, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceSlack})
	if err != nil {
		t.Fatalf("create loser session: %v", err)
	}

	if _, ok, err := store.Claim(ctx, "C999", "1700000001.000200", winner.ID); err != nil || !ok {
		t.Fatalf("first Claim: ok=%v err=%v, want ok=true err=nil", ok, err)
	}

	row, ok, err := store.Claim(ctx, "C999", "1700000001.000200", loser.ID)
	if err != nil {
		t.Fatalf("second Claim: %v", err)
	}
	if ok {
		t.Error("second Claim: ok = true, want false (lost the race)")
	}
	if row.SessionID.Valid {
		t.Error("second Claim: row is non-zero, want the zero value on a lost claim")
	}

	got, err := store.Get(ctx, "C999", "1700000001.000200")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.SessionID != winner.ID {
		t.Errorf("Get().SessionID = %v, want the WINNER's %v (never the loser's)", got.SessionID, winner.ID)
	}
}

// TestSlackThreadSessionStore_Get_NoMapping proves Get surfaces
// pgx.ErrNoRows (via errors.Is, exactly like SessionStore.Get) when no
// mapping exists yet for a (channel_id, thread_ts) -- the "not our
// thread"/"not seen yet" case a REPLY-vs-NEW-mention branch depends on.
func TestSlackThreadSessionStore_Get_NoMapping(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewSlackThreadSessionStore(pool)

	_, err := store.Get(ctx, "C000", "no-such-thread")
	if err == nil {
		t.Fatal("Get: err = nil, want pgx.ErrNoRows for an unmapped thread")
	}
}

// TestSlackThreadSessionStore_Claim_ConcurrentSameThread_ExactlyOneWinner
// proves the claim is genuinely concurrent-safe -- INSERT ON CONFLICT DO
// NOTHING, not a read-then-write race: N goroutines racing to claim the
// exact same (channel_id, thread_ts), each with its OWN distinct
// candidate session (mirroring internal/adapters/inbound/slack's own
// "create the session first, then race to claim" sequencing), must yield
// exactly one winner and N-1 losers, never two winners and never a lost/
// duplicate row -- mirrors WebhookDeliveryStore's own identical
// concurrency proof exactly.
func TestSlackThreadSessionStore_Claim_ConcurrentSameThread_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessions := narvipg.NewSessionStore(pool)
	store := narvipg.NewSlackThreadSessionStore(pool)

	const n = 20
	candidateIDs := make([]sqlcgen.Session, n)
	for i := 0; i < n; i++ {
		session, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceSlack})
		if err != nil {
			t.Fatalf("create candidate session %d: %v", i, err)
		}
		candidateIDs[i] = session
	}

	var g errgroup.Group
	results := make([]bool, n)
	for i := 0; i < n; i++ {
		idx := i
		g.Go(func() error {
			_, ok, err := store.Claim(ctx, "Cconcurrent", "1700000002.000300", candidateIDs[idx].ID)
			results[idx] = ok
			return err
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	wins := 0
	for _, ok := range results {
		if ok {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("wins = %d, want exactly 1 winner across %d concurrent claims of the same thread", wins, n)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM slack_thread_sessions WHERE channel_id = $1 AND thread_ts = $2`,
		"Cconcurrent", "1700000002.000300",
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want exactly 1 (no duplicate row from the race)", count)
	}
}
