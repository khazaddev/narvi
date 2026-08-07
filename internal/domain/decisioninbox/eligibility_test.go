package decisioninbox_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/decisioninbox"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

func TestComputeAutoApprovalEligible(t *testing.T) {
	t.Parallel()

	clean := decisioninbox.EligibilityInput{
		CIGreen:              true,
		IsDraft:              false,
		RiskLabel:            reviewpost.LabelLowRisk,
		HasNeedsHumanLabel:   false,
		OpenBlockingFindings: 0,
	}

	tests := []struct {
		name string
		in   decisioninbox.EligibilityInput
		want bool
	}{
		{"clean low-risk PR is eligible", clean, true},
		{"draft is never eligible", withDraft(clean, true), false},
		{"CI not green is never eligible", withCIGreen(clean, false), false},
		{"needs-human label overrides everything", withNeedsHuman(clean, true), false},
		{"an open blocking finding excludes it", withOpenFindings(clean, 1), false},
		{"medium risk label is not eligible", withRiskLabel(clean, reviewpost.LabelMediumRisk), false},
		{"high risk label is not eligible", withRiskLabel(clean, reviewpost.LabelHighRisk), false},
		{"never-labeled (empty risk label) is not eligible", withRiskLabel(clean, ""), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := decisioninbox.ComputeAutoApprovalEligible(tc.in); got != tc.want {
				t.Errorf("ComputeAutoApprovalEligible(%+v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func withDraft(in decisioninbox.EligibilityInput, v bool) decisioninbox.EligibilityInput {
	in.IsDraft = v
	return in
}
func withCIGreen(in decisioninbox.EligibilityInput, v bool) decisioninbox.EligibilityInput {
	in.CIGreen = v
	return in
}
func withNeedsHuman(in decisioninbox.EligibilityInput, v bool) decisioninbox.EligibilityInput {
	in.HasNeedsHumanLabel = v
	return in
}
func withOpenFindings(in decisioninbox.EligibilityInput, n int) decisioninbox.EligibilityInput {
	in.OpenBlockingFindings = n
	return in
}
func withRiskLabel(in decisioninbox.EligibilityInput, label string) decisioninbox.EligibilityInput {
	in.RiskLabel = label
	return in
}
