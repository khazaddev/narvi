package sandbox_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/sandbox"
)

// TestTransition_LegalEdges is table-driven over every legal (from,
// trigger[, target]) edge the state machine defines (§3.2's diagram: the
// linear boot path, the ready<->snapshotting cycle, every watchdog ->
// suspect edge, both suspect exits, and every stopped/failed/stale
// recovery edge), asserting the exact destination state and a nil error.
func TestTransition_LegalEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		from    sandbox.State
		gen     int
		trigger sandbox.Trigger
		want    sandbox.State
	}{
		{"pending -> spawning (spawn)", sandbox.StatePending, 1, sandbox.SpawnTrigger(2), sandbox.StateSpawning},
		{"spawning -> connecting (provider ack)", sandbox.StateSpawning, 1, sandbox.ProviderAckTrigger(), sandbox.StateConnecting},
		{"connecting -> booting (ws connected)", sandbox.StateConnecting, 1, sandbox.WSConnectedTrigger(), sandbox.StateBooting},
		{"booting -> ready (boot complete)", sandbox.StateBooting, 1, sandbox.BootCompleteTrigger(), sandbox.StateReady},
		{"ready -> snapshotting (snapshot start)", sandbox.StateReady, 1, sandbox.SnapshotStartTrigger(), sandbox.StateSnapshotting},
		{"snapshotting -> ready (snapshot complete)", sandbox.StateSnapshotting, 1, sandbox.SnapshotCompleteTrigger(), sandbox.StateReady},
		// TriggerResumeAck is resume's own second step (state.go's own
		// doc comment) -- deliberately NOT gen-fenced, same as
		// TriggerProviderAck just above: gen=1 here, matching currentGen
		// exactly (which would be rejected as stale if this WERE
		// gen-fenced, per TestTransition_GenFencing's own boundary case
		// below), and it still succeeds.
		{"spawning -> connecting (resume ack)", sandbox.StateSpawning, 1, sandbox.ResumeAckTrigger(), sandbox.StateConnecting},

		// Watchdog -> suspect, from every live state.
		{"spawning -> suspect", sandbox.StateSpawning, 1, sandbox.SuspectTrigger(), sandbox.StateSuspect},
		{"connecting -> suspect", sandbox.StateConnecting, 1, sandbox.SuspectTrigger(), sandbox.StateSuspect},
		{"booting -> suspect", sandbox.StateBooting, 1, sandbox.SuspectTrigger(), sandbox.StateSuspect},
		{"ready -> suspect", sandbox.StateReady, 1, sandbox.SuspectTrigger(), sandbox.StateSuspect},
		{"snapshotting -> suspect", sandbox.StateSnapshotting, 1, sandbox.SuspectTrigger(), sandbox.StateSuspect},

		// Recover, to every previously-live state.
		{"suspect -> spawning (recover)", sandbox.StateSuspect, 1, sandbox.RecoverTrigger(sandbox.StateSpawning), sandbox.StateSpawning},
		{"suspect -> connecting (recover)", sandbox.StateSuspect, 1, sandbox.RecoverTrigger(sandbox.StateConnecting), sandbox.StateConnecting},
		{"suspect -> booting (recover)", sandbox.StateSuspect, 1, sandbox.RecoverTrigger(sandbox.StateBooting), sandbox.StateBooting},
		{"suspect -> ready (recover)", sandbox.StateSuspect, 1, sandbox.RecoverTrigger(sandbox.StateReady), sandbox.StateReady},
		{"suspect -> snapshotting (recover)", sandbox.StateSuspect, 1, sandbox.RecoverTrigger(sandbox.StateSnapshotting), sandbox.StateSnapshotting},

		// Grace expiry, to every peer terminal outcome.
		{"suspect -> stopped (grace expired, explicit stop)", sandbox.StateSuspect, 1, sandbox.GraceExpiredTrigger(sandbox.StateStopped), sandbox.StateStopped},
		{"suspect -> failed (grace expired, genuine timeout)", sandbox.StateSuspect, 1, sandbox.GraceExpiredTrigger(sandbox.StateFailed), sandbox.StateFailed},
		{"suspect -> stale (grace expired, GC classification)", sandbox.StateSuspect, 1, sandbox.GraceExpiredTrigger(sandbox.StateStale), sandbox.StateStale},

		// Recovery rules out of every terminal state.
		{"stopped -> spawning (respawn)", sandbox.StateStopped, 1, sandbox.SpawnTrigger(2), sandbox.StateSpawning},
		{"stopped -> spawning (restore)", sandbox.StateStopped, 1, sandbox.RestoreTrigger(2), sandbox.StateSpawning},
		{"stopped -> spawning (resume)", sandbox.StateStopped, 1, sandbox.ResumeTrigger(2), sandbox.StateSpawning},
		{"failed -> spawning (respawn)", sandbox.StateFailed, 1, sandbox.SpawnTrigger(2), sandbox.StateSpawning},
		{"failed -> spawning (restore)", sandbox.StateFailed, 1, sandbox.RestoreTrigger(2), sandbox.StateSpawning},
		{"stale -> spawning (respawn)", sandbox.StateStale, 1, sandbox.SpawnTrigger(2), sandbox.StateSpawning},
		{"stale -> spawning (restore)", sandbox.StateStale, 1, sandbox.RestoreTrigger(2), sandbox.StateSpawning},
		{"stale -> spawning (resume)", sandbox.StateStale, 1, sandbox.ResumeTrigger(2), sandbox.StateSpawning},

		// Force-respawn, from every "stuck while live" state
		// EvaluateSpawnDecision's own two recovery carve-outs can produce
		// (SpawningTimeout for Spawning/Connecting/Booting; ReadyWait for
		// Ready) -- all land back in Spawning with a fresh gen, abandoning
		// whatever was stuck there.
		{"spawning -> spawning (force respawn)", sandbox.StateSpawning, 1, sandbox.ForceRespawnTrigger(2), sandbox.StateSpawning},
		{"connecting -> spawning (force respawn)", sandbox.StateConnecting, 1, sandbox.ForceRespawnTrigger(2), sandbox.StateSpawning},
		{"booting -> spawning (force respawn)", sandbox.StateBooting, 1, sandbox.ForceRespawnTrigger(2), sandbox.StateSpawning},
		{"ready -> spawning (force respawn)", sandbox.StateReady, 1, sandbox.ForceRespawnTrigger(2), sandbox.StateSpawning},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := sandbox.Transition(tc.from, tc.gen, tc.trigger)
			if err != nil {
				t.Fatalf("Transition(%s, gen=%d, %v) unexpected error: %v", tc.from, tc.gen, tc.trigger, err)
			}
			if got != tc.want {
				t.Errorf("Transition(%s, gen=%d, %v) = %s, want %s", tc.from, tc.gen, tc.trigger, got, tc.want)
			}
		})
	}
}

