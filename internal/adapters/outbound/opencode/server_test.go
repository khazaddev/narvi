package opencode

import (
	"context"
	"testing"

	"github.com/narvidev/narvi/contracts/gen/go/sandboxws"
)

// These tests need only the OpenCode SERVER itself (spawning it, hitting
// /api/health via opencodeproc, POST /session, GET /event's server.
// connected handshake) -- no AI call, so they run unconditionally, no skip
// needed. Deliberately NOT t.Parallel(): each spawns a REAL opencode serve
// process, and running several concurrently (stacked on top of every
// OTHER package's own tests already running in parallel via `go test
// ./...`) was observed to occasionally starve a fresh spawn past its own
// readiness bound on a busy dev machine -- serializing just these three
// keeps peak concurrent spawns low without giving up real coverage.

func TestAdapter_ConnectsAndReceivesServerConnected(t *testing.T) {
	a := newAdapter(t)

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	if err := a.Connected(ctx); err != nil {
		t.Fatalf("Connected() error = %v, want the persistent SSE stream to observe server.connected", err)
	}
}

func TestResolveSession_CreatesNewSession(t *testing.T) {
	a := newAdapter(t)

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	cmd := sandboxws.Prompt{
		Type:      "prompt",
		MessageId: "m1",
		SessionId: testSessionID,
		Gen:       1,
		Text:      "irrelevant for session creation",
	}

	sessionID, err := a.resolveSession(ctx, cmd)
	if err != nil {
		t.Fatalf("resolveSession() error = %v, want a fresh OpenCode session id", err)
	}
	if sessionID == "" {
		t.Fatal("resolveSession() returned an empty session id")
	}

	// A SECOND call with cmd.ConversationId now set to the id just
	// returned must reuse it directly -- no new POST /session call (§3.3:
	// resuming a conversation reuses the same OpenCode sessionID).
	resumeCmd := cmd
	resumeCmd.ConversationId = &sessionID
	resumed, err := a.resolveSession(ctx, resumeCmd)
	if err != nil {
		t.Fatalf("resolveSession() (resume) error = %v", err)
	}
	if resumed != sessionID {
		t.Errorf("resolveSession() (resume) = %q, want the exact same id %q reused", resumed, sessionID)
	}
}

func TestAdapter_Stop_NothingInFlightDoesNotError(t *testing.T) {
	a := newAdapter(t)

	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()

	stop := sandboxws.Stop{Type: "stop", MessageId: "m1", SessionId: testSessionID, Gen: 1}

	// No session was ever created for this adapter -- Stop must be a safe,
	// local no-op (no HTTP call at all).
	if err := a.Stop(ctx, stop); err != nil {
		t.Fatalf("Stop() error = %v, want nil when no session/turn has ever existed", err)
	}

	// A session DOES exist (created below) but nothing is in flight on it
	// -- OpenCode's own /abort returns false, not an error.
	cmd := sandboxws.Prompt{Type: "prompt", MessageId: "m2", SessionId: testSessionID, Gen: 1, Text: "hi"}
	sessionID, err := a.resolveSession(ctx, cmd)
	if err != nil {
		t.Fatalf("resolveSession() error = %v", err)
	}
	a.setCurrentSession(sessionID)

	if err := a.Stop(ctx, stop); err != nil {
		t.Fatalf("Stop() error = %v, want nil when the session exists but nothing is in flight", err)
	}
}
