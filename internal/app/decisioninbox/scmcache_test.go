// Unit tests for SCMCache (scmcache.go) -- deliberately NOT gated behind
// the "integration" build tag: SCMCache wraps a ports.SourceControl (a
// plain interface, faked below) and holds no Postgres dependency of its
// own, so these run under a plain `go test`, no container required.
// Mirrors this package's own aggregate_integration_test.go
// fakeDecisionInboxSourceControl precedent, but as a SEPARATE fake (that
// one lives behind the "integration" build tag and would not be visible
// to this file).
package decisioninbox_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/app/decisioninbox"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// fakeSCMCacheSourceControl is a minimal, call-counting, concurrency-safe
// test-only ports.SourceControl -- narrowed to exactly the two methods
// SCMCache itself calls (ListOpenPRsForUser, ResolveCodeOwners); every
// other method returns a plain "not implemented" error.
type fakeSCMCacheSourceControl struct {
	mu sync.Mutex

	openPRsByExternalID map[string][]ports.OpenPR
	openPRsCallCount    map[string]int
	// openPRsDelay simulates a slow live fetch -- the "born-expired
	// entries" test below needs a REAL fetch duration that outlasts a
	// (deliberately tiny, test-only) TTL to actually exercise the bug.
	openPRsDelay time.Duration
}

var _ ports.SourceControl = (*fakeSCMCacheSourceControl)(nil)

func (f *fakeSCMCacheSourceControl) ListOpenPRsForUser(ctx context.Context, spec ports.ListOpenPRsForUserSpec) ([]ports.OpenPR, bool, error) {
	f.mu.Lock()
	if f.openPRsCallCount == nil {
		f.openPRsCallCount = map[string]int{}
	}
	f.openPRsCallCount[spec.GitHubExternalID]++
	delay := f.openPRsDelay
	prs := f.openPRsByExternalID[spec.GitHubExternalID]
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}

	return prs, false, nil
}

func (f *fakeSCMCacheSourceControl) callCount(externalID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openPRsCallCount[externalID]
}

func (f *fakeSCMCacheSourceControl) ResolveCodeOwners(context.Context, ports.ResolveCodeOwnersSpec) ([]ports.Owner, error) {
	return nil, nil
}
func (f *fakeSCMCacheSourceControl) CreatePR(context.Context, ports.CreatePRSpec) (ports.PRRef, error) {
	return ports.PRRef{}, errors.New("fakeSCMCacheSourceControl: CreatePR not implemented")
}
func (f *fakeSCMCacheSourceControl) ResolveBranchSHA(context.Context, ports.ResolveBranchSHASpec) (string, string, error) {
	return "", "", errors.New("fakeSCMCacheSourceControl: ResolveBranchSHA not implemented")
}
func (f *fakeSCMCacheSourceControl) ResolveContractsFingerprint(context.Context, ports.ResolveContractsFingerprintSpec) (string, bool, error) {
	return "", false, errors.New("fakeSCMCacheSourceControl: ResolveContractsFingerprint not implemented")
}
func (f *fakeSCMCacheSourceControl) CheckRepoAccess(context.Context, ports.CheckRepoAccessSpec) (bool, error) {
	return false, errors.New("fakeSCMCacheSourceControl: CheckRepoAccess not implemented")
}
func (f *fakeSCMCacheSourceControl) GetFileContent(context.Context, ports.GetFileContentSpec) (string, string, bool, error) {
	return "", "", false, errors.New("fakeSCMCacheSourceControl: GetFileContent not implemented")
}
func (f *fakeSCMCacheSourceControl) UpdateFileContent(context.Context, ports.UpdateFileContentSpec) (string, error) {
	return "", errors.New("fakeSCMCacheSourceControl: UpdateFileContent not implemented")
}
func (f *fakeSCMCacheSourceControl) RegisterPRStack(context.Context, ports.RegisterPRStackSpec) error {
	return errors.New("fakeSCMCacheSourceControl: RegisterPRStack not implemented")
}
func (f *fakeSCMCacheSourceControl) ListMergedBetween(context.Context, ports.ListMergedBetweenSpec) ([]ports.MergedPR, bool, error) {
	return nil, false, errors.New("fakeSCMCacheSourceControl: ListMergedBetween not implemented")
}
func (f *fakeSCMCacheSourceControl) CreateBranch(context.Context, ports.CreateBranchSpec) error {
	return errors.New("fakeSCMCacheSourceControl: CreateBranch not implemented")
}
func (f *fakeSCMCacheSourceControl) MergePR(context.Context, ports.MergePRSpec) (string, error) {
	return "", errors.New("fakeSCMCacheSourceControl: MergePR not implemented")
}

