package sandbox

import (
	"fmt"
	"time"
)

// InactivityState is EvaluateInactivityTimeout's input snapshot.
type InactivityState struct {
	// LastActivity is the last activity timestamp. Zero (time.Time{})
	// means never active.
	LastActivity time.Time
	// Status is the sandbox's current state.
	Status State
	// ConnectedClientCount is the number of connected client WebSockets.
	ConnectedClientCount int
	// IsProcessing reports whether a turn is actively executing. When
	// true, inactivity checks are deferred -- the turn's own timeout
	// handles a genuinely stuck run (§5.4's Chain A). This prevents false
	// inactivity timeouts during long-running executions where the agent
	// delegates to sub-tasks and emits no tool/step events for extended
	// periods.
	IsProcessing bool
}

// InactivityConfig configures EvaluateInactivityTimeout. Populated by the
// caller from platform.Timeouts (InactivityTimeout, InactivityExtension,
// InactivityMinCheckInterval -- not given explicit values in Narvi's plan;
// chosen and documented in platform/timeouts.go, defaults of 10min/5min/30s).
type InactivityConfig struct {
	// Timeout is how long a Ready, non-processing sandbox may go without
	// activity before it is stopped.
	Timeout time.Duration
	// Extension is the additional time granted (with a warning) when
	// Timeout fires but clients are still connected.
	Extension time.Duration
	// MinCheckInterval is the minimum interval between alarm checks.
	MinCheckInterval time.Duration
}

// InactivityActionKind discriminates the InactivityAction union.
type InactivityActionKind int

// The three possible inactivity-decision actions.
const (
	InactivityActionTimeout InactivityActionKind = iota
	InactivityActionExtend
	InactivityActionSchedule
)

func (k InactivityActionKind) String() string {
	switch k {
	case InactivityActionTimeout:
		return "timeout"
	case InactivityActionExtend:
		return "extend"
	case InactivityActionSchedule:
		return "schedule"
	default:
		return fmt.Sprintf("InactivityActionKind(%d)", int(k))
	}
}

// InactivityAction is the discriminated-union result of
// EvaluateInactivityTimeout. Only the field(s) documented for Kind are
// meaningful; the others are zero-valued.
type InactivityAction struct {
	Kind InactivityActionKind
	// ShouldSnapshot is set when Kind == InactivityActionTimeout.
	ShouldSnapshot bool
	// Extension and ShouldWarn are set when Kind == InactivityActionExtend.
	Extension  time.Duration
	ShouldWarn bool
	// NextCheck is set when Kind == InactivityActionSchedule.
	NextCheck time.Duration
}

// EvaluateInactivityTimeout decides what action to take for the
// `inactivity` named persistent timer (§2).
//
// Narvi's sandbox state machine has a single live/steady state (Ready), so
// "only check inactivity in the live steady state" here means Status ==
// StateReady; whether a turn is in flight is IsProcessing, not a distinct
// sandbox status.
func EvaluateInactivityTimeout(state InactivityState, cfg InactivityConfig, now time.Time) InactivityAction {
	// Terminal states don't need inactivity monitoring.
	if IsDeadSandboxStatus(state.Status) {
		return InactivityAction{Kind: InactivityActionSchedule, NextCheck: cfg.MinCheckInterval}
	}

	// No activity recorded yet.
	if state.LastActivity.IsZero() {
		return InactivityAction{Kind: InactivityActionSchedule, NextCheck: cfg.MinCheckInterval}
	}

	// Only the live steady state is checked.
	if state.Status != StateReady {
		return InactivityAction{Kind: InactivityActionSchedule, NextCheck: cfg.MinCheckInterval}
	}

	// Defer while a turn is in flight: the agent may go quiet for
	// extended periods (e.g. delegating to sub-tasks) without emitting
	// events, which would otherwise look like inactivity. The turn's own
	// timeout handles a truly stuck run.
	if state.IsProcessing {
		return InactivityAction{Kind: InactivityActionSchedule, NextCheck: cfg.MinCheckInterval}
	}

	inactiveTime := now.Sub(state.LastActivity)

	if inactiveTime >= cfg.Timeout {
		// Clients still connected: they may be actively reviewing --
		// grant an extension and warn them.
		if state.ConnectedClientCount > 0 {
			return InactivityAction{
				Kind:       InactivityActionExtend,
				Extension:  cfg.Extension,
				ShouldWarn: true,
			}
		}
		// No clients connected: timeout and snapshot.
		return InactivityAction{Kind: InactivityActionTimeout, ShouldSnapshot: true}
	}

	// Not yet timed out: schedule the next check at the remaining time
	// (never sooner than MinCheckInterval).
	remaining := cfg.Timeout - inactiveTime
	if remaining < cfg.MinCheckInterval {
		remaining = cfg.MinCheckInterval
	}
	return InactivityAction{Kind: InactivityActionSchedule, NextCheck: remaining}
}