// TestTransition_IllegalFromTriggerCombos covers (from, trigger) pairs that
// are simply not edges in the table at all -- distinct from the dynamic-
// target-rejection and gen-fencing cases covered by their own tests below.
func TestTransition_IllegalFromTriggerCombos(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		from    sandbox.State
		trigger sandbox.Trigger
	}{
		{"pending cannot suspect", sandbox.StatePending, sandbox.SuspectTrigger()},
		{"pending cannot resume", sandbox.StatePending, sandbox.ResumeTrigger(2)},
		{"pending cannot resume-ack", sandbox.StatePending, sandbox.ResumeAckTrigger()},
		{"spawning cannot spawn again", sandbox.StateSpawning, sandbox.SpawnTrigger(2)},
		{"connecting cannot boot-complete", sandbox.StateConnecting, sandbox.BootCompleteTrigger()},
		{"booting cannot ws-connect", sandbox.StateBooting, sandbox.WSConnectedTrigger()},
		{"ready cannot spawn", sandbox.StateReady, sandbox.SpawnTrigger(2)},
		{"ready cannot snapshot-complete (must snapshot-start first)", sandbox.StateReady, sandbox.SnapshotCompleteTrigger()},
		{"snapshotting cannot snapshot-start again", sandbox.StateSnapshotting, sandbox.SnapshotStartTrigger()},
		{"suspect cannot ws-connect directly", sandbox.StateSuspect, sandbox.WSConnectedTrigger()},
		{"suspect cannot spawn directly", sandbox.StateSuspect, sandbox.SpawnTrigger(2)},
		{"stopped cannot boot-complete", sandbox.StateStopped, sandbox.BootCompleteTrigger()},
		// A resume must land in Spawning first (TriggerResume's own new
		// first-step target) -- it can never jump straight from Stopped
		// to Connecting via TriggerResumeAck, which is only legal FROM
		// Spawning (resume's own second step).
		{"stopped cannot resume-ack (must claim spawning first)", sandbox.StateStopped, sandbox.ResumeAckTrigger()},
		{"connecting cannot resume-ack (resume-ack is spawning-only)", sandbox.StateConnecting, sandbox.ResumeAckTrigger()},
		{"failed cannot resume (not resume-capable per plan)", sandbox.StateFailed, sandbox.ResumeTrigger(2)},
		{"failed cannot recover (recover is suspect-only)", sandbox.StateFailed, sandbox.RecoverTrigger(sandbox.StateReady)},
		{"stale cannot suspect directly", sandbox.StateStale, sandbox.SuspectTrigger()},
		{"stale cannot resume-ack (resume-ack is spawning-only)", sandbox.StateStale, sandbox.ResumeAckTrigger()},
		{"unknown state is always illegal", sandbox.State("bogus"), sandbox.SpawnTrigger(2)},

		// TriggerForceRespawn is deliberately narrower than TriggerSpawn --
		// only for the four "stuck while live" states (Spawning/Connecting/
		// Booting/Ready). Every other state -- including every already-
		// terminal one, which already has its own legal TriggerSpawn edge and
		// doesn't need this one -- has no TriggerForceRespawn edge at all.
		{"pending cannot force-respawn", sandbox.StatePending, sandbox.ForceRespawnTrigger(2)},
		{"suspect cannot force-respawn", sandbox.StateSuspect, sandbox.ForceRespawnTrigger(2)},
		{"stopped cannot force-respawn", sandbox.StateStopped, sandbox.ForceRespawnTrigger(2)},
		{"failed cannot force-respawn", sandbox.StateFailed, sandbox.ForceRespawnTrigger(2)},
		{"stale cannot force-respawn", sandbox.StateStale, sandbox.ForceRespawnTrigger(2)},
		{"snapshotting cannot force-respawn", sandbox.StateSnapshotting, sandbox.ForceRespawnTrigger(2)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := sandbox.Transition(tc.from, 1, tc.trigger)
			if err == nil {
				t.Fatalf("Transition(%s, gen=1, %v) = nil error, want an error", tc.from, tc.trigger)
			}
			if !errors.Is(err, sandbox.ErrIllegalTransition) {
				t.Errorf("Transition(%s, gen=1, %v) error = %v, want errors.Is(err, ErrIllegalTransition)", tc.from, tc.trigger, err)
			}
			var illegal *sandbox.IllegalTransitionError
			if !errors.As(err, &illegal) {
				t.Fatalf("Transition(%s, gen=1, %v) error = %v, want *IllegalTransitionError", tc.from, tc.trigger, err)
			}
			if illegal.From != tc.from || illegal.Trigger != tc.trigger.Kind {
				t.Errorf("IllegalTransitionError = %+v, want From=%s Trigger=%s", illegal, tc.from, tc.trigger.Kind)
			}
		})
	}
}

