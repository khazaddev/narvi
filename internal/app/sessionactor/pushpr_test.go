package sessionactor

import (
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/internal/domain/turn"
)

// TestExecutionOutcomeTrigger is table-driven over every
// sandboxws.ExecutionCompleteOutcome value its own generated
// UnmarshalJSON accepts, plus one unrecognized value, proving the exact
// (outcome -> trigger) mapping completeProcessingTurn relies on.
func TestExecutionOutcomeTrigger(t *testing.T) {
	tests := []struct {
		name    string
		outcome sandboxws.ExecutionCompleteOutcome
		want    turn.Trigger
		wantOK  bool
	}{
		{"completed", sandboxws.ExecutionCompleteOutcomeCompleted, turn.TriggerComplete, true},
		{"failed", sandboxws.ExecutionCompleteOutcomeFailed, turn.TriggerFail, true},
		{"cancelled", sandboxws.ExecutionCompleteOutcomeCancelled, turn.TriggerCancel, true},
		{"unrecognized", sandboxws.ExecutionCompleteOutcome("bogus"), 0, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := executionOutcomeTrigger(tc.outcome)
			if ok != tc.wantOK {
				t.Fatalf("executionOutcomeTrigger(%q) ok = %v, want %v", tc.outcome, ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("executionOutcomeTrigger(%q) = %v, want %v", tc.outcome, got, tc.want)
			}
		})
	}
}

// TestParseOwnerRepo is table-driven over the shapes this Step's own
// design decision 9 needs parsed correctly (https URL, with/without a
// trailing ".git", with/without a trailing slash), plus malformed inputs
// that must error rather than silently return a garbage owner/repo.
func TestParseOwnerRepo(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"plain https", "https://github.com/khazaddev/narvi", "khazaddev", "narvi", false},
		{"dot-git suffix", "https://github.com/khazaddev/narvi.git", "khazaddev", "narvi", false},
		{"trailing slash", "https://github.com/khazaddev/narvi/", "khazaddev", "narvi", false},
		{"gitlab host (generic parsing)", "https://gitlab.com/some-group/some-repo.git", "some-group", "some-repo", false},
		{"too few path segments", "https://github.com/khazaddev", "", "", true},
		{"too many path segments", "https://github.com/khazaddev/narvi/extra", "", "", true},
		{"empty path", "https://github.com/", "", "", true},
		{"malformed url", "://not a url", "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner, repo, err := parseOwnerRepo(tc.url)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseOwnerRepo(%q) = (%q, %q, nil), want an error", tc.url, owner, repo)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseOwnerRepo(%q) unexpected error: %v", tc.url, err)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo {
				t.Errorf("parseOwnerRepo(%q) = (%q, %q), want (%q, %q)", tc.url, owner, repo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}
