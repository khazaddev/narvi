package automation

import (
	"errors"
	"fmt"
)

// Status is an automation's own top-level lifecycle state -- matches the
// automation_status Postgres enum exactly (migrations/
// 000051_automations.up.sql).
type Status string

const (
	// StatusActive is an automation eligible to fire (subject to whatever
	// trigger condition Step 52 evaluates -- out of this package's own
	// scope). The initial state every automation is created in.
	StatusActive Status = "active"
	// StatusPaused is an automation that will not fire again until a
	// maintainer/admin explicitly resumes it (mockups.html's own
	// Automations view: "auto-paused chip + Resume") -- reached either via
	// TriggerAutoPause (this package's own §3.5 auto-pause mechanism) or,
	// in principle, a direct admin pause action (no such caller exists yet
	// in this Step; Transition supports the edge regardless, since pausing
	// and auto-pausing land on the identical Status either way).
	StatusPaused Status = "paused"
)

// Trigger names the kind of event/command being applied to an automation's
// own Status. Mirrors internal/domain/plan.Trigger's shape exactly: no
// Trigger here carries a payload, so it is usable directly as a map key.
type Trigger int

const (
	// TriggerAutoPause is app/automation's own closeout step reporting that
	// EvaluateFailureStrike (strike.go) crossed AutoPauseThreshold for this
	// automation: Active -> Paused. See strike.go's own doc comment for
	// the decision this trigger is applied in response to.
	TriggerAutoPause Trigger = iota
	// TriggerResume is a maintainer/admin resuming a paused automation:
	// Paused -> Active. No caller exists yet in this Step (§8.4/§10 own
	// the actual resume surface, per this Step's own scope note) -- this
	// edge is modeled now so a later Step's own resume endpoint needs no
	// domain-layer change to use it, mirroring how ActionManageAutomations
	// (internal/domain/authz) was likewise reserved ahead of its first
	// caller.
	TriggerResume
)

var triggerNames = [...]string{"auto_pause", "resume"}

func (t Trigger) String() string {
	if t < 0 || int(t) >= len(triggerNames) {
		return fmt.Sprintf("Trigger(%d)", int(t))
	}
	return triggerNames[t]
}

// ErrIllegalTransition is the sentinel error Transition returns for any
// (from, trigger) pair not in the transitions table -- mirrors
// internal/domain/turn/plan/sandbox's own identical sentinel-plus-detail
// shape exactly.
var ErrIllegalTransition = errors.New("automation: illegal transition")

// IllegalTransitionError reports a (from, trigger) combination Transition
// rejected because it is not a legal edge in the state machine.
type IllegalTransitionError struct {
	From    Status
	Trigger Trigger
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("automation: illegal transition: from %s via %s", e.From, e.Trigger)
}

func (e *IllegalTransitionError) Unwrap() error { return ErrIllegalTransition }

// transitions is the explicit Transition(from, trigger) (to, error) table
// §11 requires -- a two-state machine, each state's own single outgoing
// edge landing on the other.
var transitions = map[Status]map[Trigger]Status{
	StatusActive: {
		TriggerAutoPause: StatusPaused,
	},
	StatusPaused: {
		TriggerResume: StatusActive,
	},
}

// Transition is the single authority for whether an automation may move
// from status `from` via `trig` -- and, if so, what status it lands in.
// Every illegal combination returns a typed *IllegalTransitionError, never
// a zero-value Status silently accepted. Mirrors internal/domain/plan.
// Transition's own exact shape.
func Transition(from Status, trig Trigger) (Status, error) {
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
