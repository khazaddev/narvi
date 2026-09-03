// This file (repoaccesscache.go) holds repoAccessCache -- the small,
// process-wide, in-memory TTL cache backing imageresolve.go's own
// repo-access gate (audit fix, "warm-boot image access control", HIGH; see
// that file's own top comment for the full gate design).
//
// A verdict here is keyed by (session creator user id, normalized repo
// clone URL) -- NOT by session id -- because the same user's verdict for a
// given repo must be shared across every session they touch, exactly like
// the design decision this fix's own review settled on: repo ACCESS is a
// property of (user, repo), never of any one session. Both a positive and
// a negative verdict are cached, each for RepoAccessCacheTTL (platform.
// Timeouts) -- an indeterminate outcome (CheckRepoAccess itself failing,
// e.g. a network timeout or a GitHub 5xx) is deliberately NEVER cached: see
// imageresolve.go's own gate function for why a transient SCM outage must
// re-check on the very next spawn rather than freezing a stale "deny" (or
// "allow") in place for the rest of this cache entry's TTL.
//
// Constructed exactly once per Registry (NewRegistry) and threaded through
// to every Actor that Registry hydrates -- mirroring how
// contractDriftDetected (the OTel counter) is already threaded through
// (registry.go/hydrate.go) -- rather than being per-Actor/per-session,
// since a per-session cache would mean a user's very first sandbox on a
// second session never benefits from the first session's already-paid-for
// check.
//
// This is plain internal application state, not a port: no existing
// generic TTL-cache utility exists anywhere in this repo (checked before
// writing this), and this is intentionally too small and single-purpose to
// justify inventing one now.
//
// # Expired-entry sweep (audit-remediation addition, finding #8)
//
// set (below) opportunistically sweeps every ALREADY-expired entry out of
// the map before inserting the fresh one -- piggybacking on a call that
// already holds the lock and already has a fresh `now`, rather than a
// dedicated background goroutine/ticker this small, low-throughput cache
// does not otherwise need (no naked goroutines; every timeout/interval
// belongs in platform/timeouts.go, and a sweep interval would be one more
// of those for no real benefit here). Without this, this cache's own
// doc-comment claim -- entry count "naturally bounded by (active users) x
// (repos they've named)" -- was not actually true: an entry is only ever
// replaced if the IDENTICAL (userID, repoURL) key is checked again later,
// so a departed user or a one-off session's repo left the map growing,
// unbounded, for the life of the process. Sweeping on every set() call
// (which happens on every cache MISS that gets a genuine verdict) makes
// that claim actually hold: the map can never hold more not-yet-expired
// entries than get() would still honor, plus at most one write's worth of
// already-expired stragglers between sweeps.
//
// # Circuit breaker (audit-remediation addition, finding #5)
//
// breakerFailures/breakerLastFailure (below) are this SAME cache's own
// second, distinct piece of state: a process-wide count of consecutive
// INDETERMINATE CheckRepoAccess failures (network/timeout/5xx, or a
// rate-limited 403 -- see githubapi.isRateLimitedResponse), never a
// per-(user, repo) concept the way entries above is. Reuses domain/
// sandbox.EvaluateCircuitBreaker directly (a pure, already-generically-
// parameterized decision function -- see that package's own doc comment)
// rather than duplicating equivalent logic a second time. See imageresolve.
// go's own repoAccessAllowedForSpawn for how/when this is consulted and
// updated.

package sessionactor

import (
	"sync"
	"time"

	"github.com/narvidev/narvi/internal/domain/sandbox"
)

// repoAccessCheckBreakerThreshold is the number of consecutive
// indeterminate CheckRepoAccess failures that trips this cache's own
// circuit breaker -- mirrors domain/sandbox.CircuitBreakerThreshold's own
// "3 failures" precedent exactly (a plain int, not a duration, so -- like
// that constant -- it lives beside its own breaker rather than in
// platform/timeouts.go, which is durations only).
const repoAccessCheckBreakerThreshold = 3

// repoAccessCacheKey identifies one (session creator, repo) verdict.
// repoURL is expected to already be imagebuild.NormalizeRepoURL's own
// output -- the SAME normalization resolveAndSetImage's own fingerprint
// computation already applies to every repo URL, so two differently-
// spelled URLs for the same remote share one cache entry here exactly like
// they already share one Fingerprint.
type repoAccessCacheKey struct {
	userID  string
	repoURL string
}

// repoAccessCacheEntry is one cached verdict: allowed is only meaningful
// while now.Before(expiresAt) -- callers check expiry themselves (get,
// below) rather than relying on any DEDICATED background eviction
// goroutine/ticker; genuinely expired entries are instead reclaimed
// opportunistically by set()'s own sweepExpiredLocked call (finding #8,
// this file's own top "# Expired-entry sweep" comment) whenever a fresh
// verdict is written.
type repoAccessCacheEntry struct {
	allowed   bool
	expiresAt time.Time
}

