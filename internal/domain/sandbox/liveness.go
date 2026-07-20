package sandbox

import "time"

// LivenessConfig is the two-budget model §3.2 specifies: "Two liveness
// budgets: first_connect_budget (default 240s, covers provider cold start +
// boot) distinct from steady_heartbeat_budget (default 90s; heartbeats
// every 30s)". Both EvaluateConnectingTimeout and EvaluateHeartbeatHealth
// draw from the SAME two fields -- deliberately two-tier, with no separate
// "reconnect" budget in between.
//
// Populated by the caller from platform.Timeouts.FirstConnectBudget /
// platform.Timeouts.SteadyHeartbeatBudget.
type LivenessConfig struct {
	// FirstConnectBudget is the budget while NO sign of life has been seen
	// yet.
	FirstConnectBudget time.Duration
	// SteadyHeartbeatBudget is the budget once at least one sign of life
	// has occurred.
	SteadyHeartbeatBudget time.Duration
}

// ConnectingTimeoutResult is EvaluateConnectingTimeout's verdict.
type ConnectingTimeoutResult struct {
	// IsTimedOut reports whether the sandbox has met or exceeded its
	// budget -- the boundary itself (elapsed == budget) already counts as
	// timed out, not merely elapsed strictly past it.
	IsTimedOut bool
	// Elapsed is how long it's been since the sandbox's last sign of
	// life (or since CreatedAt, if none yet).
	Elapsed time.Duration
}

// EvaluateConnectingTimeout evaluates whether a sandbox has been stuck in
// its boot phase (Spawning, Connecting, or Booting) too long. Pure
// function: safe to call for any status -- returns IsTimedOut: false for
// every status outside those three.
//
// Draws its threshold from Narvi's own two-budget model (see
// LivenessConfig) across its three-way boot phase (Spawning, Connecting,
// Booting).
//
// Measures from the sandbox's last sign of life (lastSeenAt), not raw
// creation time: the in-sandbox supervisor posts boot-progress pings
// throughout a long boot, and each one pushes the deadline forward, so a
// legitimately slow boot is never penalized. A sandbox that has never
// signalled (lastSeenAt.IsZero()) is still in its cold first-connect phase
// and gets the longer FirstConnectBudget; once it has pinged it is
// demonstrably alive and the shorter SteadyHeartbeatBudget applies.
func EvaluateConnectingTimeout(status State, createdAt, lastSeenAt time.Time, cfg LivenessConfig, now time.Time) ConnectingTimeoutResult {
	if status != StateSpawning && status != StateConnecting && status != StateBooting {
		return ConnectingTimeoutResult{IsTimedOut: false, Elapsed: 0}
	}

	budget := cfg.SteadyHeartbeatBudget
	if lastSeenAt.IsZero() {
		budget = cfg.FirstConnectBudget
	}

	since := maxTime(createdAt, lastSeenAt)
	elapsed := now.Sub(since)
	return ConnectingTimeoutResult{
		IsTimedOut: elapsed >= budget,
		Elapsed:    elapsed,
	}
}

// HeartbeatHealth is EvaluateHeartbeatHealth's verdict.
type HeartbeatHealth struct {
	// IsStale reports whether the sandbox has missed its heartbeat
	// budget -- strictly past it (age > budget); reaching the boundary
	// exactly (age == budget) is NOT yet stale, unlike
	// EvaluateConnectingTimeout's own inclusive IsTimedOut above.
	IsStale bool
	// Age is how long it's been since the last heartbeat. Zero unless
	// IsStale.
	Age time.Duration
}

// EvaluateHeartbeatHealth evaluates the steady-state (Ready) liveness of a
// sandbox that has already shown at least one sign of life.
//
// By the time a sandbox reaches Ready it has necessarily connected at
// least once (Connecting -> Booting requires a WS connection), so in
// practice lastSeenAt is never zero here and the threshold is always
// cfg.SteadyHeartbeatBudget -- unlike EvaluateConnectingTimeout, the
// FirstConnectBudget branch of the two-budget model never actually applies
// to a Ready sandbox. The lastSeenAt.IsZero() defensive branch below --
// "no heartbeat recorded yet, not stale, sandbox may still be starting" --
// is kept anyway rather than computing a nonsensical elapsed-since-epoch
// age.
func EvaluateHeartbeatHealth(lastSeenAt time.Time, cfg LivenessConfig, now time.Time) HeartbeatHealth {
	if lastSeenAt.IsZero() {
		return HeartbeatHealth{IsStale: false}
	}

	age := now.Sub(lastSeenAt)
	if age > cfg.SteadyHeartbeatBudget {
		return HeartbeatHealth{IsStale: true, Age: age}
	}
	return HeartbeatHealth{IsStale: false}
}
