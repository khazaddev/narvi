//go:build integration

// Integration test for GitHubActorLinkNoticeStore (batch fix/deny-
// unlinked-github-actors' own anti-spam dedupe table,
// migrations/000043_github_actor_link_notices.up.sql) -- mirrors
// identitylinkprompt_store_integration_test.go's own "focused file per
// query" convention.
package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
)

// TestGitHubActorLinkNoticeStore_GetMissThenUpsertThenGetHit proves the
// basic Get/Upsert round trip: no row yet (pgx.ErrNoRows), then Upsert
// records one, then Get finds it. Get/Upsert are kept as low-level store
// primitives (this file's own focus), but handler.go's own notify
// workflow (internal/adapters/inbound/github/actornotauthorizedreply.go)
// no longer calls this pair directly -- it now goes through Claim instead,
// which checks and records in a single atomic statement rather than two
// separate calls with a network round trip in between; see Claim's own
// doc comment (githubactorlinknotice_store.go) for why.
func TestGitHubActorLinkNoticeStore_GetMissThenUpsertThenGetHit(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewGitHubActorLinkNoticeStore(pool)

	const repoFullName = "acme/notice-repo"
	const prNumber = int32(101)
	const commenterID = int64(555001)

	if _, err := store.Get(ctx, repoFullName, prNumber, commenterID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("Get (before any Upsert) = %v, want pgx.ErrNoRows", err)
	}

	created, err := store.Upsert(ctx, repoFullName, prNumber, commenterID)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if created.RepoFullName != repoFullName || created.PrNumber != prNumber || created.CommenterID != commenterID {
		t.Errorf("Upsert result = %+v, want repo=%q pr=%d commenter=%d", created, repoFullName, prNumber, commenterID)
	}
	if !created.NotifiedAt.Valid {
		t.Error("Upsert result NotifiedAt.Valid = false, want true")
	}

	got, err := store.Get(ctx, repoFullName, prNumber, commenterID)
	if err != nil {
		t.Fatalf("Get (after Upsert): %v", err)
	}
	if got.NotifiedAt.Time != created.NotifiedAt.Time {
		t.Errorf("Get NotifiedAt = %v, want %v (the row Upsert just wrote)", got.NotifiedAt.Time, created.NotifiedAt.Time)
	}
}

