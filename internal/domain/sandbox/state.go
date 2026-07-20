package sandbox

import (
	"errors"
	"fmt"
)

// State is one of the sandbox's own ten states (§3.2's diagram) --
// deliberately no "warming"/"syncing"/"running" here.
type State string

// The ten sandbox states (§3.2's diagram).
const (
	StatePending      State = "pending"
	StateSpawning     State = "spawning"
	StateConnecting   State = "connecting"
	StateBooting      State = "booting"
	StateReady        State = "ready"
	StateSnapshotting State = "snapshotting"
	StateSuspect      State = "suspect"
	StateStopped      State = "stopped"
	StateFailed       State = "failed"
	StateStale        State = "stale"
)

// TriggerKind names the kind of event/command being applied to the state
// machine. Every state transition logs `from`, `to`, `trigger`, `gen` per
// §5.3 -- TriggerKind.String() is that logged trigger name.
type TriggerKind int

const (
	// TriggerSpawn is a plain spawn: Pending -> Spawning (the initial
	// boot), or a Stopped/Failed/Stale -> Spawning respawn with no
	// snapshot/resume available. Gen-fenced.
	TriggerSpawn TriggerKind = iota
	// TriggerProviderAck is the provider's CreateSandbox call returning a
	// live provider object: Spawning -> Connecting.
	TriggerProviderAck
	// TriggerWSConnected is the sandbox agent's WebSocket connecting:
	// Connecting -> Booting.
	TriggerWSConnected
	// TriggerBootComplete is the in-sandbox boot sequence finishing:
	// Booting -> Ready.
	TriggerBootComplete
	// TriggerSnapshotStart begins a snapshot: Ready -> Snapshotting.
	TriggerSnapshotStart
	// TriggerSnapshotComplete finishes a snapshot: Snapshotting -> Ready.
	TriggerSnapshotComplete
	// TriggerSuspect is a watchdog silence/timeout firing from any live
	// state (Spawning, Connecting, Booting, Ready, Snapshotting) into
	// Suspect. Per §3.2's hard rule, a watchdog never writes Failed
	// directly -- it always goes through Suspect first.
	TriggerSuspect
	// TriggerRecover is "any liveness signal during grace returns to
	// previous state" (§3.2). Its Target carries which state to return
	// to -- Transition validates that Target is one of the five states a
	// watchdog can suspect FROM; it does not invent the target itself.
	TriggerRecover
	// TriggerGraceExpired is terminal_grace expiring with no recovery
	// signal. Its Target carries the caller-classified outcome -- one of
	// Stopped (explicit stop request), Failed (genuine timeout), or Stale
	// (GC/reconciler classification of an orphaned box) -- all three are
	// peer terminal outcomes (§3.2), not a fixed single target.
	TriggerGraceExpired
	// TriggerRestore is a snapshot restore into a freshly spawned
	// provider sandbox: Stopped/Failed/Stale -> Spawning. Gen-fenced.
	TriggerRestore
	// TriggerResume is a persistent-resume of the SAME provider sandbox
	// (no new provider object created): Stopped/Stale -> Connecting.
	// Gen-fenced (§3.2's gen-fencing rule explicitly covers "a
	// restore/resume out of stopped/stale/failed").
	TriggerResume
	// TriggerForceRespawn abandons a sandbox EvaluateSpawnDecision has
	// determined is stuck/unreachable -- a spawn/connect interrupted
	// before the sandbox ever connected (Spawning/Connecting/Booting held
	// past SpawningTimeout with no sign of life), or a Ready sandbox whose
	// WebSocket never reconnected (past ReadyWait) -- and spawns a
	// genuinely fresh one in its place: Spawning/Connecting/Booting/Ready
	// -> Spawning. Gen-fenced, exactly like TriggerSpawn/TriggerRestore/
	// TriggerResume. Deliberately a separate trigger kind from
	// TriggerSpawn (rather than folding these two recovery cases into it)
	// so the transition LOG (§5.3) can tell "a genuinely fresh/terminal-
	// state respawn" apart from "we gave up on a stuck live sandbox and
	// are abandoning it" -- TriggerSpawn's own doc comment above stays
	// scoped to Pending/Stopped/Failed/Stale, never a live state.
	TriggerForceRespawn
)

