package imagebuild_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/imagebuild"
)

// TestFingerprint_DeterministicRegardlessOfMapIterationOrder proves the
// same (base, repoSHAs, runtimeVersion) always produces the identical
// fingerprint, no matter how many times it's recomputed -- Go's own map
// iteration order is randomized per-range, so calling Fingerprint many
// times against the SAME map is itself a real (if probabilistic) exercise
// of every likely internal iteration order, not just a single lucky run.
func TestFingerprint_DeterministicRegardlessOfMapIterationOrder(t *testing.T) {
	t.Parallel()

	repoSHAs := map[string]string{
		"frontend": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"backend":  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"infra":    "cccccccccccccccccccccccccccccccccccccccc",
	}

	first := imagebuild.Fingerprint("narvi/base:v1", repoSHAs, "1.17.15")
	if first == "" {
		t.Fatal("Fingerprint returned empty string")
	}

	const iterations = 200
	for i := 0; i < iterations; i++ {
		got := imagebuild.Fingerprint("narvi/base:v1", repoSHAs, "1.17.15")
		if got != first {
			t.Fatalf("iteration %d: Fingerprint = %q, want %q (same inputs must always fingerprint identically)", i, got, first)
		}
	}
}

// TestFingerprint_DeterministicAcrossEquivalentButFreshMaps proves the
// determinism holds across genuinely SEPARATE map values built by
// inserting the SAME keys in different orders, not just repeated calls
// against one already-constructed map (which could theoretically share
// some incidental internal layout).
func TestFingerprint_DeterministicAcrossEquivalentButFreshMaps(t *testing.T) {
	t.Parallel()

	m1 := map[string]string{}
	m1["frontend"] = "sha-a"
	m1["backend"] = "sha-b"
	m1["infra"] = "sha-c"

	m2 := map[string]string{}
	m2["infra"] = "sha-c"
	m2["frontend"] = "sha-a"
	m2["backend"] = "sha-b"

	m3 := map[string]string{}
	m3["backend"] = "sha-b"
	m3["infra"] = "sha-c"
	m3["frontend"] = "sha-a"

	fp1 := imagebuild.Fingerprint("base", m1, "1.0.0")
	fp2 := imagebuild.Fingerprint("base", m2, "1.0.0")
	fp3 := imagebuild.Fingerprint("base", m3, "1.0.0")

	if fp1 != fp2 || fp2 != fp3 {
		t.Fatalf("Fingerprint differs across equivalent maps built in different insertion orders: %q, %q, %q", fp1, fp2, fp3)
	}
}

// TestFingerprint_DifferentInputsProduceDifferentFingerprints proves each
// of the three fingerprinted inputs is actually load-bearing: changing
// ANY ONE of base/repoSHAs/runtimeVersion, holding the others fixed,
// changes the result.
func TestFingerprint_DifferentInputsProduceDifferentFingerprints(t *testing.T) {
	t.Parallel()

	baseRepoSHAs := map[string]string{"app": "sha-1"}
	baseline := imagebuild.Fingerprint("narvi/base:v1", baseRepoSHAs, "1.17.15")

	tests := []struct {
		name           string
		base           string
		repoSHAs       map[string]string
		runtimeVersion string
	}{
		{
			name:           "different base",
			base:           "narvi/base:v2",
			repoSHAs:       baseRepoSHAs,
			runtimeVersion: "1.17.15",
		},
		{
			name:           "different repo sha (same repo)",
			base:           "narvi/base:v1",
			repoSHAs:       map[string]string{"app": "sha-2"},
			runtimeVersion: "1.17.15",
		},
		{
			name:           "added repo",
			base:           "narvi/base:v1",
			repoSHAs:       map[string]string{"app": "sha-1", "extra": "sha-x"},
			runtimeVersion: "1.17.15",
		},
		{
			name:           "removed repo (empty map)",
			base:           "narvi/base:v1",
			repoSHAs:       map[string]string{},
			runtimeVersion: "1.17.15",
		},
		{
			name:           "different runtime version",
			base:           "narvi/base:v1",
			repoSHAs:       baseRepoSHAs,
			runtimeVersion: "1.18.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := imagebuild.Fingerprint(tc.base, tc.repoSHAs, tc.runtimeVersion)
			if got == baseline {
				t.Errorf("Fingerprint(%q, %v, %q) = %q, want different from baseline %q",
					tc.base, tc.repoSHAs, tc.runtimeVersion, got, baseline)
			}
		})
	}
}

// TestFingerprint_NoAmbiguousConcatenationCollision proves two structurally
// different inputs that WOULD collide under naive, separator-less string
// concatenation produce distinct fingerprints -- e.g. base="a"+runtime="bc"
// vs base="ab"+runtime="c" naively concatenate to the identical "abc".
func TestFingerprint_NoAmbiguousConcatenationCollision(t *testing.T) {
	t.Parallel()

	fp1 := imagebuild.Fingerprint("a", map[string]string{}, "bc")
	fp2 := imagebuild.Fingerprint("ab", map[string]string{}, "c")

	if fp1 == fp2 {
		t.Fatalf("Fingerprint(%q, {}, %q) == Fingerprint(%q, {}, %q) = %q: naive concatenation collision not prevented",
			"a", "bc", "ab", "c", fp1)
	}
}

// TestFingerprint_EmptyRepoSHAsIsValid proves a session with no repos
// (an empty/nil map) still produces a stable, non-empty fingerprint rather
// than panicking or behaving specially.
func TestFingerprint_EmptyRepoSHAsIsValid(t *testing.T) {
	t.Parallel()

	fromNil := imagebuild.Fingerprint("base", nil, "1.0.0")
	fromEmpty := imagebuild.Fingerprint("base", map[string]string{}, "1.0.0")

	if fromNil == "" {
		t.Fatal("Fingerprint with nil repoSHAs returned empty string")
	}
	if fromNil != fromEmpty {
		t.Errorf("Fingerprint(nil) = %q, Fingerprint({}) = %q, want equal", fromNil, fromEmpty)
	}
}
