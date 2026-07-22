package gitstate_test

import (
	"testing"

	"github.com/khazaddev/narvi/internal/domain/gitstate"
)

// TestResolveSessionBranch covers both branches (explicit vs. nil) plus
// normalization of mixed-case input in each -- §3.4: "Branch names
// normalized (lowercase) before push", applied uniformly whether the
// branch came from the caller or was invented from the session id.
func TestResolveSessionBranch(t *testing.T) {
	t.Parallel()

	explicit := "Feature/My-Branch"

	tests := []struct {
		name           string
		explicitBranch *string
		sessionID      string
		want           string
	}{
		{"explicit branch, already lowercase", strPtr("feature/my-branch"), "5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a", "feature/my-branch"},
		{"explicit branch, mixed case is normalized", &explicit, "5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a", "feature/my-branch"},
		{"nil branch invents narvi/<sessionID>", nil, "5B1C1E2E-6B1A-4B1A-9B1A-6B1A4B1A9B1A", "narvi/5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a"},
		{"nil branch, already-lowercase session id", nil, "abc-123", "narvi/abc-123"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := gitstate.ResolveSessionBranch(tc.explicitBranch, tc.sessionID); got != tc.want {
				t.Errorf("ResolveSessionBranch(%v, %q) = %q, want %q", tc.explicitBranch, tc.sessionID, got, tc.want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// TestTriggerForDirtyCheck, TestTriggerForStash, TestTriggerForCheckout, and
// TestTriggerForPop are the exhaustive (both bool values) truth tables for
// each of the four real-outcome -> Trigger mapping functions
// (sequence.go), each independently testable with zero real git I/O.
func TestTriggerForDirtyCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		dirty bool
		want  gitstate.Trigger
	}{
		{true, gitstate.TriggerDirtyDetected},
		{false, gitstate.TriggerCleanDetected},
	}
	for _, tc := range tests {
		if got := gitstate.TriggerForDirtyCheck(tc.dirty); got != tc.want {
			t.Errorf("TriggerForDirtyCheck(%v) = %v, want %v", tc.dirty, got, tc.want)
		}
	}
}

func TestTriggerForStash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		succeeded bool
		want      gitstate.Trigger
	}{
		{true, gitstate.TriggerStashSucceeded},
		{false, gitstate.TriggerStashFailed},
	}
	for _, tc := range tests {
		if got := gitstate.TriggerForStash(tc.succeeded); got != tc.want {
			t.Errorf("TriggerForStash(%v) = %v, want %v", tc.succeeded, got, tc.want)
		}
	}
}

func TestTriggerForCheckout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		succeeded bool
		want      gitstate.Trigger
	}{
		{true, gitstate.TriggerCheckoutSucceeded},
		{false, gitstate.TriggerCheckoutFailed},
	}
	for _, tc := range tests {
		if got := gitstate.TriggerForCheckout(tc.succeeded); got != tc.want {
			t.Errorf("TriggerForCheckout(%v) = %v, want %v", tc.succeeded, got, tc.want)
		}
	}
}

func TestTriggerForPop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		succeeded bool
		want      gitstate.Trigger
	}{
		{true, gitstate.TriggerPopSucceeded},
		{false, gitstate.TriggerPopFailed},
	}
	for _, tc := range tests {
		if got := gitstate.TriggerForPop(tc.succeeded); got != tc.want {
			t.Errorf("TriggerForPop(%v) = %v, want %v", tc.succeeded, got, tc.want)
		}
	}
}

// TestFullBootSequenceViaHelpers proves the four helpers above compose
// correctly with Transition across all four realistic end-to-end
// trajectories the real SyncAll (internal/sandboxagent/gitclone) drives --
// clean-tree happy path, dirty-tree happy path, a stash failure, and a pop
// failure -- entirely without any real git I/O.
func TestFullBootSequenceViaHelpers(t *testing.T) {
	t.Parallel()

	t.Run("clean tree straight through to ready", func(t *testing.T) {
		t.Parallel()
		state := gitstate.StateIdle
		state = mustTransition(t, state, gitstate.TriggerForDirtyCheck(false))
		if state != gitstate.StateCheckingOutClean {
			t.Fatalf("state = %s, want checking_out_clean", state)
		}
		state = mustTransition(t, state, gitstate.TriggerForCheckout(true))
		if state != gitstate.StateReady {
			t.Fatalf("state = %s, want ready", state)
		}
	})

	t.Run("dirty tree, stash and pop both succeed", func(t *testing.T) {
		t.Parallel()
		state := gitstate.StateIdle
		state = mustTransition(t, state, gitstate.TriggerForDirtyCheck(true))
		state = mustTransition(t, state, gitstate.TriggerForStash(true))
		state = mustTransition(t, state, gitstate.TriggerForCheckout(true))
		if state != gitstate.StatePoppingStash {
			t.Fatalf("state = %s, want popping_stash", state)
		}
		state = mustTransition(t, state, gitstate.TriggerForPop(true))
		if state != gitstate.StateReady {
			t.Fatalf("state = %s, want ready", state)
		}
		if gitstate.RequiresStashRecovery(state) {
			t.Error("RequiresStashRecovery(ready) = true, want false")
		}
	})

	t.Run("dirty tree, stash fails", func(t *testing.T) {
		t.Parallel()
		state := gitstate.StateIdle
		state = mustTransition(t, state, gitstate.TriggerForDirtyCheck(true))
		state = mustTransition(t, state, gitstate.TriggerForStash(false))
		if state != gitstate.StateStashFailed {
			t.Fatalf("state = %s, want stash_failed", state)
		}
		if gitstate.RequiresStashRecovery(state) {
			t.Error("RequiresStashRecovery(stash_failed) = true, want false (nothing was ever stashed)")
		}
	})

	t.Run("dirty tree, checkout fails after a successful stash (P0)", func(t *testing.T) {
		t.Parallel()
		state := gitstate.StateIdle
		state = mustTransition(t, state, gitstate.TriggerForDirtyCheck(true))
		state = mustTransition(t, state, gitstate.TriggerForStash(true))
		state = mustTransition(t, state, gitstate.TriggerForCheckout(false))
		if state != gitstate.StateCheckoutFailedWithStash {
			t.Fatalf("state = %s, want checkout_failed_with_stash", state)
		}
		if !gitstate.RequiresStashRecovery(state) {
			t.Error("RequiresStashRecovery(checkout_failed_with_stash) = false, want true (P0)")
		}
	})

	t.Run("dirty tree, pop fails after a successful checkout (P0)", func(t *testing.T) {
		t.Parallel()
		state := gitstate.StateIdle
		state = mustTransition(t, state, gitstate.TriggerForDirtyCheck(true))
		state = mustTransition(t, state, gitstate.TriggerForStash(true))
		state = mustTransition(t, state, gitstate.TriggerForCheckout(true))
		state = mustTransition(t, state, gitstate.TriggerForPop(false))
		if state != gitstate.StatePopFailed {
			t.Fatalf("state = %s, want pop_failed", state)
		}
		if !gitstate.RequiresStashRecovery(state) {
			t.Error("RequiresStashRecovery(pop_failed) = false, want true (P0)")
		}
	})
}

func mustTransition(t *testing.T, from gitstate.State, trig gitstate.Trigger) gitstate.State {
	t.Helper()
	to, err := gitstate.Transition(from, trig)
	if err != nil {
		t.Fatalf("Transition(%s, %v) unexpected error: %v", from, trig, err)
	}
	return to
}
