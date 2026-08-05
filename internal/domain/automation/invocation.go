package automation

import (
	"errors"
	"fmt"
)

// InvocationStatus is one invocation's own outcome state -- matches the
// automation_invocation_status Postgres enum exactly (migrations/
// 000052_automation_invocations.up.sql). Unlike Run below, an invocation
// has no interesting internal progress of its own to name: it is either
// still waiting on its own fanned-out runs, or it has been decided --
// mirroring internal/domain/plan.Status's shape (one non-terminal state,
// terminal states with no outgoing edge) more closely than turn's own
// six-state machine.
type InvocationStatus string

const (
	// InvocationStatusPending is an invocation not yet fanned out, or
	// fanned out with at least one run still non-terminal. The initial
	// state every invocation is created in.
	InvocationStatusPending InvocationStatus = "pending"
	// InvocationStatusSucceeded is every one of this invocation's own
	// fanned-out runs having reached RunStatusSucceeded. Terminal.
	InvocationStatusSucceeded InvocationStatus = "succeeded"
	// InvocationStatusFailed is every run terminal, with at least one
	// having reached RunStatusFailed -- a single failed target fails the
	// WHOLE invocation for §3.5's own auto-pause accounting purposes (a
	// judgment call: §3.5 does not say whether a partial fan-out failure
	// counts as "a failed invocation"; failing the whole invocation on ANY
	// failed run is the conservative reading, and the one consistent with
	// auto-pause existing to protect against an automation that is
	// genuinely, systematically broken against at least one of its own
	// targets). Terminal.
	InvocationStatusFailed InvocationStatus = "failed"
)

// InvocationTrigger is the invocation machine's own Trigger type -- named
// distinctly from automation.go's Trigger (rather than overloading one
// Trigger type across two different machines in the same package) because
// the two machines' edges are never valid against each other's Status
// type, and a single shared Trigger type would let a caller pass an
// automation-machine trigger to Transition (this file) and vice versa with
// no compile-time signal. Mirrors this codebase's own established
// per-machine Trigger convention (turn.Trigger, plan.Trigger,
// sandbox.TriggerKind are likewise never shared across packages) applied
// WITHIN one package instead, since all three of this package's own
// machines happen to live together.
type InvocationTrigger int

const (
	// TriggerAllRunsSucceeded is every fanned-out run having reached
	// RunStatusSucceeded: Pending -> Succeeded.
	TriggerAllRunsSucceeded InvocationTrigger = iota
	// TriggerAnyRunFailed is every fanned-out run terminal, with at least
	// one RunStatusFailed: Pending -> Failed.
	TriggerAnyRunFailed
)

var invocationTriggerNames = [...]string{"all_runs_succeeded", "any_run_failed"}

func (t InvocationTrigger) String() string {
	if t < 0 || int(t) >= len(invocationTriggerNames) {
		return fmt.Sprintf("InvocationTrigger(%d)", int(t))
	}
	return invocationTriggerNames[t]
}

// ErrIllegalInvocationTransition is InvocationTransition's own sentinel --
// named distinctly from ErrIllegalTransition (automation.go) for the same
// reason InvocationTrigger is its own type, immediately above.
var ErrIllegalInvocationTransition = errors.New("automation: illegal invocation transition")

// IllegalInvocationTransitionError reports a (from, trigger) combination
// InvocationTransition rejected.
type IllegalInvocationTransitionError struct {
	From    InvocationStatus
	Trigger InvocationTrigger
}

func (e *IllegalInvocationTransitionError) Error() string {
	return fmt.Sprintf("automation: illegal invocation transition: from %s via %s", e.From, e.Trigger)
}

func (e *IllegalInvocationTransitionError) Unwrap() error { return ErrIllegalInvocationTransition }

var invocationTransitions = map[InvocationStatus]map[InvocationTrigger]InvocationStatus{
	InvocationStatusPending: {
		TriggerAllRunsSucceeded: InvocationStatusSucceeded,
		TriggerAnyRunFailed:     InvocationStatusFailed,
	},
}

// InvocationTransition is the single authority for whether an invocation
// may move from status `from` via `trig` -- and, if so, what status it
// lands in. Every illegal combination returns a typed
// *IllegalInvocationTransitionError, never a zero-value InvocationStatus
// silently accepted.
func InvocationTransition(from InvocationStatus, trig InvocationTrigger) (InvocationStatus, error) {
	byTrigger, ok := invocationTransitions[from]
	if !ok {
		return "", &IllegalInvocationTransitionError{From: from, Trigger: trig}
	}

	to, ok := byTrigger[trig]
	if !ok {
		return "", &IllegalInvocationTransitionError{From: from, Trigger: trig}
	}

	return to, nil
}

// InvocationOutcome is EvaluateInvocationOutcome's verdict.
type InvocationOutcome struct {
	// Ready reports whether every one of this invocation's own fanned-out
	// runs has reached a terminal state -- false means "still waiting",
	// and Trigger/Failed carry no meaning.
	Ready bool
	// Trigger is the InvocationTransition trigger the caller should apply
	// -- only meaningful when Ready is true.
	Trigger InvocationTrigger
	// Failed reports whether this invocation's own outcome is a failure
	// (at least one run failed) -- only meaningful when Ready is true.
	// Exactly Trigger == TriggerAnyRunFailed, surfaced as its own bool so
	// callers feeding EvaluateFailureStrike (strike.go) don't need to
	// switch on Trigger themselves.
	Failed bool
}

// EvaluateInvocationOutcome decides whether an invocation with totalRuns
// fanned-out runs, of which terminalRuns have reached a terminal state
// (succeeded or failed) and failedRuns of those are specifically
// RunStatusFailed, is ready to close, and if so what it decided.
//
// totalRuns/terminalRuns/failedRuns are plain counts the caller derives
// from its own automation_runs query (app/automation's own closeout.go) --
// this function does no counting of its own, matching every other pure
// decision in this package.
func EvaluateInvocationOutcome(totalRuns, terminalRuns, failedRuns int) InvocationOutcome {
	if terminalRuns < totalRuns {
		return InvocationOutcome{Ready: false}
	}
	if failedRuns > 0 {
		return InvocationOutcome{Ready: true, Trigger: TriggerAnyRunFailed, Failed: true}
	}
	return InvocationOutcome{Ready: true, Trigger: TriggerAllRunsSucceeded, Failed: false}
}