// TestTransition_RecoverTargetValidation proves recover's Target is
// validated against the "previously live" set, not invented by Transition
// -- rejecting a recovery into a state that was never suspectable from
// (Pending, Stopped, Failed, Stale, or Suspect itself).
func TestTransition_RecoverTargetValidation(t *testing.T) {
	t.Parallel()

	illegalTargets := []sandbox.State{
		sandbox.StatePending,
		sandbox.StateStopped,
		sandbox.StateFailed,
		sandbox.StateStale,
		sandbox.StateSuspect,
	}

	for _, target := range illegalTargets {
		t.Run("recover into "+string(target)+" is illegal", func(t *testing.T) {
			t.Parallel()

			_, err := sandbox.Transition(sandbox.StateSuspect, 1, sandbox.RecoverTrigger(target))
			if err == nil {
				t.Fatalf("Transition(suspect, recover(%s)) = nil error, want an error", target)
			}
			var illegal *sandbox.IllegalTransitionError
			if !errors.As(err, &illegal) {
				t.Fatalf("Transition(suspect, recover(%s)) error = %v, want *IllegalTransitionError", target, err)
			}
			if illegal.Target != target {
				t.Errorf("IllegalTransitionError.Target = %s, want %s", illegal.Target, target)
			}
		})
	}
}

