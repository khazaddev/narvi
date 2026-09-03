package review_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/review"
)

// TestDocsDriftState_Values pins DocsDriftState's three legal values down
// to their exact underlying strings, and confirms the zero value is not
// among them — the same "detectably unrecognized, not silently the best
// or worst case" property every closed enum in this package relies on
// (doc.go's uniform fail-conservative policy), even though DocsDriftState
// is not wired to a floor in this Step (doc.go design call #5).
func TestDocsDriftState_Values(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state review.DocsDriftState
		want  string
	}{
		{"none", review.DocsDriftStateNone, "none"},
		{"found", review.DocsDriftStateFound, "found"},
		{"skipped", review.DocsDriftStateSkipped, "skipped"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if string(tc.state) != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.state, tc.want)
			}
		})
	}

	var zero review.DocsDriftState
	if zero == review.DocsDriftStateNone || zero == review.DocsDriftStateFound || zero == review.DocsDriftStateSkipped {
		t.Error("DocsDriftState's zero value must not equal any of its three named legal values")
	}
}
