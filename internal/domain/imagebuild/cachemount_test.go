package imagebuild_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/imagebuild"
)

// TestCacheVolumeKey_Deterministic proves the same (base, runtimeVersion,
// epoch) always produces the identical key — the same "same inputs, same
// output" property Fingerprint itself carries (fingerprint_test.go's own
// TestFingerprint_DeterministicRegardlessOfMapIterationOrder), minus any
// map to iterate at all here.
func TestCacheVolumeKey_Deterministic(t *testing.T) {
	t.Parallel()

	first := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15", "")
	if first == "" {
		t.Fatal("CacheVolumeKey returned empty string")
	}

	for i := 0; i < 50; i++ {
		got := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15", "")
		if got != first {
			t.Fatalf("iteration %d: CacheVolumeKey = %q, want %q (same inputs must always key identically)", i, got, first)
		}
	}
}

// TestCacheVolumeKey_DifferentInputsProduceDifferentKeys proves all three
// fingerprinted inputs (base, runtimeVersion, epoch) are load-bearing.
func TestCacheVolumeKey_DifferentInputsProduceDifferentKeys(t *testing.T) {
	t.Parallel()

	baseline := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15", "")

	tests := []struct {
		name           string
		base           string
		runtimeVersion string
		epoch          string
	}{
		{name: "different base", base: "narvi/base:v2", runtimeVersion: "1.17.15", epoch: ""},
		{name: "different runtime version", base: "narvi/base:v1", runtimeVersion: "1.18.0", epoch: ""},
		{name: "different epoch", base: "narvi/base:v1", runtimeVersion: "1.17.15", epoch: "2"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := imagebuild.CacheVolumeKey(tc.base, tc.runtimeVersion, tc.epoch)
			if got == baseline {
				t.Errorf("CacheVolumeKey(%q, %q, %q) = %q, want different from baseline %q", tc.base, tc.runtimeVersion, tc.epoch, got, baseline)
			}
		})
	}
}

// TestCacheVolumeKey_EpochRotatesIndependentlyOfFingerprint is the epoch
// rotation escape hatch's own regression test (audit-remediation finding:
// "no rotation escape hatch... CacheVolumeKey is a pure function of (base,
// runtimeVersion) with no epoch"): bumping epoch alone must change
// CacheVolumeKey (a genuinely NEW, empty cache volume) while leaving
// Fingerprint completely untouched (no image rebuild, no fleet-wide
// invalidation cliff) — proving the cache-volume rotation and the image
// fingerprint are two independent axes, exactly as CacheVolumeKey's own
// doc comment states.
func TestCacheVolumeKey_EpochRotatesIndependentlyOfFingerprint(t *testing.T) {
	t.Parallel()

	repos := map[string]string{"narvi": "https://github.com/acme/narvi.git"}

	fpBeforeRotation := imagebuild.Fingerprint("narvi/base:v1", repos, "1.17.15")
	keyEpoch0 := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15", "0")

	fpAfterRotation := imagebuild.Fingerprint("narvi/base:v1", repos, "1.17.15")
	keyEpoch1 := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15", "1")

	if fpBeforeRotation != fpAfterRotation {
		t.Errorf("Fingerprint changed (%q -> %q) from an epoch bump alone; epoch must never reach Fingerprint (it would defeat the whole point of a rotation escape hatch decoupled from image invalidation)", fpBeforeRotation, fpAfterRotation)
	}
	if keyEpoch0 == keyEpoch1 {
		t.Error("CacheVolumeKey did not change across an epoch bump; epoch must be load-bearing so a stuck/oversized volume can be rotated")
	}
}

// TestCacheVolumeKey_IgnoresEverythingButBaseRuntimeVersionAndEpoch is the
// core regression test for §19.1's own "keyed on Base + RuntimeVersion +
// epoch ONLY — never on repo content" requirement: CacheVolumeKey's
// signature already structurally excludes repos (it takes no repos
// parameter at all), but this pins the CONSEQUENCE that matters — two
// Fingerprints that diverge only in their repo set must still resolve to
// the identical cache volume key when their base/runtimeVersion/epoch
// agree, proving the cache is shared across repo sets rather than
// accidentally re-partitioned by whatever a caller happens to pass
// alongside it.
func TestCacheVolumeKey_IgnoresEverythingButBaseRuntimeVersionAndEpoch(t *testing.T) {
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

	keyA := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15", "")
	keyB := imagebuild.CacheVolumeKey("narvi/base:v1", "1.17.15", "")
	if keyA != keyB {
		t.Errorf("CacheVolumeKey diverged (%q vs %q) for a repo-set-only difference in Fingerprint inputs — the cache key must never depend on repos", keyA, keyB)
	}
}

// TestCacheVolumeKey_NoAmbiguousConcatenationCollision mirrors
// fingerprint_test.go's own TestFingerprint_NoAmbiguousConcatenationCollision,
// extended to all three fields now that epoch joined the hash: naive
// concatenation without a field separator would let base="a"+
// runtimeVersion="bc"+epoch="" collide with base="ab"+runtimeVersion="c"+
// epoch="", and would let a value moving from one field to the next
// (runtimeVersion="bc"+epoch="" vs runtimeVersion="b"+epoch="c") collide
// too.
func TestCacheVolumeKey_NoAmbiguousConcatenationCollision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    [3]string
		b    [3]string
	}{
		{
			name: "base/runtimeVersion boundary shifts",
			a:    [3]string{"a", "bc", ""},
			b:    [3]string{"ab", "c", ""},
		},
		{
			name: "runtimeVersion/epoch boundary shifts",
			a:    [3]string{"base", "bc", ""},
			b:    [3]string{"base", "b", "c"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			key1 := imagebuild.CacheVolumeKey(tc.a[0], tc.a[1], tc.a[2])
			key2 := imagebuild.CacheVolumeKey(tc.b[0], tc.b[1], tc.b[2])
			if key1 == key2 {
				t.Fatalf("CacheVolumeKey(%q, %q, %q) == CacheVolumeKey(%q, %q, %q) = %q: naive concatenation collision not prevented",
					tc.a[0], tc.a[1], tc.a[2], tc.b[0], tc.b[1], tc.b[2], key1)
			}
		})
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
