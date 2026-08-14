package reviewverdict_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewverdict"
)

func TestFilesChangedDrifted(t *testing.T) {
	tests := []struct {
		name           string
		selfReported   int
		serverComputed int
		want           bool
	}{
		{
			name:           "identical counts never drift",
			selfReported:   10,
			serverComputed: 10,
			want:           false,
		},
		{
			name:           "small absolute difference on a small PR: ratio high but absolute below threshold",
			selfReported:   1,
			serverComputed: 2,
			want:           false,
		},
		{
			name:           "small relative difference on a large PR: absolute above threshold but ratio below it",
			selfReported:   495,
			serverComputed: 500,
			want:           false,
		},
		{
			name:           "both thresholds cleared: genuine drift",
			selfReported:   1,
			serverComputed: 20,
			want:           true,
		},
		{
			name:           "self-reported zero against a real positive server count still fires",
			selfReported:   0,
			serverComputed: 20,
			want:           true,
		},
		{
			name:           "server computed zero never fires, regardless of self-report -- indistinguishable from a failed GetPullRequest fetch",
			selfReported:   500,
			serverComputed: 0,
			want:           false,
		},
		{
			name:           "server computed negative (should be unreachable, defensively handled identically to zero) never fires",
			selfReported:   500,
			serverComputed: -1,
			want:           false,
		},
		{
			name:           "exactly at both thresholds fires (>= on both sides, not strictly >)",
			selfReported:   5,
			serverComputed: 10,
			want:           true,
		},
		{
			name:           "absolute delta above threshold but ratio below it does not fire (large PR)",
			selfReported:   94,
			serverComputed: 100,
			want:           false,
		},
		{
			name:           "just under the absolute threshold does not fire even with a large ratio",
			selfReported:   0,
			serverComputed: 4,
			want:           false,
		},
		{
			name:           "self-reported drift in the other direction (over-report) fires identically",
			selfReported:   40,
			serverComputed: 2,
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewverdict.FilesChangedDrifted(tt.selfReported, tt.serverComputed)
			if got != tt.want {
				t.Errorf("FilesChangedDrifted(%d, %d) = %v, want %v", tt.selfReported, tt.serverComputed, got, tt.want)
			}
		})
	}
}

// TestFilesChangedDrifted_ThresholdsAreNamedConstants pins down the two
// threshold VALUES this Step proposes (0.5 / 5) so a future tuning pass
// changes them deliberately, with this test as a visible signal, rather
// than silently drifting the canary's own sensitivity via an unrelated
// edit.
func TestFilesChangedDrifted_ThresholdsAreNamedConstants(t *testing.T) {
	if reviewverdict.FilesChangedDriftRatioThreshold != 0.5 {
		t.Errorf("FilesChangedDriftRatioThreshold = %v, want 0.5", reviewverdict.FilesChangedDriftRatioThreshold)
	}
	if reviewverdict.FilesChangedDriftAbsoluteThreshold != 5 {
		t.Errorf("FilesChangedDriftAbsoluteThreshold = %v, want 5", reviewverdict.FilesChangedDriftAbsoluteThreshold)
	}
}
