package reviewpost_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
)

// deepValidInput mirrors validInput() but additionally fills in the three
// Step 68 deep-path-only digest fields and marks ReviewDepth deep -- a
// caller on the deep path must pass ALL of these to validate.
func deepValidInput() reviewpost.VerdictInput {
	in := validInput()
	in.ReviewDepth = reviewtriage.DepthDeep
	in.Digest.ArchDecisions = []reviewpost.ArchDecision{
		{Decision: "Introduced a new triage package", RejectedAlternative: "Folding it into review", ConventionConformance: "Matches the one-pure-function-per-concern pattern"},
	}
	in.Digest.StackRisks = "None beyond the new migration."
	in.Digest.UnverifiedLimits = "Did not test against a live GitHub org with real stacked PRs."
	return in
}

func TestValidateVerdictInput_DeepPath(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(in *reviewpost.VerdictInput)
		wantErr error
	}{
		{
			name:    "fully populated deep-path digest is valid",
			mutate:  func(_ *reviewpost.VerdictInput) {},
			wantErr: nil,
		},
		{
			name:    "deep path with empty ArchDecisions is rejected",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.ArchDecisions = nil },
			wantErr: reviewpost.ErrEmptyDigestArchDecisions,
		},
		{
			name:    "deep path with empty StackRisks is rejected",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.StackRisks = "" },
			wantErr: reviewpost.ErrEmptyDigestStackRisks,
		},
		{
			name:    "deep path with whitespace-only StackRisks is rejected",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.StackRisks = "   " },
			wantErr: reviewpost.ErrEmptyDigestStackRisks,
		},
		{
			name:    "deep path with empty UnverifiedLimits is rejected",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.UnverifiedLimits = "" },
			wantErr: reviewpost.ErrEmptyDigestUnverifiedLimits,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := deepValidInput()
			tc.mutate(&in)

			err := reviewpost.ValidateVerdictInput(in)
			if tc.wantErr == nil {
				if err != nil {
					t.Errorf("ValidateVerdictInput() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("ValidateVerdictInput() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestValidateVerdictInput_LightPathNeverRequiresFullDigest pins the
// light-path invariant explicitly (§26.1: "the light path requests the
// full digest but does not hard-require it") -- an empty ArchDecisions/
// StackRisks/UnverifiedLimits on a light-path (or ReviewDepth=="",
// pre-Step-68) verdict must still validate cleanly. Mutating
// ValidateVerdictInput's own `in.ReviewDepth == reviewtriage.DepthDeep`
// guard into an unconditional check must fail this test.
func TestValidateVerdictInput_LightPathNeverRequiresFullDigest(t *testing.T) {
	in := validInput()
	in.ReviewDepth = reviewtriage.DepthLight
	// Digest.ArchDecisions/StackRisks/UnverifiedLimits are already empty
	// on validInput()'s own Digest -- left untouched here deliberately.

	if err := reviewpost.ValidateVerdictInput(in); err != nil {
		t.Fatalf("ValidateVerdictInput() on the light path with an empty ArchDecisions/StackRisks/UnverifiedLimits = %v, want nil", err)
	}
}

// TestValidateVerdictInput_UnresolvedDepthNeverRequiresFullDigest covers
// the ReviewDepth == "" case explicitly (a pre-Step-68 caller, or a turn
// whose depth could not be resolved) -- must degrade like the light path,
// never like the deep path.
func TestValidateVerdictInput_UnresolvedDepthNeverRequiresFullDigest(t *testing.T) {
	in := validInput()
	in.ReviewDepth = ""

	if err := reviewpost.ValidateVerdictInput(in); err != nil {
		t.Fatalf("ValidateVerdictInput() with an unresolved ReviewDepth = %v, want nil", err)
	}
}

// TestValidateVerdictInput_DeepDigestChecksRunLast proves the three new
// deep-path checks run AFTER every pre-existing check (added at the end
// of the fixed order, per ValidateVerdictInput's own doc comment) -- a
// deep-path payload with BOTH an empty top-level Summary AND an empty
// deep-path digest must still report ErrEmptySummary first.
func TestValidateVerdictInput_DeepDigestChecksRunLast(t *testing.T) {
	in := deepValidInput()
	in.Summary = ""
	in.Digest.ArchDecisions = nil

	err := reviewpost.ValidateVerdictInput(in)
	if !errors.Is(err, reviewpost.ErrEmptySummary) {
		t.Errorf("ValidateVerdictInput() = %v, want %v (pre-existing checks run first)", err, reviewpost.ErrEmptySummary)
	}
}