// TestSCMCache_ListOpenPRsForUser_IsolatesByExternalID proves the cache
// key (spec.GitHubExternalID) is what actually isolates one user's own
// cached PR list from another's (§60 review finding B1) -- SCMCache is
// constructed ONCE, process-wide, and shared across every actor's own
// inbox load; the cache key is the ENTIRE tenant-isolation boundary. A
// mutation replacing that key with a constant would make this test's own
// second call (bob) return alice's already-cached result instead of ever
// reaching bob's own fake data.
func TestSCMCache_ListOpenPRsForUser_IsolatesByExternalID(t *testing.T) {
	t.Parallel()

	fake := &fakeSCMCacheSourceControl{
		openPRsByExternalID: map[string][]ports.OpenPR{
			"alice-external-id": {{Number: 111, Title: "alice's private PR"}},
			"bob-external-id":   {{Number: 222, Title: "bob's private PR"}},
		},
	}
	cache := decisioninbox.NewSCMCache(fake, platform.DefaultTimeouts())
	now := time.Now()

	alicePRs, _, err := cache.ListOpenPRsForUser(context.Background(), ports.ListOpenPRsForUserSpec{GitHubExternalID: "alice-external-id"}, now)
	if err != nil {
		t.Fatalf("alice's call error = %v", err)
	}
	if len(alicePRs) != 1 || alicePRs[0].Number != 111 {
		t.Fatalf("alice's PRs = %+v, want exactly PR #111", alicePRs)
	}

	bobPRs, _, err := cache.ListOpenPRsForUser(context.Background(), ports.ListOpenPRsForUserSpec{GitHubExternalID: "bob-external-id"}, now)
	if err != nil {
		t.Fatalf("bob's call error = %v", err)
	}
	if len(bobPRs) != 1 || bobPRs[0].Number != 222 {
		t.Fatalf("bob's PRs = %+v, want exactly PR #222 -- got alice's own cached result instead (cache key is not isolating by external ID)", bobPRs)
	}
	for _, pr := range bobPRs {
		if pr.Number == 111 {
			t.Fatalf("bob's own result leaked alice's PR #111: %+v", bobPRs)
		}
	}

	// Both distinct external IDs must have actually reached the
	// underlying fetch -- neither a hit against the OTHER's entry.
	if got := fake.callCount("alice-external-id"); got != 1 {
		t.Errorf("alice fetch called %d times, want 1", got)
	}
	if got := fake.callCount("bob-external-id"); got != 1 {
		t.Errorf("bob fetch called %d times, want 1", got)
	}
}