// TestGitHubActorLinkNoticeStore_UpsertRefreshesNotifiedAt proves a SECOND
// Upsert for the SAME (repo, PR, commenter) key updates notified_at
// in place -- ON CONFLICT DO UPDATE, not DO NOTHING -- rather than either
// erroring or silently leaving the original (now-stale) timestamp.
func TestGitHubActorLinkNoticeStore_UpsertRefreshesNotifiedAt(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewGitHubActorLinkNoticeStore(pool)

	const repoFullName = "acme/notice-refresh-repo"
	const prNumber = int32(202)
	const commenterID = int64(555002)

	first, err := store.Upsert(ctx, repoFullName, prNumber, commenterID)
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	second, err := store.Upsert(ctx, repoFullName, prNumber, commenterID)
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	if second.NotifiedAt.Time.Before(first.NotifiedAt.Time) {
		t.Errorf("second Upsert's NotifiedAt = %v, want >= first Upsert's NotifiedAt = %v", second.NotifiedAt.Time, first.NotifiedAt.Time)
	}

	// Still exactly one row for this key -- DO UPDATE, never a second row.
	var rowCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM github_actor_link_notices WHERE repo_full_name = $1 AND pr_number = $2 AND commenter_id = $3`,
		repoFullName, prNumber, commenterID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("row count = %d, want exactly 1 (upsert must never produce a second row for the same key)", rowCount)
	}
}

// TestGitHubActorLinkNoticeStore_ScopedPerRepoPRCommenter proves the
// dedupe key is exactly (repo_full_name, pr_number, commenter_id) --
// changing ANY one of the three is a genuinely different row, never
// conflated with another.
func TestGitHubActorLinkNoticeStore_ScopedPerRepoPRCommenter(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewGitHubActorLinkNoticeStore(pool)

	if _, err := store.Upsert(ctx, "acme/repo-a", 1, 42); err != nil {
		t.Fatalf("Upsert base row: %v", err)
	}

	for _, tc := range []struct {
		name         string
		repoFullName string
		prNumber     int32
		commenterID  int64
	}{
		{"different repo", "acme/repo-b", 1, 42},
		{"different PR", "acme/repo-a", 2, 42},
		{"different commenter", "acme/repo-a", 1, 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Get(ctx, tc.repoFullName, tc.prNumber, tc.commenterID); !errors.Is(err, pgx.ErrNoRows) {
				t.Errorf("Get(%q, %d, %d) = %v, want pgx.ErrNoRows (a distinct key must never see the base row)", tc.repoFullName, tc.prNumber, tc.commenterID, err)
			}
		})
	}

	if _, err := store.Get(ctx, "acme/repo-a", 1, 42); err != nil {
		t.Errorf("Get(base row's own key) = %v, want nil", err)
	}
}

// TestGitHubActorLinkNoticeStore_ClaimFirstTimeInserts proves the WINNER
// half of Claim: no row exists yet, so Claim inserts a fresh one and
// reports Inserted == true.
func TestGitHubActorLinkNoticeStore_ClaimFirstTimeInserts(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewGitHubActorLinkNoticeStore(pool)

	const repoFullName = "acme/notice-claim-repo"
	const prNumber = int32(301)
	const commenterID = int64(555010)

	row, err := store.Claim(ctx, repoFullName, prNumber, commenterID, time.Hour)
	if err != nil {
		t.Fatalf("Claim (first time): %v", err)
	}
	if !row.Inserted {
		t.Errorf("Inserted = false, want true (no prior row existed)")
	}
	if row.RepoFullName != repoFullName || row.PrNumber != prNumber || row.CommenterID != commenterID {
		t.Errorf("Claim result = %+v, want repo=%q pr=%d commenter=%d", row, repoFullName, prNumber, commenterID)
	}
}

// TestGitHubActorLinkNoticeStore_ClaimWithinTTLReturnsErrNoRows proves the
// LOSER half: a second Claim for the SAME (repo, PR, commenter) key,
// still within the TTL just claimed, leaves the row untouched and returns
// pgx.ErrNoRows -- the caller's own signal to skip posting again. This is
// the exact atomicity guarantee that closes the TOCTOU race a separate
// Get-then-later-Upsert pair left open: the check and the write are one
// statement, so there is no window for a concurrent caller to observe
// "not yet claimed".
func TestGitHubActorLinkNoticeStore_ClaimWithinTTLReturnsErrNoRows(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewGitHubActorLinkNoticeStore(pool)

	const repoFullName = "acme/notice-claim-ttl-repo"
	const prNumber = int32(302)
	const commenterID = int64(555011)

	first, err := store.Claim(ctx, repoFullName, prNumber, commenterID, time.Hour)
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	if !first.Inserted {
		t.Fatalf("first Claim Inserted = false, want true")
	}

	if _, err := store.Claim(ctx, repoFullName, prNumber, commenterID, time.Hour); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("second Claim (still within TTL) = %v, want pgx.ErrNoRows", err)
	}

	// Still exactly one row, untouched by the failed second claim attempt.
	var rowCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM github_actor_link_notices WHERE repo_full_name = $1 AND pr_number = $2 AND commenter_id = $3`,
		repoFullName, prNumber, commenterID,
	).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("row count = %d, want exactly 1", rowCount)
	}
}

