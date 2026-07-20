// This file (timeouts.go) is the single source of truth for timeout/
// interval values in the system (§5.4, §11: "every new timeout/interval
// goes in platform/timeouts.go... no timeout literal anywhere else in the
// codebase"). The lint rule in tools/lint/narvichecks/notimeliteral
// enforces that by forbidding time.Duration unit literals (time.Second,
// time.Hour, ...) everywhere except this package and _test.go files.
//
// PR-02 scope is deliberately narrow: exactly the two invariant chains
// §5.4 names (the PR title's own parenthetical: "provider cap > supervisor
// > bridge > SSE", plus the HTTP-client/cold-start and
// first-connect/image-pull pairs from §4.1/§3.2). Timeouts consumed by
// later PRs (heartbeat intervals, terminal_grace, ws-token TTL, HMAC
// window, reconciler interval, ...) are added by the PR that first
// consumes them, not speculatively here.

package platform

import (
	"errors"
	"fmt"
	"time"
)

// MinTimeoutMargin is the minimum gap Validate requires between every
// adjacent pair in the timeout hierarchy (§5.4: "each with explicit
// margin"). Not given an explicit figure in the plan; 30s is a round,
// conservative floor relative to a hierarchy whose links span from
// seconds (SSE inactivity) to hours (provider hard cap).
const MinTimeoutMargin = 30 * time.Second