var triggerNames = [...]string{
	"spawn", "provider_ack", "ws_connected", "boot_complete",
	"snapshot_start", "snapshot_complete", "suspect", "recover",
	"grace_expired", "restore", "resume", "force_respawn",
}

func (k TriggerKind) String() string {
	if k < 0 || int(k) >= len(triggerNames) {
		return fmt.Sprintf("TriggerKind(%d)", int(k))
	}
	return triggerNames[k]
}

// Trigger is the input to Transition: which kind of event/command is being
// applied, plus whichever payload that kind requires.
//
//   - Gen is the new generation being spawned/restored/resumed to.
//     Required for TriggerSpawn, TriggerRestore, TriggerResume (validated:
//     must be strictly greater than the sandbox's current gen); ignored
//     for every other kind. This covers only the identity-rotating half of
//     §3.2's gen-fencing rule (spawn/restore/resume mint a NEW gen that
//     must be monotonic). The other half -- rejecting an already-stale
//     event (a WS connection, provider callback, or status write) that
//     names an OLD gen -- is an adapter-layer concern, not this pure state
//     machine's: §6.1 has the sandbox WS handshake itself reject a
//     gen-mismatched connection with 403 before any event reaches here, so
//     by the time a WSConnected/BootComplete/Suspect/etc. trigger is
//     constructed, its gen has already been validated against the
//     current one. Transition does not re-check it.
//   - Target is the destination state the caller is asking to transition
//     to. Required for TriggerRecover and TriggerGraceExpired (validated
//     against that trigger's legal target set); ignored for every other
//     kind.
type Trigger struct {
	Kind   TriggerKind
	Gen    int
	Target State
}

// SpawnTrigger builds a TriggerSpawn trigger carrying the new gen, so call
// sites read as e.g. sandbox.SpawnTrigger(3) rather than a bare struct
// literal with unlabeled fields.
func SpawnTrigger(gen int) Trigger { return Trigger{Kind: TriggerSpawn, Gen: gen} }

// RestoreTrigger builds a TriggerRestore trigger carrying the new gen.
func RestoreTrigger(gen int) Trigger { return Trigger{Kind: TriggerRestore, Gen: gen} }

// ResumeTrigger builds a TriggerResume trigger carrying the new gen.
func ResumeTrigger(gen int) Trigger { return Trigger{Kind: TriggerResume, Gen: gen} }

// ForceRespawnTrigger builds a TriggerForceRespawn trigger carrying the new
// gen -- abandoning a stuck live sandbox (Spawning/Connecting/Booting past
// SpawningTimeout, or Ready past ReadyWait with no reconnected WebSocket)
// and spawning a genuinely fresh one in its place.
func ForceRespawnTrigger(gen int) Trigger { return Trigger{Kind: TriggerForceRespawn, Gen: gen} }

// RecoverTrigger builds a TriggerRecover trigger carrying the state to
// recover to; Transition validates that target against the legal
// previously-live set.
func RecoverTrigger(target State) Trigger { return Trigger{Kind: TriggerRecover, Target: target} }

// GraceExpiredTrigger builds a TriggerGraceExpired trigger carrying the
// caller-classified terminal outcome; Transition validates that target
// against {Stopped, Failed, Stale}.
func GraceExpiredTrigger(target State) Trigger {
	return Trigger{Kind: TriggerGraceExpired, Target: target}
}

// The remaining trigger kinds carry no payload; these constructors exist
// only so every trigger kind has a matching constructor (consistency, and
// so call sites never need to know which kinds happen to need a payload).

// ProviderAckTrigger builds a TriggerProviderAck trigger.
func ProviderAckTrigger() Trigger { return Trigger{Kind: TriggerProviderAck} }

// WSConnectedTrigger builds a TriggerWSConnected trigger.
func WSConnectedTrigger() Trigger { return Trigger{Kind: TriggerWSConnected} }

// BootCompleteTrigger builds a TriggerBootComplete trigger.
func BootCompleteTrigger() Trigger { return Trigger{Kind: TriggerBootComplete} }

// SnapshotStartTrigger builds a TriggerSnapshotStart trigger.
func SnapshotStartTrigger() Trigger { return Trigger{Kind: TriggerSnapshotStart} }

