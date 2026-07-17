package gitstate

import (
	"errors"
	"fmt"
)

// State is one of the boot sequence's ten states (§3.4: "Boot:
// stash-if-dirty -> checkout session branch (create from base if absent) ->
// stash pop"). The state name alone always makes clear whether a stash
// currently exists unpopped, without needing to consult which trigger
// produced it -- see RequiresStashRecovery, the P0 check this exists for.
type State string

// The ten boot-sequence states.
const (
	// StateIdle is the boot sequence not yet started.
	StateIdle State = "idle"
	// StateStashing is the tree having been found dirty; a stash attempt is
	// in progress.
	StateStashing State = "stashing"
	// StateCheckingOutClean is the tree having been found clean (no stash
	// needed); checkout is in progress.
	StateCheckingOutClean State = "checking_out_clean"
	// StateCheckingOutWithStash is a stash having been successfully taken;
	// checkout is in progress; the stash is not yet popped.
	StateCheckingOutWithStash State = "checking_out_with_stash"
	// StatePoppingStash is checkout having succeeded on the stashed path;
	// pop is in progress.
	StatePoppingStash State = "popping_stash"
	// StateReady is terminal success: the correct branch is checked out and
	// no stash is outstanding -- whether because none was ever needed, or
	// because it was successfully popped.
	StateReady State = "ready"
	// StateStashFailed is terminal failure: the stash attempt itself
	// failed. No stash exists -- nothing was ever moved out of the working
	// tree -- so nothing has been lost; the tree is presumably still dirty
	// in place, unchanged from before the attempt.
	StateStashFailed State = "stash_failed"
	// StateCheckoutFailedClean is terminal failure: checkout failed on the
	// clean-tree path. No stash exists.
	StateCheckoutFailedClean State = "checkout_failed_clean"
	// StateCheckoutFailedWithStash is terminal failure: checkout failed on
	// the stashed path. A stash exists and has not been popped -- this is a
	// P0 state requiring manual recovery (see RequiresStashRecovery).
	StateCheckoutFailedWithStash State = "checkout_failed_with_stash"
	// StatePopFailed is terminal failure: checkout succeeded but the pop
	// failed (e.g. the stash conflicts with the newly-checked-out branch).
	// A stash exists and has not been popped -- this is a P0 state
	// requiring manual recovery (see RequiresStashRecovery).
	StatePopFailed State = "pop_failed"
)

// terminalStates are the five states with no outgoing edge in the
// transitions table below: one success (Ready) and four distinct failure
// modes, kept separate rather than collapsed into one generic "failed" so
// RequiresStashRecovery can tell them apart.
var terminalStates = map[State]bool{
	StateReady:                   true,
	StateStashFailed:             true,
	StateCheckoutFailedClean:     true,
	StateCheckoutFailedWithStash: true,
	StatePopFailed:               true,
}

// IsTerminal reports whether state is one of the boot machine's five
// terminal states. An unrecognized/foreign State reads as non-terminal
// (deny-list, not allow-list -- same convention as
// internal/domain/sandbox.IsDeadSandboxStatus and
// internal/domain/turn.IsTerminal), so a caller scanning for in-progress
// boots never mistakes a garbage value for "done".
func IsTerminal(state State) bool {
	return terminalStates[state]
}

// stashOutstandingStates are the two terminal states in which a stash was
// taken and never popped: the working-tree edits it holds are durable data
// sitting in the stash list, unreachable from any branch, and require
// manual recovery. This is the P0 set §3.4 exists to make impossible to
// miss from persisted state alone.
var stashOutstandingStates = map[State]bool{
	StateCheckoutFailedWithStash: true,
	StatePopFailed:               true,
}

// RequiresStashRecovery reports whether state is one of the two terminal
// states in which a stash was taken but never popped
// (CheckoutFailedWithStash, PopFailed) -- i.e. whether there is a stash
// sitting in the stash list that still needs manual recovery. This is the
// single most important correctness property of this package: it is the
// "did we lose the user's uncommitted work" check (§3.4: "User working-tree
// edits are durable data -- losing them is a P0"). It is false for every
// other state, including Ready (no stash outstanding, whether none was ever
// needed or it was popped) and the other two terminal failures
// (StashFailed, CheckoutFailedClean: neither ever took a stash).
func RequiresStashRecovery(state State) bool {
	return stashOutstandingStates[state]
}

