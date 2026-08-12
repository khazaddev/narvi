package intent

import "testing"

func TestResolveAnswerOnly(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		target     string
		confidence string
		want       bool
	}{
		{
			name:       "confident amend unblocks dispatch",
			source:     RecordSourceClassifier,
			target:     TargetAmend,
			confidence: ConfidenceHigh,
			want:       false,
		},
		{
			name:       "confident answer holds for clarification",
			source:     RecordSourceClassifier,
			target:     TargetAnswer,
			confidence: ConfidenceHigh,
			want:       true,
		},
		{
			name:       "low confidence amend still holds -- confidence gates before target",
			source:     RecordSourceClassifier,
			target:     TargetAmend,
			confidence: ConfidenceLow,
			want:       true,
		},
		{
			name:       "medium confidence amend holds -- plausibleTargetCount=2 always asks at medium",
			source:     RecordSourceClassifier,
			target:     TargetAmend,
			confidence: ConfidenceMedium,
			want:       true,
		},
		{
			name:       "fallback source always holds regardless of target/confidence",
			source:     RecordSourceFallback,
			target:     TargetAmend,
			confidence: ConfidenceHigh,
			want:       true,
		},
		{
			name:       "explicit source (never actually produced by the classifier) still fails open",
			source:     RecordSourceExplicit,
			target:     TargetAmend,
			confidence: ConfidenceHigh,
			want:       true,
		},
		{
			name:       "unrecognized target at high confidence still holds -- only TargetAmend unblocks",
			source:     RecordSourceClassifier,
			target:     "something-else",
			confidence: ConfidenceHigh,
			want:       true,
		},
		{
			name:       "unrecognized confidence value falls through DeriveNeedsClarification's own default (treated as low) -- holds",
			source:     RecordSourceClassifier,
			target:     TargetAmend,
			confidence: "not-a-real-confidence",
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveAnswerOnly(tt.source, tt.target, tt.confidence)
			if got != tt.want {
				t.Errorf("ResolveAnswerOnly(%q, %q, %q) = %v, want %v", tt.source, tt.target, tt.confidence, got, tt.want)
			}
		})
	}
}
