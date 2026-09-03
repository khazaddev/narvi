package turn_test

import (
	"testing"

	"github.com/narvidev/narvi/internal/domain/turn"
)

// TestAllEpistemicOutcomes_MatchesConstants pins the closed 3-value
// vocabulary (§20.2) -- a fourth value appearing here without every
// consumer (the reporting endpoint, the Postgres enum, analytics) agreeing
// would be a contract break, so the exhaustive list itself is asserted.
// Mirrors workflow_test.TestAllStepOutcomeStatuses_MatchesConstants
// exactly.
func TestAllEpistemicOutcomes_MatchesConstants(t *testing.T) {
	t.Parallel()

	want := []turn.EpistemicOutcome{turn.EpistemicOutcomeNone, turn.EpistemicOutcomeMinor, turn.EpistemicOutcomeStrong}
	if len(turn.AllEpistemicOutcomes) != len(want) {
		t.Fatalf("len(AllEpistemicOutcomes) = %d, want %d", len(turn.AllEpistemicOutcomes), len(want))
	}
	for i, o := range want {
		if turn.AllEpistemicOutcomes[i] != o {
			t.Errorf("AllEpistemicOutcomes[%d] = %s, want %s", i, turn.AllEpistemicOutcomes[i], o)
		}
	}
}

func TestIsValidEpistemicOutcome(t *testing.T) {
	t.Parallel()

	for _, o := range turn.AllEpistemicOutcomes {
		if !turn.IsValidEpistemicOutcome(o) {
			t.Errorf("IsValidEpistemicOutcome(%s) = false, want true", o)
		}
	}
	// review.Shippable/workflow.StepOutcomeStatus's own values are
	// DISTINCT axes -- none of them is a valid EpistemicOutcome, and this
	// pins that the vocabularies never blur together.
	for _, o := range []turn.EpistemicOutcome{"", "None", "MINOR", "ok", "needs_fix", "blocked", "auto", "low"} {
		if turn.IsValidEpistemicOutcome(o) {
			t.Errorf("IsValidEpistemicOutcome(%q) = true, want false", o)
		}
	}
}
