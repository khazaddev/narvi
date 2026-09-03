package reviewpost_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/narvidev/narvi/internal/domain/review"
	"github.com/narvidev/narvi/internal/domain/reviewpost"
)

func TestRiskLabel(t *testing.T) {
	tests := []struct {
		name string
		risk review.RiskLevel
		want string
	}{
		{name: "low", risk: review.RiskLevelLow, want: reviewpost.LabelLowRisk},
		{name: "medium", risk: review.RiskLevelMedium, want: reviewpost.LabelMediumRisk},
		{name: "high", risk: review.RiskLevelHigh, want: reviewpost.LabelHighRisk},
		{name: "zero value fails conservative to high", risk: "", want: reviewpost.LabelHighRisk},
		{name: "unrecognized fails conservative to high", risk: "extreme", want: reviewpost.LabelHighRisk},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reviewpost.RiskLabel(tc.risk); got != tc.want {
				t.Errorf("RiskLabel(%q) = %q, want %q", tc.risk, got, tc.want)
			}
		})
	}
}

func sortedCopy(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestComputeLabelSync(t *testing.T) {
	tests := []struct {
		name          string
		currentLabels []string
		risk          review.RiskLevel
		wantAdd       []string
		wantRemove    []string
	}{
		{
			name:          "no labels present yet -- adds the desired one, nothing to remove",
			currentLabels: nil,
			risk:          review.RiskLevelMedium,
			wantAdd:       []string{reviewpost.LabelMediumRisk},
			wantRemove:    nil,
		},
		{
			name:          "desired label already present -- idempotent, empty plan",
			currentLabels: []string{reviewpost.LabelLowRisk},
			risk:          review.RiskLevelLow,
			wantAdd:       nil,
			wantRemove:    nil,
		},
		{
			name:          "risk changed from low to high -- adds high, removes low",
			currentLabels: []string{reviewpost.LabelLowRisk},
			risk:          review.RiskLevelHigh,
			wantAdd:       []string{reviewpost.LabelHighRisk},
			wantRemove:    []string{reviewpost.LabelLowRisk},
		},
		{
			name:          "stale labels from more than one prior tier -- all removed",
			currentLabels: []string{reviewpost.LabelLowRisk, reviewpost.LabelMediumRisk},
			risk:          review.RiskLevelHigh,
			wantAdd:       []string{reviewpost.LabelHighRisk},
			wantRemove:    []string{reviewpost.LabelMediumRisk, reviewpost.LabelLowRisk},
		},
		{
			name:          "review:needs-human present -- NEVER touched, regardless of risk",
			currentLabels: []string{reviewpost.LabelNeedsHuman},
			risk:          review.RiskLevelLow,
			wantAdd:       []string{reviewpost.LabelLowRisk},
			wantRemove:    nil, // must not include LabelNeedsHuman.
		},
		{
			name:          "review:needs-human present alongside a stale risk label -- only the risk label is synced",
			currentLabels: []string{reviewpost.LabelNeedsHuman, reviewpost.LabelHighRisk},
			risk:          review.RiskLevelLow,
			wantAdd:       []string{reviewpost.LabelLowRisk},
			wantRemove:    []string{reviewpost.LabelHighRisk},
		},
		{
			name:          "other, unrelated labels present -- ignored entirely",
			currentLabels: []string{"bug", "needs-triage"},
			risk:          review.RiskLevelMedium,
			wantAdd:       []string{reviewpost.LabelMediumRisk},
			wantRemove:    nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := reviewpost.ComputeLabelSync(tc.currentLabels, tc.risk)
			if !reflect.DeepEqual(sortedCopy(got.Add), sortedCopy(tc.wantAdd)) {
				t.Errorf("Add = %v, want %v", got.Add, tc.wantAdd)
			}
			if !reflect.DeepEqual(sortedCopy(got.Remove), sortedCopy(tc.wantRemove)) {
				t.Errorf("Remove = %v, want %v", got.Remove, tc.wantRemove)
			}
			for _, l := range got.Remove {
				if l == reviewpost.LabelNeedsHuman {
					t.Errorf("Remove contains %q -- the needs-human escape hatch must NEVER be removed by label sync", reviewpost.LabelNeedsHuman)
				}
			}
			for _, l := range got.Add {
				if l == reviewpost.LabelNeedsHuman {
					t.Errorf("Add contains %q -- label sync must NEVER add the needs-human escape hatch itself", reviewpost.LabelNeedsHuman)
				}
			}
		})
	}
}
