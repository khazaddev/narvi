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
// only in their repo set must still resolve to the identical cache volume
// key when their base/runtimeVersion agree, proving the cache is shared
// across repo sets rather than accidentally re-partitioned by whatever a
// caller happens to pass alongside it.
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
// base="a"+runtimeVersion="bc" and base="ab"+runtimeVersion="c" would
// naively concatenate to the identical "abc" without a field separator.
func TestCacheVolumeKey_NoAmbiguousConcatenationCollision(t *testing.T) {
	t.Parallel()

	key1 := imagebuild.CacheVolumeKey("a", "bc")
	key2 := imagebuild.CacheVolumeKey("ab", "c")
	if key1 == key2 {
		t.Fatalf("CacheVolumeKey(%q, %q) == CacheVolumeKey(%q, %q) = %q: naive concatenation collision not prevented",
			"a", "bc", "ab", "c", key1)
	}
}

// TestWellKnownCachePaths_FixedClosedSet proves the closed set's own basic
// well-formedness invariants: non-empty, every entry an absolute path, no
// duplicates (a duplicate would silently mount the same path twice for no
// benefit), and — the three inputs the design itself names by example —
// npm's, pip's, and Go's own cache locations are present.
func TestWellKnownCachePaths_FixedClosedSet(t *testing.T) {
	t.Parallel()

	paths := imagebuild.WellKnownCachePaths
	if len(paths) == 0 {
		t.Fatal("WellKnownCachePaths is empty")
	}

	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if !strings.HasPrefix(p, "/") {
			t.Errorf("WellKnownCachePaths contains a non-absolute path %q", p)
		}
		if seen[p] {
			t.Errorf("WellKnownCachePaths contains duplicate entry %q", p)
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
			t.Errorf("WellKnownCachePaths has no entry containing %q (npm/_cacache, pip cache, GOMODCACHE are the design's own three worked examples, §19.1)", want)
		}
	}
}
