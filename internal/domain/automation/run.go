package automation

import (
	"errors"
	"fmt"
	"time"

	"github.com/khazaddev/narvi/internal/domain/turn"
)

// RunStatus is one run's own state -- matches the automation_run_status
// Postgres enum exactly (migrations/000053_automation_runs.up.sql), named
// to match §3.5's own sweep vocabulary verbatim ("orphaned starting
// runs... running...").
type RunStatus string

const (
	// RunStatusStarting is a run whose session has been created (or is
	// about to be) but whose turn has not yet reached Processing -- the
	// sandbox may still be cold-starting. The initial state every run is
	// created in. §3.5's "starting >5 min" sweep threshold applies here.
	RunStatusStarting RunStatus = "starting"
	// RunStatusRunning is a run whose turn has reached Processing -- the
	// agent is genuinely working. §3.5's "running >90 min" sweep threshold
	// applies here.
	RunStatusRunning RunStatus = "running"
	// RunStatusSucceeded is a run whose turn completed successfully.
	// Terminal.
	RunStatusSucceeded RunStatus = "succeeded"
	// RunStatusFailed is a run whose turn failed, was cancelled, or never
	// even reached a live sandbox before being abandoned -- turn.
	// TriggerFail/TriggerTimeout/TriggerAbandon/TriggerCancel all collapse
	// into this ONE RunStatus (a judgment call: an automation run is
	// unattended by construction in this Step -- Step 52/76 own any future
	// surface that could ever legitimately "cancel" one -- so there is no
	// present distinction worth a fifth RunStatus the way turn's own
	// cancelled≠failed≠timeout≠never_started taxonomy earns its keep for
	// an interactive session; DeriveRunStatus, below, still labels which
	// turn.FailureReason applied for anything that wants to log it, it
	// just does not branch this package's own RunStatus on it). Also
	// reached via RunTriggerOrphanTimeout (the sweep, §3.5) and
	// RunTriggerCreateFailed (session/turn creation itself failed before a
	// sandbox ever existed). Terminal.
	RunStatusFailed RunStatus = "failed"
)

// IsTerminal reports whether status is RunStatusSucceeded or
// RunStatusFailed -- deny-list, not allow-list (an unrecognized/foreign
// RunStatus reads as non-terminal), mirroring turn.IsTerminal's own
// identical convention.
func IsTerminal(status RunStatus) bool {
	return status == RunStatusSucceeded || status == RunStatusFailed
}

// RunTrigger names the kind of event/command being applied to a run's own
// RunStatus -- see invocation.go's own InvocationTrigger doc comment for
// why this package gives each of its three machines a distinctly-named
// Trigger type rather than sharing one.
type RunTrigger int

const (
	// RunTriggerProcessing is the linked session's turn reaching
	// Processing: Starting -> Running.
	RunTriggerProcessing RunTrigger = iota
	// RunTriggerComplete is the linked turn completing successfully:
	// Running -> Succeeded.
	RunTriggerComplete
	// RunTriggerFail is the linked turn reaching any terminal non-success
	// outcome (failed/timeout/cancelled) while genuinely Running: Running
	// -> Failed.
	RunTriggerFail
	// RunTriggerCreateFailed is session/turn creation itself failing for
	// this run's own target, before any sandbox ever existed: Starting ->
	// Failed.
	RunTriggerCreateFailed
	// RunTriggerOrphanTimeout is the recovery sweep (§3.5) confirming this
	// run has been stuck past its own status's threshold: Starting ->
	// Failed, or Running -> Failed.
	RunTriggerOrphanTimeout
)

var runTriggerNames = [...]string{"processing", "complete", "fail", "create_failed", "orphan_timeout"}

func (t RunTrigger) String() string {
	if t < 0 || int(t) >= len(runTriggerNames) {
		return fmt.Sprintf("RunTrigger(%d)", int(t))
	}
	return runTriggerNames[t]
}

// ErrIllegalRunTransition is RunTransition's own sentinel.
var ErrIllegalRunTransition = errors.New("automation: illegal run transition")

// IllegalRunTransitionError reports a (from, trigger) combination
// RunTransition rejected.
type IllegalRunTransitionError struct {
	From    RunStatus
	Trigger RunTrigger
}

func (e *IllegalRunTransitionError) Error() string {
	return fmt.Sprintf("automation: illegal run transition: from %s via %s", e.From, e.Trigger)
}

func (e *IllegalRunTransitionError) Unwrap() error { return ErrIllegalRunTransition }

