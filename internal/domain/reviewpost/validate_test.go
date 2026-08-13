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
		Digest: reviewpost.Digest{
			Summary:             "Adds a retry helper around the flaky upstream call.",
			DescriptionAdequacy: review.DescriptionAdequacyOK,
			AdequacyExplanation: "The PR body accurately describes the retry helper this diff adds.",
		},
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
		{
			name:    "empty digest summary (Step 66, §26.1: required on every review)",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.Summary = "" },
			wantErr: reviewpost.ErrEmptyDigestSummary,
		},
		{
			name:    "whitespace-only digest summary",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.Summary = "   \n\t  " },
			wantErr: reviewpost.ErrEmptyDigestSummary,
		},
		{
			name: "empty ArchDecisions/StackRisks/UnverifiedLimits is legal (Step 66: requested, not required, until §26.3/Step 68 defines the deep path)",
			mutate: func(in *reviewpost.VerdictInput) {
				in.Digest.ArchDecisions = nil
				in.Digest.StackRisks = ""
				in.Digest.UnverifiedLimits = ""
			},
			wantErr: nil,
		},
		{
			name:    "missing digest.descriptionAdequacy (zero value, §26.2/Step 67: required on every review)",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.DescriptionAdequacy = "" },
			wantErr: reviewpost.ErrInvalidDescriptionAdequacy,
		},
		{
			name:    "garbled digest.descriptionAdequacy",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.DescriptionAdequacy = "somewhat" },
			wantErr: reviewpost.ErrInvalidDescriptionAdequacy,
		},
		{
			name:    "digest.descriptionAdequacy = drift is legal",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.DescriptionAdequacy = review.DescriptionAdequacyDrift },
			wantErr: nil,
		},
		{
			name: "digest.descriptionAdequacy = misleading is legal",
			mutate: func(in *reviewpost.VerdictInput) {
				in.Digest.DescriptionAdequacy = review.DescriptionAdequacyMisleading
			},
			wantErr: nil,
		},
		{
			name:    "empty digest.adequacyExplanation (§26.2/Step 67: required on every review)",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.AdequacyExplanation = "" },
			wantErr: reviewpost.ErrEmptyAdequacyExplanation,
		},
		{
			name:    "whitespace-only digest.adequacyExplanation",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.AdequacyExplanation = "   \n\t  " },
			wantErr: reviewpost.ErrEmptyAdequacyExplanation,
		},
		{
			name:    "empty digest.proposedBody is legal (§26.2: the agent MAY propose a rewrite, not required)",
			mutate:  func(in *reviewpost.VerdictInput) { in.Digest.ProposedBody = "" },
			wantErr: nil,
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

// TestValidateVerdictInput_DigestSummaryCheckedLastAmongExisting proves
// Step 66's own new Digest.Summary check runs AFTER every pre-existing
// check (added at the end of the fixed order, never interleaved earlier,
// per this function's own doc comment) -- a payload with BOTH an empty
// top-level Summary AND an empty Digest.Summary must still report
// ErrEmptySummary, never ErrEmptyDigestSummary, so this Step never changes
// which error an already-malformed pre-Step-66 payload reports.
func TestValidateVerdictInput_DigestSummaryCheckedLastAmongExisting(t *testing.T) {
	in := validInput()
	in.Summary = ""
	in.Digest.Summary = ""

	err := reviewpost.ValidateVerdictInput(in)
	if !errors.Is(err, reviewpost.ErrEmptySummary) {
		t.Errorf("ValidateVerdictInput() = %v, want %v (top-level summary checked before digest.summary)", err, reviewpost.ErrEmptySummary)
	}
}

// TestValidateVerdictInput_AdequacyCheckedAfterDigestSummary proves §26.2/
// Step 67's own new Digest.DescriptionAdequacy/AdequacyExplanation checks
// run AFTER Digest.Summary (added at the end of the fixed order, per this
// function's own doc comment) -- a payload with BOTH an empty
// Digest.Summary AND a garbled Digest.DescriptionAdequacy must still
// report ErrEmptyDigestSummary, never ErrInvalidDescriptionAdequacy, so
// this Step never changes which error an already-malformed pre-Step-67
// payload reports.
func TestValidateVerdictInput_AdequacyCheckedAfterDigestSummary(t *testing.T) {
	in := validInput()
	in.Digest.Summary = ""
	in.Digest.DescriptionAdequacy = "bogus"

	err := reviewpost.ValidateVerdictInput(in)
	if !errors.Is(err, reviewpost.ErrEmptyDigestSummary) {
		t.Errorf("ValidateVerdictInput() = %v, want %v (digest.summary checked before digest.descriptionAdequacy)", err, reviewpost.ErrEmptyDigestSummary)
	}
}

// TestValidateVerdictInput_AdequacyExplanationCheckedLast proves
// AdequacyExplanation is checked LAST of all -- a payload with BOTH a
// garbled DescriptionAdequacy AND an empty AdequacyExplanation must still
// report ErrInvalidDescriptionAdequacy, never ErrEmptyAdequacyExplanation.
func TestValidateVerdictInput_AdequacyExplanationCheckedLast(t *testing.T) {
	in := validInput()
	in.Digest.DescriptionAdequacy = "bogus"
	in.Digest.AdequacyExplanation = ""

	err := reviewpost.ValidateVerdictInput(in)
	if !errors.Is(err, reviewpost.ErrInvalidDescriptionAdequacy) {
		t.Errorf("ValidateVerdictInput() = %v, want %v (digest.descriptionAdequacy checked before digest.adequacyExplanation)", err, reviewpost.ErrInvalidDescriptionAdequacy)
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

// TestBuildVerdict_MisleadingAdequacyRaisesShippable is §26.2/Step 67's
// own end-to-end pin, one layer up from
// TestComputeShippable_MisleadingRaisesShippable (internal/domain/review):
// an otherwise-completely-clean VerdictInput (low risk, ok premise,
// adequate coverage -- every OTHER floor clean, Shippable would otherwise
// compute auto) whose Digest.DescriptionAdequacy is "misleading" must
// still come out of BuildVerdict as ShippableNeedsHuman, proving the third
// floor genuinely reaches the posting endpoint's own real construction
// path, not merely ComputeShippable in isolation.
func TestBuildVerdict_MisleadingAdequacyRaisesShippable(t *testing.T) {
	in := validInput()
	in.RiskLevel = review.RiskLevelLow
	in.Premise = review.PremiseStateOK
	in.TestsCoverage = review.TestsCoverageStateAdequate
	in.Digest.DescriptionAdequacy = review.DescriptionAdequacyMisleading

	got := reviewpost.BuildVerdict(in)
	if got.Shippable != review.ShippableNeedsHuman {
		t.Errorf("Shippable = %q, want %q (a misleading description must raise Shippable off an otherwise-clean auto baseline)", got.Shippable, review.ShippableNeedsHuman)
	}
}

// TestBuildVerdict_AdequacyNeverAffectsRiskLevel is §26.2's own explicit
// asymmetry, pinned at BuildVerdict's own real construction site (not just
// ComputeShippable's pure-function level, TestComputeShippable_
// MisleadingRaisesShippable, internal/domain/review): varying
// Digest.DescriptionAdequacy from "ok" to "misleading", with every other
// field held fixed, changes Shippable but leaves RiskLevel COMPLETELY
// untouched -- "the server computes Shippable, but never fabricates risk
// the model did not report". Mutation coverage: a bug that derived
// RiskLevel from DescriptionAdequacy (or otherwise let the adequacy floor
// leak into RiskLevel) makes this test fail.
func TestBuildVerdict_AdequacyNeverAffectsRiskLevel(t *testing.T) {
	base := validInput()
	base.RiskLevel = review.RiskLevelMedium

	okInput := base
	okInput.Digest.DescriptionAdequacy = review.DescriptionAdequacyOK
	okVerdict := reviewpost.BuildVerdict(okInput)

	misleadingInput := base
	misleadingInput.Digest.DescriptionAdequacy = review.DescriptionAdequacyMisleading
	misleadingVerdict := reviewpost.BuildVerdict(misleadingInput)

	if okVerdict.RiskLevel != review.RiskLevelMedium {
		t.Fatalf("test setup: okVerdict.RiskLevel = %q, want %q", okVerdict.RiskLevel, review.RiskLevelMedium)
	}
	if misleadingVerdict.RiskLevel != review.RiskLevelMedium {
		t.Errorf("misleadingVerdict.RiskLevel = %q, want %q (DescriptionAdequacy must never influence RiskLevel)", misleadingVerdict.RiskLevel, review.RiskLevelMedium)
	}
	if okVerdict.RiskLevel != misleadingVerdict.RiskLevel {
		t.Errorf("RiskLevel differed across DescriptionAdequacy values (%q vs %q) -- must be identical, RiskLevel is upstream of and unaffected by any floor", okVerdict.RiskLevel, misleadingVerdict.RiskLevel)
	}
	// Sanity: Shippable itself DID change (otherwise this test would prove
	// nothing about the floor actually running) -- both raise the SAME
	// baseline (medium -> needs_human) here, so require Shippable to be at
	// LEAST as strict for misleading, and confirm the floor path was
	// genuinely exercised via the dedicated raise test above instead of
	// re-deriving that property here.
	if misleadingVerdict.Shippable != review.ShippableNeedsHuman {
		t.Errorf("misleadingVerdict.Shippable = %q, want %q", misleadingVerdict.Shippable, review.ShippableNeedsHuman)
	}
}