// TestSCMCache_ListOpenPRsForUser_CacheHitReturnsOriginalFetchInstant
// proves a cache HIT returns the ORIGINAL fetch's own instant, never the
// hit's own later `now` (§60 review finding T3 -- §16.2's own "never
// presented as live truth" invariant: `return cached, now, nil` on the
// hit path would pass every OTHER existing test, since the one prior
// cache-hit coverage shared `now` between the miss and the hit call,
// making the two instants indistinguishable). A real time.Sleep between
// the two calls here makes firstAsOf and a hit-path `now` bug
// unmistakably distinguishable.
func TestSCMCache_ListOpenPRsForUser_CacheHitReturnsOriginalFetchInstant(t *testing.T) {
	t.Parallel()

	fake := &fakeSCMCacheSourceControl{
		openPRsByExternalID: map[string][]ports.OpenPR{"1": {{Number: 1}}},
	}
	cache := decisioninbox.NewSCMCache(fake, platform.DefaultTimeouts())

	_, firstAsOf, err := cache.ListOpenPRsForUser(context.Background(), ports.ListOpenPRsForUserSpec{GitHubExternalID: "1"}, time.Now())
	if err != nil {
		t.Fatalf("first call error = %v", err)
	}

	time.Sleep(20 * time.Millisecond)
	secondNow := time.Now()
	_, secondAsOf, err := cache.ListOpenPRsForUser(context.Background(), ports.ListOpenPRsForUserSpec{GitHubExternalID: "1"}, secondNow)
	if err != nil {
		t.Fatalf("second call error = %v", err)
	}

	if got := fake.callCount("1"); got != 1 {
		t.Fatalf("fetch called %d times, want 1 (the second call must be a cache hit, well within DecisionInboxSCMCacheTTL)", got)
	}
	if !secondAsOf.Equal(firstAsOf) {
		t.Errorf("second call's own asOf = %v, want %v (the ORIGINAL fetch instant) -- got the hit's own later `now` (%v) instead", secondAsOf, firstAsOf, secondNow)
	}
}

// TestSCMCache_ListOpenPRsForUser_SlowFetchDoesNotBornExpire proves a
// cache entry's own expiresAt is anchored on fetch COMPLETION, never the
// PRE-fetch `now` the caller supplied (§60 review "Also do" batch,
// "born-expired entries"): expiresAt used to be computed from the
// pre-fetch `now` while the fetch itself could take up to
// GitHubListOpenPRsForUserTimeout (3 minutes, production) against a much
// shorter DecisionInboxSCMCacheTTL (2 minutes, production) -- a slow
// fetch for a heavy user could write an entry get() would immediately
// reject as already expired, so the cache never amortized for exactly
// the users it exists to help. This test reproduces the same SHAPE of
// bug at test-friendly durations: a fetch (200ms) that deliberately
// outlasts the TTL (50ms).
func TestSCMCache_ListOpenPRsForUser_SlowFetchDoesNotBornExpire(t *testing.T) {
	t.Parallel()

	const ttl = 50 * time.Millisecond
	const fetchDelay = 200 * time.Millisecond

	fake := &fakeSCMCacheSourceControl{
		openPRsByExternalID: map[string][]ports.OpenPR{"1": {{Number: 1}}},
		openPRsDelay:        fetchDelay,
	}
	timeouts := platform.DefaultTimeouts()
	timeouts.DecisionInboxSCMCacheTTL = ttl
	timeouts.GitHubListOpenPRsForUserTimeout = 10 * time.Second // plenty of headroom over fetchDelay
	cache := decisioninbox.NewSCMCache(fake, timeouts)

	preFetchNow := time.Now()
	_, _, err := cache.ListOpenPRsForUser(context.Background(), ports.ListOpenPRsForUserSpec{GitHubExternalID: "1"}, preFetchNow)
	if err != nil {
		t.Fatalf("first call error = %v", err)
	}
	if got := fake.callCount("1"); got != 1 {
		t.Fatalf("fetch called %d times after first call, want 1", got)
	}

	// A realistic, immediate follow-up request: `now` captured fresh,
	// right after the first call returns.
	secondNow := time.Now()
	if !secondNow.After(preFetchNow.Add(ttl)) {
		t.Fatalf("test setup invariant violated: the fetch (%s) must outlast the TTL (%s) for this test to actually exercise the bug -- preFetchNow=%v secondNow=%v", fetchDelay, ttl, preFetchNow, secondNow)
	}

	_, _, err = cache.ListOpenPRsForUser(context.Background(), ports.ListOpenPRsForUserSpec{GitHubExternalID: "1"}, secondNow)
	if err != nil {
		t.Fatalf("second call error = %v", err)
	}
	if got := fake.callCount("1"); got != 1 {
		t.Errorf("fetch called %d times after second call, want 1 (still a cache hit -- expiresAt must be anchored on fetch COMPLETION, not the pre-fetch `now`)", got)
	}
}