// TestGitHubActorLinkNoticeStore_ClaimAfterTTLElapsedRefreshes proves a
// THIRD outcome: once an existing notice's own notified_at is already
// older than the TTL, a fresh Claim call succeeds again (Inserted ==
// false, since this is an UPDATE of the existing row, not a fresh INSERT)
// and refreshes notified_at to now -- exactly UpsertGitHubActorLinkNotice's
// own "refresh, don't silently no-op" behavior, just folded into the same
// atomic statement as the check. notified_at is pushed into the past
// directly via SQL (real time can't practically be waited out in a fast
// test).
func TestGitHubActorLinkNoticeStore_ClaimAfterTTLElapsedRefreshes(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewGitHubActorLinkNoticeStore(pool)

	const repoFullName = "acme/notice-claim-expired-repo"
	const prNumber = int32(303)
	const commenterID = int64(555012)
	const ttl = time.Hour

	first, err := store.Claim(ctx, repoFullName, prNumber, commenterID, ttl)
	if err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	// Force the just-claimed row's own notified_at well past ttl.
	if _, err := pool.Exec(ctx,
		`UPDATE github_actor_link_notices SET notified_at = $4 WHERE repo_full_name = $1 AND pr_number = $2 AND commenter_id = $3`,
		repoFullName, prNumber, commenterID, first.NotifiedAt.Time.Add(-2*ttl),
	); err != nil {
		t.Fatalf("force notified_at into the past: %v", err)
	}

	second, err := store.Claim(ctx, repoFullName, prNumber, commenterID, ttl)
	if err != nil {
		t.Fatalf("second Claim (after TTL elapsed): %v", err)
	}
	if second.Inserted {
		t.Errorf("second Claim Inserted = true, want false (this is a refresh of an existing, now-stale row, not a fresh insert)")
	}
	if !second.NotifiedAt.Time.After(first.NotifiedAt.Time) {
		t.Errorf("second Claim NotifiedAt = %v, want strictly after the first Claim's own %v", second.NotifiedAt.Time, first.NotifiedAt.Time)
	}
}

// TestGitHubActorLinkNoticeStore_ClaimConcurrentOnlyOneWinner is the
// direct proof for the confirmed audit finding this Claim method exists
// to close: N concurrent Claim calls for the IDENTICAL (repo, PR,
// commenter) key -- simulating N concurrent webhook deliveries for the
// same still-unlinked commenter's mention -- must let exactly ONE succeed
// and every other one see pgx.ErrNoRows, never more than one "go ahead
// and post" verdict for the same TTL window.
func TestGitHubActorLinkNoticeStore_ClaimConcurrentOnlyOneWinner(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewGitHubActorLinkNoticeStore(pool)

	const repoFullName = "acme/notice-claim-concurrent-repo"
	const prNumber = int32(304)
	const commenterID = int64(555013)
	const concurrency = 10

	// start gates every goroutine's own Claim call so they all arrive
	// roughly together, proving genuine concurrency rather than an
	// accidental sequential ordering -- mirrors githubprsession_store_
	// integration_test.go's own identical "closed channel as a one-shot
	// broadcast gate" precedent. Launched via errgroup.Group (never a
	// naked `go` statement -- CLAUDE.md/§11, enforced with no test-file
	// exemption by tools/lint/narvichecks/nakedgoroutine).
	start := make(chan struct{})
	results := make([]error, concurrency)
	var g errgroup.Group
	for i := 0; i < concurrency; i++ {
		idx := i
		g.Go(func() error {
			<-start
			_, err := store.Claim(ctx, repoFullName, prNumber, commenterID, time.Hour)
			results[idx] = err
			return nil
		})
	}
	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent Claim: %v", err)
	}

	winners, losers := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, pgx.ErrNoRows):
			losers++
		default:
			t.Errorf("unexpected Claim error: %v", err)
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1 (out of %d concurrent Claim calls for the SAME key)", winners, concurrency)
	}
	if losers != concurrency-1 {
		t.Errorf("losers = %d, want %d", losers, concurrency-1)
	}
}