// TestTransition_GraceExpiredTargetValidation proves grace-expiry's Target
// is restricted to exactly {stopped, failed, stale} -- e.g. rejecting a
// straight return to ready without going through the recover trigger.
func TestTransition_GraceExpiredTargetValidation(t *testing.T) {
	t.Parallel()

	illegalTargets := []sandbox.State{
		sandbox.StateReady,
		sandbox.StatePending,
		sandbox.StateSpawning,
		sandbox.StateSuspect,
	}

	for _, target := range illegalTargets {
		t.Run("grace-expired into "+string(target)+" is illegal", func(t *testing.T) {
			t.Parallel()

			_, err := sandbox.Transition(sandbox.StateSuspect, 1, sandbox.GraceExpiredTrigger(target))
			if err == nil {
				t.Fatalf("Transition(suspect, graceExpired(%s)) = nil error, want an error", target)
			}
			var illegal *sandbox.IllegalTransitionError
			if !errors.As(err, &illegal) {
				t.Fatalf("Transition(suspect, graceExpired(%s)) error = %v, want *IllegalTransitionError", target, err)
			}
		})
	}
}

// TestTransition_GenFencing is table-driven over every gen-fenced trigger
// (Spawn/Restore/Resume) from every state that legally accepts it,
// proving a gen that is not strictly greater than currentGen is rejected
// with a distinguishable, wrapped sentinel error, and that gen == current
// is exactly as illegal as gen < current (the boundary).
func TestTransition_GenFencing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		from    sandbox.State
		trigger func(gen int) sandbox.Trigger
	}{
		{"pending + spawn", sandbox.StatePending, sandbox.SpawnTrigger},
		{"stopped + spawn", sandbox.StateStopped, sandbox.SpawnTrigger},
		{"stopped + restore", sandbox.StateStopped, sandbox.RestoreTrigger},
		{"stopped + resume", sandbox.StateStopped, sandbox.ResumeTrigger},
		{"failed + spawn", sandbox.StateFailed, sandbox.SpawnTrigger},
		{"failed + restore", sandbox.StateFailed, sandbox.RestoreTrigger},
		{"stale + spawn", sandbox.StateStale, sandbox.SpawnTrigger},
		{"stale + restore", sandbox.StateStale, sandbox.RestoreTrigger},
		{"stale + resume", sandbox.StateStale, sandbox.ResumeTrigger},
		{"spawning + force-respawn", sandbox.StateSpawning, sandbox.ForceRespawnTrigger},
		{"connecting + force-respawn", sandbox.StateConnecting, sandbox.ForceRespawnTrigger},
		{"booting + force-respawn", sandbox.StateBooting, sandbox.ForceRespawnTrigger},
		{"ready + force-respawn", sandbox.StateReady, sandbox.ForceRespawnTrigger},
	}

	for _, tc := range tests {
		t.Run(tc.name+"/gen equal to current is stale", func(t *testing.T) {
			t.Parallel()
			_, err := sandbox.Transition(tc.from, 5, tc.trigger(5))
			assertStaleGen(t, err, tc.from, 5, 5)
		})
		t.Run(tc.name+"/gen less than current is stale", func(t *testing.T) {
			t.Parallel()
			_, err := sandbox.Transition(tc.from, 5, tc.trigger(3))
			assertStaleGen(t, err, tc.from, 5, 3)
		})
		t.Run(tc.name+"/gen greater than current succeeds", func(t *testing.T) {
			t.Parallel()
			_, err := sandbox.Transition(tc.from, 5, tc.trigger(6))
			if err != nil {
				t.Fatalf("Transition(%s, gen=5, trigger(6)) unexpected error: %v", tc.from, err)
			}
		})
	}
}

