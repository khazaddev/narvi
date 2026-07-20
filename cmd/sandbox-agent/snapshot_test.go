// Real, in-process round-trip test for HandleSnapshot (Step 22,
// "snapshots & restore", design decision 4): a fake control-plane server
// backing BOTH the sandbox-WS endpoint and the new snapshot-mint endpoint,
// driving a real *wsbridge.Bridge connection -- unlike
// push_integration_test.go's own subprocess-based proof (needed there
// specifically because os.Executable() must resolve to the real compiled
// binary for git's own credential-helper re-invocation to work),
// HandleSnapshot needs no subprocess at all: it never shells out to git or
// re-invokes this binary, so calling it directly, in-process, against a
// real Bridge is both sufficient and simpler -- mirrors
// internal/sandboxagent/wsbridge's own bridge_test.go conventions (a real
// httptest.Server standing in for the control plane, a real Bridge.Run in
// the background) rather than a copy of push_integration_test.go's
// heavier subprocess machinery.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/wsbridge"
)

const snapshotTestWait = 5 * time.Second

// fakeSnapshotCP stands in for the real control plane's sandbox-WS
// endpoint AND its new snapshot-mint endpoint (design decision 2), for an
// in-process (no subprocess, no real Postgres) proof of HandleSnapshot's
// own full round trip: obtain a real snapshotId from a fake CP, then
// report it back as a real, critical snapshot_ready event over a real
// *wsbridge.Bridge connection.
type fakeSnapshotCP struct {
	server         *httptest.Server
	sessionID      string
	mintSnapshotID string
	// mintStatus, when non-zero, makes the mint endpoint respond with this
	// HTTP status (and a failure body) instead of a real 200 + snapshotId.
	mintStatus int
	frames     chan json.RawMessage
}

func newFakeSnapshotCP(t *testing.T, sessionID string) *fakeSnapshotCP {
	t.Helper()
	fcp := &fakeSnapshotCP{
		sessionID: sessionID,
		frames:    make(chan json.RawMessage, 8),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/sessions/"+sessionID+"/snapshot", func(w http.ResponseWriter, _ *http.Request) {
		if fcp.mintStatus != 0 {
			http.Error(w, "mint failed", fcp.mintStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"snapshotId": fcp.mintSnapshotID})
	})
	mux.HandleFunc("/sessions/"+sessionID+"/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		ctx := r.Context()
		if _, _, err := conn.Read(ctx); err != nil { // the bridge's own first "ready" event
			return
		}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			cp := make(json.RawMessage, len(data))
			copy(cp, data)
			select {
			case fcp.frames <- cp:
			default:
			}
		}
	})

	fcp.server = httptest.NewServer(mux)
	t.Cleanup(fcp.server.Close)
	return fcp
}

func (fcp *fakeSnapshotCP) wsURL() string {
	return "ws" + strings.TrimPrefix(fcp.server.URL, "http") + "/sessions/" + fcp.sessionID + "/ws?type=sandbox"
}

// waitForFrameType polls fcp.frames for one whose "type" matches want,
// within timeout -- any other frame type seen along the way (e.g. a
// heartbeat) is silently skipped, never mistaken for the one under test.
func waitForFrameType(t *testing.T, fcp *fakeSnapshotCP, want string, timeout time.Duration) json.RawMessage {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case data := <-fcp.frames:
			var peek struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(data, &peek); err != nil {
				continue
			}
			if peek.Type == want {
				return data
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %q frame", want)
			return nil
		}
	}
}

// newTestBridgeHandler builds a real *commandHandler + *wsbridge.Bridge
// pair against fcp's own fake server, running Bridge.Run in the
// background (t.Cleanup tears it down) -- mirrors main.go's own two-phase
// commandHandler/Bridge construction (commandHandler's own doc comment)
// exactly.
func newTestBridgeHandler(ctx context.Context, t *testing.T, fcp *fakeSnapshotCP, gen int) *commandHandler {
	t.Helper()

	cfg := sessionconfig.SessionConfig{
		BootMode:          sessionconfig.SessionConfigBootModeFresh,
		ControlPlaneWsUrl: fcp.wsURL(),
		Gen:               gen,
		SandboxToken:      "test-sandbox-token",
		SessionId:         fcp.sessionID,
	}
	timeouts := platform.DefaultTimeouts()

	handler := &commandHandler{runCtx: ctx, cfg: boot.Config{SessionConfig: &cfg}, timeouts: timeouts}
	bridge := wsbridge.New(cfg, "sbx-1", handler,
		timeouts.SandboxWSDialTimeout, timeouts.SandboxWSHeartbeatInterval,
		timeouts.SandboxWSReconnectMinBackoff, timeouts.SandboxWSReconnectMaxBackoff)
	handler.bridge = bridge

	var group errgroup.Group
	group.Go(func() error { return bridge.Run(ctx) })
	t.Cleanup(func() { _ = group.Wait() })

	return handler
}