// SnapshotCompleteTrigger builds a TriggerSnapshotComplete trigger.
func SnapshotCompleteTrigger() Trigger { return Trigger{Kind: TriggerSnapshotComplete} }

// SuspectTrigger builds a TriggerSuspect trigger.
func SuspectTrigger() Trigger { return Trigger{Kind: TriggerSuspect} }

// Sentinel errors Transition can return, wrapped by the typed errors below
// so callers/tests can tell "no such (from, trigger) edge" apart from "a
// stale gen was rejected" via errors.Is, while still getting full structured
// detail via errors.As.
var (
	// ErrIllegalTransition means the (from, trigger) pair is not in the
	// transition table, or -- for TriggerRecover/TriggerGraceExpired --
	// the trigger's Target is not in that trigger's legal target set.
	ErrIllegalTransition = errors.New("sandbox: illegal transition")

	// ErrStaleGen means a gen-fenced trigger (Spawn/Restore/Resume)
	// carried a Gen that is not strictly greater than the sandbox's
	// current gen (§3.2: "stale-gen inputs are rejected and logged").
	ErrStaleGen = errors.New("sandbox: stale generation")
)

// IllegalTransitionError reports an (from, trigger[, target]) combination
// Transition rejected because it is not a legal edge in the state machine.
type IllegalTransitionError struct {
	From    State
	Trigger TriggerKind
	// Target is set only when Trigger carries one (Recover/GraceExpired)
	// and it was the Target itself that was illegal, not the (from,
	// trigger) pair.
	Target State
}

func (e *IllegalTransitionError) Error() string {
	if e.Target != "" {
		return fmt.Sprintf("sandbox: illegal transition: from %s via %s to target %s",
			e.From, e.Trigger, e.Target)
	}
	return fmt.Sprintf("sandbox: illegal transition: from %s via %s", e.From, e.Trigger)
}

func (e *IllegalTransitionError) Unwrap() error { return ErrIllegalTransition }

// StaleGenError reports a gen-fenced trigger whose Gen did not strictly
// exceed the sandbox's current gen.
type StaleGenError struct {
	From         State
	Trigger      TriggerKind
	CurrentGen   int
	AttemptedGen int
}

func (e *StaleGenError) Error() string {
	return fmt.Sprintf(
		"sandbox: stale gen: from %s via %s: attempted gen %d not strictly greater than current gen %d",
		e.From, e.Trigger, e.AttemptedGen, e.CurrentGen)
}

func (e *StaleGenError) Unwrap() error { return ErrStaleGen }

// transitionRule describes one legal (from, trigger-kind) edge.
//
//   - For fixed-target triggers, targets holds exactly that one target and
//     dynamic is false: Transition uses targets[0] outright.
//   - For dynamic-target triggers (Recover, GraceExpired), targets holds
//     the legal SET the caller-supplied Trigger.Target must belong to;
//     Transition validates membership but never invents the target --
//     the caller's Target is used as-is once validated.
type transitionRule struct {
	targets   []State
	dynamic   bool
	genFenced bool
}

