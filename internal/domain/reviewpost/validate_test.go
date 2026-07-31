package reviewpost_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// validInput returns a VerdictInput that passes every check --
// individual test cases mutate a copy of this to isolate exactly one bad
// field at a time.
func validInput() reviewpost.VerdictInput {
	return reviewpost.VerdictInput{
		RiskLevel:         review.RiskLevelLow,
		Premise:           review.PremiseStateOK,
		BlastRadius:       []review.Tag{review.TagAuth},
		FilesChanged:      3,
		TestsCoverage:     review.TestsCoverageStateAdequate,
		DocsDrift:         review.DocsDriftStateNone,
		ProposedShippable: review.ProposedShippableAuto,
		Summary:           "Looks good, minor nit.",
	}
}

func TestValidateVerdictInput(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(in *reviewpost.VerdictInput)
		wantErr error
	}{
		{
			name:    "valid input",
			mutate:  func(_ *reviewpost.VerdictInput) {},
			wantErr: nil,
		},
		{
			name:    "missing riskLevel (zero value)",
			mutate:  func(in *reviewpost.VerdictInput) { in.RiskLevel = "" },
			wantErr: reviewpost.ErrInvalidRiskLevel,
		},
		{
			name:    "garbled riskLevel",
			mutate:  func(in *reviewpost.VerdictInput) { in.RiskLevel = "extreme" },
			wantErr: reviewpost.ErrInvalidRiskLevel,
		},
		{
			name:    "missing premise (zero value)",
			mutate:  func(in *reviewpost.VerdictInput) { in.Premise = "" },
			wantErr: reviewpost.ErrInvalidPremise,
		},
		{
			name:    "garbled premise",
			mutate:  func(in *reviewpost.VerdictInput) { in.Premise = "maybe" },
			wantErr: reviewpost.ErrInvalidPremise,
		},
		{
			name:    "missing testsCoverage (zero value)",
			mutate:  func(in *reviewpost.VerdictInput) { in.TestsCoverage = "" },
			wantErr: reviewpost.ErrInvalidTestsCoverage,
		},
		{
			name:    "missing docsDrift (zero value)",
			mutate:  func(in *reviewpost.VerdictInput) { in.DocsDrift = "" },
			wantErr: reviewpost.ErrInvalidDocsDrift,
		},
		{
			name:    "missing proposedShippable (zero value)",
			mutate:  func(in *reviewpost.VerdictInput) { in.ProposedShippable = "" },
			wantErr: reviewpost.ErrInvalidProposedShippable,
		},
		{
			name:    "garbled proposedShippable",
			mutate:  func(in *reviewpost.VerdictInput) { in.ProposedShippable = "definitely" },
			wantErr: reviewpost.ErrInvalidProposedShippable,
		},
		{
			name:    "unrecognized blastRadius tag",
			mutate:  func(in *reviewpost.VerdictInput) { in.BlastRadius = []review.Tag{"frontend"} },
			wantErr: reviewpost.ErrInvalidBlastRadiusTag,
		},
		{
			name:    "one valid tag among many, one bad -- still rejected",
			mutate:  func(in *reviewpost.VerdictInput) { in.BlastRadius = []review.Tag{review.TagAuth, "bogus"} },
			wantErr: reviewpost.ErrInvalidBlastRadiusTag,
		},
		{
			name:    "empty blastRadius is legal (reviewer found no tagged area)",
			mutate:  func(in *reviewpost.VerdictInput) { in.BlastRadius = nil },
			wantErr: nil,
		},
		{
			name:    "negative filesChanged",
			mutate:  func(in *reviewpost.VerdictInput) { in.FilesChanged = -1 },
			wantErr: reviewpost.ErrNegativeFilesChanged,
		},
		{
			name:    "zero filesChanged is legal",
			mutate:  func(in *reviewpost.VerdictInput) { in.FilesChanged = 0 },
			wantErr: nil,
		},
		{
			name:    "empty summary",
			mutate:  func(in *reviewpost.VerdictInput) { in.Summary = "" },
			wantErr: reviewpost.ErrEmptySummary,
		},
		{
			name:    "whitespace-only summary",
			mutate:  func(in *reviewpost.VerdictInput) { in.Summary = "   \n\t  " },
			wantErr: reviewpost.ErrEmptySummary,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
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

// TestValidateVerdictInput_FieldOrder proves the checks run in the
// documented fixed order: a payload with BOTH an invalid riskLevel AND an
// invalid premise always reports the riskLevel error first, never the
// premise one -- a caller building a 400 message must get a deterministic
// answer, not one that depends on struct field order or map iteration.
func TestValidateVerdictInput_FieldOrder(t *testing.T) {
	in := validInput()
	in.RiskLevel = "bogus"
	in.Premise = "bogus"

	err := reviewpost.ValidateVerdictInput(in)
	if !errors.Is(err, reviewpost.ErrInvalidRiskLevel) {
		t.Errorf("ValidateVerdictInput() = %v, want %v (riskLevel checked first)", err, reviewpost.ErrInvalidRiskLevel)
	}
}

// TestBuildVerdict_ShippableAlwaysComputedNeverCopiedFromProposed proves
// BuildVerdict's own central contract: Shippable is ALWAYS
// review.ComputeShippable's own result, regardless of what
// ProposedShippable says -- a caller "proposing" auto must never leak
// into an authoritative auto when the real floors say otherwise.
func TestBuildVerdict_ShippableAlwaysComputedNeverCopiedFromProposed(t *testing.T) {
	in := validInput()
	in.RiskLevel = review.RiskLevelLow
	in.Premise = review.PremiseStateNotAPR // forces the premise floor to Block.
	in.TestsCoverage = review.TestsCoverageStateAdequate
	in.ProposedShippable = review.ProposedShippableAuto // the model insists "auto" -- must be ignored.

	got := reviewpost.BuildVerdict(in)
	if got.Shippable != review.ShippableBlock {
		t.Errorf("Shippable = %q, want %q (ProposedShippableAuto must never override the computed floor)", got.Shippable, review.ShippableBlock)
	}
	if got.ProposedShippable != review.ProposedShippableAuto {
		t.Errorf("ProposedShippable = %q, want %q (still carried verbatim as audit data)", got.ProposedShippable, review.ProposedShippableAuto)
	}
}
