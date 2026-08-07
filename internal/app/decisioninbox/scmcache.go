// Package decisioninbox is the app-layer read-model aggregator backing
// the decision inbox (Step 60, "decision inbox: read model + API", §16).
// It combines existing Postgres state (plans, sessions, automations,
// outbox, review_findings, sentinel_fixes, artifacts) with live
// SourceControl data into the four-kind taxonomy internal/domain/
// decisioninbox classifies and ranks -- see aggregate.go's own doc
// comment for the full per-kind design. This package holds NO new
// authoritative state of its own: the SCM cache below is exactly what
// §16.2 calls for ("SCM data is cached with a short TTL... never
// presented as live truth"), never a second source of truth for anything
// Postgres or GitHub already owns.
package decisioninbox

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// ttlEntry is one cached value plus when it was fetched.
type ttlEntry[V any] struct {
	value     V
	fetchedAt time.Time
	expiresAt time.Time
}

// ttlCache is a small, generic, mutex-guarded TTL cache. Mirrors
// internal/app/sessionactor/repoaccesscache.go's own established shape
// (opportunistic expired-entry sweep on write, no dedicated background
// sweeper -- that file's own doc comment: "no generic TTL-cache utility
// exists anywhere in this repo", confirmed still true immediately before
// writing this one) with ONE addition that cache doesn't need: fetchedAt
// is returned back to the caller by get, since §16.2's whole point --
// unlike repoAccessCache's own pure allow/deny gate -- is SURFACING
// staleness to the end user ("as of 2 min ago"), never silently trusting
// the cache.
//
// Generic over (K, V) rather than one hand-copied cache per SCM data kind
// this Step caches (ListOpenPRsForUser's own []ports.OpenPR,
// ResolveCodeOwners' own []ports.Owner) -- a genuinely generic shape is
// straightforward to write once and reuse, rather than repeating
// repoAccessCache's own "no existing generic TTL-cache utility" gap a
// second time in the same codebase.
type ttlCache[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]ttlEntry[V]
}

func newTTLCache[K comparable, V any]() *ttlCache[K, V] {
	return &ttlCache[K, V]{entries: make(map[K]ttlEntry[V])}
}

// get returns the cached value for key, plus when it was fetched, if a
// live (unexpired) entry exists. ok=false covers both "never fetched" and
// "fetched, but the TTL has since elapsed" uniformly.
func (c *ttlCache[K, V]) get(key K, now time.Time) (value V, fetchedAt time.Time, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, found := c.entries[key]
	if !found || !now.Before(entry.expiresAt) {
		var zero V
		return zero, time.Time{}, false
	}
	return entry.value, entry.fetchedAt, true
}

// set records a fresh value for key, fetched at now, valid until now+ttl.
func (c *ttlCache[K, V]) set(key K, value V, now time.Time, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sweepExpiredLocked(now)
	c.entries[key] = ttlEntry[V]{value: value, fetchedAt: now, expiresAt: now.Add(ttl)}
}

// sweepExpiredLocked removes every already-expired entry -- callers must
// already hold c.mu. Piggybacks on set's own call (which already holds
// the lock and has a fresh now), mirroring repoAccessCache.
// sweepExpiredLocked's own identical precedent exactly.
func (c *ttlCache[K, V]) sweepExpiredLocked(now time.Time) {
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
}

// codeOwnersCacheKey identifies one ResolveCodeOwners lookup -- a PR's own
// changed-file set is typically stable across repeated inbox loads within
// one cache TTL window, so caching per (repo, ref, joined paths) still
// hits efficiently on a repeat load of the SAME PR.
type codeOwnersCacheKey struct {
	owner, repo, ref string
	pathsKey         string
}

func codeOwnersKey(owner, repo, ref string, paths []string) codeOwnersCacheKey {
	sorted := append([]string(nil), paths...)
	sort.Strings(sorted)
	return codeOwnersCacheKey{owner: owner, repo: repo, ref: ref, pathsKey: strings.Join(sorted, "\x00")}
}

// SCMCache is the §16.2 short-TTL cache wrapping every live SourceControl
// read the decision-inbox aggregator makes -- constructed once and
// threaded through Deps, exactly like repoAccessCache is constructed once
// per Registry and threaded through every Actor (see that file's own doc
// comment for the identical reasoning: a per-request cache would mean a
// second concurrent request moments later never benefits from the first
// request's already-paid-for fetch).
type SCMCache struct {
	sourceControl ports.SourceControl
	timeouts      platform.Timeouts

	openPRs    *ttlCache[string, []ports.OpenPR]
	codeOwners *ttlCache[codeOwnersCacheKey, []ports.Owner]
}

