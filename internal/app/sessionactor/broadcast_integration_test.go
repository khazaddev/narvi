//go:build integration

// Integration test proving the GENERIC (not per-handler) broadcast wiring
// (Step 19 design decision) against a REAL Postgres instance -- gated
// behind the "integration" build tag, matching this package's own
// testcontainers-Postgres conventions exactly. Run via `make
// test-integration`.
package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/platform"
)

// TestActorTransact_BroadcastsOnlyAfterCommit proves: (a) a successful
// transact that appended N events (via appendRawEvent, which
// appendEvent's own production call sites also feed the identical
// pendingBroadcast queue) calls the injected fake ports.EventBroadcaster
// exactly N times, with the exact payloads in order, and NOT before
// commit (asserted from inside fn itself, before transact's own tx.Commit
// has even run); (b) a transact whose fn returns an error never calls the
// broadcaster at all, even though appendRawEvent ran (queuing a payload)
// earlier in that same failed attempt -- the queue is discarded, not
// carried into the next attempt.
func TestActorTransact_BroadcastsOnlyAfterCommit(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	fb := &fakeBroadcaster{}
	r := NewRegistry(ctx, pool, platform.DefaultTimeouts(), fb, nil, nil, "", nil, nil, "")
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	// (a) a successful transact appending 2 events.
	err = a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if len(fb.calls) != 0 {
			t.Fatalf("broadcaster already called %d times DURING fn (before commit) -- want 0", len(fb.calls))
		}
		if err := a.appendRawEvent(ctx, tx, "token", "msg-hello", json.RawMessage(`{"text":"hello"}`)); err != nil {
			return err
		}
		return a.appendRawEvent(ctx, tx, "token", "msg-world", json.RawMessage(`{"text":"world"}`))
	})
	if err != nil {
		t.Fatalf("transact: %v", err)
	}
	if len(fb.calls) != 2 {
		t.Fatalf("broadcaster called %d times after a successful 2-event transact, want 2", len(fb.calls))
	}
	if string(fb.calls[0].payload) != `{"text":"hello"}` {
		t.Errorf("first broadcast payload = %s, want hello", fb.calls[0].payload)
	}
	if string(fb.calls[1].payload) != `{"text":"world"}` {
		t.Errorf("second broadcast payload = %s, want world", fb.calls[1].payload)
	}
	for i, c := range fb.calls {
		if c.sessionID != sessionID.String() {
			t.Errorf("broadcast call %d sessionID = %q, want %q", i, c.sessionID, sessionID.String())
		}
	}

	// (b) a transact whose fn returns an error never broadcasts, even
	// though appendRawEvent ran (and queued a payload) earlier in that
	// same attempt.
	fb.calls = nil
	wantErr := errors.New("boom")
	err = a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := a.appendRawEvent(ctx, tx, "token", "msg-never-broadcast", json.RawMessage(`{"text":"never broadcast"}`)); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("transact error = %v, want %v", err, wantErr)
	}
	if len(fb.calls) != 0 {
		t.Errorf("broadcaster called %d times after a FAILED transact, want 0", len(fb.calls))
	}
	if a.pendingBroadcast != nil {
		t.Errorf("pendingBroadcast = %v, want nil (discarded) after a failed transact", a.pendingBroadcast)
	}

	// A subsequent SUCCESSFUL transact still works cleanly after the prior
	// failed one -- the discarded queue from (b) must not have corrupted
	// anything for later attempts.
	err = a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return a.appendRawEvent(ctx, tx, "token", "msg-after-failure", json.RawMessage(`{"text":"after failure"}`))
	})
	if err != nil {
		t.Fatalf("transact after a prior failure: %v", err)
	}
	if len(fb.calls) != 1 || string(fb.calls[0].payload) != `{"text":"after failure"}` {
		t.Errorf("broadcast calls after recovery = %+v, want exactly one {\"text\":\"after failure\"}", fb.calls)
	}
}

// TestActorTransact_NilBroadcasterNeverPanics proves an Actor hydrated
// from a Registry built WITHOUT a broadcaster (nil, matching every OTHER
// pre-existing integration test in this package) still transacts and
// appends events successfully -- the commit-time broadcast step never
// panics on a nil broadcaster.
func TestActorTransact_NilBroadcasterNeverPanics(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	r := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil, "")
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	err = a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		return a.appendRawEvent(ctx, tx, "token", "msg-hi", json.RawMessage(`{"text":"hi"}`))
	})
	if err != nil {
		t.Fatalf("transact with nil broadcaster: %v", err)
	}
}