// Timeouts is the single struct holding every timeout/interval this PR
// covers, wired into two independent invariant chains:
//
//	Chain A (provider cap > supervisor > bridge > SSE):
//	  ProviderHardCap > SupervisorTurnCap > TurnDeadline > SSEInactivityTimeout
//
//	Chain B (two independent pairs, §4.1 / §3.2):
//	  ProviderHTTPClientTimeout > ProviderWorstColdStart
//	  FirstConnectBudget        > ImagePullBootP99
//
// Validate checks every pairwise link in both chains and requires at
// least MinTimeoutMargin of headroom, returning ALL violations found
// (joined), not just the first.
type Timeouts struct {
	// --- Chain A: provider cap > supervisor turn cap > CP turn_deadline > OpenCode SSE inactivity ---

	// ProviderHardCap is the absolute ceiling a sandbox provider enforces
	// on a running sandbox. §5.4 gives this explicitly: 2h.
	ProviderHardCap time.Duration

	// SupervisorTurnCap is the control plane's own cap on a single turn,
	// enforced independently of (and comfortably below) the provider's
	// hard cap, so the CP always terminates a runaway turn before the
	// provider would forcibly reclaim the sandbox. Not given an explicit
	// value in the plan; chosen as 90m, well below the 2h provider cap.
	SupervisorTurnCap time.Duration

	// TurnDeadline is the CP's turn_deadline named persistent timer
	// (§3.3 "Dispatch arms turn_deadline"; §2 "Named persistent timers").
	// Not given an explicit value in the plan; chosen as 60m, below
	// SupervisorTurnCap, so the per-turn deadline always fires before the
	// coarser supervisor cap would.
	TurnDeadline time.Duration

	// SSEInactivityTimeout is the OpenCode SSE inactivity timeout (§7:
	// "SSE inactivity timeout configurable (default 120s)").
	SSEInactivityTimeout time.Duration

	// --- Chain B: provider HTTP client timeout / cold start, first-connect budget / image pull+boot ---

	// ProviderHTTPClientTimeout is the HTTP client timeout used for calls
	// to the sandbox provider's API. §4.1: "The provider HTTP client
	// timeout MUST exceed the provider's worst cold-start." Not given an
	// explicit value; chosen as 5m, comfortably above ProviderWorstColdStart.
	ProviderHTTPClientTimeout time.Duration

	// ProviderWorstColdStart is our estimate of the worst-case provider
	// cold-start latency. §4.1: "Modal cold scheduling alone can take
	// 220s+" — taken as the stated floor.
	ProviderWorstColdStart time.Duration

	// FirstConnectBudget is the liveness budget covering provider cold
	// start + boot before the first sandbox WS connection. §3.2 gives this
	// explicitly: "first_connect_budget (default 240s, covers provider
	// cold start + boot)".
	FirstConnectBudget time.Duration

	// ImagePullBootP99 is our estimate of the p99 latency of image pull +
	// container boot that FirstConnectBudget must clear with margin. Not
	// given an explicit figure in the plan; chosen as 90s.
	ImagePullBootP99 time.Duration

	// --- PR-06 standalone additions: no ordering relationship with the
	// two chains above, so (per that PR's own instructions) not wired into
	// a fake invariant link — just plain fields with sensible defaults.

	// HMACWindow is the internal-auth HMAC freshness window (§5.2,
	// explicit: "bearer timestamp.signature, 5-min window, fail closed").
	HMACWindow time.Duration

	// ShutdownGracePeriod bounds how long `narvi serve` waits for
	// in-flight requests to drain (via http.Server.Shutdown) after
	// receiving SIGINT/SIGTERM before giving up. Not specified in the
	// plan; chosen as 10s (invented).
	ShutdownGracePeriod time.Duration

	// HealthCheckTimeout bounds how long the /health handler waits on
	// pool.Ping before reporting unhealthy, so a stuck DB never hangs the
	// handler past this. Not specified in the plan; chosen as 2s
	// (invented).
	HealthCheckTimeout time.Duration

	// --- Step 07 standalone additions: no ordering relationship with the
	// two chains above (or with the PR-06 additions), so — per that PR's
	// own precedent — just plain fields with sensible defaults, not wired
	// into a fake invariant link.

	// SteadyHeartbeatBudget is the liveness budget for a sandbox that has
	// already shown at least one sign of life (heartbeat or boot-progress
	// ping), distinct from FirstConnectBudget above. §3.2 gives this
	// explicitly: "steady_heartbeat_budget (default 90s; heartbeats every
	// 30s)".
	SteadyHeartbeatBudget time.Duration

	// TerminalGracePeriod is how long a sandbox stays in "suspect" before
	// a watchdog's silence/timeout is treated as genuinely dead (§3.2:
	// "two-phase terminalization: a watchdog never writes failed directly.
	// It writes suspect and arms terminal_grace (default 60s)").
	TerminalGracePeriod time.Duration

	// CircuitBreakerWindow is the sliding window the sandbox spawn circuit
	// breaker counts permanent failures within. §3.2 gives this explicitly:
	// "3 permanent spawn failures within 5 min blocks spawning". The
	// companion threshold (3) is a plain int, not a duration, so it lives
	// as a named constant in domain/sandbox instead of here.
	CircuitBreakerWindow time.Duration

	// SpawnCooldown is the minimum interval between spawn attempts (bypassed
	// for failed/stopped sandboxes). Not given an explicit figure in the
	// plan; chosen as 30s.
	SpawnCooldown time.Duration

	// SpawnReadyWait is how long a sandbox that reports "ready" without an
	// active WebSocket is given to reconnect before a fresh spawn is
	// considered. Not given an explicit figure in the plan; chosen as 60s.
	SpawnReadyWait time.Duration

	// SpawnStuckTimeout is the max time a sandbox may remain in a
	// spawning/connecting-style status before it is treated as dead (an
	// interrupted spawn) and a fresh spawn is allowed. Not given an explicit
	// figure in the plan; chosen as 120s.
	SpawnStuckTimeout time.Duration

	// InactivityTimeout is how long a ready, non-processing sandbox may go
	// without activity before it is stopped (and snapshotted). Not given an
	// explicit figure in the plan; chosen as 10min.
	InactivityTimeout time.Duration

	// InactivityExtension is the additional time granted (with a warning)
	// when the inactivity timeout fires but clients are still connected.
	// Not given an explicit figure in the plan; chosen as 5min.
	InactivityExtension time.Duration

	// InactivityMinCheckInterval is the minimum interval between inactivity
	// alarm checks. Not given an explicit figure in the plan; chosen as 30s.
	InactivityMinCheckInterval time.Duration

	// --- Step 11 standalone additions: no ordering relationship with
	// either invariant chain above (or with the PR-06/Step 07 additions),
	// so — per those additions' own precedent — plain fields with
	// sensible defaults, not wired into a fake invariant link.

	// ActorIdleTTL is how long a session actor may go without processing
	// any command before it evicts itself (§2, explicit: "evicts after
	// idle TTL (default 30 min without commands or connected clients)").
	ActorIdleTTL time.Duration

	// TimerPumpInterval is how often the per-pod timer pump
	// (app/sessionactor) polls session_timers for due rows (§2: "A
	// per-pod timer pump polls due timers"). Not given an explicit figure
	// in the plan; chosen as 5s — near-real-time timer delivery without
	// hammering Postgres with a poll query.
	TimerPumpInterval time.Duration

	// TimerClaimDuration is how long a timer the pump has just claimed
	// (pushed fires_at forward, see app/sessionactor's timer pump) is
	// protected from being picked up again by a concurrent or later pump
	// tick before the actor handling it finishes. Not given an explicit
	// figure in the plan; chosen as 30s — comfortably longer than a
	// single actor's expected processing time for one timer, but short
	// enough that a genuinely crashed pod's claimed timer is retried
	// reasonably quickly.
	TimerClaimDuration time.Duration

	// --- Step 13 standalone additions: no ordering relationship with
	// either invariant chain above (or with any prior Step's standalone
	// additions), so — per those additions' own precedent — plain fields
	// with sensible defaults, not wired into a fake invariant link.

	// HookTimeout is the max wall-clock time a single boot hook
	// (setup.sh/start.sh, §6.4) may run before sandbox-agent kills it. Not
	// specified in the plan; chosen generously as 10min since setup.sh may
	// install dependencies.
	HookTimeout time.Duration

	// ProcessStopGracePeriod is the grace period between SIGTERM and
	// SIGKILL when sandbox-agent's supervisor (internal/sandboxagent/
	// supervisor) stops one supervised process group. Not specified in the
	// plan; chosen as 10s — matches the existing, unrelated
	// ShutdownGracePeriod's own value (control-plane's own HTTP graceful
	// shutdown) only by coincidence; this is a distinct field for a
	// distinct subsystem, never reused across the two.
	ProcessStopGracePeriod time.Duration

	// SupervisorShutdownTimeout is the outer bound across ALL of
	// sandbox-agent's supervised processes during its own bounded
	// shutdown (Supervisor.StopAll), distinct from the per-process
	// ProcessStopGracePeriod above — this is the ceiling for StopAll as a
	// whole, not for any single process within it. Not specified in the
	// plan; chosen as 30s.
	SupervisorShutdownTimeout time.Duration

	// RepoSHADiscoveryTimeout bounds each individual `git -C <dir>
	// rev-parse HEAD` call sandbox-agent's boot-fingerprint assembly
	// (internal/sandboxagent/boot.DiscoverRepoSHAs) makes per repo — a
	// very minor, sub-second local git-plumbing call with no natural
	// existing Timeouts field. Not specified in the plan; chosen as 5s.
	RepoSHADiscoveryTimeout time.Duration

	// --- Step 14 standalone additions: no ordering relationship with
	// either invariant chain above (or with any prior Step's standalone
	// additions), so — per those additions' own precedent — plain fields
	// with sensible defaults, not wired into a fake invariant link.

	// ServiceReadinessTimeout bounds how long
	// internal/sandboxagent/services.Run waits for ONE declared
	// services.yml service (§14.2) to become ready (port dial or HTTP
	// health check succeeding) before giving up on it (PhaseTimeout). Not
	// specified in the plan; chosen as 30s — generous enough for a
	// typical dev-server/mock-server cold start without being so long
	// that a primary service's timeout stalls the whole boot sequence
	// for an unreasonable time.
	ServiceReadinessTimeout time.Duration

	// ServiceReadinessPollInterval is how often
	// internal/sandboxagent/services.Run retries a service's readiness
	// check while waiting. Not specified in the plan; chosen as 250ms —
	// frequent enough that readiness is detected promptly relative to
	// ServiceReadinessTimeout's 30s budget, without hammering the port/
	// health endpoint.
	ServiceReadinessPollInterval time.Duration

	// --- Step 15 standalone additions: no ordering relationship with
	// either invariant chain above (or with any prior Step's standalone
	// additions), so — per those additions' own precedent — plain fields
	// with sensible defaults, not wired into a fake invariant link.

	// RepoCloneTimeout bounds how long a single `git clone` invocation
	// (internal/sandboxagent/gitclone.CloneAll) may run before
	// sandbox-agent kills it. Not specified in the plan; chosen
	// generously as 5m since a large repo's initial clone can be slow.
	RepoCloneTimeout time.Duration

	// CredentialFetchTimeout bounds a single credential-helper call to CP
	// (internal/sandboxagent/credentials.CPClient.Fetch) minting a fresh
	// git credential. Not specified in the plan; chosen as 10s — a
	// lightweight mint call, not a large data transfer.
	CredentialFetchTimeout time.Duration

	// CredentialExpiryBuffer is the credential-helper's cache staleness
	// buffer (§5.2, explicit: "caches to disk with flock, 5-min expiry
	// buffer"): a cached credential within this buffer of its own
	// ExpiresAt is treated as already stale, never handed back as-is.
	CredentialExpiryBuffer time.Duration

	// --- Step 16 standalone additions: no ordering relationship with
	// either invariant chain above (or with any prior Step's standalone
	// additions), so — per those additions' own precedent — plain fields
	// with sensible defaults, not wired into a fake invariant link.

	// SandboxWSHeartbeatInterval is how often
	// internal/sandboxagent/wsbridge.Bridge sends a "heartbeat" event over
	// the sandbox WS connection. §6.1 gives this explicitly: "heartbeat
	// (30s, ...)" — unlike the other three fields in this group, this one
	// is not invented.
	SandboxWSHeartbeatInterval time.Duration

	// SandboxWSDialTimeout bounds a single sandbox-WS connect attempt
	// (internal/sandboxagent/wsbridge.Bridge.Run's call to
	// websocket.Dial). Not specified in the plan; chosen as 15s — generous
	// for a handshake round trip without letting one stuck attempt stall
	// the reconnect loop for long.
	SandboxWSDialTimeout time.Duration

	// SandboxWSReconnectMinBackoff is the initial (and floor) backoff
	// between sandbox-WS reconnect attempts after a non-fatal connect
	// failure (§6.1: "else exponential-backoff reconnect"). Not specified
	// in the plan; chosen as 1s.
	SandboxWSReconnectMinBackoff time.Duration

	// SandboxWSReconnectMaxBackoff is the ceiling the exponential backoff
	// above is capped at. Not specified in the plan; chosen as 30s.
	SandboxWSReconnectMaxBackoff time.Duration

	// --- Step 17 standalone additions: no ordering relationship with
	// either invariant chain above (or with any prior Step's standalone
	// additions), so — per those additions' own precedent — plain fields
	// with sensible defaults, not wired into a fake invariant link.
	// SSEInactivityTimeout (Chain A, already above) is deliberately REUSED
	// for the OpenCode adapter's own SSE-inactivity fallback (§7) rather
	// than duplicated here — it already exists specifically for this.

	// OpenCodeReadinessTimeout bounds how long
	// internal/sandboxagent/opencodeproc waits for a freshly-spawned
	// `opencode serve` process to report healthy (GET /api/health) before
	// giving up. Not specified in the plan; chosen as 30s — OpenCode's own
	// startup may need to initialize providers/plugins, generous by the
	// same reasoning as ServiceReadinessTimeout above.
	OpenCodeReadinessTimeout time.Duration

	// OpenCodeReadinessPollInterval is how often
	// internal/sandboxagent/opencodeproc retries the health check while
	// waiting. Not specified in the plan; chosen as 250ms, matching
	// ServiceReadinessPollInterval's own precedent exactly.
	OpenCodeReadinessPollInterval time.Duration

	// --- Step 18 standalone additions: no ordering relationship with
	// either invariant chain above (or with any prior Step's standalone
	// additions), so — per those additions' own precedent — a plain field
	// with a sensible default, not wired into a fake invariant link.

	// SandboxEventAckTimeout bounds how long
	// internal/adapters/inbound/wshub's read loop waits for the session
	// actor's own reply (via the per-message SandboxEvent.Reply channel,
	// internal/app/sessionactor) on ONE inbound sandbox-WS event before
	// giving up on acking THAT message and moving on to the next frame.
	// Not specified in the plan; chosen as 5s — generous relative to a
	// single Postgres transaction (the actor's own handleSandboxEvent),
	// small relative to SandboxWSHeartbeatInterval's 30s so a lost/slow
	// ack is noticed and abandoned well before the sandbox-agent's own
	// heartbeat cadence would otherwise mask it.
	SandboxEventAckTimeout time.Duration

	// --- Step 19 standalone additions: no ordering relationship with
	// either invariant chain above (or with any prior Step's standalone
	// additions), so — per those additions' own precedent — plain fields
	// with sensible defaults, not wired into a fake invariant link.

	// ClientSubscribeTimeout bounds how long internal/adapters/inbound/
	// wshub's client-WS handshake waits for the browser's first inbound
	// message (the subscribe{token, clientId} frame) before closing the
	// connection with code 4001 ("re-auth required"). §6.2 gives this
	// explicitly: "subscribe{token, clientId} within 30s".
	ClientSubscribeTimeout time.Duration

	// WSTokenTTL is how long a minted ws-token (internal/platform.
	// GenerateToken, POST /api/sessions/:id/ws-token) remains valid before
	// the client hub's own subscribe-time verification rejects it with
	// close code 4002 ("token expired"). §6.2 gives this explicitly: "24h
	// TTL".
	WSTokenTTL time.Duration

	// --- Step 20 standalone additions: no ordering relationship with
	// either invariant chain above (or with any prior Step's standalone
	// additions), so — per those additions' own precedent — plain fields
	// with sensible defaults, not wired into a fake invariant link.

	// OAuthStateTTL is how long the short-lived narvi_oauth_state cookie
	// (internal/adapters/inbound/auth's own login handler, §13.1) remains
	// valid before the CSRF-protection state value it carries is
	// considered expired (enforced by the cookie's own MaxAge/Expires, so
	// an abandoned login attempt's browser simply stops sending it). Not
	// specified in the plan; chosen as 10min — generous for a real browser
	// round-trip to GitHub and back, short enough that an abandoned login
	// attempt's own state cookie doesn't linger.
	OAuthStateTTL time.Duration

	// UserSessionTTL is how long a minted user-session (internal/platform.
	// GenerateToken, the narvi_auth_session cookie, §13.1) remains valid
	// before internal/adapters/inbound/auth's own Middleware rejects it.
	// Not specified in the plan; chosen as 30 days — a fairly standard
	// "stay signed in" web-app session length given GitHub itself is the
	// re-authentication backstop and no MFA/step-up flow exists to shorten
	// it for.
	UserSessionTTL time.Duration

	// --- Step 21 standalone additions ("e2e happy path"): no ordering
	// relationship with either invariant chain above (or with any prior
	// Step's standalone additions), so — per those additions' own
	// precedent — plain fields with sensible defaults, not wired into a
	// fake invariant link.

	// SandboxCommandSendTimeout bounds one internal/app/ports.
	// SandboxCommander.SendCommand write (internal/adapters/inbound/
	// wshub's own SandboxRegistry) -- generous for a single WS frame
	// write, small enough that a genuinely-dead connection is noticed
	// promptly. Not specified in the plan; chosen as 10s.
	SandboxCommandSendTimeout time.Duration

	// ScmCredentialTTL is the expiry window internal/adapters/inbound/
	// httpapi's scm-credentials endpoint mints for each credential it
	// hands back. §5.2 gives the SANDBOX-side cache's own staleness
	// buffer explicitly ("5-min expiry buffer") — this is a DIFFERENT
	// concept: the server-minted credential's own lifetime, which must
	// comfortably exceed a single push operation's realistic duration
	// plus that 5-min sandbox-side cache buffer. Not specified in the
	// plan; chosen as 15min.
	ScmCredentialTTL time.Duration

	// PRCreateTimeout bounds a single internal/app/ports.SourceControl.
	// CreatePR call (internal/adapters/outbound/githubapi's own real POST
	// to api.github.com, called by app/sessionactor's own
	// createPRBestEffort, pushpr.go) -- a genuine outbound network call
	// that must never run against the actor's own long-lived lifecycle
	// context unbounded. Not specified in the plan; chosen as 30s,
	// generous for a single GitHub REST API POST.
	PRCreateTimeout time.Duration

	// --- Audit-remediation (outbound-adapters lens, turn-completion
	// batch) standalone additions: no ordering relationship with either
	// invariant chain above (or with any prior Step's standalone
	// additions), so -- per those additions' own precedent -- plain
	// fields with sensible defaults, not wired into a fake invariant
	// link.

	// OpenCodeSSEReconnectInterval is how long
	// internal/adapters/outbound/opencode.Adapter.runEventLoop waits
	// before retrying a dropped persistent GET /event connection.
	// Deliberately much shorter than SSEInactivityTimeout so a dropped
	// connection has a real chance to reconnect well before any per-turn
	// SSE-inactivity fallback finalizes a turn based on stale silence --
	// fixes a confirmed audit finding where reusing SSEInactivityTimeout
	// itself as the reconnect delay made reconnection structurally
	// unable to ever win that race. Not specified in the plan; chosen as
	// 2s.
	OpenCodeSSEReconnectInterval time.Duration

	// OpenCodeRequestTimeout bounds every internal/adapters/outbound/
	// opencode.Adapter.doJSON-routed HTTP call (session resolution, model
	// catalog, prompt_async, abort, and the final-message-fetch fallback)
	// via a per-request context.WithTimeout wrap -- deliberately NOT
	// applied to the adapter's own persistent GET /event connection
	// (connectAndConsume), which is intentionally long-lived for the
	// adapter's whole lifetime. Generous enough for a legitimately slow
	// catalog/message-list response, bounded so a hung TCP connection can
	// never wedge a turn indefinitely -- most critically for the
	// SSE-inactivity fallback's own final-message fetch, which is called
	// exactly when a turn already looks stuck and must never itself be
	// able to hang forever. Not specified in the plan; chosen as 30s.
	OpenCodeRequestTimeout time.Duration

	// --- Audit-remediation (inbound-hygiene lens, WS/REST hygiene batch)
	// standalone additions: no ordering relationship with either
	// invariant chain above (or with any prior Step's standalone
	// additions), so -- per those additions' own precedent -- plain
	// fields with sensible defaults, not wired into a fake invariant
	// link.

	// ClientWSPingInterval is how often internal/adapters/inbound/wshub's
	// client-WS handler (client.go, NewClientHandler) sends a real,
	// server-initiated websocket ping to a subscribed browser connection
	// and waits (bounded by this SAME duration) for the peer's pong --
	// the genuine liveness check for a client connection that only ever
	// passively watches live broadcasts and so never itself sends an
	// application frame. A Ping that goes unanswered proves the
	// connection is genuinely unresponsive, closed with custom code 4003
	// ("idle timeout"). Not specified in the plan; chosen as 30s,
	// matching SandboxWSHeartbeatInterval's own existing cadence
	// precedent (§6.1) for the analogous sandbox-side mechanism.
	ClientWSPingInterval time.Duration

	// ClientFetchHistoryMinInterval is the minimum time internal/adapters/
	// inbound/wshub's client-WS read loop (client.go, readClientLoop)
	// requires between two successive fetch_history requests it actually
	// processes on one connection -- each processed request runs a real
	// Postgres query (events.ListForSession), so this bounds how often a
	// single connection can trigger one, independent of the connection's
	// own liveness. A fetch_history frame arriving before this interval
	// has elapsed since the last one was processed is logged and dropped;
	// the connection stays open. Not specified in the plan; chosen as
	// 250ms -- generous for any real pagination UI (up to 4 requests/sec)
	// while preventing a tight-loop hammer.
	ClientFetchHistoryMinInterval time.Duration
}

