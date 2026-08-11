package autoapproval_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/autoapproval"
	"github.com/khazaddev/narvi/internal/domain/review"
)

// cleanVerdict is a Shippable == auto verdict with a small, non-sensitive
// diff -- the one baseline every table case below starts from, mutating
// EXACTLY one field/input at a time, so a failing case proves that ONE
// criterion and no other (this file's own mutation-testing discipline,
// matching the old internal/domain/decisioninbox/eligibility_test.go
// this package replaces).
func cleanVerdict() review.Verdict {
	return review.Verdict{
		RiskLevel:         review.RiskLevelLow,
		Premise:           review.PremiseStateOK,
		BlastRadius:       []review.Tag{review.TagPublicAPI}, // present, but NOT in the default sensitive list
		FilesChanged:      5,
		TestsCoverage:     review.TestsCoverageStateAdequate,
		DocsDrift:         review.DocsDriftStateNone,
		ProposedShippable: review.ProposedShippableAuto,
		Shippable:         review.ShippableAuto,
	}
}

func cleanInput() autoapproval.EligibilityInput {
	return autoapproval.EligibilityInput{
		Verdict:            cleanVerdict(),
		VerdictHeadSHA:     "abc123",
		CurrentHeadSHA:     "abc123",
		CIGreen:            true,
		HasNeedsHumanLabel: false,
	}
}

func TestComputeEligible(t *testing.T) {
	t.Parallel()

	cfg := autoapproval.DefaultEligibilityConfig()

	tests := []struct {
		name         string
		in           autoapproval.EligibilityInput
		cfg          autoapproval.EligibilityConfig
		wantEligible bool
		wantReason   autoapproval.Reason
	}{
		{
			name:         "a clean, low-risk, fresh, CI-green verdict is eligible",
			in:           cleanInput(),
			cfg:          cfg,
			wantEligible: true,
			wantReason:   autoapproval.ReasonNone,
		},

		// --- criterion 1: the needs-human escape hatch, checked FIRST,
		// regardless of every other field's value ---
		{
			name:         "needs-human label overrides an otherwise-eligible verdict",
			in:           withNeedsHuman(cleanInput(), true),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonNeedsHumanLabel,
		},

		// --- criterion 2: the stale-head-SHA guard ---
		{
			name:         "a verdict head sha that differs from the current head sha is stale",
			in:           withCurrentHeadSHA(cleanInput(), "def456"),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonStaleVerdict,
		},
		{
			name:         "an empty verdict head sha is stale (never treated as a wildcard match)",
			in:           withVerdictHeadSHA(cleanInput(), ""),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonStaleVerdict,
		},

		// --- criterion 3: CI green at the CURRENT head ---
		{
			name:         "CI not green at head is never eligible",
			in:           withCIGreen(cleanInput(), false),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonCINotGreen,
		},

		// --- criterion 4: Shippable == auto, exercised via three
		// DISTINCT, independently-meaningful ways Shippable can fail to
		// be auto (risk baseline, coverage floor, premise floor) -- see
		// doc.go for why this ONE check is "no floor raised" in full,
		// never a redundant separate branch ---
		{
			name: "risk baseline alone (medium) prevents shippable=auto",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.RiskLevel = review.RiskLevelMedium
				v.Shippable = review.ComputeShippable(v.RiskLevel, v.TestsCoverage, v.Premise)
				return v
			}),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonNotShippableAuto,
		},
		{
			name: "the coverage floor alone (insufficient) prevents shippable=auto",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.TestsCoverage = review.TestsCoverageStateInsufficient
				v.Shippable = review.ComputeShippable(v.RiskLevel, v.TestsCoverage, v.Premise)
				return v
			}),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonNotShippableAuto,
		},
		{
			name: "the premise floor alone (questionable) prevents shippable=auto",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.Premise = review.PremiseStateQuestionable
				v.Shippable = review.ComputeShippable(v.RiskLevel, v.TestsCoverage, v.Premise)
				return v
			}),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonNotShippableAuto,
		},

		// --- criterion 5: diff size under the configured threshold ---
		{
			name: "a diff exceeding the configured file-count threshold is not eligible",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.FilesChanged = cfg.MaxFilesChanged + 1
				return v
			}),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonDiffTooLarge,
		},
		{
			name: "a diff exactly AT the configured threshold is still eligible (inclusive boundary)",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.FilesChanged = cfg.MaxFilesChanged
				return v
			}),
			cfg:          cfg,
			wantEligible: true,
			wantReason:   autoapproval.ReasonNone,
		},

		// --- criterion 6: no sensitive path touched ---
		{
			name: "a blast radius tag in the configured sensitive list is not eligible",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.BlastRadius = []review.Tag{review.TagAuth}
				return v
			}),
			cfg:          cfg,
			wantEligible: false,
			wantReason:   autoapproval.ReasonSensitivePathTouched,
		},
		{
			name: "a blast radius tag NOT in the configured sensitive list stays eligible",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.BlastRadius = []review.Tag{review.TagDependencies}
				return v
			}),
			cfg:          cfg,
			wantEligible: true,
			wantReason:   autoapproval.ReasonNone,
		},
		{
			name: "a custom per-repo sensitive list is honored over the built-in default",
			in: withVerdict(cleanInput(), func(v review.Verdict) review.Verdict {
				v.BlastRadius = []review.Tag{review.TagDependencies} // not in the DEFAULT list...
				return v
			}),
			cfg:          autoapproval.EligibilityConfig{MaxFilesChanged: cfg.MaxFilesChanged, SensitiveTags: []review.Tag{review.TagDependencies}}, // ...but IS in this repo's own custom list
			wantEligible: false,
			wantReason:   autoapproval.ReasonSensitivePathTouched,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotEligible, gotReason := autoapproval.ComputeEligible(tc.in, tc.cfg)
			if gotEligible != tc.wantEligible {
				t.Errorf("ComputeEligible(...) eligible = %v, want %v", gotEligible, tc.wantEligible)
			}
			if gotReason != tc.wantReason {
				t.Errorf("ComputeEligible(...) reason = %q, want %q", gotReason, tc.wantReason)
			}
		})
	}
}

