package gitstate

import (
	"errors"
	"fmt"
)

// State is one of the boot sequence's twelve states (§3.4: "Boot:
// stash-if-dirty -> checkout session branch (create from base if absent) ->
// stash pop", extended by §19.3's own boot-time fetch step ahead of that
// original sequence). The state name alone always makes clear whether a
// stash currently exists unpopped, without needing to consult which trigger
// produced it -- see RequiresStashRecovery, the P0 check this exists for.
type State string

// The twelve boot-sequence states: the original ten (Step 29, §3.4's own
// stash-if-dirty -> checkout -> pop sequence) plus StateFetching/
// StateFetchFailed (Step 40, §19.3's own boot-time fetch step, which now
// runs BEFORE any of the original ten -- see StateFetching's own doc
// comment for why it is a new, separate initial state rather than a
// redefinition of StateIdle, whose own meaning/transitions are completely
// unchanged by this addition).
const (
	// StateFetching is the boot sequence's REAL starting point as of §19.3:
	// a bounded `git fetch origin <resolved-branch> <default-branch>` is
	// attempted, with credentials wired, before anything else (including
	// the dirty-check that used to run first, from StateIdle). This exists
	// so a warm-from-tip repo_image boot (§19.1/§19.2) can reconcile the
	// gap between the image's baked, possibly-stale tip and the session's
	// real target branch BEFORE checkoutBranch ever has to decide where to
	// create that branch from -- closing the "silently fork a same-named
	// branch at a stale base" trap §19.3 exists to prevent. A caller
	// (internal/sandboxagent/gitclone.SyncAll) now initializes each repo's
	// own state to StateFetching instead of StateIdle; every one of
	// StateIdle's own pre-existing transitions is completely unchanged --
	// StateFetching is a NEW predecessor in the chain, not a
	// redefinition of what Idle has always meant ("the boot sequence [the
	// original stash/checkout/pop one] not yet started").
	StateFetching State = "fetching"
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
	// StateFetchFailed is terminal failure (§19.3, Step 40): the boot-time
	// fetch itself failed AND the degrade policy does not allow proceeding
	// anyway -- i.e. the session explicitly named a branch (repo.Branch !=
	// nil) that is neither already local nor fetchable from the remote.
	// §19.3's own non-negotiable rule: this is the ONE outcome that must
	// fail the repo outright rather than degrade-and-proceed, because
	// proceeding here would mean checkoutBranch's existing HEAD-fallback
	// silently forking a same-named branch at a stale base -- exactly the
	// failure mode this whole design exists to prevent. No stash has been
	// taken yet at this point in the sequence (StateFetching precedes the
	// dirty-check entirely), so this is never a RequiresStashRecovery case.
	StateFetchFailed State = "fetch_failed"
)

// terminalStates are the six states with no outgoing edge in the
// transitions table below: one success (Ready) and five distinct failure
// modes, kept separate rather than collapsed into one generic "failed" so
// RequiresStashRecovery can tell them apart.
var terminalStates = map[State]bool{
	StateReady:                   true,
	StateStashFailed:             true,
	StateCheckoutFailedClean:     true,
	StateCheckoutFailedWithStash: true,
	StatePopFailed:               true,
	StateFetchFailed:             true,
}

// IsTerminal reports whether state is one of the boot machine's six
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
	// TriggerFetchSucceeded is the boot-time fetch (§19.3) genuinely
	// succeeding: Fetching -> Idle. Legal only from Fetching. Lands on the
	// SAME destination as TriggerFetchFailedDegraded below (both let the
	// existing dirty-check/checkout/pop machine proceed unchanged from
	// Idle) -- kept as a separate, distinctly-named trigger anyway because
	// the caller (gitclone.SyncAll) needs to know which one actually fired,
	// to decide whether a "proceeding on stale image state" warning belongs
	// in the boot log. Mirrors internal/domain/turn's TriggerFail/
	// TriggerTimeout both landing on StateFailed for the same "distinct
	// causes, same destination" reason.
	TriggerFetchSucceeded
	// TriggerFetchFailedDegraded is the boot-time fetch failing, but the
	// degrade policy allowing the boot to proceed anyway (§19.3: the target
	// branch is already resolvable locally, OR repo.Branch was nil -- an
	// invented narvi/<sessionID> branch, "acceptable from HEAD"): Fetching
	// -> Idle, same destination as TriggerFetchSucceeded, for the same
	// reason -- see that trigger's own doc comment. "Warm boot must never
	// become network-dependent for liveness" (§19.3).
	TriggerFetchFailedDegraded
	// TriggerFetchFailedFatal is the boot-time fetch failing with the
	// degrade policy NOT satisfied (§19.3's own non-negotiable rule: the
	// session explicitly named a branch that is neither local nor
	// fetchable): Fetching -> FetchFailed. Legal only from Fetching.
	TriggerFetchFailedFatal
)

var triggerNames = [...]string{
	"dirty_detected", "clean_detected", "stash_succeeded", "stash_failed",
	"checkout_succeeded", "checkout_failed", "pop_succeeded", "pop_failed",
	"fetch_succeeded", "fetch_failed_degraded", "fetch_failed_fatal",
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
// entry here; anything not listed -- including every edge out of the six
// terminal states (Ready, StashFailed, CheckoutFailedClean,
// CheckoutFailedWithStash, PopFailed, FetchFailed), which are absent from
// this map entirely -- is illegal.
var transitions = map[State]map[Trigger]State{
	StateFetching: {
		TriggerFetchSucceeded:      StateIdle,
		TriggerFetchFailedDegraded: StateIdle,
		TriggerFetchFailedFatal:    StateFetchFailed,
	},
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