// DefaultTimeouts returns the shipped defaults for every field, each
// justified above on the struct field and (briefly) inline here.
func DefaultTimeouts() Timeouts {
	return Timeouts{
		ProviderHardCap:           2 * time.Hour,     // §5.4, explicit
		SupervisorTurnCap:         90 * time.Minute,  // not specified; chosen with margin below ProviderHardCap
		TurnDeadline:              60 * time.Minute,  // not specified; chosen with margin below SupervisorTurnCap
		SSEInactivityTimeout:      120 * time.Second, // §7, explicit
		ProviderHTTPClientTimeout: 5 * time.Minute,   // not specified; must clear ProviderWorstColdStart (§4.1) with margin
		ProviderWorstColdStart:    220 * time.Second, // §4.1, "220s+" floor
		FirstConnectBudget:        240 * time.Second, // §3.2, explicit
		ImagePullBootP99:          90 * time.Second,  // not specified; chosen with margin below FirstConnectBudget

		HMACWindow:          5 * time.Minute,  // §5.2, explicit
		ShutdownGracePeriod: 10 * time.Second, // not specified; invented
		HealthCheckTimeout:  2 * time.Second,  // not specified; invented

		SteadyHeartbeatBudget:      90 * time.Second,  // §3.2, explicit
		TerminalGracePeriod:        60 * time.Second,  // §3.2, explicit
		CircuitBreakerWindow:       5 * time.Minute,   // §3.2, explicit
		SpawnCooldown:              30 * time.Second,  // not specified; chosen
		SpawnReadyWait:             60 * time.Second,  // not specified; chosen
		SpawnStuckTimeout:          120 * time.Second, // not specified; chosen
		InactivityTimeout:          10 * time.Minute,  // not specified; chosen
		InactivityExtension:        5 * time.Minute,   // not specified; chosen
		InactivityMinCheckInterval: 30 * time.Second,  // not specified; chosen

		ActorIdleTTL:       30 * time.Minute, // §2, explicit
		TimerPumpInterval:  5 * time.Second,  // not specified; chosen
		TimerClaimDuration: 30 * time.Second, // not specified; chosen

		HookTimeout:               10 * time.Minute, // not specified; chosen generously (setup.sh may install deps)
		ProcessStopGracePeriod:    10 * time.Second, // not specified; chosen
		SupervisorShutdownTimeout: 30 * time.Second, // not specified; chosen
		RepoSHADiscoveryTimeout:   5 * time.Second,  // not specified; chosen

		ServiceReadinessTimeout:      30 * time.Second,       // not specified; chosen
		ServiceReadinessPollInterval: 250 * time.Millisecond, // not specified; chosen

		RepoCloneTimeout:       5 * time.Minute,  // not specified; chosen generously (large repos)
		CredentialFetchTimeout: 10 * time.Second, // not specified; chosen (lightweight mint call)
		CredentialExpiryBuffer: 5 * time.Minute,  // §5.2, explicit

		SandboxWSHeartbeatInterval:   30 * time.Second, // §6.1, explicit
		SandboxWSDialTimeout:         15 * time.Second, // not specified; chosen
		SandboxWSReconnectMinBackoff: 1 * time.Second,  // not specified; chosen
		SandboxWSReconnectMaxBackoff: 30 * time.Second, // not specified; chosen

		OpenCodeReadinessTimeout:      30 * time.Second,       // not specified; chosen (OpenCode startup may init providers/plugins)
		OpenCodeReadinessPollInterval: 250 * time.Millisecond, // not specified; chosen, matches ServiceReadinessPollInterval

		SandboxEventAckTimeout: 5 * time.Second, // not specified; chosen

		ClientSubscribeTimeout: 30 * time.Second, // §6.2, explicit
		WSTokenTTL:             24 * time.Hour,   // §6.2, explicit

		OAuthStateTTL:  10 * time.Minute,    // not specified; chosen
		UserSessionTTL: 30 * 24 * time.Hour, // not specified; chosen ("stay signed in" duration)

		SandboxCommandSendTimeout: 10 * time.Second, // not specified; chosen
		ScmCredentialTTL:          15 * time.Minute, // not specified; chosen (comfortably exceeds a single push + the 5-min sandbox-side cache buffer)
		PRCreateTimeout:           30 * time.Second, // not specified; chosen (generous for a single GitHub REST API POST)

		OpenCodeSSEReconnectInterval: 2 * time.Second,  // not specified; chosen, deliberately short relative to SSEInactivityTimeout
		OpenCodeRequestTimeout:       30 * time.Second, // not specified; chosen, bounds every doJSON call except the persistent SSE connection

		ClientWSPingInterval:          30 * time.Second,       // not specified; chosen, matches SandboxWSHeartbeatInterval's own 30s cadence (§6.1)
		ClientFetchHistoryMinInterval: 250 * time.Millisecond, // not specified; chosen, generous for real pagination while blocking a tight-loop hammer
	}
}

