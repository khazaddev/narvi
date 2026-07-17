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