// TestDefaultEligibilityConfig_ReturnsFreshSlice pins that
// DefaultSensitiveTags never hands back a slice two callers could
// mutate into affecting each other -- a caller appending to (or
// overwriting an element of) its own returned slice must never leak into
// a LATER, independent call's own result.
func TestDefaultEligibilityConfig_ReturnsFreshSlice(t *testing.T) {
	t.Parallel()

	first := autoapproval.DefaultSensitiveTags()
	first[0] = review.Tag("tampered")

	second := autoapproval.DefaultSensitiveTags()
	if second[0] == review.Tag("tampered") {
		t.Fatalf("DefaultSensitiveTags: mutating one call's result affected a later call -- want an independent slice each time")
	}
}

func withNeedsHuman(in autoapproval.EligibilityInput, v bool) autoapproval.EligibilityInput {
	in.HasNeedsHumanLabel = v
	return in
}
func withCIGreen(in autoapproval.EligibilityInput, v bool) autoapproval.EligibilityInput {
	in.CIGreen = v
	return in
}
func withCurrentHeadSHA(in autoapproval.EligibilityInput, sha string) autoapproval.EligibilityInput {
	in.CurrentHeadSHA = sha
	return in
}
func withVerdictHeadSHA(in autoapproval.EligibilityInput, sha string) autoapproval.EligibilityInput {
	in.VerdictHeadSHA = sha
	return in
}
func withVerdict(in autoapproval.EligibilityInput, mutate func(review.Verdict) review.Verdict) autoapproval.EligibilityInput {
	in.Verdict = mutate(in.Verdict)
	return in
}
