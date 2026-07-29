package slack

import "testing"

// Table-driven tests for slackEvent.isIgnorable (event.go) -- the M14
// audit finding this closes: confirmed via grep, there was no event_test.go
// in this package at all, and zero tests exercised isIgnorable directly,
// at any level (only indirectly, incidentally, through full HTTP-handler
// integration tests that happen to post an ignorable event as part of a
// larger scenario). isIgnorable is a pure function -- no DB, no network --
// so this is a plain, fast unit test, no "integration" build tag needed.
func TestSlackEvent_IsIgnorable(t *testing.T) {
	tests := []struct {
		name string
		ev   slackEvent
		want bool
	}{
		{
			name: "bot-authored event is ignorable",
			ev:   slackEvent{Type: "message", BotID: "B0123456"},
			want: true,
		},
		{
			name: "message with a subtype (edit/join/etc.) is ignorable",
			ev:   slackEvent{Type: "message", Subtype: "message_changed"},
			want: true,
		},
		{
			name: "genuine app_mention is NOT ignorable",
			ev:   slackEvent{Type: "app_mention", Channel: "C0123", User: "U0123", Text: "@narvi help", TS: "1700000000.000100"},
			want: false,
		},
		{
			name: "genuine plain message (no subtype, no bot_id) is NOT ignorable",
			ev:   slackEvent{Type: "message", Channel: "C0123", User: "U0123", Text: "a plain reply", TS: "1700000000.000200"},
			want: false,
		},
		{
			name: "some other event type is ignorable",
			ev:   slackEvent{Type: "reaction_added", Channel: "C0123", User: "U0123"},
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ev.isIgnorable(); got != tc.want {
				t.Errorf("isIgnorable() = %v, want %v (event=%+v)", got, tc.want, tc.ev)
			}
		})
	}
}