// NewSCMCache builds an SCMCache wrapping sourceControl.
func NewSCMCache(sourceControl ports.SourceControl, timeouts platform.Timeouts) *SCMCache {
	return &SCMCache{
		sourceControl: sourceControl,
		timeouts:      timeouts,
		openPRs:       newTTLCache[string, []ports.OpenPR](),
		codeOwners:    newTTLCache[codeOwnersCacheKey, []ports.Owner](),
	}
}

// ListOpenPRsForUser returns spec's own open-PR list, live-fetching on a
// cache miss/expiry and caching the result for platform.Timeouts.
// DecisionInboxSCMCacheTTL. asOf is the instant the returned data was
// ACTUALLY fetched (a cache hit returns the ORIGINAL fetch's own instant,
// never now) -- the "as of 2 min ago" staleness §16.2 requires be
// displayed, never silently masked.
//
// A truncated fetch (ports.SourceControl.ListOpenPRsForUser's own
// truncated return -- §60 review finding C1: a degraded/partial read,
// e.g. one of GitHub's two underlying search queries itself failed while
// the other still returned a real, if incomplete, result) is NEVER
// cached: the caller still gets today's best-effort partial result
// (never blanked out), but writing it into the cache for the full TTL
// would silently present a transient, partial read as a confirmed-
// complete, fresh empty-or-partial queue for up to
// DecisionInboxSCMCacheTTL -- the exact "never presented as live truth"
// hazard §16.2 exists to prevent. Only the NEXT request is affected, by
// retrying live instead of serving a stale, possibly-still-wrong cache
// hit.
func (c *SCMCache) ListOpenPRsForUser(ctx context.Context, spec ports.ListOpenPRsForUserSpec, now time.Time) (prs []ports.OpenPR, asOf time.Time, err error) {
	if cached, fetchedAt, ok := c.openPRs.get(spec.GitHubExternalID, now); ok {
		return cached, fetchedAt, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeouts.GitHubListOpenPRsForUserTimeout)
	prs, truncated, err := c.sourceControl.ListOpenPRsForUser(callCtx, spec)
	cancel()
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("decisioninbox: list open prs for user: %w", err)
	}

	// fetchedAt (§60 review finding, "born-expired entries"): anchored on
	// FETCH COMPLETION, a fresh now taken AFTER the (potentially slow, up
	// to GitHubListOpenPRsForUserTimeout == 3 minutes) call above returns
	// -- deliberately NOT the pre-fetch `now` parameter this method was
	// called with. expiresAt was previously computed from that pre-fetch
	// instant while the fetch itself could take up to 3 minutes against a
	// 2-minute TTL (DecisionInboxSCMCacheTTL): a slow fetch for a heavy
	// user could write an entry get() would immediately reject as already
	// expired, so the cache never amortized for exactly the users it
	// exists to help. The displayed asOf below is this SAME completion
	// instant -- still the real, honest "when the data was actually
	// fetched" moment §16.2 requires, just measured at the end of the
	// call rather than the (arbitrarily earlier, for a slow fetch) start.
	fetchedAt := time.Now()

	if truncated {
		platform.Logger(ctx).Warn("decisioninbox: list open prs for user returned a truncated/degraded result -- not caching", "github_external_id", spec.GitHubExternalID)
		return prs, fetchedAt, nil
	}

	c.openPRs.set(spec.GitHubExternalID, prs, fetchedAt, c.timeouts.DecisionInboxSCMCacheTTL)
	return prs, fetchedAt, nil
}

// ResolveCodeOwners returns spec's own owner resolution, live-fetching on
// a cache miss/expiry and caching the result -- mirrors ListOpenPRsForUser
// above exactly, keyed on (owner, repo, ref, sorted paths).
func (c *SCMCache) ResolveCodeOwners(ctx context.Context, spec ports.ResolveCodeOwnersSpec, now time.Time) (owners []ports.Owner, asOf time.Time, err error) {
	key := codeOwnersKey(spec.Owner, spec.Repo, spec.Ref, spec.Paths)
	if cached, fetchedAt, ok := c.codeOwners.get(key, now); ok {
		return cached, fetchedAt, nil
	}

	callCtx, cancel := context.WithTimeout(ctx, c.timeouts.GitHubResolveCodeOwnersTimeout)
	defer cancel()
	owners, err = c.sourceControl.ResolveCodeOwners(callCtx, spec)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("decisioninbox: resolve code owners: %w", err)
	}

	c.codeOwners.set(key, owners, now, c.timeouts.DecisionInboxSCMCacheTTL)
	return owners, now, nil
}