func assertStaleGen(t *testing.T, err error, from sandbox.State, currentGen, attemptedGen int) {
	t.Helper()

	if err == nil {
		t.Fatalf("Transition(%s, gen=%d, trigger(%d)) = nil error, want stale-gen error", from, currentGen, attemptedGen)
	}
	if !errors.Is(err, sandbox.ErrStaleGen) {
		t.Errorf("error = %v, want errors.Is(err, ErrStaleGen)", err)
	}
	var staleGen *sandbox.StaleGenError
	if !errors.As(err, &staleGen) {
		t.Fatalf("error = %v, want *StaleGenError", err)
	}
	if staleGen.CurrentGen != currentGen || staleGen.AttemptedGen != attemptedGen || staleGen.From != from {
		t.Errorf("StaleGenError = %+v, want From=%s CurrentGen=%d AttemptedGen=%d", staleGen, from, currentGen, attemptedGen)
	}
}

// TestTriggerKind_String proves every named trigger kind stringifies to its
// documented name, and that an out-of-range value falls back to a
// numbered placeholder instead of panicking or returning garbage.
func TestTriggerKind_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind sandbox.TriggerKind
		want string
	}{
		{sandbox.TriggerSpawn, "spawn"},
		{sandbox.TriggerProviderAck, "provider_ack"},
		{sandbox.TriggerWSConnected, "ws_connected"},
		{sandbox.TriggerBootComplete, "boot_complete"},
		{sandbox.TriggerSnapshotStart, "snapshot_start"},
		{sandbox.TriggerSnapshotComplete, "snapshot_complete"},
		{sandbox.TriggerSuspect, "suspect"},
		{sandbox.TriggerRecover, "recover"},
		{sandbox.TriggerGraceExpired, "grace_expired"},
		{sandbox.TriggerRestore, "restore"},
		{sandbox.TriggerResume, "resume"},
		{sandbox.TriggerResumeAck, "resume_ack"},
		{sandbox.TriggerForceRespawn, "force_respawn"},
		{sandbox.TriggerKind(-1), "TriggerKind(-1)"},
		{sandbox.TriggerKind(999), "TriggerKind(999)"},
	}

	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("TriggerKind(%d).String() = %q, want %q", int(tc.kind), got, tc.want)
		}
	}
}

// TestIllegalTransitionError_Error proves the error message differs
// depending on whether Target is set, so a dynamic-target rejection reads
// distinctly from a plain (from, trigger) rejection.
func TestIllegalTransitionError_Error(t *testing.T) {
	t.Parallel()

	withoutTarget := &sandbox.IllegalTransitionError{From: sandbox.StatePending, Trigger: sandbox.TriggerSuspect}
	if got := withoutTarget.Error(); got == "" {
		t.Fatal("Error() = empty string")
	}

	withTarget := &sandbox.IllegalTransitionError{From: sandbox.StateSuspect, Trigger: sandbox.TriggerRecover, Target: sandbox.StatePending}
	if got := withTarget.Error(); got == withoutTarget.Error() {
		t.Errorf("Error() with Target set should differ from without: got %q", got)
	}
}

// TestStaleGenError_Error proves the message is non-empty and mentions
// both gens (a smoke test -- the structured fields are what tests above
// actually assert on).
func TestStaleGenError_Error(t *testing.T) {
	t.Parallel()

	err := &sandbox.StaleGenError{From: sandbox.StatePending, Trigger: sandbox.TriggerSpawn, CurrentGen: 5, AttemptedGen: 5}
	if got := err.Error(); got == "" {
		t.Fatal("Error() = empty string")
	}
}
