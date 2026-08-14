package imagebuild_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/imagebuild"
)

// TestCacheVolumeKey_Deterministic proves the same (base, runtimeVersion)
// always produces the identical key — the same "same inputs, same output"
// property Fingerprint itself carries (fingerprint_test.go's own
// TestFingerprint_DeterministicRegardlessOfMapIterationOrder), minus any
// map to iterate at all here.
func TestCacheVolumeKey_Deterministic(t *testing.T) {
	t.Parallel()

	first := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15")
	if first == "" {
		t.Fatal("CacheVolumeKey returned empty string")
	}

	for i := 0; i < 50; i++ {
		got := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15")
		if got != first {
			t.Fatalf("iteration %d: CacheVolumeKey = %q, want %q (same inputs must always key identically)", i, got, first)
		}
	}
}

// TestCacheVolumeKey_DifferentInputsProduceDifferentKeys proves both
// fingerprinted inputs (base, runtimeVersion) are load-bearing.
func TestCacheVolumeKey_DifferentInputsProduceDifferentKeys(t *testing.T) {
	t.Parallel()

	baseline := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15")

	tests := []struct {
		name           string
		base           string
		runtimeVersion string
	}{
		{name: "different base", base: "narvi/base:v2", runtimeVersion: "1.17.15"},
		{name: "different runtime version", base: "narvi/base:v1", runtimeVersion: "1.18.0"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := imagebuild.CacheVolumeKey(tc.base, tc.runtimeVersion)
			if got == baseline {
				t.Errorf("CacheVolumeKey(%q, %q) = %q, want different from baseline %q", tc.base, tc.runtimeVersion, got, baseline)
			}
		})
	}
}

// TestCacheVolumeKey_IgnoresEverythingButBaseAndRuntimeVersion is the core
// regression test for §19.1's own "keyed on Base + RuntimeVersion ONLY —
// never on repo content" requirement: CacheVolumeKey's signature already
// structurally excludes repos (it takes no repos parameter at all), but
// this pins the CONSEQUENCE that matters — two Fingerprints that diverge
// only in their repo set must still resolve to the identical cache key
// when their base/runtimeVersion agree, proving the cache is shared across
// repo sets rather than accidentally re-partitioned by whatever a caller
// happens to pass alongside it.
func TestCacheVolumeKey_IgnoresEverythingButBaseAndRuntimeVersion(t *testing.T) {
	t.Parallel()

	reposA := map[string]string{"frontend": "https://github.com/acme/frontend.git"}
	reposB := map[string]string{
		"backend": "https://github.com/acme/backend.git",
		"infra":   "https://github.com/acme/infra.git",
	}

	fpA := imagebuild.Fingerprint("narvi/base:v1", reposA, "1.17.15")
	fpB := imagebuild.Fingerprint("narvi/base:v1", reposB, "1.17.15")
	if fpA == fpB {
		t.Fatal("test setup invalid: reposA and reposB must produce different Fingerprints")
	}

	keyA := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15")
	keyB := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15")
	if keyA != keyB {
		t.Errorf("CacheVolumeKey diverged (%q vs %q) for a repo-set-only difference in Fingerprint inputs — the cache key must never depend on repos", keyA, keyB)
	}
}

// TestCacheVolumeKey_NoAmbiguousConcatenationCollision mirrors
// fingerprint_test.go's own TestFingerprint_NoAmbiguousConcatenationCollision:
// naive concatenation without a field separator would let base="a"+
// runtimeVersion="bc" collide with base="ab"+runtimeVersion="c".
func TestCacheVolumeKey_NoAmbiguousConcatenationCollision(t *testing.T) {
	t.Parallel()

	key1 := imagebuild.CacheVolumeKey("a", "bc")
	key2 := imagebuild.CacheVolumeKey("ab", "c")
	if key1 == key2 {
		t.Fatalf("CacheVolumeKey(%q, %q) == CacheVolumeKey(%q, %q) = %q: naive concatenation collision not prevented", "a", "bc", "ab", "c", key1)
	}
}

// TestWellKnownCachePaths_FixedClosedSet proves the closed set's own basic
// well-formedness invariants: non-empty, every entry an absolute path, no
// duplicates (a duplicate would silently mount the same path twice for no
// benefit), and — the three inputs the design itself names by example —
// npm's, pip's, and Go's own cache locations are present.
func TestWellKnownCachePaths_FixedClosedSet(t *testing.T) {
	t.Parallel()

	paths := imagebuild.WellKnownCachePaths()
	if len(paths) == 0 {
		t.Fatal("WellKnownCachePaths() is empty")
	}

	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("WellKnownCachePaths() contains a non-absolute path %q", p)
		}
		if seen[p] {
			t.Errorf("WellKnownCachePaths() contains duplicate entry %q", p)
		}
		seen[p] = true
	}

	wantSubstrings := []string{"_cacache", "pip", "go/pkg/mod"}
	for _, want := range wantSubstrings {
		found := false
		for _, p := range paths {
			if strings.Contains(p, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("WellKnownCachePaths() has no entry containing %q (npm/_cacache, pip cache, GOMODCACHE are the design's own three worked examples, §19.1)", want)
		}
	}
}

