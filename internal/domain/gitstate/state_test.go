package gitstate_test

import (
	"errors"
	"testing"

	"github.com/narvidev/narvi/internal/domain/gitstate"
)

// TestTransition_LegalEdges is table-driven over every legal (from,
// trigger) edge the boot machine defines (§3.4: stash-if-dirty ->
// checkout session branch -> stash pop, plus every terminal-failure edge),
// asserting the exact destination state and a nil error.
func TestTransition_LegalEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		from    gitstate.State
		trigger gitstate.Trigger
		want    gitstate.State
	}{
		{"fetching -> idle (fetch succeeded)", gitstate.StateFetching, gitstate.TriggerFetchSucceeded, gitstate.StateIdle},
		{"fetching -> idle (fetch failed, degraded)", gitstate.StateFetching, gitstate.TriggerFetchFailedDegraded, gitstate.StateIdle},
		{"fetching -> fetch_failed (fetch failed, fatal)", gitstate.StateFetching, gitstate.TriggerFetchFailedFatal, gitstate.StateFetchFailed},
		{"idle -> stashing (dirty detected)", gitstate.StateIdle, gitstate.TriggerDirtyDetected, gitstate.StateStashing},
		{"idle -> checking_out_clean (clean detected)", gitstate.StateIdle, gitstate.TriggerCleanDetected, gitstate.StateCheckingOutClean},
		{"stashing -> checking_out_with_stash (stash succeeded)", gitstate.StateStashing, gitstate.TriggerStashSucceeded, gitstate.StateCheckingOutWithStash},
		{"stashing -> stash_failed (stash failed)", gitstate.StateStashing, gitstate.TriggerStashFailed, gitstate.StateStashFailed},
		{"checking_out_clean -> ready (checkout succeeded)", gitstate.StateCheckingOutClean, gitstate.TriggerCheckoutSucceeded, gitstate.StateReady},
		{"checking_out_clean -> checkout_failed_clean (checkout failed)", gitstate.StateCheckingOutClean, gitstate.TriggerCheckoutFailed, gitstate.StateCheckoutFailedClean},
		{"checking_out_with_stash -> popping_stash (checkout succeeded)", gitstate.StateCheckingOutWithStash, gitstate.TriggerCheckoutSucceeded, gitstate.StatePoppingStash},
		{"checking_out_with_stash -> checkout_failed_with_stash (checkout failed)", gitstate.StateCheckingOutWithStash, gitstate.TriggerCheckoutFailed, gitstate.StateCheckoutFailedWithStash},
		{"popping_stash -> ready (pop succeeded)", gitstate.StatePoppingStash, gitstate.TriggerPopSucceeded, gitstate.StateReady},
		{"popping_stash -> pop_failed (pop failed)", gitstate.StatePoppingStash, gitstate.TriggerPopFailed, gitstate.StatePopFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := gitstate.Transition(tc.from, tc.trigger)
			if err != nil {
				t.Fatalf("Transition(%s, %v) unexpected error: %v", tc.from, tc.trigger, err)
			}
			if got != tc.want {
				t.Errorf("Transition(%s, %v) = %s, want %s", tc.from, tc.trigger, got, tc.want)
			}
		})
	}
}

var allTriggers = []gitstate.Trigger{
	gitstate.TriggerDirtyDetected, gitstate.TriggerCleanDetected,
	gitstate.TriggerStashSucceeded, gitstate.TriggerStashFailed,
	gitstate.TriggerCheckoutSucceeded, gitstate.TriggerCheckoutFailed,
	gitstate.TriggerPopSucceeded, gitstate.TriggerPopFailed,
	gitstate.TriggerFetchSucceeded, gitstate.TriggerFetchFailedDegraded, gitstate.TriggerFetchFailedFatal,
}

var allTerminalStates = []gitstate.State{
	gitstate.StateReady,
	gitstate.StateStashFailed,
	gitstate.StateCheckoutFailedClean,
	gitstate.StateCheckoutFailedWithStash,
	gitstate.StatePopFailed,
	gitstate.StateFetchFailed,
}