// transitions is the explicit Transition(from, trigger) (to, error) table
// §11 requires. Every (from, trigger) edge the state machine allows is an
// entry here; anything not listed is illegal.
var transitions = map[State]map[TriggerKind]transitionRule{
	StatePending: {
		TriggerSpawn: {targets: []State{StateSpawning}, genFenced: true},
	},
	StateSpawning: {
		TriggerProviderAck: {targets: []State{StateConnecting}},
		TriggerSuspect:     {targets: []State{StateSuspect}},
		// A spawn interrupted before the sandbox ever connected
		// (EvaluateSpawnDecision's own SpawningTimeout carve-out) --
		// abandon it and spawn fresh, rather than skipping indefinitely.
		TriggerForceRespawn: {targets: []State{StateSpawning}, genFenced: true},
	},
	StateConnecting: {
		TriggerWSConnected: {targets: []State{StateBooting}},
		TriggerSuspect:     {targets: []State{StateSuspect}},
		// Same interrupted-spawn carve-out as StateSpawning above.
		TriggerForceRespawn: {targets: []State{StateSpawning}, genFenced: true},
	},
	StateBooting: {
		TriggerBootComplete: {targets: []State{StateReady}},
		TriggerSuspect:      {targets: []State{StateSuspect}},
		// Same interrupted-spawn carve-out as StateSpawning above.
		TriggerForceRespawn: {targets: []State{StateSpawning}, genFenced: true},
	},
	StateReady: {
		TriggerSnapshotStart: {targets: []State{StateSnapshotting}},
		TriggerSuspect:       {targets: []State{StateSuspect}},
		// Ready but never reconnected (EvaluateSpawnDecision's own
		// ReadyWait carve-out) -- abandon it and spawn fresh.
		TriggerForceRespawn: {targets: []State{StateSpawning}, genFenced: true},
	},
	StateSnapshotting: {
		TriggerSnapshotComplete: {targets: []State{StateReady}},
		TriggerSuspect:          {targets: []State{StateSuspect}},
	},
	StateSuspect: {
		// "Any liveness signal during grace returns to previous state"
		// (§3.2) -- legal targets are exactly the five states a watchdog
		// can suspect FROM. Recovering into e.g. Pending or Stopped is
		// illegal: those were never "previously live".
		TriggerRecover: {
			targets: []State{StateSpawning, StateConnecting, StateBooting, StateReady, StateSnapshotting},
			dynamic: true,
		},
		// Grace expiry: Stopped, Failed, and Stale are peer terminal
		// outcomes (§3.2), not a fixed single target -- which of the
		// three applies is caller-supplied context (explicit stop vs.
		// genuine timeout vs. GC/reconciler classification).
		TriggerGraceExpired: {
			targets: []State{StateStopped, StateFailed, StateStale},
			dynamic: true,
		},
	},
	StateStopped: {
		TriggerSpawn:   {targets: []State{StateSpawning}, genFenced: true},
		TriggerRestore: {targets: []State{StateSpawning}, genFenced: true},
		TriggerResume:  {targets: []State{StateConnecting}, genFenced: true},
	},
	StateFailed: {
		// "failed + resume-capable" is NOT a plan-stated recovery rule --
		// only "failed + snapshot -> restore" is. So Failed has no
		// TriggerResume edge.
		TriggerSpawn:   {targets: []State{StateSpawning}, genFenced: true},
		TriggerRestore: {targets: []State{StateSpawning}, genFenced: true},
	},
	StateStale: {
		TriggerSpawn:   {targets: []State{StateSpawning}, genFenced: true},
		TriggerRestore: {targets: []State{StateSpawning}, genFenced: true},
		TriggerResume:  {targets: []State{StateConnecting}, genFenced: true},
	},
}

// Transition is the single authority for whether a sandbox may move from
// state `from`, currently at generation `currentGen`, via `trig` -- and, if
// so, what state it lands in. Every illegal combination returns a typed
// error (IllegalTransitionError or StaleGenError), never a zero-value
// State silently accepted. "Single authority" is scoped to STATE transitions
// and identity-rotating gen fencing (see Trigger's Gen field doc) -- it is
// not the enforcement point for rejecting stale-gen WS connections or
// provider callbacks, which never reach here at all if the adapter layer
// (§6.1) is doing its job.
func Transition(from State, currentGen int, trig Trigger) (State, error) {
	byTrigger, ok := transitions[from]
	if !ok {
		return "", &IllegalTransitionError{From: from, Trigger: trig.Kind}
	}

	rule, ok := byTrigger[trig.Kind]
	if !ok {
		return "", &IllegalTransitionError{From: from, Trigger: trig.Kind}
	}

	var target State
	if rule.dynamic {
		if !stateIn(rule.targets, trig.Target) {
			return "", &IllegalTransitionError{From: from, Trigger: trig.Kind, Target: trig.Target}
		}
		target = trig.Target
	} else {
		target = rule.targets[0]
	}

	if rule.genFenced && trig.Gen <= currentGen {
		return "", &StaleGenError{
			From:         from,
			Trigger:      trig.Kind,
			CurrentGen:   currentGen,
			AttemptedGen: trig.Gen,
		}
	}

	return target, nil
}

func stateIn(states []State, target State) bool {
	for _, s := range states {
		if s == target {
			return true
		}
	}
	return false
}
