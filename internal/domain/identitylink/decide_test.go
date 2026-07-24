package identitylink_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/identitylink"
)

// TestDecide is exhaustive over the shapes §13.2 step 2/3/4 names: zero
// matches, exactly one, and more than one -- proving the "never guess"
// rule holds for every non-unique case, not just the empty one.
func TestDecide(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		matchedUserIDs []string
		wantUserID     string
		wantOK         bool
	}{
		{"zero matches", nil, "", false},
		{"zero matches, empty slice", []string{}, "", false},
		{"exactly one match", []string{"user-1"}, "user-1", true},
		{"two matches", []string{"user-1", "user-2"}, "", false},
		{"three matches", []string{"user-1", "user-2", "user-3"}, "", false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotUserID, gotOK := identitylink.Decide(tt.matchedUserIDs)
			if gotOK != tt.wantOK {
				t.Errorf("Decide(%v) ok = %v, want %v", tt.matchedUserIDs, gotOK, tt.wantOK)
			}
			if gotUserID != tt.wantUserID {
				t.Errorf("Decide(%v) userID = %q, want %q", tt.matchedUserIDs, gotUserID, tt.wantUserID)
			}
		})
	}
}