// TestTransition_IllegalEdges covers every (from, trigger) combination that
// is not in the transitions table: mismatched pairs among the six
// non-terminal states, an unknown state, and (in a separate exhaustive
// loop below) every trigger applied to every terminal state.
func TestTransition_IllegalEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		from    gitstate.State
		trigger gitstate.Trigger
	}{
		{"fetching cannot detect dirty directly", gitstate.StateFetching, gitstate.TriggerDirtyDetected},
		{"fetching cannot detect clean directly", gitstate.StateFetching, gitstate.TriggerCleanDetected},
		{"fetching cannot stash-succeed directly", gitstate.StateFetching, gitstate.TriggerStashSucceeded},
		{"fetching cannot stash-fail directly", gitstate.StateFetching, gitstate.TriggerStashFailed},
		{"fetching cannot checkout-succeed directly", gitstate.StateFetching, gitstate.TriggerCheckoutSucceeded},
		{"fetching cannot checkout-fail directly", gitstate.StateFetching, gitstate.TriggerCheckoutFailed},
		{"fetching cannot pop-succeed directly", gitstate.StateFetching, gitstate.TriggerPopSucceeded},
		{"fetching cannot pop-fail directly", gitstate.StateFetching, gitstate.TriggerPopFailed},
		{"idle cannot fetch-succeed (fetching already passed)", gitstate.StateIdle, gitstate.TriggerFetchSucceeded},
		{"idle cannot fetch-fail-degraded (fetching already passed)", gitstate.StateIdle, gitstate.TriggerFetchFailedDegraded},
		{"idle cannot fetch-fail-fatal (fetching already passed)", gitstate.StateIdle, gitstate.TriggerFetchFailedFatal},
		{"idle cannot stash-succeed directly", gitstate.StateIdle, gitstate.TriggerStashSucceeded},
		{"idle cannot stash-fail directly", gitstate.StateIdle, gitstate.TriggerStashFailed},
		{"idle cannot checkout-succeed directly", gitstate.StateIdle, gitstate.TriggerCheckoutSucceeded},
		{"idle cannot checkout-fail directly", gitstate.StateIdle, gitstate.TriggerCheckoutFailed},
		{"idle cannot pop-succeed directly", gitstate.StateIdle, gitstate.TriggerPopSucceeded},
		{"idle cannot pop-fail directly", gitstate.StateIdle, gitstate.TriggerPopFailed},
		{"stashing cannot fetch-succeed", gitstate.StateStashing, gitstate.TriggerFetchSucceeded},
		{"stashing cannot fetch-fail-degraded", gitstate.StateStashing, gitstate.TriggerFetchFailedDegraded},
		{"stashing cannot fetch-fail-fatal", gitstate.StateStashing, gitstate.TriggerFetchFailedFatal},
		{"stashing cannot detect dirty again", gitstate.StateStashing, gitstate.TriggerDirtyDetected},
		{"stashing cannot detect clean", gitstate.StateStashing, gitstate.TriggerCleanDetected},
		{"stashing cannot checkout-succeed directly", gitstate.StateStashing, gitstate.TriggerCheckoutSucceeded},
		{"stashing cannot checkout-fail directly", gitstate.StateStashing, gitstate.TriggerCheckoutFailed},
		{"stashing cannot pop-succeed directly", gitstate.StateStashing, gitstate.TriggerPopSucceeded},
		{"stashing cannot pop-fail directly", gitstate.StateStashing, gitstate.TriggerPopFailed},
		{"checking_out_clean cannot fetch-succeed", gitstate.StateCheckingOutClean, gitstate.TriggerFetchSucceeded},
		{"checking_out_clean cannot fetch-fail-degraded", gitstate.StateCheckingOutClean, gitstate.TriggerFetchFailedDegraded},
		{"checking_out_clean cannot fetch-fail-fatal", gitstate.StateCheckingOutClean, gitstate.TriggerFetchFailedFatal},
		{"checking_out_clean cannot detect dirty", gitstate.StateCheckingOutClean, gitstate.TriggerDirtyDetected},
		{"checking_out_clean cannot detect clean again", gitstate.StateCheckingOutClean, gitstate.TriggerCleanDetected},
		{"checking_out_clean cannot stash-succeed", gitstate.StateCheckingOutClean, gitstate.TriggerStashSucceeded},
		{"checking_out_clean cannot stash-fail", gitstate.StateCheckingOutClean, gitstate.TriggerStashFailed},
		{"checking_out_clean cannot pop-succeed", gitstate.StateCheckingOutClean, gitstate.TriggerPopSucceeded},
		{"checking_out_clean cannot pop-fail", gitstate.StateCheckingOutClean, gitstate.TriggerPopFailed},
		{"checking_out_with_stash cannot fetch-succeed", gitstate.StateCheckingOutWithStash, gitstate.TriggerFetchSucceeded},
		{"checking_out_with_stash cannot fetch-fail-degraded", gitstate.StateCheckingOutWithStash, gitstate.TriggerFetchFailedDegraded},
		{"checking_out_with_stash cannot fetch-fail-fatal", gitstate.StateCheckingOutWithStash, gitstate.TriggerFetchFailedFatal},
		{"checking_out_with_stash cannot detect dirty", gitstate.StateCheckingOutWithStash, gitstate.TriggerDirtyDetected},
		{"checking_out_with_stash cannot detect clean", gitstate.StateCheckingOutWithStash, gitstate.TriggerCleanDetected},
		{"checking_out_with_stash cannot stash-succeed again", gitstate.StateCheckingOutWithStash, gitstate.TriggerStashSucceeded},
		{"checking_out_with_stash cannot stash-fail", gitstate.StateCheckingOutWithStash, gitstate.TriggerStashFailed},
		{"checking_out_with_stash cannot pop-succeed directly", gitstate.StateCheckingOutWithStash, gitstate.TriggerPopSucceeded},
		{"checking_out_with_stash cannot pop-fail directly", gitstate.StateCheckingOutWithStash, gitstate.TriggerPopFailed},
		{"popping_stash cannot fetch-succeed", gitstate.StatePoppingStash, gitstate.TriggerFetchSucceeded},
		{"popping_stash cannot fetch-fail-degraded", gitstate.StatePoppingStash, gitstate.TriggerFetchFailedDegraded},
		{"popping_stash cannot fetch-fail-fatal", gitstate.StatePoppingStash, gitstate.TriggerFetchFailedFatal},
		{"popping_stash cannot detect dirty", gitstate.StatePoppingStash, gitstate.TriggerDirtyDetected},
		{"popping_stash cannot detect clean", gitstate.StatePoppingStash, gitstate.TriggerCleanDetected},
		{"popping_stash cannot stash-succeed", gitstate.StatePoppingStash, gitstate.TriggerStashSucceeded},
		{"popping_stash cannot stash-fail", gitstate.StatePoppingStash, gitstate.TriggerStashFailed},
		{"popping_stash cannot checkout-succeed again", gitstate.StatePoppingStash, gitstate.TriggerCheckoutSucceeded},
		{"popping_stash cannot checkout-fail", gitstate.StatePoppingStash, gitstate.TriggerCheckoutFailed},
		{"unknown state is always illegal", gitstate.State("bogus"), gitstate.TriggerDirtyDetected},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertIllegal(t, tc.from, tc.trigger)
		})
	}

	// Every trigger is illegal from every terminal state: no outgoing
	// edges exist at all once the boot machine reaches Ready, StashFailed,
	// CheckoutFailedClean, CheckoutFailedWithStash, or PopFailed.
	for _, from := range allTerminalStates {
		for _, trig := range allTriggers {
			t.Run("terminal state "+string(from)+" has no outgoing edge via "+trig.String(), func(t *testing.T) {
				t.Parallel()
				assertIllegal(t, from, trig)
			})
		}
	}
}

