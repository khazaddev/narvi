//go:build integration

// Integration tests for LinearAgentSessionStore.Claim ("Linear
// ingress", §8.10) -- mirrors webhookdelivery_store_integration_test.go's
// own structure/precedent exactly (a focused file per store), proving the
// SAME "(xmax = 0) AS inserted" atomic-claim idiom against a real
// Postgres instance, for a DIFFERENT identity (Linear's own AgentSession
// id, not a webhook delivery id) -- see migrations/
// 000030_linear_agent_sessions.up.sql's own doc comment for the full "why
// this table, why this race matters" writeup.
package postgres_test

import (
	"context"
	"testing"

	"golang.org/x/sync/errgroup"

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestLinearAgentSessionStore_Claim_FirstClaimInserted proves the first
// claim for a given agent_session_id is reported as freshly inserted,
// with organization_id populated and session_id still NULL (not yet
// attached -- that happens only after CreateSessionCore itself returns,
// via SetSessionID).
func TestLinearAgentSessionStore_Claim_FirstClaimInserted(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewLinearAgentSessionStore(pool)

	row, err := store.Claim(ctx, "agent-session-abc-123", "org-xyz")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !row.Inserted {
		t.Error("Inserted = false, want true (first claim for this identity)")
	}
	if row.AgentSessionID != "agent-session-abc-123" {
		t.Errorf("AgentSessionID = %q, want %q", row.AgentSessionID, "agent-session-abc-123")
	}
	if row.OrganizationID != "org-xyz" {
		t.Errorf("OrganizationID = %q, want %q", row.OrganizationID, "org-xyz")
	}
	if row.SessionID.Valid {
		t.Error("SessionID.Valid = true, want false (not attached until SetSessionID runs)")
	}
}

// TestLinearAgentSessionStore_Claim_SecondClaimIsDuplicate proves a
// second claim for the SAME agent_session_id -- a genuine redelivery of
// the `created` event, or Linear somehow double-firing -- is detected as
// a duplicate (Inserted == false), never a second row, and that the
// row's own organization_id stays pinned to the FIRST claim's value.
func TestLinearAgentSessionStore_Claim_SecondClaimIsDuplicate(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewLinearAgentSessionStore(pool)

	first, err := store.Claim(ctx, "agent-session-dup", "org-first")
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if !first.Inserted {
		t.Fatal("first Claim: Inserted = false, want true")
	}

	second, err := store.Claim(ctx, "agent-session-dup", "org-second")
	if err != nil {
		t.Fatalf("second Claim (redelivery): %v", err)
	}
	if second.Inserted {
		t.Error("second Claim: Inserted = true, want false (a redelivery of the same agent session identity must be detected as a duplicate)")
	}
	if second.OrganizationID != "org-first" {
		t.Errorf("second Claim OrganizationID = %q, want %q (unchanged from the first claim)", second.OrganizationID, "org-first")
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM linear_agent_sessions WHERE agent_session_id = $1`, "agent-session-dup",
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (never double-inserted)", count)
	}
}

// TestLinearAgentSessionStore_ConcurrentClaims_ExactlyOneWinner proves the
// atomic claim holds under a real concurrent race (mirrors §9.3 test 10's
// own "concurrent @mentions -> exactly one review session" invariant,
// adapted to Linear's own AgentSession identity): N goroutines claiming
// the SAME agent_session_id simultaneously must yield exactly ONE
// Inserted == true winner, with the rest all correctly reporting
// Inserted == false -- never two winners, and never a lost/errored claim.
func TestLinearAgentSessionStore_ConcurrentClaims_ExactlyOneWinner(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewLinearAgentSessionStore(pool)

	const concurrency = 10
	results := make([]bool, concurrency)

	group, gctx := errgroup.WithContext(ctx)
	for i := 0; i < concurrency; i++ {
		i := i
		group.Go(func() error {
			row, err := store.Claim(gctx, "agent-session-race", "org-race")
			if err != nil {
				return err
			}
			results[i] = row.Inserted
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatalf("concurrent Claim: %v", err)
	}

	winners := 0
	for _, inserted := range results {
		if inserted {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1 across %d concurrent claims", winners, concurrency)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM linear_agent_sessions WHERE agent_session_id = $1`, "agent-session-race",
	).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("row count = %d, want 1 (exactly one row survives the race)", count)
	}
}