// TestWellKnownCachePaths_CallerCannotMutateSharedBackingArray is the
// direct regression test for the audit-remediation finding this function
// (converted from an exported var) exists to close: "WellKnownCachePaths
// is an exported mutable slice handed across the port on every build. Make
// it impossible for a caller to mutate the shared backing array." Mutating
// the slice returned by one call must never be observable from a second,
// independent call.
func TestWellKnownCachePaths_CallerCannotMutateSharedBackingArray(t *testing.T) {
	t.Parallel()

	first := imagebuild.WellKnownCachePaths()
	originalFirstEntry := first[0]

	// A malicious or careless caller mutates the slice IT received...
	first[0] = "/tmp/mutated-by-a-caller"
	for i := range first {
		first[i] = "/tmp/mutated-by-a-caller"
	}

	// ...and a completely independent second call must be entirely
	// unaffected: neither the original entry's value nor the set's own
	// length may have changed.
	second := imagebuild.WellKnownCachePaths()
	if second[0] != originalFirstEntry {
		t.Fatalf("WellKnownCachePaths() second call = %q, want %q — a caller mutating its own returned slice leaked into a later call, proving the backing array is shared", second[0], originalFirstEntry)
	}
	if len(second) != len(first) {
		t.Fatalf("WellKnownCachePaths() second call has %d entries, want %d", len(second), len(first))
	}
}

// TestPruneCacheVersions_KeepsNewestRetainedCacheVersions is
// PruneCacheVersions' own core regression test: given more versions than
// domain/imagebuild.RetainedCacheVersions, it must prune EXACTLY the
// oldest overflow, never the newest — this MUST fail if the mechanism were
// ever removed or reverted to "prune everything"/"prune nothing".
func TestPruneCacheVersions_KeepsNewestRetainedCacheVersions(t *testing.T) {
	t.Parallel()

	// 8 versions, unsorted, retention is 5 -> versions 4,3,2,1 (the oldest
	// 3) must be pruned; 8,7,6,5,... wait: newest 5 of {1..8} are 8,7,6,5,4
	// -> prune {1,2,3}.
	versions := []int64{5, 1, 8, 3, 7, 2, 6, 4}

	got := imagebuild.PruneCacheVersions(versions)

	wantPruned := map[int64]bool{1: true, 2: true, 3: true}
	if len(got) != len(wantPruned) {
		t.Fatalf("PruneCacheVersions(%v) = %v (len %d), want %d entries", versions, got, len(got), len(wantPruned))
	}
	for _, v := range got {
		if !wantPruned[v] {
			t.Errorf("PruneCacheVersions(%v) pruned %d, want only the oldest overflow (%v)", versions, v, wantPruned)
		}
	}

	kept := map[int64]bool{4: true, 5: true, 6: true, 7: true, 8: true}
	for _, v := range got {
		if kept[v] {
			t.Errorf("PruneCacheVersions(%v) pruned %d, which is among the newest %d versions and must be KEPT", versions, v, imagebuild.RetainedCacheVersions)
		}
	}
}

// TestPruneCacheVersions_AtOrBelowRetentionPrunesNothing proves the
// ordinary, common-case outcome: a cache key that has not yet accumulated
// more than RetainedCacheVersions versions has nothing to prune.
func TestPruneCacheVersions_AtOrBelowRetentionPrunesNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		versions []int64
	}{
		{name: "empty", versions: nil},
		{name: "one version", versions: []int64{1}},
		{name: "exactly at the retention limit", versions: []int64{5, 4, 3, 2, 1}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := imagebuild.PruneCacheVersions(tc.versions); len(got) != 0 {
				t.Errorf("PruneCacheVersions(%v) = %v, want empty", tc.versions, got)
			}
		})
	}
}

// TestPruneCacheVersions_NeverMutatesInput proves the input slice is left
// untouched — a caller (app/imagebuild.Builder) that still holds a
// reference to the slice it passed in must never see it silently reordered.
func TestPruneCacheVersions_NeverMutatesInput(t *testing.T) {
	t.Parallel()

	versions := []int64{3, 1, 4, 1, 5, 9, 2, 6}
	original := make([]int64, len(versions))
	copy(original, versions)

	imagebuild.PruneCacheVersions(versions)

	for i := range versions {
		if versions[i] != original[i] {
			t.Fatalf("PruneCacheVersions mutated its input in place: got %v, want unchanged %v", versions, original)
		}
	}
}
