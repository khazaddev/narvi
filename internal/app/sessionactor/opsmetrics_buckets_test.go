package sessionactor

import (
	"testing"
)

// TestRelocatedHistogramBucketsAreUnchanged pins the exact explicit bucket
// boundaries of the four histograms that moved here from sandbox-agent.
//
// §33.3 makes their preservation load-bearing, not incidental: the whole
// design is "ship the fact, record centrally", whose stated guarantee is that
// each metric keeps its name AND its hand-tuned buckets so SLO 1's semantics
// and BootDurationP95High's p95 survive the move unchanged.
//
// The move deleted that guarantee's only guard. The sandbox-side suite had a
// bucket-shape test (boot/telemetry_test.go's
// TestRunHooks_HookRerunDurationHistogram_UsesFineGrainedSubFiveSecondBuckets)
// and it went with the file. Worse, opsmetrics.go's own comment named
// internal/ops's TestNoMetricDrift as the CI guard that would catch "a
// bucket-shape change here going unnoticed" -- it would not. That check
// compares metric NAME strings; ScanRegisteredInstruments records only
// {Name, File, Line} and reads the registration call's first argument. It has
// no notion of bucket boundaries. The rename half of that claim was true and
// the bucket half was not, so the comment was corrected alongside this test.
//
// Without this, deleting a WithExplicitBucketBoundaries option or truncating a
// slice leaves every test green: TestNoMetricDrift still finds the name, and
// the boottiming tests assert only Count and attributes. The histogram would
// silently fall back to the SDK's default millisecond-oriented boundaries,
// collapsing every sub-5s warm hook rerun into one bucket and degrading the
// exact p95 SLO 1's alert reads.
//
// The values are byte-for-byte what the deleted files carried, verified by
// reading them out of the pre-move commit rather than retyped from memory.
func TestRelocatedHistogramBucketsAreUnchanged(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		got  []float64
		want []float64
	}{
		{
			name: "boot duration",
			got:  bootDurationBuckets,
			want: []float64{1, 2, 3, 5, 7.5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 300, 600, 900},
		},
		{
			name: "hook rerun duration",
			got:  hookRerunDurationBuckets,
			want: []float64{0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4, 5, 7.5, 10, 15, 20, 30, 60, 120, 300, 600},
		},
		{
			name: "git fetch duration",
			got:  gitFetchDurationBuckets,
			want: []float64{0.1, 0.25, 0.5, 1, 2, 3, 5, 7.5, 10, 15, 20, 30, 45, 60, 90, 120},
		},
		{
			name: "git checkout duration",
			got:  gitCheckoutDurationBuckets,
			want: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5, 7.5, 10, 15, 20, 30, 60},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if len(tc.got) != len(tc.want) {
				t.Fatalf("%s buckets: len = %d, want %d (the slice changed shape; these boundaries came from the deleted sandbox-side histogram and §33.3 requires them preserved)",
					tc.name, len(tc.got), len(tc.want))
			}
			for i := range tc.want {
				if tc.got[i] != tc.want[i] {
					t.Errorf("%s buckets[%d] = %v, want %v", tc.name, i, tc.got[i], tc.want[i])
				}
			}

			// A boundary set that is not strictly increasing is not a
			// histogram shape at all -- the SDK's own contract -- and a
			// non-positive boundary is meaningless for a duration.
			for i, b := range tc.got {
				if b <= 0 {
					t.Errorf("%s buckets[%d] = %v, want a positive duration boundary", tc.name, i, b)
				}
				if i > 0 && b <= tc.got[i-1] {
					t.Errorf("%s buckets[%d] = %v is not greater than buckets[%d] = %v; boundaries must strictly increase",
						tc.name, i, b, i-1, tc.got[i-1])
				}
			}
		})
	}
}