func assertIllegal(t *testing.T, from gitstate.State, trig gitstate.Trigger) {
	t.Helper()

	_, err := gitstate.Transition(from, trig)
	if err == nil {
		t.Fatalf("Transition(%s, %v) = nil error, want an error", from, trig)
	}
	if !errors.Is(err, gitstate.ErrIllegalTransition) {
		t.Errorf("Transition(%s, %v) error = %v, want errors.Is(err, ErrIllegalTransition)", from, trig, err)
	}
	var illegal *gitstate.IllegalTransitionError
	if !errors.As(err, &illegal) {
		t.Fatalf("Transition(%s, %v) error = %v, want *IllegalTransitionError", from, trig, err)
	}
	if illegal.From != from || illegal.Trigger != trig {
		t.Errorf("IllegalTransitionError = %+v, want From=%s Trigger=%s", illegal, from, trig)
	}
	if illegal.Error() == "" {
		t.Error("IllegalTransitionError.Error() is empty")
	}
}

// TestTrigger_String covers every named Trigger constant plus the
// out-of-range fallback branch.
func TestTrigger_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		trigger gitstate.Trigger
		want    string
	}{
		{gitstate.TriggerDirtyDetected, "dirty_detected"},
		{gitstate.TriggerCleanDetected, "clean_detected"},
		{gitstate.TriggerStashSucceeded, "stash_succeeded"},
		{gitstate.TriggerStashFailed, "stash_failed"},
		{gitstate.TriggerCheckoutSucceeded, "checkout_succeeded"},
		{gitstate.TriggerCheckoutFailed, "checkout_failed"},
		{gitstate.TriggerPopSucceeded, "pop_succeeded"},
		{gitstate.TriggerPopFailed, "pop_failed"},
		{gitstate.TriggerFetchSucceeded, "fetch_succeeded"},
		{gitstate.TriggerFetchFailedDegraded, "fetch_failed_degraded"},
		{gitstate.TriggerFetchFailedFatal, "fetch_failed_fatal"},
		{gitstate.Trigger(-1), "Trigger(-1)"},
		{gitstate.Trigger(999), "Trigger(999)"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.trigger.String(); got != tc.want {
				t.Errorf("Trigger(%d).String() = %q, want %q", int(tc.trigger), got, tc.want)
			}
		})
	}
}

