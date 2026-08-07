package shadowcompare_test

import (
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/domain/shadowcompare"
)

func timePtr(t time.Time) *time.Time { return &t }

func TestTurnSnapshot_DurationSeconds(t *testing.T) {
	t.Parallel()

	dispatched := time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)
	completed := dispatched.Add(42 * time.Second)

	tests := []struct {
		name         string
		dispatchedAt *time.Time
		completedAt  *time.Time
		want         *float64
	}{
		{
			name:         "both set: real duration",
			dispatchedAt: timePtr(dispatched),
			completedAt:  timePtr(completed),
			want:         floatPtr(42),
		},
		{
			name:         "dispatchedAt nil: still processing or never dispatched",
			dispatchedAt: nil,
			completedAt:  timePtr(completed),
			want:         nil,
		},
		{
			name:         "completedAt nil: still processing",
			dispatchedAt: timePtr(dispatched),
			completedAt:  nil,
			want:         nil,
		},
		{
			name:         "both nil: pending",
			dispatchedAt: nil,
			completedAt:  nil,
			want:         nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			snap := shadowcompare.TurnSnapshot{DispatchedAt: tc.dispatchedAt, CompletedAt: tc.completedAt}
			got := snap.DurationSeconds()
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("DurationSeconds() = %v, want %v", derefFloat(got), derefFloat(tc.want))
			}
			if got != nil && *got != *tc.want {
				t.Errorf("DurationSeconds() = %v, want %v", *got, *tc.want)
			}
		})
	}
}

func TestCompare_BundlesBothSidesUnchanged(t *testing.T) {
	t.Parallel()

	modelA, effortA := "anthropic/claude-sonnet-4-5", "high"
	modelB, effortB := "openai/gpt-5.3-codex-spark", "xhigh"
	a := shadowcompare.TurnSnapshot{TurnID: "turn-a", SessionID: "sess-a", ModelID: &modelA, Effort: &effortA, Status: "completed", CreatedAt: time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)}
	b := shadowcompare.TurnSnapshot{TurnID: "turn-b", SessionID: "sess-b", ModelID: &modelB, Effort: &effortB, Status: "processing", CreatedAt: time.Date(2026, 8, 7, 11, 0, 0, 0, time.UTC)}

	got := shadowcompare.Compare(a, b)
	if got.TurnA != a {
		t.Errorf("Compare(a, b).TurnA = %+v, want %+v unchanged", got.TurnA, a)
	}
	if got.TurnB != b {
		t.Errorf("Compare(a, b).TurnB = %+v, want %+v unchanged", got.TurnB, b)
	}
}

func floatPtr(f float64) *float64 { return &f }

func derefFloat(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}