// TestHandleSnapshot_Success_SendsRealSnapshotReady proves the full round
// trip: a real Mint call against the fake CP's snapshot-mint endpoint
// succeeds, and HandleSnapshot sends back a real, schema-valid, CRITICAL
// snapshot_ready event over the real Bridge connection carrying the exact
// id the fake CP returned.
func TestHandleSnapshot_Success_SendsRealSnapshotReady(t *testing.T) {
	fcp := newFakeSnapshotCP(t, "snapshot-success-session")
	fcp.mintSnapshotID = "snap-real-abc"

	ctx, cancel := context.WithTimeout(context.Background(), snapshotTestWait)
	t.Cleanup(cancel)

	handler := newTestBridgeHandler(ctx, t, fcp, 3)

	handler.HandleSnapshot(ctx, sandboxws.Snapshot{
		Type: "snapshot", MessageId: "msg-1", SessionId: fcp.sessionID, Gen: 3,
	})

	raw := waitForFrameType(t, fcp, "snapshot_ready", snapshotTestWait)

	var got sandboxws.SnapshotReady
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal SnapshotReady: %v (raw: %s)", err, raw)
	}
	if got.SnapshotId != "snap-real-abc" {
		t.Errorf("SnapshotId = %q, want %q", got.SnapshotId, "snap-real-abc")
	}
	if got.SessionId != fcp.sessionID {
		t.Errorf("SessionId = %q, want %q", got.SessionId, fcp.sessionID)
	}
	if got.Gen != 3 {
		t.Errorf("Gen = %d, want 3", got.Gen)
	}
	if got.AckId != "snapshot_ready:"+got.MessageId {
		t.Errorf("AckId = %q, want %q", got.AckId, "snapshot_ready:"+got.MessageId)
	}
	if got.MessageId == "" {
		t.Error("MessageId is empty, want a freshly minted uuid")
	}
	// Message-id correlation fix: CommandMessageId must echo the Snapshot
	// command's own MessageId verbatim ("msg-1" above), NOT this event's
	// own freshly-minted MessageId -- the control plane's own
	// handleSnapshotReadyEvent needs this to tell two snapshot attempts on
	// the same live sandbox apart (gen alone can't).
	if got.CommandMessageId == nil {
		t.Fatal("CommandMessageId is nil, want it to echo the Snapshot command's own MessageId")
	}
	if *got.CommandMessageId != "msg-1" {
		t.Errorf("CommandMessageId = %q, want %q", *got.CommandMessageId, "msg-1")
	}
}

// TestHandleSnapshot_MintFailure_SendsNothing proves design decision 2's
// own honest, documented failure-reporting gap: a mint-endpoint failure
// makes HandleSnapshot log and return, sending NOTHING over the bridge --
// no snapshot_ready, no crash, no hang (there is no NACK-shaped event on
// the wire to report this failure with).
func TestHandleSnapshot_MintFailure_SendsNothing(t *testing.T) {
	fcp := newFakeSnapshotCP(t, "snapshot-failure-session")
	fcp.mintStatus = http.StatusInternalServerError

	ctx, cancel := context.WithTimeout(context.Background(), snapshotTestWait)
	t.Cleanup(cancel)

	handler := newTestBridgeHandler(ctx, t, fcp, 1)

	handler.HandleSnapshot(ctx, sandboxws.Snapshot{
		Type: "snapshot", MessageId: "msg-1", SessionId: fcp.sessionID, Gen: 1,
	})

	select {
	case data := <-fcp.frames:
		var peek struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(data, &peek)
		t.Fatalf("HandleSnapshot sent a frame after a mint failure (type=%q), want nothing", peek.Type)
	case <-time.After(500 * time.Millisecond):
		// Expected: nothing sent. A short, fixed wait, not a poll-for-a-
		// positive-signal -- there is nothing to positively wait FOR when
		// asserting something did NOT happen (same reasoning
		// dispatch_integration_test.go's own circuit-breaker-blocks-spawn
		// test uses its own fixed sleep for).
	}
}
