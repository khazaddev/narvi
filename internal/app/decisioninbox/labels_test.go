package decisioninbox

// Internal-package test (not decisioninbox_test): classifyPRLabels is
// unexported, and Step 62 (§21.2) removed its own former OBSERVABLE
// effect on RevalidateForMerge's ok/refused outcome (risk labels no
// longer gate merge eligibility at all -- see revalidate_integration_test.go's
// own note at the former MostRestrictiveRiskLabelWins_Refused site). This
// file tests classifyPRLabels DIRECTLY instead, so its own precedence
// rule (§60 review finding A6: "most restrictive risk label wins, never
// whichever GitHub happens to return last") keeps real, targeted
// coverage rather than being silently dropped.

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

func TestClassifyPRLabels_MostRestrictiveRiskLabelWins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"high then low: high wins", []string{"review:high-risk", "review:low-risk"}, reviewpost.LabelHighRisk},
		{"low then high: high still wins (order-independent)", []string{"review:low-risk", "review:high-risk"}, reviewpost.LabelHighRisk},
		{"medium then low: medium wins", []string{"review:medium-risk", "review:low-risk"}, reviewpost.LabelMediumRisk},
		{"low then medium: medium still wins", []string{"review:low-risk", "review:medium-risk"}, reviewpost.LabelMediumRisk},
		{"only low: low", []string{"review:low-risk"}, reviewpost.LabelLowRisk},
		{"no risk label at all: empty", []string{"some-other-label"}, ""},
		{"all three at once: high wins", []string{"review:low-risk", "review:medium-risk", "review:high-risk"}, reviewpost.LabelHighRisk},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, gotRiskLabel, _ := classifyPRLabels(tc.labels)
			if gotRiskLabel != tc.want {
				t.Errorf("classifyPRLabels(%v) riskLabel = %q, want %q", tc.labels, gotRiskLabel, tc.want)
			}
		})
	}
}

func TestClassifyPRLabels_NeedsHumanAndHandoffIndependentOfRiskLabel(t *testing.T) {
	t.Parallel()

	hasNeedsHuman, riskLabel, isHandoff := classifyPRLabels([]string{"review:low-risk", "review:needs-human", "handoff"})
	if !hasNeedsHuman {
		t.Error("hasNeedsHuman = false, want true")
	}
	if riskLabel != reviewpost.LabelLowRisk {
		t.Errorf("riskLabel = %q, want %q", riskLabel, reviewpost.LabelLowRisk)
	}
	if !isHandoff {
		t.Error("isHandoff = false, want true")
	}
}