// Trigger names the kind of event/command being applied to the boot
// machine. Unlike internal/domain/sandbox's Trigger, no Trigger here
// carries a payload -- every (from, trigger) edge in the transitions table
// below has exactly one fixed destination, so Trigger is usable directly as
// a map key with no wrapping struct needed (same reasoning as
// internal/domain/turn.Trigger).
type Trigger int

const (
	// TriggerDirtyDetected is the working tree found dirty at boot: Idle ->
	// Stashing. Legal only from Idle.
	TriggerDirtyDetected Trigger = iota
	// TriggerCleanDetected is the working tree found clean at boot: Idle ->
	// CheckingOutClean (no stash needed). Legal only from Idle.
	TriggerCleanDetected
	// TriggerStashSucceeded is the stash attempt succeeding: Stashing ->
	// CheckingOutWithStash. Legal only from Stashing.
	TriggerStashSucceeded
	// TriggerStashFailed is the stash attempt itself failing: Stashing ->
	// StashFailed. Legal only from Stashing.
	TriggerStashFailed
	// TriggerCheckoutSucceeded is the session-branch checkout succeeding.
	// Legal from either CheckingOutClean (-> Ready) or
	// CheckingOutWithStash (-> PoppingStash) -- same trigger name, different
	// destination depending on the FROM state, exactly like
	// internal/domain/turn's TriggerCancel applying uniformly across
	// several from-states, or internal/domain/sandbox's TriggerSuspect
	// firing from five different live states.
	TriggerCheckoutSucceeded
	// TriggerCheckoutFailed is the session-branch checkout failing. Legal
	// from either CheckingOutClean (-> CheckoutFailedClean) or
	// CheckingOutWithStash (-> CheckoutFailedWithStash).
	TriggerCheckoutFailed
	// TriggerPopSucceeded is the stash pop succeeding: PoppingStash ->
	// Ready. Legal only from PoppingStash.
	TriggerPopSucceeded
	// TriggerPopFailed is the stash pop failing (e.g. conflicts with the
	// newly-checked-out branch): PoppingStash -> PopFailed. Legal only from
	// PoppingStash.
	TriggerPopFailed
)

var triggerNames = [...]string{
	"dirty_detected", "clean_detected", "stash_succeeded", "stash_failed",
	"checkout_succeeded", "checkout_failed", "pop_succeeded", "pop_failed",
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
var ErrIllegalTransition = errors.New("gitstate: illegal transition")

// IllegalTransitionError reports a (from, trigger) combination Transition
// rejected because it is not a legal edge in the state machine.
type IllegalTransitionError struct {
	From    State
	Trigger Trigger
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("gitstate: illegal transition: from %s via %s", e.From, e.Trigger)
}

func (e *IllegalTransitionError) Unwrap() error { return ErrIllegalTransition }

// transitions is the explicit Transition(from, trigger) (to, error) table
// §11 requires, matching internal/domain/sandbox and internal/domain/turn's
// house style. Every (from, trigger) edge the boot machine allows is an
// entry here; anything not listed -- including every edge out of the five
// terminal states (Ready, StashFailed, CheckoutFailedClean,
// CheckoutFailedWithStash, PopFailed), which are absent from this map
// entirely -- is illegal.
var transitions = map[State]map[Trigger]State{
	StateIdle: {
		TriggerDirtyDetected: StateStashing,
		TriggerCleanDetected: StateCheckingOutClean,
	},
	StateStashing: {
		TriggerStashSucceeded: StateCheckingOutWithStash,
		TriggerStashFailed:    StateStashFailed,
	},
	StateCheckingOutClean: {
		TriggerCheckoutSucceeded: StateReady,
		TriggerCheckoutFailed:    StateCheckoutFailedClean,
	},
	StateCheckingOutWithStash: {
		TriggerCheckoutSucceeded: StatePoppingStash,
		TriggerCheckoutFailed:    StateCheckoutFailedWithStash,
	},
	StatePoppingStash: {
		TriggerPopSucceeded: StateReady,
		TriggerPopFailed:    StatePopFailed,
	},
}

// Transition is the single authority for whether the boot machine may move
// from state `from` via `trig` -- and, if so, what state it lands in. Every
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
