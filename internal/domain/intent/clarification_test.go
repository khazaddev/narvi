package intent

import "testing"

func TestDeriveNeedsClarification(t *testing.T) {
	tests := []struct {
		name                 string
		confidence           string
		plausibleTargetCount int
		want                 bool
	}{
		{"high confidence, single plausible target", ConfidenceHigh, 1, false},
		{"high confidence, zero plausible targets", ConfidenceHigh, 0, false},
		{"high confidence, multiple plausible targets never asks", ConfidenceHigh, 5, false},
		{"medium confidence, single plausible target", ConfidenceMedium, 1, false},
		{"medium confidence, zero plausible targets", ConfidenceMedium, 0, false},
		{"medium confidence, two plausible targets asks", ConfidenceMedium, 2, true},
		{"medium confidence, many plausible targets asks", ConfidenceMedium, 4, true},
		{"low confidence, single plausible target still asks", ConfidenceLow, 1, true},
		{"low confidence, zero plausible targets still asks", ConfidenceLow, 0, true},
		{"low confidence, multiple plausible targets asks", ConfidenceLow, 3, true},
		{"unrecognized confidence treated as low", "unknown", 1, true},
		{"empty confidence treated as low", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveNeedsClarification(tt.confidence, tt.plausibleTargetCount)
			if got != tt.want {
				t.Errorf("DeriveNeedsClarification(%q, %d) = %v, want %v", tt.confidence, tt.plausibleTargetCount, got, tt.want)
			}
		})
	}
}
