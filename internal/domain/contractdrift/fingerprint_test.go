package contractdrift_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/contractdrift"
)

// TestFingerprint_DeterministicRegardlessOfMapIterationOrder proves the
// same entries map always produces the identical fingerprint, no matter
// how many times it's recomputed -- Go's own map iteration order is
// randomized per-range, so calling Fingerprint many times against the
// SAME map is itself a real (if probabilistic) exercise of every likely
// internal iteration order, not just a single lucky run. Mirrors
// internal/domain/imagebuild.Fingerprint's own identical test precedent.
func TestFingerprint_DeterministicRegardlessOfMapIterationOrder(t *testing.T) {
	t.Parallel()

	entries := map[string]string{
		"openapi.yaml": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"schemas":      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"README.md":    "cccccccccccccccccccccccccccccccccccccccc",
	}

	first := contractdrift.Fingerprint(entries)
	if first == "" {
		t.Fatal("Fingerprint returned empty string")
	}

	const iterations = 200
	for i := 0; i < iterations; i++ {
		got := contractdrift.Fingerprint(entries)
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
	m1["openapi.yaml"] = "sha-a"
	m1["schemas"] = "sha-b"
	m1["README.md"] = "sha-c"

	m2 := map[string]string{}
	m2["README.md"] = "sha-c"
	m2["openapi.yaml"] = "sha-a"
	m2["schemas"] = "sha-b"

	m3 := map[string]string{}
	m3["schemas"] = "sha-b"
	m3["README.md"] = "sha-c"
	m3["openapi.yaml"] = "sha-a"

	fp1 := contractdrift.Fingerprint(m1)
	fp2 := contractdrift.Fingerprint(m2)
	fp3 := contractdrift.Fingerprint(m3)

	if fp1 != fp2 || fp2 != fp3 {
		t.Fatalf("Fingerprint differs across equivalent maps built in different insertion orders: %q, %q, %q", fp1, fp2, fp3)
	}
}

// TestFingerprint_DifferentInputsProduceDifferentFingerprints proves the
// entries map is actually load-bearing: changing a path, a sha, adding an
// entry, or removing one all change the result.
func TestFingerprint_DifferentInputsProduceDifferentFingerprints(t *testing.T) {
	t.Parallel()

	baseline := contractdrift.Fingerprint(map[string]string{"openapi.yaml": "sha-1"})

	tests := []struct {
		name    string
		entries map[string]string
	}{
		{
			name:    "different sha (same path)",
			entries: map[string]string{"openapi.yaml": "sha-2"},
		},
		{
			name:    "added entry",
			entries: map[string]string{"openapi.yaml": "sha-1", "extra.yaml": "sha-x"},
		},
		{
			name:    "removed entry (empty map)",
			entries: map[string]string{},
		},
		{
			name:    "different path (same sha)",
			entries: map[string]string{"other.yaml": "sha-1"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := contractdrift.Fingerprint(tc.entries)
			if got == baseline {
				t.Errorf("Fingerprint(%v) = %q, want different from baseline %q", tc.entries, got, baseline)
			}
		})
	}
}

// TestFingerprint_NoAmbiguousConcatenationCollision proves two structurally
// different single-entry inputs that WOULD collide under naive,
// separator-less string concatenation produce distinct fingerprints --
// e.g. path="a"+sha="bc" vs path="ab"+sha="c" naively concatenate to the
// identical "abc".
func TestFingerprint_NoAmbiguousConcatenationCollision(t *testing.T) {
	t.Parallel()

	fp1 := contractdrift.Fingerprint(map[string]string{"a": "bc"})
	fp2 := contractdrift.Fingerprint(map[string]string{"ab": "c"})

	if fp1 == fp2 {
		t.Fatalf("Fingerprint({\"a\":\"bc\"}) == Fingerprint({\"ab\":\"c\"}) = %q: naive concatenation collision not prevented", fp1)
	}
}

// TestFingerprint_EmptyEntriesIsValid proves an empty (or nil) entries map
// -- "the contracts directory exists at this ref but is empty" -- still
// produces a stable, non-empty fingerprint rather than panicking or
// behaving specially, and that nil/empty are treated identically.
func TestFingerprint_EmptyEntriesIsValid(t *testing.T) {
	t.Parallel()

	fromNil := contractdrift.Fingerprint(nil)
	fromEmpty := contractdrift.Fingerprint(map[string]string{})

	if fromNil == "" {
		t.Fatal("Fingerprint with nil entries returned empty string")
	}
	if fromNil != fromEmpty {
		t.Errorf("Fingerprint(nil) = %q, Fingerprint({}) = %q, want equal", fromNil, fromEmpty)
	}
}

// TestFingerprint_SubdirectoryEntryUsedAsIs proves a subdirectory entry
// (whose sha is ALREADY the recursive git tree hash of everything nested
// under it, per GitHub's Contents API) is hashed exactly like any other
// entry -- Fingerprint itself has no notion of "file" vs. "directory", it
// only ever sees the flat path->sha map a caller already assembled from a
// single, non-recursive listing (see this package's own doc.go and this
// file's own doc comment on Fingerprint for the full reasoning; the
// caller-side non-recursion proof lives in
// internal/adapters/outbound/githubapi's own tests).
func TestFingerprint_SubdirectoryEntryUsedAsIs(t *testing.T) {
	t.Parallel()

	withSubdir := contractdrift.Fingerprint(map[string]string{
		"openapi.yaml": "sha-file",
		"schemas":      "sha-subdir-tree-hash",
	})
	withoutSubdir := contractdrift.Fingerprint(map[string]string{
		"openapi.yaml": "sha-file",
	})

	if withSubdir == withoutSubdir {
		t.Error("Fingerprint did not change when a subdirectory entry was added; want the subdirectory's own tree-hash sha to be load-bearing")
	}

	// Changing ONLY the subdirectory's own sha (simulating something
	// changing deep inside it, which GitHub reflects as a new tree hash on
	// the subdirectory entry itself) must also change the result --
	// proving Fingerprint never needs to look INSIDE the subdirectory to
	// notice that.
	withSubdirChanged := contractdrift.Fingerprint(map[string]string{
		"openapi.yaml": "sha-file",
		"schemas":      "sha-subdir-tree-hash-CHANGED",
	})
	if withSubdirChanged == withSubdir {
		t.Error("Fingerprint did not change when a subdirectory entry's own sha changed")
	}
}
