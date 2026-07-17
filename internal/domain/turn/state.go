package turn

import (
	"errors"
	"fmt"
)

// State is one of the turn's own six states, matching the turn_status
// enum exactly (migrations/000005_turns.up.sql).
type State string

// The six turn states (§3.3: "pending → dispatched → processing →
// completed | failed | cancelled").
const (
	StatePending    State = "pending"
	StateDispatched State = "dispatched"
	StateProcessing State = "processing"
	StateCompleted  State = "completed"
	StateFailed     State = "failed"
	StateCancelled  State = "cancelled"
)

// terminalStates are the three states with no outgoing edge in the
// transitions table below.
var terminalStates = map[State]bool{
	StateCompleted: true,
	StateFailed:    true,
	StateCancelled: true,
}

// IsTerminal reports whether status is one of the turn machine's three
// terminal states (Completed, Failed, Cancelled). An unrecognized/foreign
// State reads as non-terminal (deny-list, not allow-list — same
// convention as internal/domain/sandbox.IsDeadSandboxStatus), so a caller
// scanning for pending work never mistakes a garbage value for "done".
func IsTerminal(status State) bool {
	return terminalStates[status]
}

// Trigger names the kind of event/command being applied to the turn
// machine. Unlike internal/domain/sandbox's TriggerKind, no Trigger here
// carries a payload (no gen fencing, no dynamic target) — every (from,
// trigger) edge in the transitions table below has exactly one fixed
// destination, so Trigger is usable directly as a map key with no
// wrapping struct needed.
//
// Trigger.String() is the name logged as `trigger` on every state
// transition (§5.3).
type Trigger int

const (
	// TriggerDispatch is the turn being handed to an already-live sandbox:
	// Pending → Dispatched. Per §3.3 ("Enqueue → if no live sandbox,
	// trigger spawn and return; dispatch happens when sandbox connects"),
	// this transition only fires once a sandbox is live — it is NOT "the
	// turn is queued, waiting for a sandbox". This is the transition that
	// arms turn_deadline — see doc.go for why.
	TriggerDispatch Trigger = iota
	// TriggerStartProcessing is the agent confirming it has actually begun
	// working on the dispatched turn: Dispatched → Processing. Does not
	// re-arm turn_deadline — the timer is already running from
	// TriggerDispatch, so the (expected-to-be-brief) hand-off gap between
	// "sandbox connected" and "agent confirmed it started" is bounded by
	// the same timer, not left unwatched.
	TriggerStartProcessing
	// TriggerComplete is a real terminal event arriving reporting the
	// agent genuinely finished: Processing → Completed.
	TriggerComplete
	// TriggerFail is a real terminal event arriving reporting the agent
	// genuinely failed: Processing → Failed. Implies session-level
	// failure_reason = failed (the generic case) — distinct from
	// TriggerTimeout precisely so a genuine agent failure and a
	// turn_deadline expiry are never confused (cancelled ≠ failed ≠
	// timeout ≠ never_started).
	TriggerFail
	// TriggerTimeout is turn_deadline expiring with no terminal event
	// having arrived: Processing → Failed. Its own distinct trigger (not
	// TriggerFail) so a genuine agent failure and a deadline expiry are
	// never confused. Implies session-level failure_reason = timeout.
	TriggerTimeout
	// TriggerAbandon is the turn being given up on before it ever reached
	// Processing (e.g. the session's spawn attempts are exhausted, or the
	// session itself is being torn down): Pending → Failed or Dispatched
	// → Failed. Implies session-level failure_reason = never_started,
	// precisely because it never reached Processing. See doc.go for why
	// this is one trigger usable from two FROM states rather than two
	// distinct triggers.
	TriggerAbandon
	// TriggerCancel is an explicit cancel from any pre-terminal state:
	// Pending → Cancelled, Dispatched → Cancelled, Processing →
	// Cancelled. Implies session-level failure_reason = cancelled.
	TriggerCancel
)

var triggerNames = [...]string{
	"dispatch", "start_processing", "complete", "fail", "timeout", "abandon", "cancel",
}

func (t Trigger) String() string {
	if t < 0 || int(t) >= len(triggerNames) {
		return fmt.Sprintf("Trigger(%d)", int(t))
	}
	return triggerNames[t]
}

// ErrIllegalTransition is the sentinel error Transition returns for any
// (from, trigger) pair not in the transitions table, wrapped by
// IllegalTransitionError so callers/tests can tell it apart via errors.Is
// while still getting full structured detail via errors.As.
var ErrIllegalTransition = errors.New("turn: illegal transition")

// IllegalTransitionError reports a (from, trigger) combination Transition
// rejected because it is not a legal edge in the state machine.
type IllegalTransitionError struct {
	From    State
	Trigger Trigger
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("turn: illegal transition: from %s via %s", e.From, e.Trigger)
}

func (e *IllegalTransitionError) Unwrap() error { return ErrIllegalTransition }

// transitions is the explicit Transition(from, trigger) (to, error) table
// §11 requires. Every (from, trigger) edge the state machine allows is an
// entry here; anything not listed — including every edge out of the three
// terminal states, which are absent from this map entirely — is illegal.
// Every edge here has exactly one fixed destination (no dynamic-target
// triggers, unlike internal/domain/sandbox's Recover/GraceExpired).
var transitions = map[State]map[Trigger]State{
	StatePending: {
		TriggerDispatch: StateDispatched,
		TriggerAbandon:  StateFailed,
		TriggerCancel:   StateCancelled,
	},
	StateDispatched: {
		TriggerStartProcessing: StateProcessing,
		TriggerAbandon:         StateFailed,
		TriggerCancel:          StateCancelled,
	},
	StateProcessing: {
		TriggerComplete: StateCompleted,
		TriggerFail:     StateFailed,
		TriggerTimeout:  StateFailed,
		TriggerCancel:   StateCancelled,
	},
}

// Transition is the single authority for whether a turn may move from
// state `from` via `trig` — and, if so, what state it lands in. Every
// illegal combination returns a typed *IllegalTransitionError, never a
// zero-value State silently accepted.
func Transition(from State, trig Trigger) (State, error) {
	byTrigger, ok := transitions[from]
	if !ok {
		return "", &IllegalTransitionError{From: from, Trigger: trig}
	}

	to, ok := byTrigger[trig]
	if !ok {
		return "", &IllegalTransitionError{From: from, Trigger: trig}
	}

	return to, nil
}