// TestIsTerminal covers all twelve states, proving the terminal set is
// exactly {ready, stash_failed, checkout_failed_clean,
// checkout_failed_with_stash, pop_failed, fetch_failed}, plus an
// unrecognized state.
func TestIsTerminal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state gitstate.State
		want  bool
	}{
		{gitstate.StateFetching, false},
		{gitstate.StateIdle, false},
		{gitstate.StateStashing, false},
		{gitstate.StateCheckingOutClean, false},
		{gitstate.StateCheckingOutWithStash, false},
		{gitstate.StatePoppingStash, false},
		{gitstate.StateReady, true},
		{gitstate.StateStashFailed, true},
		{gitstate.StateCheckoutFailedClean, true},
		{gitstate.StateCheckoutFailedWithStash, true},
		{gitstate.StatePopFailed, true},
		{gitstate.StateFetchFailed, true},
		{gitstate.State("some_future_state"), false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()
			if got := gitstate.IsTerminal(tc.state); got != tc.want {
				t.Errorf("IsTerminal(%s) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestRequiresStashRecovery is the exhaustive truth table over all twelve
// states for the package's single most important correctness property:
// whether a stash exists and has not been popped, requiring manual
// recovery (§3.4's P0). True ONLY for CheckoutFailedWithStash and
// PopFailed -- every other state, including the other four terminal
// states (Ready, StashFailed, CheckoutFailedClean, and FetchFailed -- no
// stash has ever been taken by the time a fetch fails, since the fetch
// step runs BEFORE the dirty-check/stash sequence even begins) must read
// false.
func TestRequiresStashRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		state gitstate.State
		want  bool
	}{
		{gitstate.StateFetching, false},
		{gitstate.StateIdle, false},
		{gitstate.StateStashing, false},
		{gitstate.StateCheckingOutClean, false},
		{gitstate.StateCheckingOutWithStash, false},
		{gitstate.StatePoppingStash, false},
		{gitstate.StateReady, false},
		{gitstate.StateStashFailed, false},
		{gitstate.StateCheckoutFailedClean, false},
		{gitstate.StateCheckoutFailedWithStash, true},
		{gitstate.StatePopFailed, true},
		{gitstate.StateFetchFailed, false},
		{gitstate.State("some_future_state"), false},
	}

	for _, tc := range tests {
		t.Run(string(tc.state), func(t *testing.T) {
			t.Parallel()
			if got := gitstate.RequiresStashRecovery(tc.state); got != tc.want {
				t.Errorf("RequiresStashRecovery(%s) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestNormalizeBranchName covers an already-lowercase name, an
// all-uppercase name, and a mixed-case name (§3.4: "Branch names
// normalized (lowercase) before push").
func TestNormalizeBranchName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"already lowercase", "feat/session-branch", "feat/session-branch"},
		{"all uppercase", "FEAT/SESSION-BRANCH", "feat/session-branch"},
		{"mixed case", "Feat/Session-Branch-123", "feat/session-branch-123"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := gitstate.NormalizeBranchName(tc.input); got != tc.want {
				t.Errorf("NormalizeBranchName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
