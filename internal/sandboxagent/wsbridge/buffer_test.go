package wsbridge

import "testing"

// entry is a tiny test-only constructor for outboundEntry, since the
// unexported fields aren't directly settable as a composite literal from
// outside this package's own test files (this IS an internal _test.go, so
// it can, but a helper keeps the table below readable).
func entry(ackID string, critical bool) outboundEntry {
	return outboundEntry{ackID: ackID, critical: critical, payload: []byte(`{}`)}
}

// TestEvictionDecision is table-driven over evictionDecision's own pure,
// deterministic contract -- see doc.go for the full reasoning this policy
// implements. No I/O, no Bridge, no network: exactly the buffer's own
// bookkeeping decision in isolation.
func TestEvictionDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		current    []outboundEntry
		newEntry   outboundEntry
		cap        int
		wantEvict  bool
		wantIndex  int
		wantReason string
	}{
		{
			name:      "under cap, no eviction",
			current:   []outboundEntry{entry("", false), entry("", false)},
			newEntry:  entry("", false),
			cap:       10,
			wantEvict: false,
		},
		{
			name:      "exactly at cap, non-critical new entry, buffer full of non-critical -> evicts oldest non-critical",
			current:   []outboundEntry{entry("", false), entry("", false), entry("", false)},
			newEntry:  entry("", false),
			cap:       3,
			wantEvict: true,
			wantIndex: 0,
		},
		{
			name:      "at cap, buffer full of ALL critical, new entry arrives -> nothing evicted, buffer grows past cap",
			current:   []outboundEntry{entry("ack:1", true), entry("ack:2", true), entry("ack:3", true)},
			newEntry:  entry("ack:4", true),
			cap:       3,
			wantEvict: false,
		},
		{
			name: "mixed buffer -> evicts the OLDEST non-critical specifically, not just any non-critical",
			current: []outboundEntry{
				entry("ack:1", true), // index 0: critical, must survive
				entry("", false),     // index 1: oldest non-critical -- must be the one evicted
				entry("ack:2", true), // index 2: critical, must survive
				entry("", false),     // index 3: newer non-critical -- must survive (not the oldest)
			},
			newEntry:  entry("", false),
			cap:       4,
			wantEvict: true,
			wantIndex: 1,
		},
		{
			name:      "new entry itself critical does not change the decision -- still evicts oldest non-critical",
			current:   []outboundEntry{entry("", false), entry("ack:1", true)},
			newEntry:  entry("ack:new", true),
			cap:       2,
			wantEvict: true,
			wantIndex: 0,
		},
		{
			name:      "over cap already (more entries than cap), all critical -> still no eviction",
			current:   []outboundEntry{entry("ack:1", true), entry("ack:2", true), entry("ack:3", true), entry("ack:4", true)},
			newEntry:  entry("ack:5", true),
			cap:       3,
			wantEvict: false,
		},
		{
			name:      "empty buffer, cap zero -> at/over cap immediately but nothing to evict",
			current:   nil,
			newEntry:  entry("", false),
			cap:       0,
			wantEvict: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			index, evict := evictionDecision(tt.current, tt.newEntry, tt.cap)
			if evict != tt.wantEvict {
				t.Fatalf("evictionDecision() evict = %v, want %v", evict, tt.wantEvict)
			}
			if evict && index != tt.wantIndex {
				t.Errorf("evictionDecision() index = %d, want %d", index, tt.wantIndex)
			}
		})
	}
}

// TestOutboundBuffer_AddAckSnapshot exercises the mutex-wrapped buffer
// itself (add/ack/snapshot), not just the pure decision function above --
// proving eviction is actually applied, acking actually removes, and
// snapshot returns entries in original order.
func TestOutboundBuffer_AddAckSnapshot(t *testing.T) {
	t.Parallel()

	buf := newOutboundBuffer()

	buf.add(outboundEntry{critical: false, payload: []byte(`"first"`)})
	buf.add(outboundEntry{ackID: "execution_complete:1", critical: true, payload: []byte(`"second"`)})
	buf.add(outboundEntry{critical: false, payload: []byte(`"third"`)})

	snap := buf.snapshot()
	if len(snap) != 3 {
		t.Fatalf("snapshot() len = %d, want 3", len(snap))
	}
	if string(snap[0].payload) != `"first"` || string(snap[1].payload) != `"second"` || string(snap[2].payload) != `"third"` {
		t.Fatalf("snapshot() out of order: %+v", snap)
	}

	buf.ack("execution_complete:1")
	snap = buf.snapshot()
	if len(snap) != 2 {
		t.Fatalf("after ack, snapshot() len = %d, want 2", len(snap))
	}
	for _, e := range snap {
		if e.ackID == "execution_complete:1" {
			t.Fatalf("acked entry still present after ack(): %+v", snap)
		}
	}

	// Acking an unknown ackID is a silent no-op.
	buf.ack("no-such-ackid")
	if len(buf.snapshot()) != 2 {
		t.Fatalf("acking an unknown ackID changed the buffer: %+v", buf.snapshot())
	}
}

// TestOutboundBuffer_EvictionAppliedOnAdd proves the buffer actually
// applies evictionDecision when adding past cap, using a tiny buffer cap
// exercised via direct entries append (evictionDecision is exercised
// against a real outboundBuffer.add call, not the package constant, by
// filling to outboundBufferCap -- this is intentionally a slower,
// heavier-weight confirmation than TestEvictionDecision above; keep it
// small enough to run fast).
func TestOutboundBuffer_EvictionAppliedOnAdd(t *testing.T) {
	t.Parallel()

	buf := newOutboundBuffer()
	for i := 0; i < outboundBufferCap; i++ {
		buf.add(outboundEntry{critical: false, payload: []byte(`"filler"`)})
	}
	// Buffer is now exactly at cap, all non-critical. One more non-critical
	// entry should evict the oldest, keeping the buffer AT cap, not over.
	buf.add(outboundEntry{critical: false, payload: []byte(`"newest"`)})

	snap := buf.snapshot()
	if len(snap) != outboundBufferCap {
		t.Fatalf("snapshot() len = %d, want exactly outboundBufferCap = %d (oldest non-critical should have been evicted)",
			len(snap), outboundBufferCap)
	}
	if string(snap[len(snap)-1].payload) != `"newest"` {
		t.Fatalf("newest entry missing from tail of buffer: %+v", snap[len(snap)-1])
	}

	// Now fill entirely with critical entries and prove the buffer is
	// allowed to grow past cap rather than evict one.
	buf2 := newOutboundBuffer()
	for i := 0; i < outboundBufferCap; i++ {
		buf2.add(outboundEntry{ackID: "critical", critical: true, payload: []byte(`"c"`)})
	}
	buf2.add(outboundEntry{ackID: "one-more", critical: true, payload: []byte(`"c"`)})
	if got := len(buf2.snapshot()); got != outboundBufferCap+1 {
		t.Fatalf("all-critical buffer len = %d, want %d (grown past cap, nothing evicted)", got, outboundBufferCap+1)
	}
}
