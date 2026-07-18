package wsbridge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
)

// CommandHandler is the pluggable dispatch target for the 5 business
// commands this package does NOT implement the actual behavior of --
// "ack" and "shutdown" are both handled internally by Run (see Run's own
// doc comment), never exposed here. Every method is handed the already
// gen-checked, concretely-typed command.
type CommandHandler interface {
	HandlePrompt(ctx context.Context, cmd sandboxws.Prompt)
	HandleStop(ctx context.Context, cmd sandboxws.Stop)
	HandlePush(ctx context.Context, cmd sandboxws.Push)
	HandleSnapshot(ctx context.Context, cmd sandboxws.Snapshot)
	HandleGitSyncComplete(ctx context.Context, cmd sandboxws.GitSyncComplete)
}

// FatalConnectError is returned by Run when the WS handshake itself
// returns 401/403/404/410 (§6.1: "Agent treats 401/403/404/410 as fatal (no
// retry)"). Any OTHER connect failure (network error, 5xx, timeout) is
// retried with exponential backoff instead of ever surfacing as an error
// from Run.
type FatalConnectError struct {
	Status int
}

func (e *FatalConnectError) Error() string {
	return fmt.Sprintf("wsbridge: fatal connect status %d (no retry, §6.1)", e.Status)
}

// ErrShutdownRequested is returned by Run when a "shutdown" command was
// received over the bridge -- distinct from ctx being canceled (an OS
// signal) so the caller (main.go) can tell the two apart if it ever needs
// to, even though both currently converge on the same graceful-shutdown
// sequence.
var ErrShutdownRequested = errors.New("wsbridge: shutdown requested by control plane")

// Bridge is one session's own sandbox-WS client: connection lifecycle
// (dial, fatal-status classification, exponential-backoff reconnect),
// the ack-protocol outbound buffer, heartbeat/boot_progress reporting, and
// inbound command dispatch. Build one with New; drive it with Run.
type Bridge struct {
	dialURL      string
	sandboxToken string
	sandboxID    string
	sessionID    string
	sessionGen   int
	handler      CommandHandler

	dialTimeout         time.Duration
	heartbeatInterval   time.Duration
	reconnectMinBackoff time.Duration
	reconnectMaxBackoff time.Duration

	buffer *outboundBuffer

	// connMu guards conn, the CURRENT live connection (nil when
	// disconnected) -- read by SendCritical/SendBestEffort, which may be
	// called concurrently with Run's own reconnect loop swapping it out.
	connMu sync.RWMutex
	conn   *websocket.Conn

	// bootMu guards lastBootPhase, read by the heartbeat loop and written
	// by SendBootProgress/MarkBootComplete.
	bootMu        sync.Mutex
	lastBootPhase *string
}

// New builds a Bridge for one session, from its full SessionConfig (dial
// URL = sc.ControlPlaneWsUrl VERBATIM -- unlike Step 15's scm-credentials
// derivation, this field IS already the real WS connect target, no URL
// surgery needed) and a CommandHandler for the 5 business commands.
// sandboxID is this Step's own invented, HONEST-GAP value for the
// X-Sandbox-ID header -- see doc.go.
func New(
	sc sessionconfig.SessionConfig,
	sandboxID string,
	handler CommandHandler,
	dialTimeout, heartbeatInterval, reconnectMinBackoff, reconnectMaxBackoff time.Duration,
) *Bridge {
	return &Bridge{
		dialURL:      sc.ControlPlaneWsUrl,
		sandboxToken: sc.SandboxToken,
		sandboxID:    sandboxID,
		sessionID:    sc.SessionId,
		sessionGen:   sc.Gen,
		handler:      handler,

		dialTimeout:         dialTimeout,
		heartbeatInterval:   heartbeatInterval,
		reconnectMinBackoff: reconnectMinBackoff,
		reconnectMaxBackoff: reconnectMaxBackoff,

		buffer: newOutboundBuffer(),
	}
}

func (b *Bridge) getConn() *websocket.Conn {
	b.connMu.RLock()
	defer b.connMu.RUnlock()
	return b.conn
}

func (b *Bridge) setConn(c *websocket.Conn) {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	b.conn = c
}

func (b *Bridge) getLastBootPhase() *string {
	b.bootMu.Lock()
	defer b.bootMu.Unlock()
	return b.lastBootPhase
}

func (b *Bridge) setLastBootPhase(phase *string) {
	b.bootMu.Lock()
	defer b.bootMu.Unlock()
	b.lastBootPhase = phase
}

// MarkBootComplete clears the tracked lastBootPhase back to nil --
// events.schema.json's own Heartbeat.LastBootPhase doc comment: "Null once
// boot has completed (no more boot phases to report)." Call this once
// main.go's own boot.RunBoot call returns successfully.
func (b *Bridge) MarkBootComplete() {
	b.setLastBootPhase(nil)
}

// newMessageID mints a fresh messageId for a Bridge-originated event
// (ready/heartbeat/boot_progress) -- SendCritical/SendBestEffort's own
// caller-supplied msg already carries its own messageId, so this is never
// used for those.
func (b *Bridge) newMessageID() string {
	return uuid.NewString()
}
