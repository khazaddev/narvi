package reviewverdict_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewverdict"
)

func TestContradictionRate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		total     int
		contested int
		wantRate  float64
		wantOK    bool
	}{
		{"zero total is not yet computed, never a real zero rate", 0, 0, 0, false},
		{"zero contested out of a real total is a genuine 0% rate", 10, 0, 0, true},
		{"all contested is a genuine 100% rate", 4, 4, 1, true},
		{"a partial rate divides correctly", 4, 1, 0.25, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotRate, gotOK := reviewverdict.ContradictionRate(tc.total, tc.contested)
			if gotOK != tc.wantOK {
				t.Errorf("ContradictionRate(%d, %d) ok = %v, want %v", tc.total, tc.contested, gotOK, tc.wantOK)
			}
			if gotOK && gotRate != tc.wantRate {
				t.Errorf("ContradictionRate(%d, %d) rate = %v, want %v", tc.total, tc.contested, gotRate, tc.wantRate)
			}
		})
	}
}
