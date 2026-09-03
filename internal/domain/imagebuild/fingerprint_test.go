package imagebuild_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/imagebuild"
)

// TestFingerprint_DeterministicRegardlessOfMapIterationOrder proves the
// same (base, repos, runtimeVersion) always produces the identical
// fingerprint, no matter how many times it's recomputed -- Go's own map
// iteration order is randomized per-range, so calling Fingerprint many
// times against the SAME map is itself a real (if probabilistic) exercise
// of every likely internal iteration order, not just a single lucky run.
func TestFingerprint_DeterministicRegardlessOfMapIterationOrder(t *testing.T) {
	t.Parallel()

	repos := map[string]string{
		"frontend": "https://github.com/acme/frontend.git",
		"backend":  "https://github.com/acme/backend.git",
		"infra":    "https://github.com/acme/infra.git",
	}

	first := imagebuild.Fingerprint("narvi/base:v1", repos, "1.17.15")
	if first == "" {
		t.Fatal("Fingerprint returned empty string")
	}

	const iterations = 200
	for i := 0; i < iterations; i++ {
		got := imagebuild.Fingerprint("narvi/base:v1", repos, "1.17.15")
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
// ANY ONE of base/repos/runtimeVersion, holding the others fixed, changes
// the result.
func TestFingerprint_DifferentInputsProduceDifferentFingerprints(t *testing.T) {
	t.Parallel()

	baseRepos := map[string]string{"app": "https://github.com/acme/app.git"}
	baseline := imagebuild.Fingerprint("narvi/base:v1", baseRepos, "1.17.15")

	tests := []struct {
		name           string
		base           string
		repos          map[string]string
		runtimeVersion string
	}{
		{
			name:           "different base",
			base:           "narvi/base:v2",
			repos:          baseRepos,
			runtimeVersion: "1.17.15",
		},
		{
			name:           "different repo url (same repo, genuinely different remote)",
			base:           "narvi/base:v1",
			repos:          map[string]string{"app": "https://github.com/acme/app-fork.git"},
			runtimeVersion: "1.17.15",
		},
		{
			name:           "added repo",
			base:           "narvi/base:v1",
			repos:          map[string]string{"app": "https://github.com/acme/app.git", "extra": "https://github.com/acme/extra.git"},
			runtimeVersion: "1.17.15",
		},
		{
			name:           "removed repo (empty map)",
			base:           "narvi/base:v1",
			repos:          map[string]string{},
			runtimeVersion: "1.17.15",
		},
		{
			name:           "different runtime version",
			base:           "narvi/base:v1",
			repos:          baseRepos,
			runtimeVersion: "1.18.0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := imagebuild.Fingerprint(tc.base, tc.repos, tc.runtimeVersion)
			if got == baseline {
				t.Errorf("Fingerprint(%q, %v, %q) = %q, want different from baseline %q",
					tc.base, tc.repos, tc.runtimeVersion, got, baseline)
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

// TestFingerprint_EmptyReposIsValid proves a session with no repos (an
// empty/nil map) still produces a stable, non-empty fingerprint rather
// than panicking or behaving specially.
func TestFingerprint_EmptyReposIsValid(t *testing.T) {
	t.Parallel()

	fromNil := imagebuild.Fingerprint("base", nil, "1.0.0")
	fromEmpty := imagebuild.Fingerprint("base", map[string]string{}, "1.0.0")

	if fromNil == "" {
		t.Fatal("Fingerprint with nil repos returned empty string")
	}
	if fromNil != fromEmpty {
		t.Errorf("Fingerprint(nil) = %q, Fingerprint({}) = %q, want equal", fromNil, fromEmpty)
	}
}

// --- §19.1's own URL-normalization requirement: two differently-spelled
// URLs naming the SAME remote must fingerprint identically, never
// silently mint two images for one repo set. ---

// TestFingerprint_EquivalentURLsProduceIdenticalFingerprint proves every
// individual normalization rule NormalizeRepoURL implements (host case,
// trailing slash, trailing ".git" suffix, and combinations of those) by
// pairing a canonical URL against a differently-spelled-but-equivalent one
// and asserting their fingerprints match, holding base/runtimeVersion/repo
// name fixed throughout.
func TestFingerprint_EquivalentURLsProduceIdenticalFingerprint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a    string
		b    string
	}{
		{
			name: "trailing .git suffix",
			a:    "https://github.com/acme/repo1",
			b:    "https://github.com/acme/repo1.git",
		},
		{
			name: "trailing slash",
			a:    "https://github.com/acme/repo1",
			b:    "https://github.com/acme/repo1/",
		},
		{
			name: "host case",
			a:    "https://github.com/acme/repo1",
			b:    "https://GitHub.com/acme/repo1",
		},
		{
			name: "trailing slash AFTER .git suffix",
			a:    "https://github.com/acme/repo1",
			b:    "https://github.com/acme/repo1.git/",
		},
		{
			name: "every variance combined",
			a:    "https://github.com/acme/repo1",
			b:    "https://GitHub.com/acme/repo1.git/",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fpA := imagebuild.Fingerprint("narvi/base:v1", map[string]string{"repo1": tc.a}, "1.17.15")
			fpB := imagebuild.Fingerprint("narvi/base:v1", map[string]string{"repo1": tc.b}, "1.17.15")
			if fpA != fpB {
				t.Errorf("Fingerprint with url %q = %q, Fingerprint with equivalent url %q = %q, want equal",
					tc.a, fpA, tc.b, fpB)
			}
		})
	}
}

// TestFingerprint_GenuinelyDifferentURLProducesDifferentFingerprint proves
// normalization does not go too far: two URLs that merely LOOK similar but
// name different remotes (different path) still fingerprint differently.
func TestFingerprint_GenuinelyDifferentURLProducesDifferentFingerprint(t *testing.T) {
	t.Parallel()

	fp1 := imagebuild.Fingerprint("narvi/base:v1", map[string]string{"repo1": "https://github.com/acme/repo1.git"}, "1.17.15")
	fp2 := imagebuild.Fingerprint("narvi/base:v1", map[string]string{"repo1": "https://github.com/acme/repo2.git"}, "1.17.15")

	if fp1 == fp2 {
		t.Fatal("Fingerprint for two different repo paths collided after normalization")
	}
}

// TestNormalizeRepoURL_Idempotent proves NormalizeRepoURL is stable under
// repeated application -- normalizing an already-normalized URL is a
// no-op, which is what makes it safe to call unconditionally inside
// Fingerprint regardless of whether a caller already normalized upstream
// (e.g. imageresolve.go's own persisted repo_urls column, which stores
// NormalizeRepoURL's own output directly).
func TestNormalizeRepoURL_Idempotent(t *testing.T) {
	t.Parallel()

	raw := "https://GitHub.com/acme/repo1.git/"
	once := imagebuild.NormalizeRepoURL(raw)
	twice := imagebuild.NormalizeRepoURL(once)

	if once != twice {
		t.Errorf("NormalizeRepoURL(%q) = %q, NormalizeRepoURL(that) = %q, want idempotent", raw, once, twice)
	}
	want := "https://github.com/acme/repo1"
	if once != want {
		t.Errorf("NormalizeRepoURL(%q) = %q, want %q", raw, once, want)
	}
}
