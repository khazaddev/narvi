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

	// SupervisorTurnCap's CURRENT role is an invariant-chain bound ensuring
	// config sanity only: Validate below checks ProviderHardCap >
	// SupervisorTurnCap > TurnDeadline as a pairwise VALUE comparison, and
	// this field is otherwise referenced nowhere else in the codebase --
	// there is no real-time code path that reads a turn's own start time
	// and compares it against this value to actually terminate anything.
	// TurnDeadline's own already-armed named timer (handleTurnDeadlineTimer,
	// app/sessionactor/timerfired.go) is what actually terminates a
	// runaway turn in every case where the timer pump itself is healthy,
	// since TurnDeadline (60m) always fires strictly before this field's
	// own value (90m) would. This field remains reserved as a genuine
	// backstop for the (currently unhandled) case where turn_deadline
	// itself fails to fire -- a future periodic sweep across active turns
	// (a reconciler/health-check mechanism, not built today) could use it
	// for that -- but no such independent, real-time enforcement exists
	// yet; do not read this field's presence, its value, or its Validate()
	// check as proof one does. Not given an explicit value in the plan;
	// chosen as 90m, well below the 2h provider cap.
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
	// start + boot before the first sign of life (see
	// internal/domain/sandbox/liveness.go's EvaluateConnectingTimeout: this
	// single ceiling applies across the whole Spawning/Connecting/Booting
	// span from spawn until the FIRST liveness signal arrives; every signal
	// after that -- including a boot-progress report emitted mid-boot --
	// re-arms the watchdog and switches it onto the shorter
	// SteadyHeartbeatBudget for all subsequent checks). §3.2 gives this
	// explicitly: "first_connect_budget (default 240s, covers provider
	// cold start + boot)".
	//
	// Audit-remediation note (config/platform-hardening batch): "cold
	// start + boot" here is read as ONE ceiling over the whole
	// pre-first-signal span, not a literal sum of ProviderWorstColdStart
	// (220s, §4.1's own floor for provider scheduling ALONE) plus
	// ImagePullBootP99 (90s, this codebase's own invented sub-phase
	// estimate) stacked sequentially on top of it. Read additively, the
	// two would total 310s against this field's own 240s value -- but the
	// plan text does not actually say the two phases are sequential and
	// non-overlapping, and reading them that way would demand raising this
	// field past its own §3.2-mandated 240s value, which is not a call to
	// make unilaterally in a bundled audit batch. See ImagePullBootP99's
	// own doc comment for the matching note, and Validate()'s
	// "FirstConnectBudget > ImagePullBootP99" check for the objectively-
	// correct, deliberately weaker statement this ambiguity leaves
	// available.
	FirstConnectBudget time.Duration

	// ImagePullBootP99 is our estimate of the p99 latency of the
	// image-pull-and-boot sub-phase that FirstConnectBudget's own single
	// pre-first-signal ceiling must clear with margin. Not given an
	// explicit figure in the plan; chosen as 90s.
	//
	// Audit-remediation note (config/platform-hardening batch):
	// deliberately NOT modeled as additional time layered on top of
	// ProviderWorstColdStart's own 220s floor (§4.1: "Modal cold
	// scheduling alone can take 220s+") -- this codebase has no evidence
	// the two are sequential/non-overlapping rather than this sub-phase
	// being nested within (a portion of) that same cold-start window, and
	// FirstConnectBudget's own 240s value is §3.2-mandated, not something
	// this invented estimate should be allowed to force upward. Treat this
	// as an estimate of the boot sub-phase's own worst case, understood to
	// fit within whatever headroom the single 240s ceiling leaves once
	// cold start has resolved -- a conservative, self-contained sanity
	// floor, not one leg of a two-leg sum. See FirstConnectBudget's own
	// doc comment above for the fuller reasoning.
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

	// --- Audit-remediation (outbound-adapters lens, config/platform-
	// hardening batch) standalone addition: no ordering relationship with
	// either invariant chain above (or with any prior batch's standalone
	// additions), so -- per those additions' own precedent -- a plain
	// field with a sensible default, not wired into a fake invariant link.

	// ExpiredCredentialCleanupInterval is how often
	// internal/adapters/outbound/postgres.RunExpiredTokenCleanup ticks,
	// deleting ws_tokens/user_sessions rows whose expires_at has already
	// passed (migrations/000016_ws_tokens.up.sql,
	// migrations/000017_auth_v1.up.sql -- both tables check expires_at
	// only at read/verify time; nothing else ever purges an expired row,
	// so left alone table growth is unbounded). Both tables' own TTLs
	// (WSTokenTTL 24h, UserSessionTTL 30 days) are on the order of
	// hours/days, so hourly cleanup is more than frequent enough. Not
	// specified in the plan; chosen as 1h.
	ExpiredCredentialCleanupInterval time.Duration

	// --- Step 22 standalone additions ("snapshots & restore"): no
	// ordering relationship with either invariant chain above (or with any
	// prior Step's standalone additions), so -- per those additions' own
	// precedent -- a plain field with a sensible default, not wired into a
	// fake invariant link.

	// SnapshotMintTimeout bounds sandbox-agent's own call
	// (internal/sandboxagent/snapshotclient.Client.Mint) to the control
	// plane's new snapshot-mint endpoint (design decision 2, POST
	// /sessions/{id}/snapshot), which itself makes a real, network-bound
	// SandboxProvider.TakeSnapshot call server-side -- more generous than
	// CredentialFetchTimeout's own 10s (a lightweight mint call with no
	// real provider round trip behind it) since a real snapshot operation
	// can genuinely take longer. Not specified in the plan; chosen as 60s.
	SnapshotMintTimeout time.Duration

	// --- Step 25 standalone addition ("reconciler + GC"): no ordering
	// relationship with either invariant chain above (or with any prior
	// Step's standalone additions), so -- per those additions' own
	// precedent -- a plain field with a sensible default, not wired into a
	// fake invariant link.

	// ReconcilerInterval is how often the process-wide reconciler
	// (internal/app/reconciler, §5.3) ticks: one ports.SandboxProvider.List
	// call compared against Postgres's own expected-alive set, reaping any
	// orphaned provider-side sandbox instance found. IMPLEMENTATION_PLAN.md
	// row 25 gives this explicitly: "60s loop against the provider API".
	ReconcilerInterval time.Duration

	// --- Step 25 fix (reconciler orphan-GC debounce): a real,
	// empirically-reproduced race, not covered by either invariant chain
	// above -- but DOES need its own pairwise check against
	// ReconcilerInterval (see Validate() below) for the guarantee it
	// exists to provide to actually hold, so it is not a plain standalone
	// addition either.

	// ReconcilerOrphanConfirmationPeriod is the minimum wall-clock time a
	// provider-side ref found in provider.List() with no matching row in
	// Postgres's own expected-alive set (SandboxStore.ListLiveProviderIDs)
	// must remain CONTINUOUSLY unexplained, across separate ReconcileOnce
	// ticks, before app/reconciler.Reconciler actually calls StopSandbox on
	// it -- a debounce/minimum-confirmation-count grace period closing a
	// real race: internal/app/sessionactor/dispatch.go's own deliberate
	// three-step spawn sequencing (see that file's own top "# Sequencing"
	// comment) commits a sandboxes row already in a LIVE status
	// (status='spawning') with provider_id still NULL, THEN calls the
	// real, network-bound SandboxProvider.CreateSandbox OUTSIDE any
	// transaction, THEN commits a SECOND, LATER transact
	// (recordProviderOutcome) that finally records provider_id.
	// ListLiveSandboxProviderIDs requires provider_id IS NOT NULL, so that
	// row is invisible to the reconciler's own expected-alive set for the
	// whole window between CreateSandbox returning success and
	// provider_id actually being committed, even though status is already
	// genuinely live. A reconciler tick landing in exactly that window
	// would, without this debounce, see the real, already-created, wanted
	// cloud object as an "unexplained" ref and kill a legitimate, in-flight
	// spawn on its very first sighting -- requiring no race with a second
	// actor, no double-click: inherent to every successful spawn's own
	// normal timing.
	//
	// The real window this must comfortably exceed is bounded, at the
	// absolute outside, by ProviderHTTPClientTimeout (5m -- CreateSandbox's
	// own worst-case duration) but is realistically sub-second (ordinary
	// network latency plus one small Postgres commit). This field is
	// deliberately NOT set anywhere near that 5m theoretical ceiling:
	// app/reconciler.Reconciler's own tick-by-tick structure already
	// guarantees a ref is NEVER reaped on its first sighting regardless of
	// this value (see ReconcileOnce's own doc comment) -- under normal
	// ticking, the gap between a ref's first sighting and its second is
	// ALREADY a full ReconcilerInterval (60s), dwarfing the sub-second real
	// race window on its own. This field's actual job is a second,
	// independent safety margin against ticks landing unusually close
	// together (a slow ReconcileOnce call delaying the ticker, a
	// misconfigured smaller ReconcilerInterval in some future environment,
	// or a test driving ticks back-to-back) -- chosen as 30s: comfortably
	// above any realistic sub-second race window, matching
	// MinTimeoutMargin's own existing 30s floor elsewhere in this struct,
	// and (deliberately, see Validate() below) at least MinTimeoutMargin
	// below ReconcilerInterval's own 60s so the "reaped on the SECOND
	// consecutive tick, never the first" guarantee this whole mechanism
	// promises actually holds under the shipped default, not merely on
	// average -- a real orphan is still fully reaped within at most two
	// tick intervals (120s worst case), meaningfully faster than leaving
	// it uncleaned indefinitely.
	ReconcilerOrphanConfirmationPeriod time.Duration

	// --- Step 26 standalone additions ("image builds", §8.5-note/§10-P2):
	// no ordering relationship with either invariant chain above (or with
	// any prior Step's standalone additions), so -- per those additions'
	// own precedent -- plain fields with sensible defaults, not wired into
	// a fake invariant link.

	// RepoSHAResolutionTimeout bounds a single internal/app/ports.
	// SourceControl.ResolveBranchSHA call (app/sessionactor's own
	// resolveAndSetImage, dispatch.go/imageresolve.go) -- one or two real
	// outbound GitHub API GETs per repo (the repo's default branch, then
	// its HEAD commit), called once per repo IN A LOOP, each bounded
	// individually so one slow/hanging repo can't stall the others
	// indefinitely. Not specified in the plan; chosen as 10s, matching
	// CredentialFetchTimeout's own reasoning exactly ("a lightweight...
	// call, not a large data transfer").
	RepoSHAResolutionTimeout time.Duration

	// ImageBuildPumpInterval is how often the process-wide background
	// image-build loop (internal/app/imagebuild, mirroring app/reconciler's
	// own ReconcilerInterval-driven ticker shape) polls image_builds for
	// rows eligible to (re)attempt now. Not specified in the plan; chosen
	// as 60s, matching ReconcilerInterval's own precedent -- image builds
	// are a slow, infrequent background maintenance concern, not a
	// latency-sensitive one.
	ImageBuildPumpInterval time.Duration

	// ImageBuildBackoffBase is domain/imagebuild.BackoffConfig.BaseDelay:
	// the retry delay scheduled after a fingerprint's FIRST failed build
	// attempt. Not specified in the plan; chosen as 1min -- see
	// domain/imagebuild.EvaluateBackoff's own doc comment for the full
	// schedule this produces alongside ImageBuildBackoffMax below.
	ImageBuildBackoffBase time.Duration

	// ImageBuildBackoffMax is domain/imagebuild.BackoffConfig.MaxDelay: the
	// ceiling the exponential schedule above plateaus at. Not specified in
	// the plan; chosen as 30min -- deliberately the SAME cadence §3.5's own
	// language ("not fixed 30 min") explicitly rejects as a FIRST-failure
	// delay, but a reasonable EVENTUAL steady-state ceiling once a build is
	// confirmed persistently broken: this is the cap the schedule grows
	// INTO after repeated failures, never the delay applied from the very
	// first one, so it does not contradict §3.5.
	ImageBuildBackoffMax time.Duration

	// --- Step 27 standalone addition ("mocking + contract drift", §14.3):
	// no ordering relationship with either invariant chain above (or with
	// any prior Step's standalone additions), so -- per those additions'
	// own precedent -- a plain field with a sensible default, not wired
	// into a fake invariant link.

	// ContractsFingerprintResolutionTimeout bounds a single internal/app/
	// ports.SourceControl.ResolveContractsFingerprint call (app/
	// sessionactor's own checkContractDrift, contractdrift.go) -- one real
	// outbound GitHub Contents API GET per repo, called once per repo IN A
	// LOOP, each bounded individually so one slow/hanging repo can't stall
	// the others indefinitely -- mirrors RepoSHAResolutionTimeout's own
	// identical reasoning and value exactly (this codebase's own
	// convention is one named timeout per distinct network-call type, even
	// when two types happen to share the same chosen value -- see
	// RepoSHAResolutionTimeout's own addition in Step 26 for the precedent
	// this repeats rather than reuses). Not specified in the plan; chosen
	// as 10s, matching RepoSHAResolutionTimeout/CredentialFetchTimeout's
	// own "lightweight call, not a large data transfer" reasoning.
	ContractsFingerprintResolutionTimeout time.Duration

	// --- Step 29 standalone addition ("gitstate in-sandbox", §3.4): no
	// ordering relationship with either invariant chain above (or with any
	// prior Step's standalone additions), so -- per those additions' own
	// precedent -- a plain field with a sensible default, not wired into a
	// fake invariant link.

	// GitSyncStepTimeout bounds each individual git subprocess
	// internal/sandboxagent/gitclone.SyncAll spawns while reconciling one
	// already-existing repo at boot (`git status --porcelain`, `git stash
	// push`, `git rev-parse --verify`, `git checkout`/`git checkout -b`,
	// `git stash pop`) -- every one of these is local-only (no network),
	// unlike RepoCloneTimeout's own network-bound clone/push operations, so
	// a much smaller budget than RepoCloneTimeout's 5m is appropriate; still
	// more generous than RepoSHADiscoveryTimeout's 5s since checkout/stash
	// can touch a large working tree, not just read one small ref. Not
	// specified in the plan; chosen as 30s, matching ServiceReadinessTimeout/
	// OpenCodeReadinessTimeout's own "generous for typical local operations
	// without stalling the whole boot sequence" reasoning.
	GitSyncStepTimeout time.Duration

	// --- Step 31 standalone addition ("webhook toolkit", §5.1/§5.2): no
	// ordering relationship with either invariant chain above (or with any
	// prior Step's standalone additions), so -- per those additions' own
	// precedent -- a plain field with a sensible default, not wired into a
	// fake invariant link.

	// WebhookTimestampFreshnessWindow bounds how far a provider-supplied
	// webhook timestamp (e.g. Slack's X-Slack-Request-Timestamp) may drift
	// from now before platform.VerifyWebhookTimestamp rejects it as a
	// possible replay -- checked SEPARATELY from (in addition to) the
	// signature itself, mirroring Slack's own signing-secrets guidance.
	// Deliberately a DISTINCT field from HMACWindow above even though both
	// happen to default to 5 minutes: HMACWindow guards Narvi's own
	// internal "{timestamp}.{signature}" bearer scheme (hmacauth.go),
	// this one guards third-party provider webhook signatures
	// (webhooksig.go) -- two functionally distinct subsystems that must
	// stay independently rotatable/tunable, matching
	// ProcessStopGracePeriod/ShutdownGracePeriod's own "same value, two
	// distinct fields for two distinct subsystems" precedent. Not given
	// an explicit figure in the plan; chosen as 5 minutes, matching
	// Slack's own commonly recommended replay window.
	WebhookTimestampFreshnessWindow time.Duration

	// --- Step 34 standalone additions ("Linear ingress", §8.10): no
	// ordering relationship with either invariant chain above (or with any
	// prior Step's standalone additions), so -- per those additions' own
	// precedent -- plain fields with sensible defaults, not wired into a
	// fake invariant link.

	// LinearWebhookTimestampWindow bounds how far a Linear webhook's own
	// body-level webhookTimestamp field may drift from now before this
	// Step's webhook handler rejects it as a possible replay -- a
	// DELIBERATELY DISTINCT field from WebhookTimestampFreshnessWindow
	// above (Step 31's generic 5-minute default, "matching Slack's own
	// commonly recommended replay window") even though both guard the same
	// general class of check, because Linear's own real, current developer
	// docs (confirmed during this Step's investigation) recommend a much
	// tighter window: "verify it's within a minute of the time your system
	// sees it." Using the shared 5-minute field here would silently accept
	// a replay window 5x wider than Linear's own stated guidance -- exactly
	// the kind of same-value-different-subsystem confusion
	// ProcessStopGracePeriod/ShutdownGracePeriod's own precedent (cited by
	// WebhookTimestampFreshnessWindow's own doc comment) argues against.
	// Chosen as 60s, Linear's own explicit figure, not invented.
	LinearWebhookTimestampWindow time.Duration

	// LinearOutboundActivityTimeout bounds the one outbound Linear
	// GraphQL API call this Step makes synchronously from inside the
	// webhook handler itself (posting an initial acknowledgment Agent
	// Activity -- see internal/adapters/outbound/linearapi's own doc
	// comment for why this is a minimal direct call, not the general
	// Notifier/outbox abstraction Step 35 owns). Linear's own real docs
	// require a webhook receiver to "return a response ... within 5
	// seconds" -- this must clear that budget with real margin, so a slow
	// or hanging Linear API call never itself causes Linear to consider
	// the webhook delivery failed. Chosen as 3s: generous for a single
	// lightweight GraphQL mutation, comfortably below the 5s ceiling with
	// margin for the rest of the handler's own (fast, no-network) work.
	LinearOutboundActivityTimeout time.Duration

	// --- Audit-remediation addition (HIGH, "releasing the
	// linear_agent_sessions claim after a SetSessionID failure can spawn a
	// duplicate, independently-dispatched agent"): no ordering relationship
	// with either invariant chain above (or with any prior standalone
	// addition), so -- per those additions' own precedent -- plain fields
	// with sensible defaults, not wired into a fake invariant link.

	// LinearSetSessionIDTimeout bounds ONE attempt of the retried
	// AgentSessions.SetSessionID call in internal/adapters/inbound/linear's
	// own handleCreated (webhook.go's own setSessionIDWithRetry) -- a
	// single, local Postgres UPDATE against a row this same request
	// already won the claim on, never an outbound network call. Not
	// specified in the plan (this fix postdates it); chosen as 2s --
	// generous for a single-row UPDATE even under a transient connection
	// blip, while keeping the retry loop's own worst-case added latency
	// small relative to Linear's 5s webhook-response requirement.
	//
	// LOW audit fix ("stale/inverted doc comment"): this field's own
	// comment used to justify 2s by claiming it was "deliberately much
	// shorter than IdentityEmailFetchTimeout's own lightweight outbound API
	// call budget" -- true when IdentityEmailFetchTimeout was still 10s,
	// but false once the L5 audit fix dropped that field well below 2s
	// (300ms, later retuned to 800ms by the HIGH audit fix -- see that
	// field's own doc comment). LinearSetSessionIDTimeout (2s) is now
	// numerically LARGER than IdentityEmailFetchTimeout (800ms), the
	// OPPOSITE of what the old comment asserted. That inversion does not
	// make either value wrong: the two fields were never comparable by
	// magnitude in the first place -- this one bounds a single, LOCAL
	// Postgres UPDATE against a row already claimed by this same request
	// (a connection blip is the only realistic failure mode, and even a
	// generous budget for that is cheap), while IdentityEmailFetchTimeout
	// bounds a genuine outbound network call to a THIRD-PARTY provider API
	// under a much tighter, retry-multiplied, externally-imposed ack
	// budget (Slack's ~3s). A local operation being allowed a numerically
	// larger per-attempt ceiling than a retried outbound one is not a
	// contradiction; it simply reflects that neither field's own budget was
	// ever derived FROM the other's value.
	LinearSetSessionIDTimeout time.Duration

	// LinearSetSessionIDMaxAttempts/LinearSetSessionIDRetryBaseDelay/
	// LinearSetSessionIDRetryMaxDelay configure platform.Retry's own
	// doubling-capped-at-max backoff for setSessionIDWithRetry -- mirrors
	// IdentityEmailFetchMaxAttempts/IdentityEmailFetchRetryBaseDelay/
	// IdentityEmailFetchRetryMaxDelay's own identical shape (a foreground,
	// in-process retry bounded by the caller's own webhook-response
	// budget, internal/platform/retry.go), reused here for a DIFFERENT
	// call: by the time setSessionIDWithRetry ever runs,
	// httpapi.CreateSessionCore has ALREADY committed the real session
	// and fired TriggerDispatch (see handleCreated's own top doc comment)
	// -- retrying this idempotent UPDATE is always safe and never risks a
	// duplicate session, unlike releasing the claim and letting Linear
	// redeliver the whole `created` event. Not specified in the plan;
	// chosen as 3 attempts / 100ms base / 500ms max -- with 3 attempts,
	// the worst case (2 waits: 100ms then 200ms) adds well under a second
	// of wall-clock time to the handler's own critical path.
	LinearSetSessionIDMaxAttempts    int
	LinearSetSessionIDRetryBaseDelay time.Duration
	LinearSetSessionIDRetryMaxDelay  time.Duration

	// --- Step 33 standalone addition ("Slack ingress", §8.10): no
	// ordering relationship with either invariant chain above (or with
	// any prior Step's standalone additions), so -- per those additions'
	// own precedent -- a plain field with a sensible default, not wired
	// into a fake invariant link.

	// SlackAckTimeout bounds a single internal/adapters/inbound/slack
	// ackClient.postAck call (a real outbound POST to Slack's own
	// chat.postMessage, made synchronously in the inbound webhook request
	// path before that handler answers Slack's own delivery with 200) --
	// mirrors PRCreateTimeout's own "a genuine outbound network call that
	// must never run against an unbounded context" precedent exactly:
	// without this, a Slack API outage would hang every webhook request
	// touching a new-or-busy thread indefinitely, since neither
	// http.DefaultClient nor the request's own context carries any
	// deadline otherwise. Not specified in the plan; chosen as 10s,
	// generous for a single Slack Web API POST while still well short of
	// Slack's own ~3s "retry the webhook" outer expectation being made
	// noticeably worse.
	SlackAckTimeout time.Duration

	// --- Step 38 standalone addition ("plan mode, cross-channel", §8.1/
	// §13.3): no ordering relationship with either invariant chain above (or
	// with any prior Step's standalone additions), so -- per those
	// additions' own precedent -- a plain field with a sensible default, not
	// wired into a fake invariant link.

	// SlackInteractivityAckTimeout bounds the ENTIRE synchronous decide+
	// update sequence internal/adapters/inbound/slack/interactive.go's
	// block_actions handling runs for approve_plan/reject_plan: the shared
	// httpapi.DecidePlan call (opens a tx, locks the session row, the
	// guarded UPDATE, possibly inserting+dispatching a new turn, enqueuing
	// cross-channel notifications, committing) followed by the real,
	// synchronous chat.update call reflecting the outcome -- ONE shared
	// bounded context covers both, not two separately-budgeted calls that
	// could each individually fit within their own budget yet still
	// together exceed Slack's real window.
	//
	// Deliberately a SEPARATE, much tighter constant from SlackAckTimeout
	// above, even though both nominally guard "a Slack ack": SlackAckTimeout
	// was sized for the Events API's own in-thread ack (handler.go's
	// ackClient.postAck -- a single outbound chat.postMessage POST, with no
	// DB transaction ahead of it, and generous headroom relative to Slack's
	// own separate "~3s, then retry the whole webhook delivery" outer
	// expectation for THAT route). This field guards a COMPLETELY DIFFERENT
	// and far more time-pressured budget: Slack's own real interactivity
	// payload ack window, which Slack's docs describe as a hard ~3 seconds --
	// miss it and Slack shows the user a "dispatch_failed" error, even when
	// Narvi's own backend goes on to complete the action correctly a moment
	// later. And unlike SlackAckTimeout's single POST, this budget must
	// cover the WHOLE guarded-UPDATE transaction described above plus the
	// follow-up chat.update, so it cannot reuse SlackAckTimeout's own more
	// generous 10s value without risking exactly the kind of DB-contention-
	// blows-past-Slack's-real-budget failure this field exists to prevent.
	// Not given an explicit figure in the plan; chosen as 2.5s -- leaves
	// real margin (roughly 500ms) below Slack's own ~3s hard ceiling for
	// network/serialization overhead getting the response back to Slack,
	// while still being tight enough to fail fast (and let the handler
	// answer Slack's own required 200 promptly regardless) under DB
	// contention or a slow Slack response, rather than hang until either
	// finishes.
	SlackInteractivityAckTimeout time.Duration

	// --- Step 35 standalone additions ("outbox delivery", §5.1): no
	// ordering relationship with either invariant chain above (or with any
	// prior Step's standalone additions), so -- per those additions' own
	// precedent -- plain fields with sensible defaults, not wired into a
	// fake invariant link. OutboxClaimDuration is the one exception: see
	// the audit-fix note directly above that field below, matching Step
	// 25's own ReconcilerOrphanConfirmationPeriod precedent of a Step's
	// later fix adding its own single, independent pairwise check without
	// retroactively promoting the whole family into either named chain.

	// OutboxPumpInterval is how often the process-wide background outbox
	// delivery loop (internal/app/outboxworker, mirroring app/imagebuild's
	// own ImageBuildPumpInterval-driven ticker shape) polls the outbox
	// table for rows eligible to (re)attempt delivery now. Not specified in
	// the plan; chosen as 5s -- deliberately much shorter than
	// ImageBuildPumpInterval/ReconcilerInterval's own 60s: unlike a slow,
	// expensive image build or a coarse reconciliation sweep, an outbox
	// entry is a small, cheap notification a real user (in a Slack thread,
	// a Linear session, a GitHub PR) is actively waiting to see, so this
	// loop polls near-real-time, matching TimerPumpInterval's own identical
	// "near-real-time delivery without hammering Postgres" reasoning.
	OutboxPumpInterval time.Duration

	// OutboxBackoffBase is domain/outbox.BackoffConfig.BaseDelay: the retry
	// delay scheduled after an outbox entry's FIRST failed delivery
	// attempt. Not specified in the plan; chosen as 30s -- see
	// domain/outbox.EvaluateBackoff's own doc comment for the full
	// schedule this produces alongside OutboxBackoffMax below, and
	// domain/outbox.MaxAttempts's own doc comment for why this combination
	// comfortably survives resilience scenario 9's own 10-minute outage
	// (§9.3: "Slack API 500s for 10 min -> notification eventually
	// delivered, no loss") without ever dead-lettering partway through it.
	OutboxBackoffBase time.Duration

	// OutboxBackoffMax is domain/outbox.BackoffConfig.MaxDelay: the ceiling
	// the exponential schedule above plateaus at. Not specified in the
	// plan; chosen as 5min -- deliberately shorter than ImageBuildBackoffMax's
	// own 30min: a stuck image build can reasonably wait half an hour
	// between retries with no one watching in real time, but an outbox
	// entry backs a live notification a human is waiting on, so this
	// plateaus much sooner.
	OutboxBackoffMax time.Duration

	// OutboxDeliveryTimeout bounds ONE outbound notifier call
	// (ports.Notifier.Deliver, routed to whichever of the Slack/Linear/
	// GitHub adapters owns the claimed row's own kind) during a single
	// delivery attempt -- mirrors RepoSHAResolutionTimeout's own "a
	// lightweight outbound call, bounded individually so one slow/hanging
	// call can't stall the rest of the batch" reasoning exactly. Not
	// specified in the plan; chosen as 15s -- more generous than
	// RepoSHAResolutionTimeout/CredentialFetchTimeout's own 10s (a
	// notification POST can occasionally be slower than a lightweight GET,
	// e.g. Slack/Linear API tail latency), but still far short of
	// OutboxBackoffBase so a single hung call never meaningfully delays the
	// next pump tick's own batch.
	OutboxDeliveryTimeout time.Duration

	// --- Audit fix (H6, correctness -- internal/app/outboxworker/
	// builder.go): the ORIGINAL model below computed ONE now := time.Now()
	// per batch-level claimBatch call and stamped every row in that batch
	// (up to pumpBatchSize=20) with the identical claim-expiry timestamp,
	// before ANY of them were actually delivered. PumpOnce then attempts
	// each claimed row SEQUENTIALLY, one at a time, each bounded by
	// OutboxDeliveryTimeout -- so a row late in the batch could still be
	// waiting for its own attempt() call to even START, long after its
	// shared claim-expiry timestamp had already elapsed, letting a
	// concurrent tick (this pod's next tick, or another pod's own
	// Builder -- ListDuePendingOutboxEntries' own FOR UPDATE SKIP LOCKED
	// exists specifically so multiple pods run this loop concurrently)
	// re-claim that same row while the first delivery was still in
	// flight -- and, if that second builder's own delivery was ALSO still
	// in flight when the first builder finally reached its own turn, a
	// naive renewal guarded only by "status = 'pending'" (both builders'
	// own claims leave status at 'pending' -- the outbox table has no
	// third, in-flight status) would succeed for BOTH builders, and BOTH
	// would call notifier.Deliver on the same row concurrently: a genuine
	// double-delivery race, empirically reproduced against a real
	// Postgres testcontainer with a deliberately slow concurrent
	// claimant. Status alone cannot tell "untouched since I last observed
	// it" apart from "a different builder already re-claimed/renewed it
	// and is mid-delivery on it right now".
	//
	// The fix is a per-row re-claim/heartbeat (RenewOutboxClaim, called
	// from attempt() in internal/app/outboxworker/builder.go immediately
	// before the real notifier.Deliver call, using time.Now() at THAT
	// moment -- not the batch's shared claim-time now) that is ALSO a
	// genuine optimistic-concurrency compare-and-swap against the row's
	// own next_attempt_at: it only renews (and only succeeds) if the
	// row's CURRENT next_attempt_at still matches the value THIS caller
	// last observed (row.NextAttemptAt, from its own prior
	// ClaimOutboxEntry/RenewOutboxClaim return). If a different builder
	// already won the race, that builder's own claim/renewal already
	// changed next_attempt_at away from the value this caller observed,
	// so this caller's own renewal correctly fails (pgx.ErrNoRows) instead
	// of proceeding to deliver -- attempt() already treats that error as
	// "stop, do not deliver". This CAS is what actually gives the renewal
	// single-writer teeth: at most one builder's renewal for a given prior
	// next_attempt_at value can ever succeed, so at most one builder ever
	// proceeds to notifier.Deliver for a given row at a time. It never
	// increments attempts again (that already happened once, at
	// claimBatch time, via ClaimOutboxEntry). See that query's own doc
	// comment (queries/outbox.sql) for the full mechanism.
	//
	// This changes what OutboxClaimDuration itself protects: no longer "one
	// batch's own worst-case total sequential processing time" (which the
	// original doc comment reasoned about, and which no fixed value could
	// ever safely bound against an arbitrarily large pumpBatchSize/attempt
	// backlog), but "one row's own renewal window, renewed fresh
	// immediately before each real delivery attempt" -- so it now only
	// ever needs to comfortably outlast a SINGLE OutboxDeliveryTimeout-
	// bounded attempt, which Validate() below enforces as a new,
	// independent pairwise check (OutboxClaimDuration > OutboxDeliveryTimeout,
	// mirroring Step 25's own ReconcilerInterval > ReconcilerOrphanConfirmationPeriod
	// precedent of a single, narrow link added outside either named chain).

	// OutboxClaimDuration protects a just-claimed (or just-renewed) outbox
	// row from being re-selected by a concurrent/later pump tick (this
	// pod's or another pod's own outboxworker.Builder) before THIS row's
	// own real delivery attempt has recorded an outcome -- mirrors
	// TimerClaimDuration's own identical "push the due-again time forward
	// by a protection window at claim time" mechanism exactly, needed here
	// because the outbox table (unlike image_builds' own 'building'
	// status) has no third, in-flight status distinct from
	// pending/delivered/dead_letter to mark a row claimed with.
	// ClaimOutboxEntry bumps next_attempt_at forward by this duration (and
	// increments attempts) at batch-claim time; attempt() then RENEWS the
	// SAME row's own next_attempt_at by this same duration, from a FRESH
	// time.Now(), immediately before its own real notifier call, via the
	// genuine compare-and-swap described in the audit-fix note directly
	// above (guarded by the row's own next_attempt_at still matching what
	// this caller last observed, not merely status='pending') -- WITHOUT
	// incrementing attempts again. RecordOutboxEntryFailure/
	// MarkOutboxEntryDeadLetter then
	// overwrite that provisional value with the real domain/outbox.
	// EvaluateBackoff decision once the attempt's real outcome is known.
	// Self-healing exactly like TimerClaimDuration's own precedent: a pod
	// that crashes mid-delivery simply leaves the row due again once this
	// window elapses, picked up by a later tick with no separate sweep
	// needed. Not specified in the plan; chosen as 45s -- OutboxDeliveryTimeout
	// (15s) above is this window's own worst-case real single-attempt
	// duration, and Validate() below requires at least MinTimeoutMargin
	// (30s) of headroom beyond it, so 45s sits exactly at that minimum
	// margin (matching ReconcilerInterval/ReconcilerOrphanConfirmationPeriod's
	// own identical "exactly at the minimum margin, not extra slack beyond
	// it" precedent) rather than TimerClaimDuration's own unrelated 30s
	// value, which this field no longer needs to match now that it
	// protects one renewed row's own window rather than reasoning (as the
	// pre-fix comment above used to) about an entire batch's own
	// sequential processing time.
	OutboxClaimDuration time.Duration

	// --- Step 36 standalone addition ("intent classifier", §8.3/§18): no
	// ordering relationship with either invariant chain above (or with any
	// prior Step's standalone additions), so -- per those additions' own
	// precedent -- a plain field with a sensible default, not wired into a
	// fake invariant link.

	// IntentClassifierLLMTimeout bounds ONE outbound ports.LLM.Complete
	// call (internal/adapters/outbound/llm's Anthropic adapter, called
	// once per session by internal/app/intentclassifier.Classify).
	// Configured directly on the Anthropic SDK client at construction time
	// (option.WithRequestTimeout) -- §18.1's own explicit rule is that
	// this is the ONLY timeout layer for this call: never a second,
	// redundant context.WithTimeout raced against it, since the SDK's own
	// internal abort always resolves first. Not specified in the plan;
	// chosen as 10s, matching RepoSHAResolutionTimeout/
	// CredentialFetchTimeout's own "lightweight call, not a large data
	// transfer" reasoning -- a structured-output classification call over
	// a short prompt is exactly that kind of call, and this is a
	// high-volume, latency-sensitive path called on every session across
	// every surface (never a "remotely complicated" reasoning task, hence
	// this Step's own choice of a fast/cheap model with no extended
	// thinking enabled).
	IntentClassifierLLMTimeout time.Duration

	// --- Step 39 standalone additions ("identities + full RBAC", §13.2):
	// no ordering relationship with either invariant chain above (or with
	// any prior Step's standalone additions), so -- per those additions'
	// own precedent -- plain fields with sensible defaults, not wired into
	// a fake invariant link.

	// IdentityEmailFetchTimeout bounds ONE outbound provider profile-email
	// API call (Slack users.info / Linear's `user(id) { email }` query,
	// internal/app/identitylink's own Resolve) -- a single attempt's own
	// budget.
	//
	// Audit fix (L5, "the retry timing doc is wrong, and the real worst
	// case blocks Slack's actual ~3s webhook ack window"): this used to be
	// 10s, matching RepoSHAResolutionTimeout/CredentialFetchTimeout's own
	// "lightweight call, not a large data transfer" reasoning -- but unlike
	// those two fields, THIS timeout is spent inside a loop of
	// IdentityEmailFetchMaxAttempts attempts (platform.Retry, via
	// identitylink.FetchEmailWithRetry), run SYNCHRONOUSLY, inline, on the
	// Slack Events API webhook request path (internal/adapters/inbound/
	// slack/identity.go's own resolveSlackActor, called from handler.go's
	// handleEvent BEFORE thread<->session mapping, turn creation, or the
	// in-thread ack -- see that package's own doc.go for the full request-
	// handling order). So the REAL worst case this field feeds into is
	// IdentityEmailFetchMaxAttempts x IdentityEmailFetchTimeout PLUS every
	// backoff wait between attempts -- not the backoff waits in isolation.
	// At the OLD 10s/3-attempts value that real worst case was
	// 3x10s+600ms =~ 30.6s -- catastrophically over Slack's own real ~3s
	// "answer this webhook or it gets redelivered" budget (see slack's own
	// doc.go), for JUST this one step, with the entire rest of the
	// handler's own work (thread mapping, session/turn creation, the ack)
	// still to come in the SAME request. That fix lowered this field to
	// 300ms.
	//
	// HIGH audit fix ("300ms is unrealistically tight, inconsistent with
	// this codebase's own established precedent for the SAME call"): the
	// 300ms value above over-corrected -- it was picked purely to make the
	// retry-loop arithmetic fit Slack's ack budget, with no grounding in
	// real Slack/Linear API latency. This codebase's own CLOSEST precedent
	// for this exact call is SlackInteractivityIdentityFetchTimeout, below
	// -- it already budgets 800ms for a SINGLE, un-retried attempt at the
	// EXACT SAME users.info/GetUserEmail call, under an even TIGHTER
	// overall budget (SlackInteractivityAckTimeout, 2500ms, shared with a
	// real DB transaction AND a second outbound call). 300ms was 2.67x
	// tighter than that already-vetted "realistic minimum" number. The
	// concrete failure this invited: a provider answering in a genuinely
	// healthy, unremarkable ~400-600ms (very plausible real RTT/TLS
	// overhead, not evidence of anything broken, and a SYSTEMATIC
	// per-provider floor rather than independent jitter) would make ALL
	// IdentityEmailFetchMaxAttempts attempts time out IDENTICALLY every
	// single time -- retrying never helps against a consistent latency
	// floor above the per-attempt ceiling -- silently and PERMANENTLY
	// falling back to bot attribution for every message from every user on
	// an entirely healthy provider, while ALSO tripping M19's own Warn
	// log/identity_email_fetch_failures_total (see that counter's own doc
	// comment, internal/app/identitylink/retry.go) on every single one of
	// those messages: exactly the false-positive "the provider API is
	// broken" noise that counter exists to avoid, even though nothing was
	// actually broken.
	//
	// Raised to 800ms -- reusing SlackInteractivityIdentityFetchTimeout's
	// own already-vetted figure directly rather than inventing a new one,
	// since it is this codebase's own closest, already-defended precedent
	// for "one lightweight attempt at this exact call, under a tight
	// budget". This comfortably covers the realistic ~400-600ms
	// healthy-but-unremarkable case with genuine margin (200ms+) to spare,
	// so a normal, healthy provider succeeds within budget in the vast
	// majority of real-world cases, not just "technically fits the
	// arithmetic if everything is fast" -- while a real hang/dead provider
	// still fails within a bounded time so the retry loop can do its job.
	// Raising the per-attempt timeout back toward 10s-scale would blow the
	// ack budget again the way the ORIGINAL 10s value did, so
	// IdentityEmailFetchMaxAttempts was lowered from 3 to 2 (see that
	// field's own doc comment for why THIS lever, not a shorter per-attempt
	// timeout, was chosen) to keep the total worst case comfortably inside
	// Slack's real ~3s ack window -- see IdentityEmailFetchRetryBaseDelay/
	// IdentityEmailFetchRetryMaxDelay's own doc comment for the full
	// worst-case arithmetic and the headroom it leaves for the rest of the
	// handler's own work in the same request.
	IdentityEmailFetchTimeout time.Duration

	// IdentityEmailFetchMaxAttempts is how many times platform.Retry calls
	// the profile-email fetch before giving up (§13.2: "a provider
	// email-API failure is a retryable error, not an empty identity...
	// retry with backoff"). Not specified in the plan; originally chosen as
	// 3 -- enough to ride out a brief blip without indefinitely delaying
	// the webhook handler's own response (this whole retry loop runs
	// SYNCHRONOUSLY, inline, on the ingress request path -- see
	// internal/app/identitylink's own doc.go for why unbounded/background
	// retry, like domain/outbox's own persisted-schedule approach, is the
	// wrong shape for this specific call). The L5 audit fix kept this at 3
	// and fixed the real worst-case budget by shrinking the per-attempt
	// timeout instead (see IdentityEmailFetchTimeout's own doc comment) --
	// that shrink is what the HIGH audit fix below found unrealistically
	// tight.
	//
	// HIGH audit fix (see IdentityEmailFetchTimeout's own doc comment for
	// the full incident): once the per-attempt timeout was raised back to
	// a realistic 800ms, 3 attempts at 800ms no longer fits Slack's ~3s ack
	// budget with meaningful headroom (3x800ms alone is already 2.4s,
	// before any backoff wait or the rest of the handler's own work).
	// Lowered to 2 -- still genuine retry-with-backoff behavior (one retry
	// after the first failure), satisfying §13.2's own explicit
	// requirement, deliberately NOT reduced to 1 (which would remove retry
	// semantics the plan explicitly wants: a single blip on an otherwise
	// healthy provider would then never get a second chance). This is the
	// "reduce attempt count" lever, used instead of shrinking the
	// per-attempt timeout back down below a realistic figure -- see
	// IdentityEmailFetchTimeout's own doc comment for why THAT lever was
	// rejected as the primary fix. See IdentityEmailFetchRetryBaseDelay/
	// IdentityEmailFetchRetryMaxDelay's own doc comment for the full
	// worst-case arithmetic this field is one factor of.
	IdentityEmailFetchMaxAttempts int

	// IdentityEmailFetchRetryBaseDelay/IdentityEmailFetchRetryMaxDelay
	// configure platform.Retry's own doubling-capped-at-max backoff
	// between attempts -- mirrors domain/outbox.BackoffConfig's identical
	// shape, but MUCH shorter: this retry loop's own caller (a Slack/
	// Linear webhook handler) is still on the hook to answer promptly,
	// unlike outbox delivery's own background, persisted-schedule retry.
	//
	// Audit fix (L5, see IdentityEmailFetchTimeout's own doc comment for
	// the full incident): the PREVIOUS doc comment here claimed the worst
	// case "adds well under 1s of wall-clock time to the handler's own
	// critical path" -- true of the two backoff WAITS alone (200ms+400ms
	// at the old values), but this silently omitted that each of
	// IdentityEmailFetchMaxAttempts attempts is ITSELF bounded by
	// IdentityEmailFetchTimeout and can genuinely take that long if the
	// provider API hangs rather than erroring quickly -- the REAL worst
	// case is IdentityEmailFetchMaxAttempts x IdentityEmailFetchTimeout +
	// every backoff wait summed, not the backoff waits in isolation. This
	// mirrors how OutboxClaimDuration's own doc comment (audit fix H6) was
	// corrected to describe its real mechanism instead of an incomplete
	// one -- no more silent omission of the per-attempt timeout's own
	// contribution to the total.
	//
	// HIGH audit fix (see IdentityEmailFetchTimeout's own doc comment for
	// the full incident): with IdentityEmailFetchTimeout now 800ms and
	// IdentityEmailFetchMaxAttempts now 2, the REAL worst case -- 2
	// attempts at 800ms EACH genuinely timing out, plus the ONE backoff
	// wait between them, pessimistically at IdentityEmailFetchRetryMaxDelay
	// (150ms, a looser bound than the 100ms the actual, undoubled first
	// wait below would reach -- with only 2 attempts, platform.Retry's own
	// doubling never gets a second wait to double INTO) -- is
	// 2x800ms + 1x150ms = 1.75s total. That leaves 1.25s (~42%) of headroom
	// under Slack's own real ~3s webhook-ack budget (internal/adapters/
	// inbound/slack's own doc.go) for the REST of the handler's own work in
	// the same request (thread<->session mapping, turn creation, the
	// in-thread ack) -- comfortably more than the fast, no-network Postgres
	// work that remains actually needs, and a full 1.25s of absolute
	// margin, not just a technically-nonzero one. See internal/platform/
	// timeouts_test.go's own
	// TestDefaultTimeouts_IdentityEmailFetchWorstCaseTimingBudget, which
	// asserts this invariant directly against that external ~3s constant.
	// Linear's own equivalent path (internal/adapters/inbound/linear/
	// identity.go's own resolveActor) shares these SAME fields but has a
	// looser real budget of its own (~10s to post its one required
	// acknowledgment activity -- internal/adapters/inbound/linear's own
	// doc.go) -- comfortably protected by a fix sized for Slack's tighter
	// requirement, with no separate Linear-specific tuning needed.
	//
	// Chosen as 100ms/150ms (unchanged by the HIGH fix -- only the
	// attempt count and per-attempt timeout above needed retuning): the
	// ACTUAL (non-pessimistic) wait with only 2 attempts is a single
	// 100ms delay (the base; there is no second wait left to double into),
	// comfortably under the 150ms pessimistic bound used above.
	IdentityEmailFetchRetryBaseDelay time.Duration
	IdentityEmailFetchRetryMaxDelay  time.Duration

	// SlackInteractivityIdentityFetchTimeout bounds the ONE identity-
	// resolution profile-email fetch attempt Slack's own interactivity
	// path (internal/adapters/inbound/slack's decideAndUpdateMessage/
	// handleViewSubmission, interactive.go) allows itself, with
	// deliberately NO retry loop at all (unlike IdentityEmailFetchTimeout/
	// IdentityEmailFetchMaxAttempts' own general-purpose, retried budget,
	// used by the Events API ingress path instead) -- this path shares
	// Slack's own hard ~3s interactivity-ack window with DecidePlan's own
	// guarded-UPDATE transaction AND the chat.update call that reflects
	// its outcome (see SlackInteractivityAckTimeout's own doc comment),
	// so there simply isn't room for a multi-attempt backoff loop here. A
	// failed/timed-out fetch on this path defers to bot attribution for
	// THIS click; the SAME still-unlinked identity gets a full, properly-
	// retried resolution attempt the next time any OTHER event from it
	// arrives (an Events API message, a later click, a modal submission).
	// Not specified in the plan; chosen as 800ms -- comfortably inside
	// SlackInteractivityAckTimeout (2500ms) with real margin left for the
	// DecidePlan+chat.update calls that follow it in the same shared
	// budget.
	//
	// HIGH audit fix (see IdentityEmailFetchTimeout's own doc comment): THIS
	// field's own 800ms was later reused, deliberately and directly, as
	// IdentityEmailFetchTimeout's own retuned per-attempt value too -- this
	// field was already this codebase's own closest, real-latency-grounded
	// precedent for "one lightweight attempt at this exact users.info/
	// GetUserEmail call", so the fix for that OTHER field's own
	// unrealistically-tight 300ms simply pointed back at this number rather
	// than inventing a new one. The two fields still serve genuinely
	// different budgets (this one shares a tighter 2500ms window with a DB
	// transaction and a second outbound call; IdentityEmailFetchTimeout
	// shares a looser ~3s window but is spent across up to
	// IdentityEmailFetchMaxAttempts attempts) -- they now simply agree on
	// what a single realistic attempt at this call costs.
	SlackInteractivityIdentityFetchTimeout time.Duration

	// IdentityLinkPromptTTL is how long a magic-link identity_link_prompts
	// row (§13.2 step 4: "a short-lived magic link") stays valid before
	// GetIdentityLinkPromptByNonceHash's own caller (internal/adapters/
	// inbound/identitylink's magic-link consume handler) must treat it as
	// expired. Not specified in the plan beyond "short-lived"; chosen as
	// 24h -- long enough that a Slack/Linear user who doesn't immediately
	// click the link (e.g. it arrives outside working hours) still has a
	// realistic same-day-ish window, but still bounded, unlike
	// UserSessionTTL's own "stay signed in" 30-day figure, which answers a
	// genuinely different question (how long a browser stays logged in,
	// not how long a one-time linking action stays offered).
	IdentityLinkPromptTTL time.Duration

	// --- Audit-remediation (completeness-vs-plan lens, GitHub PR-payload-
	// correctness batch): no ordering relationship with either invariant
	// chain above (or with any prior standalone addition), so -- per those
	// additions' own precedent -- a plain field with a sensible default,
	// not wired into a fake invariant link.

	// GitHubGetPRTimeout bounds a single internal/adapters/outbound/
	// githubapi.Adapter.GetPullRequest call (a real outbound GET
	// https://api.github.com/repos/{owner}/{repo}/pulls/{number}), made
	// synchronously from inside internal/adapters/inbound/github's own
	// webhook handler (H5 audit fix: resolving an issue_comment mention's
	// TRUE head branch/repo, since that event type's own payload never
	// carries them directly -- see headresolve.go's own doc comment) --
	// mirrors PRCreateTimeout's/SlackAckTimeout's own identical "a genuine
	// outbound network call made inline in a webhook handler must never
	// run against an unbounded context" precedent exactly. Not specified
	// in the plan (this fix postdates it); chosen as 10s, generous for a
	// single lightweight GitHub REST GET while still keeping the whole
	// webhook response prompt.
	GitHubGetPRTimeout time.Duration
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

		ExpiredCredentialCleanupInterval: time.Hour, // not specified; chosen, comfortably frequent relative to both WSTokenTTL (24h) and UserSessionTTL (30 days)

		SnapshotMintTimeout: 60 * time.Second, // not specified; chosen (a real provider TakeSnapshot round trip, more generous than CredentialFetchTimeout)

		ReconcilerInterval: 60 * time.Second, // IMPLEMENTATION_PLAN.md row 25, explicit ("60s loop")

		ReconcilerOrphanConfirmationPeriod: 30 * time.Second, // not specified; chosen, comfortably above the realistic sub-second spawn-commit race window; exactly MinTimeoutMargin below ReconcilerInterval (the minimum Validate allows, zero slack beyond it) so the two-tick guarantee holds under the shipped default

		RepoSHAResolutionTimeout: 10 * time.Second, // not specified; chosen, matches CredentialFetchTimeout's own "lightweight call" reasoning

		ImageBuildPumpInterval: 60 * time.Second, // not specified; chosen, matches ReconcilerInterval's own cadence
		ImageBuildBackoffBase:  1 * time.Minute,  // not specified; chosen -- see EvaluateBackoff's own doc comment for the schedule this produces
		ImageBuildBackoffMax:   30 * time.Minute, // not specified; chosen -- the eventual steady-state ceiling, never the first-failure delay (§3.5)

		ContractsFingerprintResolutionTimeout: 10 * time.Second, // not specified; chosen, matches RepoSHAResolutionTimeout's own "lightweight call" reasoning

		GitSyncStepTimeout: 30 * time.Second, // not specified; chosen, generous for a local-only stash/checkout/pop step without stalling boot

		WebhookTimestampFreshnessWindow: 5 * time.Minute, // not specified; chosen, matches Slack's own commonly recommended replay window

		LinearWebhookTimestampWindow:  60 * time.Second, // Linear's own docs, explicit ("within a minute")
		LinearOutboundActivityTimeout: 3 * time.Second,  // not specified; chosen, comfortably below Linear's own 5s webhook-response requirement

		LinearSetSessionIDTimeout:        2 * time.Second,        // not specified; chosen, generous for a single-row Postgres UPDATE
		LinearSetSessionIDMaxAttempts:    3,                      // not specified; chosen -- originally matched IdentityEmailFetchMaxAttempts, which the HIGH audit fix (see that field's own doc comment) later lowered to 2; kept at 3 here since setSessionIDWithRetry's own retry is over a cheap, always-safe-to-retry LOCAL Postgres UPDATE (see LinearSetSessionIDTimeout's own doc comment), never the retried-outbound-call budget that fix was retuning
		LinearSetSessionIDRetryBaseDelay: 100 * time.Millisecond, // not specified; chosen, keeps the synchronous ingress path fast
		LinearSetSessionIDRetryMaxDelay:  500 * time.Millisecond, // not specified; chosen

		SlackAckTimeout: 10 * time.Second, // not specified; chosen, generous for a single Slack chat.postMessage POST, mirrors PRCreateTimeout's own reasoning

		SlackInteractivityAckTimeout: 2500 * time.Millisecond, // not specified; chosen, a SEPARATE and much tighter budget than SlackAckTimeout -- see field doc comment for why (Slack's real interactivity ack window is a hard ~3s, covering the whole decide+update sequence, not just SlackAckTimeout's single POST)

		OutboxPumpInterval:    5 * time.Second,  // not specified; chosen, near-real-time delivery, matches TimerPumpInterval's own reasoning
		OutboxBackoffBase:     30 * time.Second, // not specified; chosen -- see domain/outbox.EvaluateBackoff's own doc comment for the schedule this produces
		OutboxBackoffMax:      5 * time.Minute,  // not specified; chosen, shorter than ImageBuildBackoffMax since a live notification is being waited on
		OutboxDeliveryTimeout: 15 * time.Second, // not specified; chosen, generous for a single outbound notifier POST
		OutboxClaimDuration:   45 * time.Second, // not specified; chosen -- exactly MinTimeoutMargin above OutboxDeliveryTimeout (audit fix H6, see field doc comment)

		IntentClassifierLLMTimeout: 10 * time.Second, // not specified; chosen, matches RepoSHAResolutionTimeout's own "lightweight call" reasoning

		IdentityEmailFetchTimeout:              800 * time.Millisecond, // audit fix HIGH -- was 300ms (itself audit fix L5's shrink from 10s); see field doc comment for why 300ms was unrealistically tight and why 800ms (reusing SlackInteractivityIdentityFetchTimeout's own precedent) is the realistic figure
		IdentityEmailFetchMaxAttempts:          2,                      // audit fix HIGH -- was 3; see field doc comment for why the attempt count, not the per-attempt timeout, absorbed the budget cut this time
		IdentityEmailFetchRetryBaseDelay:       100 * time.Millisecond, // audit fix L5 -- was 200ms; see IdentityEmailFetchRetryMaxDelay's own doc comment
		IdentityEmailFetchRetryMaxDelay:        150 * time.Millisecond, // audit fix L5 -- was 1s; see field doc comment for the full worst-case timing budget
		SlackInteractivityIdentityFetchTimeout: 800 * time.Millisecond, // not specified; chosen, comfortably inside SlackInteractivityAckTimeout with margin for DecidePlan+chat.update
		IdentityLinkPromptTTL:                  24 * time.Hour,         // not specified beyond "short-lived"; chosen

		GitHubGetPRTimeout: 10 * time.Second, // not specified (fix postdates the plan); chosen, generous for a single GitHub REST GET, mirrors PRCreateTimeout/SlackAckTimeout's own reasoning
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
	// Deliberately the weaker of two possible statements -- "the overall
	// budget exceeds the boot-sub-phase's own estimate" -- not "the overall
	// budget exceeds cold-start-plus-boot summed" (which would require
	// FirstConnectBudget > ProviderWorstColdStart+ImagePullBootP99 instead).
	// See FirstConnectBudget's and ImagePullBootP99's own doc comments
	// above for why this codebase does not currently model those two as an
	// additive sum.
	check("FirstConnectBudget > ImagePullBootP99",
		"FirstConnectBudget", t.FirstConnectBudget, "ImagePullBootP99", t.ImagePullBootP99)

	// Step 25 fix (reconciler orphan-GC debounce): ReconcilerOrphanConfirmationPeriod
	// must stay at least MinTimeoutMargin below ReconcilerInterval, or the
	// "reaped on the SECOND consecutive tick, never the first" guarantee
	// app/reconciler.Reconciler's own debounce promises silently degrades
	// to "third tick" (or later) instead -- see that field's own doc
	// comment for the full reasoning. The shipped defaults (60s/30s) sit
	// exactly at that minimum margin, not with extra slack beyond it.
	check("ReconcilerInterval > ReconcilerOrphanConfirmationPeriod",
		"ReconcilerInterval", t.ReconcilerInterval, "ReconcilerOrphanConfirmationPeriod", t.ReconcilerOrphanConfirmationPeriod)

	// Audit fix (H6, outbox claim-lease race): OutboxClaimDuration must
	// stay at least MinTimeoutMargin above OutboxDeliveryTimeout, or a
	// single real delivery attempt's own worst-case duration could outlive
	// the very claim-renewal window attempt() just refreshed to protect it
	// -- see OutboxClaimDuration's own doc comment for the full reasoning.
	check("OutboxClaimDuration > OutboxDeliveryTimeout",
		"OutboxClaimDuration", t.OutboxClaimDuration, "OutboxDeliveryTimeout", t.OutboxDeliveryTimeout)

	return errors.Join(errs...)
}
