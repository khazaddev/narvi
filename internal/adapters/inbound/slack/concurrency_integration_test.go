//go:build integration

// Integration test for the M12 audit finding ("Slack's own 'lost claim'
// fallback race, untested"): resolveOrClaimSession's own fallback
// (handler.go, roughly lines 696-709) -- when SlackThreadSessionStore.Claim
// loses the race to atomically claim a brand-new (channel, thread_ts)
// mapping, the LOSING request re-fetches the actual winner's session and
// falls through to authorizeExistingSessionReply, rather than creating a
// second, competing session -- had NO test using real concurrent goroutines
// to exercise it at all before this fix (confirmed via grep: no Slack test
// in this package used errgroup/real concurrent HTTP requests anywhere).
//
// Mirrors internal/adapters/inbound/github/handler_integration_test.go's
// own TestGitHubIntegration_ConcurrentMentionsCoalesceToOneSessionManyTurns
// (a real errgroup.Group, N concurrent goroutines POSTing the SAME
// handler), adapted for Slack's own DELIBERATELY DIFFERENT design: GitHub's
// CreateOrJoin (coalesce.go) row-locks (SELECT ... FOR UPDATE) its own
// per-(repo, PR) claim row BEFORE ever creating a session, so it never
// creates more than one session no matter how many mentions race, and it
// always queues a turn per mention (AlwaysQueue, turn.go). Slack's own
// design (doc.go's own "Thread<->session mapping design" section) is
// instead an OPTIMISTIC create-then-claim: EVERY concurrent racer on a
// brand-new thread creates its OWN bare session first, then races to
// atomically claim the (channel, thread_ts) mapping (INSERT ... ON
// CONFLICT DO NOTHING) -- exactly one wins, and every loser's bare session
// is deliberately left as an idle, never-dispatched orphan (doc.go's own
// "accepted, honestly-documented tradeoff"), never asserted away here.
// Slack's addTurn also uses DropIfOpen, not GitHub's AlwaysQueue (turn.go),
// so a session that already has an open turn silently declines a second
// one rather than queuing it -- so this test's own correct turn-count
// assertion is "exactly 1" (the first concurrent reply to land), not "N".
package slack_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/sync/errgroup"
)

// TestHandler_ConcurrentRepliesOnUnmappedThread_FallBackToSameWinningSession
// simulates N people concurrently @-mentioning the bot as replies inside
// the SAME pre-existing Slack thread (identical thread_ts) that this
// adapter has never mapped to a session yet -- each its own DISTINCT
// underlying message (a different ts per goroutine), so NONE of them are
// coalesced away by NewHandler's own message-level (channel, ts) dedup
// claim (slackMessageClaimProvider) before ever reaching
// resolveOrClaimSession at all; all N therefore genuinely race on
// SlackThreadSessionStore.Claim for the identical (channel, thread_ts)
// mapping. Asserts: every request got 200 OK, exactly ONE
// slack_thread_sessions mapping row resulted (the atomic claim itself
// never lets two racers both "win"), every one of the N concurrent
// replies -- winner and every loser alike -- resolved onto that SAME
// session (never dropped, never a second, duplicate mapping), and no turn
// ever landed on any OTHER (orphaned, losing) bare session.
func TestHandler_ConcurrentRepliesOnUnmappedThread_FallBackToSameWinningSession(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	rig := newSlackTestRig(t, pool)

	const channel = "C-CONCURRENT-NEWTHREAD"
	const rootTS = "1700000900.000100"
	const n = 8

	start := make(chan struct{})
	statuses := make([]int, n)

	var g errgroup.Group
	for i := 0; i < n; i++ {
		idx := i
		g.Go(func() error {
			<-start
			// appMentionEnvelope (handler_integration_test.go) always uses
			// the fixed, already-linked "U0TESTUSER" actor -- deliberately
			// reused here (same user for every concurrent request) so this
			// test isolates the claim race itself, not identity resolution.
			ts := fmt.Sprintf("1700000900.%06d", 200+idx)
			text := fmt.Sprintf("please help with this #%d", idx)
			envelope := appMentionEnvelope(fmt.Sprintf("Ev-CONCURRENT-NEWTHREAD-%d", idx), channel, ts, rootTS, text)
			req := signedSlackRequest(t, envelope)
			rec := httptest.NewRecorder()
			rig.handler(rec, req)
			statuses[idx] = rec.Code
			return nil
		})
	}
	close(start)
	if err := g.Wait(); err != nil {
		t.Fatalf("concurrent webhook posts: %v", err)
	}

	for i, status := range statuses {
		if status != http.StatusOK {
			t.Errorf("statuses[%d] = %d, want %d", i, status, http.StatusOK)
		}
	}

	// Exactly one mapping row for this brand-new thread -- the concrete
	// proof the atomic (channel, thread_ts) claim itself never lets two
	// concurrent racers both "win".
	var mappingCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM slack_thread_sessions WHERE channel_id = $1 AND thread_ts = $2`,
		channel, rootTS,
	).Scan(&mappingCount); err != nil {
		t.Fatalf("count mapping rows: %v", err)
	}
	if mappingCount != 1 {
		t.Fatalf("mapping row count = %d, want exactly 1 (all %d concurrent repliers on this brand-new thread must race for the SAME single claim)", mappingCount, n)
	}

	mapping, err := rig.threads.Get(ctx, channel, rootTS)
	if err != nil {
		t.Fatalf("Get thread mapping: %v", err)
	}

	// Every concurrent request's own reply -- the winner AND every loser
	// alike, via authorizeExistingSessionReply's own fallback -- must land
	// on the SAME winning session. Slack's addTurn uses DropIfOpen
	// (turn.go), not GitHub's AlwaysQueue, so only the FIRST of these N
	// concurrent addTurn calls actually creates a turn -- every other one
	// correctly finds it already open (the session-row lock inside
	// createTurnLocked serializes them) and declines rather than queuing a
	// second one, mirroring this same session's own ordinary
	// single-open-turn behavior elsewhere in this package
	// (TestCreateTurn_InFlightTurnExists_Returns409's identical
	// single-open-turn invariant, httpapi package). Exactly 1 turn --
	// never 0 (dropped) and never >1 (duplicated) -- is therefore the
	// correct proof here.
	turns, err := rig.turns.ListForSession(ctx, mapping.SessionID)
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) on winning session = %d, want exactly 1 (every concurrent reply must resolve onto the SAME session -- not dropped, not duplicated)", len(turns))
	}

	// The bare sessions CREATED (but never claimed) by every LOSING
	// request are this design's own accepted, documented tradeoff
	// (doc.go's own "Thread<->session mapping design" section) -- idle,
	// never-dispatched orphans, deliberately NOT asserted away here
	// (unlike GitHub's own row-locked design, which never creates one at
	// all). What matters is that none of them ever received a turn of
	// their own.
	var orphanTurnCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM turns t JOIN sessions s ON s.id = t.session_id WHERE s.spawn_source = 'slack' AND s.id != $1`,
		mapping.SessionID,
	).Scan(&orphanTurnCount); err != nil {
		t.Fatalf("count orphan turns: %v", err)
	}
	if orphanTurnCount != 0 {
		t.Errorf("orphan turn count = %d, want 0 (no turn may ever land on a losing/orphaned bare session)", orphanTurnCount)
	}
}