// TestLinearAgentSessionStore_SetSessionID_AttachesRealID proves
// SetSessionID attaches a real session id onto an already-claimed row,
// and that GetByAgentSessionID's own subsequent lookup reflects it --
// the `prompted`-event routing lookup's own read path.
func TestLinearAgentSessionStore_SetSessionID_AttachesRealID(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewLinearAgentSessionStore(pool)
	sessions := narvipg.NewSessionStore(pool)

	if _, err := store.Claim(ctx, "agent-session-attach", "org-attach"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	created, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceLinear,
		Repos:       []byte(`[{"name":"narvi","url":"https://github.com/narvidev/narvi","branch":null}]`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := store.SetSessionID(ctx, "agent-session-attach", created.ID); err != nil {
		t.Fatalf("SetSessionID: %v", err)
	}

	got, err := store.GetByAgentSessionID(ctx, "agent-session-attach")
	if err != nil {
		t.Fatalf("GetByAgentSessionID: %v", err)
	}
	if !got.SessionID.Valid {
		t.Fatal("SessionID.Valid = false, want true after SetSessionID")
	}
	if got.SessionID != created.ID {
		t.Errorf("SessionID = %v, want %v", got.SessionID, created.ID)
	}
}

// TestLinearAgentSessionStore_Release_UnclaimsRowWithNoSessionID proves
// the H3 audit-fix's own headline case: Release genuinely un-claims a row
// still stuck at a NULL session_id (an authz denial, a CreateSessionCore
// error, or a SetSessionID error -- any post-claim failure BEFORE
// SetSessionID ever won) -- the row is gone afterward, and a fresh Claim
// for the SAME agent_session_id is reported as a brand-new insert, not a
// duplicate.
func TestLinearAgentSessionStore_Release_UnclaimsRowWithNoSessionID(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewLinearAgentSessionStore(pool)

	const agentSessionID = "agent-session-release-no-session-id"

	claimed, err := store.Claim(ctx, agentSessionID, "org-release")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if !claimed.Inserted {
		t.Fatal("Claim: Inserted = false, want true")
	}
	if claimed.SessionID.Valid {
		t.Fatal("Claim: SessionID.Valid = true, want false before SetSessionID ever runs")
	}

	if err := store.Release(ctx, agentSessionID); err != nil {
		t.Fatalf("Release: %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM linear_agent_sessions WHERE agent_session_id = $1`, agentSessionID,
	).Scan(&count); err != nil {
		t.Fatalf("count rows after Release: %v", err)
	}
	if count != 0 {
		t.Fatalf("row count after Release = %d, want 0 (a row with no session_id must be genuinely un-claimed)", count)
	}

	// A later, distinct `created` event (or a manual redelivery) for the
	// SAME agent_session_id must be free to claim it again from scratch --
	// this is exactly what H3 says was impossible before this fix (the row
	// would otherwise be stuck forever).
	reclaimed, err := store.Claim(ctx, agentSessionID, "org-release-retry")
	if err != nil {
		t.Fatalf("re-Claim after Release: %v", err)
	}
	if !reclaimed.Inserted {
		t.Error("re-Claim after Release: Inserted = false, want true (the released row must be claimable again as fresh)")
	}
	if reclaimed.OrganizationID != "org-release-retry" {
		t.Errorf("re-Claim OrganizationID = %q, want %q (a genuinely fresh row, not a stale one)", reclaimed.OrganizationID, "org-release-retry")
	}
}

// TestLinearAgentSessionStore_Release_GuardedAgainstRowWithRealSessionID
// proves Release's own load-bearing guard: a row that already has a REAL
// session_id attached (SetSessionID already won) is left COMPLETELY
// untouched by a later, unrelated Release call -- releasing it would let
// a future event re-claim the SAME agent_session_id and attempt to create
// a SECOND, colliding session, exactly the coalescing failure
// linear_agent_sessions exists to prevent.
func TestLinearAgentSessionStore_Release_GuardedAgainstRowWithRealSessionID(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewLinearAgentSessionStore(pool)
	sessions := narvipg.NewSessionStore(pool)

	const agentSessionID = "agent-session-release-guard"

	if _, err := store.Claim(ctx, agentSessionID, "org-guard"); err != nil {
		t.Fatalf("Claim: %v", err)
	}

	created, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceLinear,
		Repos:       []byte(`[{"name":"narvi","url":"https://github.com/narvidev/narvi","branch":null}]`),
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	if err := store.SetSessionID(ctx, agentSessionID, created.ID); err != nil {
		t.Fatalf("SetSessionID: %v", err)
	}

	// A later, entirely unrelated failure elsewhere in the SAME request
	// (or a completely separate, buggy caller) tries to Release this SAME
	// agent_session_id anyway -- this must be a safe no-op.
	if err := store.Release(ctx, agentSessionID); err != nil {
		t.Fatalf("Release (on an already-attached row): %v", err)
	}

	got, err := store.GetByAgentSessionID(ctx, agentSessionID)
	if err != nil {
		t.Fatalf("GetByAgentSessionID after guarded Release: %v", err)
	}
	if !got.SessionID.Valid {
		t.Fatal("SessionID.Valid = false after guarded Release, want true (a row with a REAL session_id must never be un-claimed)")
	}
	if got.SessionID != created.ID {
		t.Errorf("SessionID = %v after guarded Release, want unchanged %v", got.SessionID, created.ID)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM linear_agent_sessions WHERE agent_session_id = $1`, agentSessionID,
	).Scan(&count); err != nil {
		t.Fatalf("count rows after guarded Release: %v", err)
	}
	if count != 1 {
		t.Errorf("row count after guarded Release = %d, want 1 (row must survive untouched)", count)
	}
}
