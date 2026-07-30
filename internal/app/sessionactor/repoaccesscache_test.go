package sessionactor

import (
	"testing"
	"time"
)

// TestRepoAccessCache_GetSetRoundTrip proves the basic (userID, repoURL)
// verdict cache round-trips correctly and respects TTL expiry -- the
// pre-existing behavior this file's own audit-remediation additions below
// must not disturb.
func TestRepoAccessCache_GetSetRoundTrip(t *testing.T) {
	c := newRepoAccessCache()
	now := time.Now()

	if _, ok := c.get("user1", "https://github.com/acme/repo1", now); ok {
		t.Fatal("get on empty cache returned ok=true, want false")
	}

	c.set("user1", "https://github.com/acme/repo1", true, now, time.Minute)
	allowed, ok := c.get("user1", "https://github.com/acme/repo1", now)
	if !ok || !allowed {
		t.Fatalf("get after set = (%v, %v), want (true, true)", allowed, ok)
	}

	// After the TTL elapses, the entry must no longer be honored.
	_, ok = c.get("user1", "https://github.com/acme/repo1", now.Add(2*time.Minute))
	if ok {
		t.Error("get after TTL expiry returned ok=true, want false")
	}
}

// TestRepoAccessCache_SetSweepsExpiredEntries is the audit-remediation
// regression test (correctness-availability, finding #8): set() must
// opportunistically sweep every entry whose TTL has already elapsed,
// keeping the map from growing unboundedly for the life of the process --
// this cache's own doc comment claims entry count is "naturally bounded by
// (active users) x (repos they've named)"; before this fix that was not
// actually true (an entry was only ever replaced if the identical
// (userID, repoURL) key was checked again later).
func TestRepoAccessCache_SetSweepsExpiredEntries(t *testing.T) {
	c := newRepoAccessCache()
	now := time.Now()

	// Insert several entries with a short TTL, all now expired.
	for i := 0; i < 5; i++ {
		c.set(userIDForTest(i), "https://github.com/acme/stale-repo", true, now, time.Second)
	}
	if got := len(c.entries); got != 5 {
		t.Fatalf("entries after 5 sets = %d, want 5", got)
	}

	// Advance well past every entry's own TTL, then perform ONE more set
	// for a brand-new key -- this must sweep every stale entry out,
	// leaving only the fresh one behind.
	later := now.Add(time.Hour)
	c.set("brand-new-user", "https://github.com/acme/fresh-repo", true, later, time.Minute)

	if got := len(c.entries); got != 1 {
		t.Errorf("entries after sweep = %d, want 1 (every expired entry must be swept, leaving only the fresh one)", got)
	}
	if _, ok := c.get("brand-new-user", "https://github.com/acme/fresh-repo", later); !ok {
		t.Error("the fresh entry itself must survive its own insertion")
	}
}

func userIDForTest(i int) string {
	return "stale-user-" + string(rune('a'+i))
}

// TestRepoAccessCache_CircuitBreaker_TripsAfterThresholdFailures is the
// audit-remediation regression test (correctness-availability, finding
// #5): breakerOpen must report closed (proceed) until
// repoAccessCheckBreakerThreshold consecutive failures have been recorded
// within window, then open (skip further checks) -- and must reset once a
// success is recorded.
func TestRepoAccessCache_CircuitBreaker_TripsAfterThresholdFailures(t *testing.T) {
	c := newRepoAccessCache()
	now := time.Now()
	const window = time.Minute

	if c.breakerOpen(now, window) {
		t.Fatal("breakerOpen on a fresh cache = true, want false (closed)")
	}

	for i := 0; i < repoAccessCheckBreakerThreshold-1; i++ {
		c.recordCheckFailure(now)
	}
	if c.breakerOpen(now, window) {
		t.Fatalf("breakerOpen after %d failures = true, want false (threshold is %d)", repoAccessCheckBreakerThreshold-1, repoAccessCheckBreakerThreshold)
	}

	c.recordCheckFailure(now)
	if !c.breakerOpen(now, window) {
		t.Fatalf("breakerOpen after %d failures = false, want true (threshold reached)", repoAccessCheckBreakerThreshold)
	}

	// A success resets the breaker.
	c.recordCheckSuccess()
	if c.breakerOpen(now, window) {
		t.Error("breakerOpen after recordCheckSuccess = true, want false (a success must reset the failure count)")
	}
}

// TestRepoAccessCache_CircuitBreaker_ResetsAfterWindowElapses proves the
// breaker also self-heals purely from time passing, even with no explicit
// recordCheckSuccess call -- mirroring domain/sandbox.
// EvaluateCircuitBreaker's own "window has passed since the last counted
// failure: reset" behavior, reused here directly.
func TestRepoAccessCache_CircuitBreaker_ResetsAfterWindowElapses(t *testing.T) {
	c := newRepoAccessCache()
	now := time.Now()
	const window = time.Minute

	for i := 0; i < repoAccessCheckBreakerThreshold; i++ {
		c.recordCheckFailure(now)
	}
	if !c.breakerOpen(now, window) {
		t.Fatal("breakerOpen right after tripping = false, want true")
	}

	later := now.Add(2 * window)
	if c.breakerOpen(later, window) {
		t.Error("breakerOpen after the window has elapsed = true, want false (should have self-reset)")
	}
}
