package intent

import "testing"

func TestCorroborateTarget(t *testing.T) {
	tests := []struct {
		name                string
		classifierTarget    string
		deterministicTarget string
		irreversible        bool
		wantFinalTarget     string
		wantCorroborated    bool
	}{
		{
			name:                "not irreversible: classifier target passes through, always corroborated",
			classifierTarget:    TargetRequest,
			deterministicTarget: "",
			irreversible:        false,
			wantFinalTarget:     TargetRequest,
			wantCorroborated:    true,
		},
		{
			name:                "not irreversible: even a disagreeing deterministic signal is ignored",
			classifierTarget:    TargetRequest,
			deterministicTarget: TargetReview,
			irreversible:        false,
			wantFinalTarget:     TargetRequest,
			wantCorroborated:    true,
		},
		{
			name:                "irreversible, no deterministic signal: classifier target kept, not corroborated",
			classifierTarget:    TargetReview,
			deterministicTarget: "",
			irreversible:        true,
			wantFinalTarget:     TargetReview,
			wantCorroborated:    false,
		},
		{
			name:                "irreversible, signals agree on review",
			classifierTarget:    TargetReview,
			deterministicTarget: TargetReview,
			irreversible:        true,
			wantFinalTarget:     TargetReview,
			wantCorroborated:    true,
		},
		{
			name:                "irreversible, signals agree on request",
			classifierTarget:    TargetRequest,
			deterministicTarget: TargetRequest,
			irreversible:        true,
			wantFinalTarget:     TargetRequest,
			wantCorroborated:    true,
		},
		{
			name:                "irreversible, signals disagree: deterministic wins the recorded value, not corroborated",
			classifierTarget:    TargetRequest,
			deterministicTarget: TargetReview,
			irreversible:        true,
			wantFinalTarget:     TargetReview,
			wantCorroborated:    false,
		},
		{
			name:                "irreversible, signals disagree the other direction",
			classifierTarget:    TargetReview,
			deterministicTarget: TargetRequest,
			irreversible:        true,
			wantFinalTarget:     TargetRequest,
			wantCorroborated:    false,
		},
		{
			name:                "irreversible, classifier target empty (e.g. fallback), deterministic signal present, disagree",
			classifierTarget:    "",
			deterministicTarget: TargetReview,
			irreversible:        true,
			wantFinalTarget:     TargetReview,
			wantCorroborated:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTarget, gotCorroborated := CorroborateTarget(tt.classifierTarget, tt.deterministicTarget, tt.irreversible)
			if gotTarget != tt.wantFinalTarget {
				t.Errorf("finalTarget = %q, want %q", gotTarget, tt.wantFinalTarget)
			}
			if gotCorroborated != tt.wantCorroborated {
				t.Errorf("corroborated = %v, want %v", gotCorroborated, tt.wantCorroborated)
			}
		})
	}
}
