package reviewverdict_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewverdict"
)

func TestCounterReviewCorroborated(t *testing.T) {
	tests := []struct {
		name     string
		starts   []reviewverdict.SubTaskStartRecord
		finishes []reviewverdict.SubTaskFinishRecord
		want     bool
	}{
		{
			name:     "no starts at all: never corroborated",
			starts:   nil,
			finishes: nil,
			want:     false,
		},
		{
			name: "counter-reviewer start but no finish at all: not corroborated (still active, or race not yet visible)",
			starts: []reviewverdict.SubTaskStartRecord{
				{SubTaskID: "sub-1", SubAgentType: review.CounterReviewerAgentName},
			},
			finishes: nil,
			want:     false,
		},
		{
			name: "counter-reviewer start plus finish but outcome failed: not corroborated",
			starts: []reviewverdict.SubTaskStartRecord{
				{SubTaskID: "sub-1", SubAgentType: review.CounterReviewerAgentName},
			},
			finishes: []reviewverdict.SubTaskFinishRecord{
				{SubTaskID: "sub-1", Outcome: "failed"},
			},
			want: false,
		},
		{
			name: "counter-reviewer start plus finish but outcome cancelled: not corroborated",
			starts: []reviewverdict.SubTaskStartRecord{
				{SubTaskID: "sub-1", SubAgentType: review.CounterReviewerAgentName},
			},
			finishes: []reviewverdict.SubTaskFinishRecord{
				{SubTaskID: "sub-1", Outcome: "cancelled"},
			},
			want: false,
		},
		{
			name: "counter-reviewer start plus completed finish for the SAME subTaskId: corroborated",
			starts: []reviewverdict.SubTaskStartRecord{
				{SubTaskID: "sub-1", SubAgentType: review.CounterReviewerAgentName},
			},
			finishes: []reviewverdict.SubTaskFinishRecord{
				{SubTaskID: "sub-1", Outcome: "completed"},
			},
			want: true,
		},
		{
			name: "multiple unrelated sub-tasks alongside the real counter-reviewer pair: still finds the right one",
			starts: []reviewverdict.SubTaskStartRecord{
				{SubTaskID: "sub-fact-check", SubAgentType: "fact-check"},
				{SubTaskID: "sub-scribe", SubAgentType: "architecture-scribe"},
				{SubTaskID: "sub-1", SubAgentType: review.CounterReviewerAgentName},
			},
			finishes: []reviewverdict.SubTaskFinishRecord{
				{SubTaskID: "sub-fact-check", Outcome: "completed"},
				{SubTaskID: "sub-scribe", Outcome: "completed"},
				{SubTaskID: "sub-1", Outcome: "completed"},
			},
			want: true,
		},
		{
			name: "only a different subAgentType present (fact-check), no counter-reviewer at all: not corroborated",
			starts: []reviewverdict.SubTaskStartRecord{
				{SubTaskID: "sub-fact-check", SubAgentType: "fact-check"},
			},
			finishes: []reviewverdict.SubTaskFinishRecord{
				{SubTaskID: "sub-fact-check", Outcome: "completed"},
			},
			want: false,
		},
		{
			name: "finish exists but for a DIFFERENT subTaskId than the counter-reviewer start: must not false-positive",
			starts: []reviewverdict.SubTaskStartRecord{
				{SubTaskID: "sub-1", SubAgentType: review.CounterReviewerAgentName},
			},
			finishes: []reviewverdict.SubTaskFinishRecord{
				{SubTaskID: "sub-2", Outcome: "completed"},
			},
			want: false,
		},
		{
			name: "empty SubAgentType (legacy/unverified-live subtaskPart fallback path) never matches",
			starts: []reviewverdict.SubTaskStartRecord{
				{SubTaskID: "sub-1", SubAgentType: ""},
			},
			finishes: []reviewverdict.SubTaskFinishRecord{
				{SubTaskID: "sub-1", Outcome: "completed"},
			},
			want: false,
		},
		{
			name: "two counter-reviewer starts (e.g. a retried sub-task): either completing corroborates",
			starts: []reviewverdict.SubTaskStartRecord{
				{SubTaskID: "sub-1", SubAgentType: review.CounterReviewerAgentName},
				{SubTaskID: "sub-2", SubAgentType: review.CounterReviewerAgentName},
			},
			finishes: []reviewverdict.SubTaskFinishRecord{
				{SubTaskID: "sub-1", Outcome: "failed"},
				{SubTaskID: "sub-2", Outcome: "completed"},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reviewverdict.CounterReviewCorroborated(tt.starts, tt.finishes)
			if got != tt.want {
				t.Errorf("CounterReviewCorroborated(%+v, %+v) = %v, want %v", tt.starts, tt.finishes, got, tt.want)
			}
		})
	}
}
