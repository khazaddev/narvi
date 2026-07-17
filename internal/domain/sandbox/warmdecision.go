package sandbox

import "fmt"

// WarmState is EvaluateWarmDecision's input snapshot.
type WarmState struct {
	// HasActiveWebSocket reports whether a live sandbox WebSocket
	// connection currently exists.
	HasActiveWebSocket bool
	// Status is the sandbox's current state, or "" if no sandbox exists
	// yet for this session.
	Status State
	// IsSpawningInMemory reports whether a spawn is already in progress
	// (in-memory flag).
	IsSpawningInMemory bool
}

// WarmActionKind discriminates the WarmAction union.
type WarmActionKind int

// The two possible warm-decision actions.
const (
	WarmActionSpawn WarmActionKind = iota
	WarmActionSkip
)

func (k WarmActionKind) String() string {
	switch k {
	case WarmActionSpawn:
		return "spawn"
	case WarmActionSkip:
		return "skip"
	default:
		return fmt.Sprintf("WarmActionKind(%d)", int(k))
	}
}

// WarmAction is the discriminated-union result of EvaluateWarmDecision.
type WarmAction struct {
	Kind WarmActionKind
	// Reason is set when Kind == WarmActionSkip.
	Reason string
}

// warmSkipStatuses are the statuses a warm spawn must not duplicate or
// disturb: Spawning/Connecting/Booting because a boot is already in
// progress (see spawndecision.go's identical guard), Suspect because the
// sandbox is mid-grace-period and may still recover (§3.2 -- warming over
// it would risk duplicating/orphaning a box the grace timer hasn't finished
// judging), and Stale/Stopped/Failed because those are dead statuses a warm
// spawn should recover via the full EvaluateSpawnDecision path, not this
// coarse pre-gate.
var warmSkipStatuses = map[State]bool{
	StateSpawning:   true,
	StateConnecting: true,
	StateBooting:    true,
	StateSuspect:    true,
	StateStale:      true,
	StateStopped:    true,
	StateFailed:     true,
}

// EvaluateWarmDecision decides whether to warm (proactively spawn) a
// sandbox, triggered when a user starts typing, to reduce latency for
// their first prompt (§8 item 7: "warm-on-type ... must not create orphan
// sessions").
func EvaluateWarmDecision(state WarmState) WarmAction {
	if state.HasActiveWebSocket {
		return WarmAction{Kind: WarmActionSkip, Reason: "sandbox already connected"}
	}

	if state.IsSpawningInMemory {
		return WarmAction{Kind: WarmActionSkip, Reason: "already spawning"}
	}

	if warmSkipStatuses[state.Status] {
		return WarmAction{Kind: WarmActionSkip, Reason: "sandbox status is " + string(state.Status)}
	}

	return WarmAction{Kind: WarmActionSpawn}
}