// repoAccessCache is a mutex-guarded map -- the simplest correct shape for
// this cache's own access pattern (frequent reads, infrequent writes, no
// need for sharding at this scale -- stale entries are reclaimed via an
// opportunistic sweep in set(), below, rather than a dedicated background
// sweeper). Safe for concurrent use by every Actor goroutine in this
// process, since it is shared (via Registry) rather than per-Actor.
type repoAccessCache struct {
	mu      sync.Mutex
	entries map[repoAccessCacheKey]repoAccessCacheEntry

	// breakerFailures/breakerLastFailure are this cache's own SECOND,
	// distinct piece of state (finding #5): a process-wide, not
	// per-(user, repo), count of consecutive INDETERMINATE CheckRepoAccess
	// failures -- see this file's own top "# Circuit breaker" comment and
	// breakerOpen/recordCheckFailure/recordCheckSuccess below.
	breakerFailures    int
	breakerLastFailure time.Time
}

// newRepoAccessCache builds an empty repoAccessCache.
func newRepoAccessCache() *repoAccessCache {
	return &repoAccessCache{entries: make(map[repoAccessCacheKey]repoAccessCacheEntry)}
}

// get returns the cached verdict for (userID, repoURL) if one exists and
// has not yet expired as of now. ok=false means "no cached verdict -- a
// live CheckRepoAccess call is required", covering both "never checked"
// and "checked, but the TTL has since elapsed" uniformly.
func (c *repoAccessCache) get(userID, repoURL string, now time.Time) (allowed bool, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, found := c.entries[repoAccessCacheKey{userID: userID, repoURL: repoURL}]
	if !found || !now.Before(entry.expiresAt) {
		return false, false
	}
	return entry.allowed, true
}

// set records a fresh verdict for (userID, repoURL), valid until now+ttl.
// Called only with a genuine CheckRepoAccess result (true or false, err ==
// nil) -- see this file's own top comment for why an indeterminate outcome
// must never be cached.
func (c *repoAccessCache) set(userID, repoURL string, allowed bool, now time.Time, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sweepExpiredLocked(now)

	c.entries[repoAccessCacheKey{userID: userID, repoURL: repoURL}] = repoAccessCacheEntry{
		allowed:   allowed,
		expiresAt: now.Add(ttl),
	}
}

// sweepExpiredLocked removes every entry whose expiresAt has already
// passed as of now (finding #8) -- callers must already hold c.mu. Piggy-
// backs on set()'s own call, which already has a fresh `now` and already
// holds the lock, rather than a dedicated background sweeper (see this
// file's own top comment).
func (c *repoAccessCache) sweepExpiredLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}

// recordCheckFailure records one indeterminate CheckRepoAccess failure
// (network/timeout/5xx, or a rate-limited 403 -- NEVER a definitive deny)
// toward this cache's own process-wide circuit breaker (finding #5) --
// called by imageresolve.go's repoAccessAllowedForSpawn on every such
// failure, immediately before it denies (uncached) this one spawn.
func (c *repoAccessCache) recordCheckFailure(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.breakerFailures++
	c.breakerLastFailure = now
}

// recordCheckSuccess resets the breaker's own failure count to zero --
// called after ANY genuine (err == nil) CheckRepoAccess result, allow or
// deny alike, since either one proves the SCM API is reachable and
// answering again right now.
func (c *repoAccessCache) recordCheckSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.breakerFailures = 0
}

// breakerOpen reports whether this cache's own circuit breaker is
// currently OPEN -- i.e. CheckRepoAccess has failed indeterminately
// repoAccessCheckBreakerThreshold times within the last window, so the
// caller should short-circuit straight to a deny for THIS repo check
// WITHOUT calling CheckRepoAccess again (still fail-closed; only the
// network round trip itself is skipped -- see imageresolve.go's own call
// site). Reuses domain/sandbox.EvaluateCircuitBreaker directly (see this
// file's own top comment for why).
func (c *repoAccessCache) breakerOpen(now time.Time, window time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	decision := sandbox.EvaluateCircuitBreaker(
		sandbox.CircuitBreakerState{FailureCount: c.breakerFailures, LastFailureTime: c.breakerLastFailure},
		sandbox.CircuitBreakerConfig{Threshold: repoAccessCheckBreakerThreshold, Window: window},
		now,
	)
	if decision.ShouldReset {
		c.breakerFailures = 0
	}
	return !decision.ShouldProceed
}
