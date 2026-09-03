package wshub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/clientws"
)

// TestTruncateEventsToByteBudget proves the fix for a real, live-reproduced
// bug found in review: initialReplayLimit alone (an item-count cap) does
// not bound the SubscribedPayload's own total marshaled size, and
// coder/websocket's own default 32KiB per-message read limit means a
// session with enough large event payloads could blow the entire subscribe
// handshake with ErrMessageTooBig instead of just replaying fewer events.
func TestTruncateEventsToByteBudget(t *testing.T) {
	t.Parallel()

	elem := func(n int) clientws.SubscribedPayloadEventsElem {
		return map[string]interface{}{"payload": strings.Repeat("x", n)}
	}
	sizeOf := func(e clientws.SubscribedPayloadEventsElem) int {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return len(b)
	}

	t.Run("empty input stays empty", func(t *testing.T) {
		t.Parallel()
		got := truncateEventsToByteBudget(nil, 1000)
		if len(got) != 0 {
			t.Fatalf("len(got) = %d, want 0", len(got))
		}
	})

	t.Run("everything fits under budget", func(t *testing.T) {
		t.Parallel()
		wire := []clientws.SubscribedPayloadEventsElem{elem(10), elem(10), elem(10)}
		got := truncateEventsToByteBudget(wire, 10_000)
		if len(got) != 3 {
			t.Fatalf("len(got) = %d, want 3 (nothing should be dropped when well under budget)", len(got))
		}
	})

	t.Run("truncates once the running total exceeds budget, preserving order", func(t *testing.T) {
		t.Parallel()
		e0, e1, e2 := elem(100), elem(100), elem(100)
		size := sizeOf(e0)
		wire := []clientws.SubscribedPayloadEventsElem{e0, e1, e2}
		// Budget for exactly 2 elements' worth (plus a little slack so the
		// 3rd is what actually pushes it over, not the 2nd).
		got := truncateEventsToByteBudget(wire, size*2+1)
		if len(got) != 2 {
			t.Fatalf("len(got) = %d, want 2", len(got))
		}
	})

	t.Run("always keeps at least one element, even if it alone exceeds the budget", func(t *testing.T) {
		t.Parallel()
		wire := []clientws.SubscribedPayloadEventsElem{elem(10_000)}
		got := truncateEventsToByteBudget(wire, 1) // a budget of 1 byte is trivially exceeded
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1 (must never truncate to zero)", len(got))
		}
	})

	t.Run("a huge first element does not prevent a small second one from being dropped correctly", func(t *testing.T) {
		t.Parallel()
		big, small := elem(10_000), elem(10)
		wire := []clientws.SubscribedPayloadEventsElem{big, small}
		got := truncateEventsToByteBudget(wire, sizeOf(big)) // exactly big's own size, no room for small
		if len(got) != 1 {
			t.Fatalf("len(got) = %d, want 1 (the oversized first element consumes the whole budget)", len(got))
		}
	})
}
