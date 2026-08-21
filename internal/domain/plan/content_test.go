package plan

import "testing"

func i64(n int64) *int64 { return &n }

func TestExtractContent(t *testing.T) {
	tests := []struct {
		name              string
		events            []ContentEvent
		lowerBoundEventID *int64
		upperBoundEventID *int64
		want              string
	}{
		{
			name:              "no events at all falls back",
			events:            nil,
			lowerBoundEventID: i64(10),
			upperBoundEventID: nil,
			want:              ContentFallbackText,
		},
		{
			name: "single token event in window, unbounded above (the original single-turn case)",
			events: []ContentEvent{
				{ID: 15, Type: "token", Text: "final plan text"},
				{ID: 14, Type: "tool_call", Text: ""},
				{ID: 11, Type: "token", Text: "final plan text"}, // cumulative -- same messageId, earlier partial superseded
			},
			lowerBoundEventID: i64(10),
			upperBoundEventID: nil,
			want:              "final plan text",
		},
		{
			name: "newest-first scan finds the LAST token event by arrival order first",
			events: []ContentEvent{
				{ID: 20, Type: "token", Text: "the full, final text"},
				{ID: 19, Type: "token", Text: "the full, fin"},
				{ID: 18, Type: "token", Text: "the full"},
			},
			lowerBoundEventID: i64(10),
			upperBoundEventID: nil,
			want:              "the full, final text",
		},
		{
			name: "events at or below lowerBound belong to an earlier turn and are excluded",
			events: []ContentEvent{
				{ID: 12, Type: "token", Text: "this turn's own text"},
				{ID: 10, Type: "token", Text: "an EARLIER turn's text -- must never be returned"},
				{ID: 9, Type: "token", Text: "even earlier"},
			},
			lowerBoundEventID: i64(10),
			upperBoundEventID: nil,
			want:              "this turn's own text",
		},
		{
			name: "lowerBound exactly matches an event id -- that event itself is excluded (exclusive bound)",
			events: []ContentEvent{
				{ID: 10, Type: "token", Text: "must never be returned -- this IS the dispatch boundary event"},
			},
			lowerBoundEventID: i64(10),
			upperBoundEventID: nil,
			want:              ContentFallbackText,
		},
		{
			name: "events strictly above upperBound belong to a LATER turn and are excluded -- the defining bug this generalization fixes",
			events: []ContentEvent{
				{ID: 30, Type: "token", Text: "a LATER turn's own text -- e.g. the approval-dispatched implementation turn"},
				{ID: 25, Type: "token", Text: "this plan version's own real content"},
				{ID: 20, Type: "tool_call", Text: ""},
			},
			lowerBoundEventID: i64(15),
			upperBoundEventID: i64(28),
			want:              "this plan version's own real content",
		},
		{
			// Regression case for a real off-by-one caught live by
			// httpapi/plans_integration_test.go against a real Postgres
			// instance, not by a unit test: a turn's own DispatchedEventID is
			// the events-log watermark that existed BEFORE any of that
			// turn's events were produced (see ExtractContent's own doc
			// comment on upperBoundEventID) -- so when the NEXT turn
			// dispatches immediately after THIS turn's own last (and only)
			// event with no intervening activity, that next turn's own
			// DispatchedEventID exactly EQUALS this turn's own last event's
			// id. That event must still be counted as belonging to THIS
			// turn, never excluded -- an exclusive upper-bound comparison
			// here would silently drop it, exactly the failure the
			// integration test caught (a plan turn whose only token event
			// happened to land exactly on the next turn's own dispatch
			// watermark fell back to ContentFallbackText instead of
			// returning its own real content).
			name: "upperBound exactly matches an event id -- that event is INCLUDED (it is this turn's own, not the next turn's)",
			events: []ContentEvent{
				{ID: 28, Type: "token", Text: "this turn's own last event -- happens to equal the NEXT turn's own dispatch watermark"},
				{ID: 20, Type: "token", Text: "an earlier token event from this same turn"},
			},
			lowerBoundEventID: i64(15),
			upperBoundEventID: i64(28),
			want:              "this turn's own last event -- happens to equal the NEXT turn's own dispatch watermark",
		},
		{
			name: "nothing in window at all falls back, even with plenty of events outside it",
			events: []ContentEvent{
				{ID: 30, Type: "token", Text: "later turn"},
				{ID: 5, Type: "token", Text: "earlier turn"},
			},
			lowerBoundEventID: i64(10),
			upperBoundEventID: i64(28),
			want:              ContentFallbackText,
		},
		{
			name: "a token event with empty text (a heartbeat/keepalive artifact) is skipped, never treated as content",
			events: []ContentEvent{
				{ID: 16, Type: "token", Text: ""},
				{ID: 15, Type: "token", Text: "the real content"},
			},
			lowerBoundEventID: i64(10),
			upperBoundEventID: nil,
			want:              "the real content",
		},
		{
			name: "non-token event types are ignored regardless of position",
			events: []ContentEvent{
				{ID: 16, Type: "tool_result", Text: ""},
				{ID: 15, Type: "sub_task_finish", Text: ""},
				{ID: 14, Type: "token", Text: "the content"},
			},
			lowerBoundEventID: i64(10),
			upperBoundEventID: nil,
			want:              "the content",
		},
		{
			name:              "nil lowerBound scans to the oldest supplied event",
			events:            []ContentEvent{{ID: 3, Type: "token", Text: "very old"}},
			lowerBoundEventID: nil,
			upperBoundEventID: nil,
			want:              "very old",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractContent(tt.events, tt.lowerBoundEventID, tt.upperBoundEventID)
			if got != tt.want {
				t.Errorf("ExtractContent() = %q, want %q", got, tt.want)
			}
		})
	}
}