var runTransitions = map[RunStatus]map[RunTrigger]RunStatus{
	RunStatusStarting: {
		RunTriggerProcessing:    RunStatusRunning,
		RunTriggerCreateFailed:  RunStatusFailed,
		RunTriggerOrphanTimeout: RunStatusFailed,
	},
	RunStatusRunning: {
		RunTriggerComplete:      RunStatusSucceeded,
		RunTriggerFail:          RunStatusFailed,
		RunTriggerOrphanTimeout: RunStatusFailed,
	},
}

// RunTransition is the single authority for whether a run may move from
// status `from` via `trig` -- and, if so, what status it lands in. Every
// illegal combination returns a typed *IllegalRunTransitionError, never a
// zero-value RunStatus silently accepted.
func RunTransition(from RunStatus, trig RunTrigger) (RunStatus, error) {
	byTrigger, ok := runTransitions[from]
	if !ok {
		return "", &IllegalRunTransitionError{From: from, Trigger: trig}
	}

	to, ok := byTrigger[trig]
	if !ok {
		return "", &IllegalRunTransitionError{From: from, Trigger: trig}
	}

	return to, nil
}

// DeriveRunStatus derives a run's own current RunStatus from an ordered
// (any order; every turn is inspected) slice of its linked session's own
// turn.Summary -- the SAME "derive an aggregate status from a child
// summary slice" shape internal/domain/session.DeriveStatus already
// establishes for a session's own turns, reused here rather than
// reinvented:
//
//   - Zero turns: Starting (a run's session/turn-creation write and this
//     read can race harmlessly; a run just fanned out with no turn
//     visible yet is exactly "still starting", never an error).
//   - Any turn Processing: Running -- regardless of any OTHER turn's own
//     state (an automation run dispatches exactly one turn in this Step's
//     own scope, but this function does not assume that; the strongest
//     signal wins).
//   - Any (other) turn non-terminal (Pending/Dispatched): Starting.
//   - Every turn terminal: derived from the LAST (final element) turn's
//     own outcome -- Completed -> Succeeded; anything else (Failed,
//     Cancelled) -> Failed (see RunStatusFailed's own doc comment for why
//     turn's three-way failure taxonomy collapses to one RunStatus here).
func DeriveRunStatus(turns []turn.Summary) RunStatus {
	if len(turns) == 0 {
		return RunStatusStarting
	}

	anyNonTerminal := false
	anyProcessing := false
	for _, t := range turns {
		if !turn.IsTerminal(t.Status) {
			anyNonTerminal = true
			if t.Status == turn.StateProcessing {
				anyProcessing = true
			}
		}
	}
	if anyProcessing {
		return RunStatusRunning
	}
	if anyNonTerminal {
		return RunStatusStarting
	}

	last := turns[len(turns)-1]
	if last.Status == turn.StateCompleted {
		return RunStatusSucceeded
	}
	return RunStatusFailed
}

// OrphanThresholds configures IsOrphaned -- both fields populated by the
// caller from platform.Timeouts (this package imports no duration literals
// of its own, §11).
type OrphanThresholds struct {
	// StartingThreshold is §3.5's "starting >5 min" sweep threshold,
	// populated from platform.Timeouts.AutomationRunStartingOrphanThreshold.
	StartingThreshold time.Duration
	// RunningThreshold is §3.5's "running >90 min" sweep threshold,
	// populated from platform.Timeouts.AutomationRunRunningOrphanThreshold.
	RunningThreshold time.Duration
}

// IsOrphaned reports whether a run currently in status, having been in
// that status continuously since `since`, is orphaned as of `now` per
// §3.5's own two sweep thresholds -- the recovery-sweep loop (app/
// automation) calls this with an injected now, never time.Now() directly
// (§11). since is exclusive: exactly at the threshold is NOT yet orphaned
// (strictly greater), matching this codebase's own established
// >-not->=-at-threshold convention for comparable timeout checks (e.g.
// app/reconciler's own ReconcilerOrphanConfirmationPeriod comparison).
//
// A status other than RunStatusStarting/RunStatusRunning (including any
// unrecognized/foreign value) is never orphaned -- deny-list, not
// allow-list, mirroring IsTerminal's own identical convention: a terminal
// run has nothing left to sweep.
func IsOrphaned(status RunStatus, since time.Time, now time.Time, cfg OrphanThresholds) bool {
	switch status {
	case RunStatusStarting:
		return now.Sub(since) > cfg.StartingThreshold
	case RunStatusRunning:
		return now.Sub(since) > cfg.RunningThreshold
	default:
		return false
	}
}