// TimeoutInvariantError reports one broken pairwise link in the timeout
// hierarchy: LesserValue is not at least RequiredMargin below GreaterValue.
type TimeoutInvariantError struct {
	// Chain names the pairwise relationship, e.g.
	// "ProviderHardCap > SupervisorTurnCap".
	Chain string

	LesserField  string
	LesserValue  time.Duration
	GreaterField string
	GreaterValue time.Duration

	RequiredMargin time.Duration
}

func (e *TimeoutInvariantError) Error() string {
	gap := e.GreaterValue - e.LesserValue
	return fmt.Sprintf(
		"timeout invariant violated (%s): %s=%s and %s=%s leave only %s of margin, need at least %s",
		e.Chain, e.LesserField, e.LesserValue, e.GreaterField, e.GreaterValue,
		gap, e.RequiredMargin,
	)
}

// Validate checks every pairwise link in both invariant chains and returns
// ALL violations found (via errors.Join), not just the first, so a single
// broken link never masks another. Returns nil when every link holds with
// at least MinTimeoutMargin of headroom.
func (t Timeouts) Validate() error {
	var errs []error

	check := func(chain, greaterField string, greater time.Duration, lesserField string, lesser time.Duration) {
		if greater < lesser+MinTimeoutMargin {
			errs = append(errs, &TimeoutInvariantError{
				Chain:          chain,
				LesserField:    lesserField,
				LesserValue:    lesser,
				GreaterField:   greaterField,
				GreaterValue:   greater,
				RequiredMargin: MinTimeoutMargin,
			})
		}
	}

	// Chain A: provider cap > supervisor > bridge (turn_deadline) > SSE.
	check("ProviderHardCap > SupervisorTurnCap",
		"ProviderHardCap", t.ProviderHardCap, "SupervisorTurnCap", t.SupervisorTurnCap)
	check("SupervisorTurnCap > TurnDeadline",
		"SupervisorTurnCap", t.SupervisorTurnCap, "TurnDeadline", t.TurnDeadline)
	check("TurnDeadline > SSEInactivityTimeout",
		"TurnDeadline", t.TurnDeadline, "SSEInactivityTimeout", t.SSEInactivityTimeout)

	// Chain B: two independent pairs (§4.1, §3.2).
	check("ProviderHTTPClientTimeout > ProviderWorstColdStart",
		"ProviderHTTPClientTimeout", t.ProviderHTTPClientTimeout, "ProviderWorstColdStart", t.ProviderWorstColdStart)
	check("FirstConnectBudget > ImagePullBootP99",
		"FirstConnectBudget", t.FirstConnectBudget, "ImagePullBootP99", t.ImagePullBootP99)

	return errors.Join(errs...)
}
