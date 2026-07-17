//go:build integration

package sessionactor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestConcurrentGetOrSpawn_ExactlyOneWins simulates "two pods" as two
// independent Registry instances sharing one Postgres pool, each racing
// GetOrSpawn for the SAME session id. Exactly one must succeed; the other
// must get ErrSessionActorElsewhere immediately -- never hang waiting for
// the advisory lock (the deliberate fail-fast, never-block behavior GetOrSpawn documents).
func TestConcurrentGetOrSpawn_ExactlyOneWins(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	r1 := NewRegistry(ctx, pool, platform.DefaultTimeouts())
	r2 := NewRegistry(ctx, pool, platform.DefaultTimeouts())
	t.Cleanup(func() { _ = r1.Shutdown() })
	t.Cleanup(func() { _ = r2.Shutdown() })

	type outcome struct {
		actor *Actor
		err   error
	}
	outcomes := make(chan outcome, 2)

	// errgroup, not a bare `go` statement, per §11/nakedgoroutine (no test
	// exemption).
	var eg errgroup.Group
	eg.Go(func() error {
		a, err := r1.GetOrSpawn(ctx, sessionID)
		outcomes <- outcome{actor: a, err: err}
		return nil
	})
	eg.Go(func() error {
		a, err := r2.GetOrSpawn(ctx, sessionID)
		outcomes <- outcome{actor: a, err: err}
		return nil
	})
	if err := eg.Wait(); err != nil {
		t.Fatalf("errgroup.Wait: %v", err)
	}
	close(outcomes)

	var succeeded, gotElsewhere int
	for o := range outcomes {
		switch {
		case o.err == nil && o.actor != nil:
			succeeded++
		case errors.Is(o.err, ErrSessionActorElsewhere):
			gotElsewhere++
		default:
			t.Fatalf("unexpected GetOrSpawn outcome: actor=%v err=%v", o.actor, o.err)
		}
	}

	if succeeded != 1 || gotElsewhere != 1 {
		t.Fatalf("got %d succeeded / %d ErrSessionActorElsewhere, want exactly 1 and 1", succeeded, gotElsewhere)
	}
}

// TestActorTransact_StaleEpochEvictsSelf proves epoch fencing (§2):
// hydrate an Actor, bump the session's actor_epoch directly against the
// DB (simulating a second pod having since taken over), then attempt a
// transactional write through the ORIGINAL actor handle -- it must fail
// with ErrStaleEpoch, and must not have run the caller's write function
// at all. It then drives the same failure through the real command path
// (Send -> handle -> transact) and confirms the actor evicts itself and
// releases its advisory lock, proven by a fresh GetOrSpawn succeeding
// again afterward.
func TestActorTransact_StaleEpochEvictsSelf(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	r := NewRegistry(ctx, pool, platform.DefaultTimeouts())
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	// Simulate a second pod having since taken over: bump the actor
	// epoch directly against the DB, exactly as a competing Registry's
	// hydrateAndAcquire would (the actual cross-process race for
	// OWNERSHIP is covered separately by
	// TestConcurrentGetOrSpawn_ExactlyOneWins; this test isolates the
	// FENCING check itself).
	sessionStore := narvipg.NewSessionStore(pool)
	if _, err := sessionStore.BumpActorEpoch(ctx, sessionID); err != nil {
		t.Fatalf("BumpActorEpoch: %v", err)
	}

	var fnRan bool
	err = a.transact(ctx, func(_ context.Context, _ pgx.Tx) error {
		fnRan = true
		return nil
	})
	if !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("transact() after epoch bump = %v, want ErrStaleEpoch", err)
	}
	if fnRan {
		t.Error("transact() ran the caller's write function despite a stale epoch -- the fencing check did not short-circuit before it")
	}

	// Now drive the SAME failure through the real command path, proving
	// the actor evicts itself as a consequence, not just that transact()
	// in isolation returns the right error.
	if err := a.Send(ctx, TimerFired{Name: TimerInactivity}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var a2 *Actor
	waitUntil(t, 5*time.Second, func() bool {
		got, err := r.GetOrSpawn(ctx, sessionID)
		if err != nil {
			return false
		}
		a2 = got
		return a2 != a
	})
	if a2 == nil || a2 == a {
		t.Fatal("GetOrSpawn never returned a FRESH actor -- the stale one never evicted itself / released its advisory lock")
	}
}

// TestActorIdleEviction_ReleasesLock proves an actor with no commands for
// longer than a (short, injected) ActorIdleTTL evicts itself -- removed
// from the Registry's map AND its advisory lock released, confirmed by a
// second GetOrSpawn for the same session succeeding again afterward
// (which could only happen if the lock was genuinely released, not just
// the in-memory map entry cleared).
func TestActorIdleEviction_ReleasesLock(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	timeouts := platform.DefaultTimeouts()
	timeouts.ActorIdleTTL = 100 * time.Millisecond

	r := NewRegistry(ctx, pool, timeouts)
	t.Cleanup(func() { _ = r.Shutdown() })

	a1, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	var a2 *Actor
	waitUntil(t, 5*time.Second, func() bool {
		got, err := r.GetOrSpawn(ctx, sessionID)
		if err != nil {
			return false
		}
		a2 = got
		return a2 != a1
	})
	if a2 == nil || a2 == a1 {
		t.Fatal("GetOrSpawn never returned a FRESH actor after the idle TTL -- it never evicted itself / released its advisory lock")
	}
}
