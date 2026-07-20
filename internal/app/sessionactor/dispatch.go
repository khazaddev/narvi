// This file (dispatch.go) implements handleEnsureDispatched (Step 21,
// "e2e happy path", design decision 3) -- the spawn/dispatch orchestration
// this whole Step is built around. Every actual DECISION is made by an
// already-built, already-tested pure domain function
// (internal/domain/sandbox.EvaluateCircuitBreaker/EvaluateSpawnDecision,
// internal/domain/turn.NextToDispatch/Transition) -- this file's own job
// is reading fresh state, calling the right decision function, and
// writing the result back, exactly like timerfired.go's own top comment
// describes for the 5 named timers.
//
// # Sequencing (do not collapse into one transaction)
//
// The spawn branch is deliberately split into THREE steps, never one big
// transact: (1) a transact that reads fresh state, evaluates the circuit
// breaker and spawn decision, and -- if the decision is Spawn -- mints a
// token, upserts the sandboxes row (gen bump, status='spawning',
// token_hash), arms connecting_deadline, and commits; (2) OUTSIDE any
// transaction, the real (possibly slow, network-bound) SandboxProvider.
// CreateSandbox call; (3) a SECOND transact that records the outcome
// (provider_id + a Spawning->Connecting transition on success; circuit-
// breaker bookkeeping + a Suspect transition on a permanent failure; a
// no-op log on a transient one). A real network call must never hold a
// Postgres transaction open -- see actor.go's own transact doc comment
// for why every write in this package already goes through a fresh
// connection, never the long-lived advisory-lock one.
//
// The dispatch branch (a Ready/Suspect sandbox with a Pending turn ready
// to go) is, as of this fix, split the SAME three-step way as the spawn
// branch above -- it used to run entirely inside ONE transact, on the
// theory that SandboxCommander.SendCommand is "a single bounded WS frame
// write, not a slow external API call" and therefore safe to make while
// holding the transact's own FOR UPDATE lock on the session row. An
// adversarial review proved that reasoning wrong empirically: a
// deliberately-3s-delayed SendCommand blocked a concurrent
// `actor_epoch = actor_epoch + 1` UPDATE against the same row for
// essentially the whole 3s -- i.e. a slow-but-alive sandbox connection
// (bounded only by platform.Timeouts.SandboxCommandSendTimeout, currently
// 10s) could delay a legitimate second-pod takeover (BumpActorEpoch,
// hydrate.go) by up to that long. So the dispatch branch now follows
// exactly the same shape: (1) a transact that transitions the turn
// Pending->Dispatched->Processing and arms turn_deadline, and commits;
// (2) OUTSIDE any transaction, the real SandboxCommander.SendCommand call;
// (3) on failure (including ports.ErrNoLiveSandboxConnection), a SECOND,
// small transact fails the turn. Unlike the spawn branch, there is no
// reverse edge to fall back to on failure here: domain/turn's transition
// table has no Processing->Pending edge (and internal/domain/turn/state.go
// is off-limits this Step, so none is added) -- the turn is already
// committed Processing by the time SendCommand is attempted, so a send
// failure moves it forward, to Failed, via the exact same
// "fails-with-reason + synthetic execution_complete" machinery
// handleTurnDeadlineTimer (timerfired.go) already uses for a turn_deadline
// expiry (see failDispatchedTurn's own doc comment for exactly why that
// specific existing edge, not a different one, is reused here).
//
// The resume branch (Step 23, "resume") originally needed a genuinely
// different shape from spawn/restore above: sandbox.TriggerResume used to
// go STRAIGHT from Stopped/Stale to Connecting, with no interim
// "Spawning"-like state to write before calling the provider the way
// planFreshSpawn/planRestore write spawning/gen/token first. An
// adversarial review found a real, empirically-reproduced concurrency bug
// in that shape: two DIFFERENT actor instances for the SAME session (a
// legitimate stale-epoch/pod-handover takeover -- the exact same race
// executeSpawn/executeRestore already handle correctly for their OWN
// outcome-recording step) could both read the same Stopped/Stale row,
// both have EvaluateSpawnDecision return SpawnActionResume, and BOTH call
// ResumeSandbox for the identical provider object before either call
// returned -- nothing marked the row as "a resume attempt is already in
// flight" the way Spawning already does for spawn/restore.
//
// The fix (this Step's own follow-up) gives resume the SAME two-step
// shape spawn/restore already have: sandbox.TriggerResume (state.go) now
// lands in Spawning, not Connecting -- an interim "claimed, in flight"
// marker exactly like a fresh spawn's own first step -- reusing
// Spawning's own already-correct EvaluateSpawnDecision no-op guard for
// free to close the race. A new sandbox.TriggerResumeAck is the second
// step, applied once ResumeSandbox actually returns success: Spawning ->
// Connecting. So the resume branch is now genuinely a third instance of
// the SAME shape spawn/restore already use, not a special case: (1)
// tryPlanSpawn's own transact validates the (from, trigger, gen) edge via
// sandbox.Transition and writes the interim claim (planResume, reusing
// the SAME UpsertSandboxForSpawn upsert planFreshSpawn/planRestore
// already share -- its own hardcoded status='spawning' now genuinely
// matches the Transition-validated target for resume too, not merely a
// coincidence self-corrected one write later), mints a fresh token/gen
// (sandbox tokens are hashed at rest, one per gen, per §5.2 -- paraphrased,
// not verbatim -- applying to a resume's own gen bump exactly as it does
// to a spawn/restore's), and arms TimerConnectingDeadline, all before
// this transact commits; (2) OUTSIDE any transaction,
// SandboxProvider.ResumeSandbox is called against the EXISTING provider
// object (never a new one -- ResumeSandbox's own port signature returns
// only an error, so there is no new ref to record); (3) a second, fresh
// transact (recordResumeOutcome) records the outcome: on success,
// Spawning -> Connecting via TriggerResumeAck, deliberately never calling
// UpdateProviderID (the provider object never changed); on failure,
// recordSpawnFailure is reused UNCHANGED -- a permanent resume failure
// now behaves identically to a permanent spawn/restore failure (same
// circuit-breaker increment, same transitionSandboxToSuspect path), since
// it now has the exact same interim Spawning write to transition out of
// that a permanent spawn/restore failure already does. See
// planResume/executeResume/recordResumeOutcome's own doc comments below
// for the full detail, and this Step's own PR description for the
// honest, deliberately-deferred gap this still leaves open (unrelated to,
// and not fixed by, this concurrency fix): ResumeSandbox's own port
// signature (§4.1) has no CreateSpec/SESSION_CONFIG delivery channel, so
// there is still no way for the control plane to actually deliver this
// freshly minted token/gen to the already-running provider instance today
// -- left for Step 48 (RWX) to resolve for real.

package sessionactor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/domain/turn"
	"github.com/khazaddev/narvi/internal/platform"
)

// placeholderBaseImage is the CreateSpec.Image value used until Step 26
// ("image builds") makes real per-session image construction a thing --
// see ports.CreateSpec.Image's own doc comment ("Empty means the
// provider's own default base image"). A non-empty, clearly-named
// placeholder is used instead of leaving Image empty, so a real
// SandboxProvider's own request log unambiguously shows which image narvi
// asked for, even though nothing today builds or publishes an image under
// this exact tag.
const placeholderBaseImage = "narvi/sandbox-agent:placeholder"

// scmCommitName/scmCommitEmail are the currently-unused-anywhere-in-git
// placeholder git author identity values Step 17's own gap already
// documented (cmd/sandbox-agent's commandHandler never actually invokes
// git commit itself -- OpenCode's own tooling does, configured with
// whatever scmName/scmEmail this Prompt carries). Real per-user git
// identity (attributing a commit to the actual prompting human) is
// explicitly out of scope for this Step too -- these two constants just
// keep the required wire fields non-empty.
const (
	scmCommitName  = "narvi-agent"
	scmCommitEmail = "agent@narvi.dev"
)

// hashSandboxToken mirrors internal/adapters/inbound/wshub.
// HashSandboxToken's algorithm exactly (SHA-256, unsalted, hex-encoded --
// a sandbox token is a high-entropy, server-generated secret, not a
// low-entropy human password, so the salted-slow-hash rationale for
// passwords does not apply). Duplicated here, rather than calling wshub's
// own exported function, because internal/adapters/inbound/wshub already
// imports internal/app/sessionactor (wshub.NewSandboxHandler takes a
// *sessionactor.Registry) -- importing wshub from here would create an
// import cycle, and app/sessionactor must never import
// internal/adapters/* regardless (internal/app/ports/doc.go's own
// import-direction rule), so this small duplication is correct, not
// merely expedient.
func hashSandboxToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// spawnPlan is what planDispatch's own transact hands back to
// handleEnsureDispatched when (and only when) the spawn branch decided to
// actually spawn, restore, OR resume -- the real provider call itself
// happens OUTSIDE that transact, in executeSpawn/executeRestore/
// executeResume, using exactly this plan.
//
// Step 23's own concurrency fix folds resume into this SAME type (see the
// resume/providerObjectID fields below) rather than keeping the separate
// resumePlan type this Step originally introduced: now that
// sandbox.TriggerResume's own first-step target is Spawning (state.go),
// resume's own write genuinely IS the same "commit an interim claim
// inside tryPlanSpawn's transact, then call the provider outside any
// transaction" shape spawn/restore already have -- planDispatch's own
// two-plan return shape (spawnPlan, dispatchPlan) is restored to exactly
// what it was before resume needed a third, structurally-different plan
// type at all.
type spawnPlan struct {
	gen  int
	spec ports.CreateSpec

	// restore is true when this plan is a Stopped/Failed/Stale ->
	// Spawning RESTORE (§3.2: "stopped|stale + snapshot -> restore (new
	// gen)"), as opposed to a plain fresh spawn (Step 22, "snapshots &
	// restore", design decision 6). handleEnsureDispatched dispatches
	// restore==true to executeRestore instead of executeSpawn. Mutually
	// exclusive with resume (below) -- EvaluateSpawnDecision's own
	// priority ordering (spawndecision.go, untouched by this fix) never
	// returns both kinds for the same call. planDispatch's own two-plan
	// return shape (Step 21) is deliberately unchanged by this addition --
	// a restore decision is still exactly branch (a) ("no live sandbox,
	// needs [re]spawning") planDispatch already recognizes, just
	// restoring from a snapshot instead of creating fresh.
	restore bool
	// snapshotID is set only when restore is true: the snapshot to
	// restore from (ports.SnapshotID(action.SnapshotImageID)).
	snapshotID ports.SnapshotID

	// resume is true when this plan is a Stopped/Stale -> Spawning
	// interim claim for a persistent RESUME of an existing provider
	// sandbox object (Step 23, "resume") -- reusing the SAME two-step
	// shape restore/spawn already have, now that TriggerResume's own
	// target is Spawning rather than a special one-step jump straight to
	// Connecting. handleEnsureDispatched dispatches resume==true to
	// executeResume instead of executeSpawn/executeRestore.
	resume bool
	// providerObjectID is set only when resume is true: the SAME
	// provider sandbox instance being resumed (action.ProviderObjectID
	// from EvaluateSpawnDecision) -- never a new one, unlike spec/
	// snapshotID above, which ask the provider to CREATE one.
	// executeResume calls ResumeSandbox with this id directly; spec is
	// left at its zero value on a resume plan and never read by
	// executeResume, since ResumeSandbox's own port signature (§4.1)
	// takes no CreateSpec at all.
	providerObjectID string
}

// dispatchPlan is what planDispatch's own transact hands back to
// handleEnsureDispatched when (and only when) the dispatch branch decided
// to actually dispatch a Pending turn to an already-live sandbox -- the
// real SandboxCommander.SendCommand call itself happens OUTSIDE that
// transact, in executeDispatch, using exactly this plan. By the time this
// is returned, the turn has already been committed Pending->Dispatched->
// Processing and turn_deadline is already armed -- see this file's own top
// comment for why.
type dispatchPlan struct {
	turnID  pgtype.UUID
	payload json.RawMessage
}

// handleEnsureDispatched implements the EnsureDispatched command (Step
// 21, design decision 3): read fresh state, decide whether to spawn,
// resume, or restore a sandbox, or dispatch a pending turn (or do nothing
// this round), and act. Resume (Step 23) added alongside spawn/restore;
// spawn.resume is checked before spawn.restore since both are carried on
// the SAME spawnPlan type (its own doc comment above explains why) and
// are mutually exclusive by construction.
func (a *Actor) handleEnsureDispatched(ctx context.Context) error {
	spawn, dispatch, err := a.planDispatch(ctx)
	if err != nil {
		return err
	}
	switch {
	case spawn != nil && spawn.resume:
		return a.executeResume(ctx, spawn)
	case spawn != nil && spawn.restore:
		return a.executeRestore(ctx, spawn)
	case spawn != nil:
		return a.executeSpawn(ctx, spawn)
	case dispatch != nil:
		return a.executeDispatch(ctx, dispatch)
	default:
		// Nothing to do this round: no pending turn, branch (c)'s no-op,
		// or a defensive skip (nil provider/commander) inside one of the
		// try* helpers below.
		return nil
	}
}

// planDispatch runs the ENTIRE read-fresh-state-and-decide step inside one
// transact (§2's own epoch-fencing discipline: every read that a write
// decision depends on must be fenced the same way the write itself is).
// Returns a non-nil *spawnPlan or *dispatchPlan (never both) only when the
// corresponding branch decided to act and its own "commit state" half has
// already committed (a resume's own interim Spawning claim, exactly like
// a fresh spawn/restore's, per this file's own top comment) -- the caller
// (handleEnsureDispatched) then performs the actual network call
// (CreateSandbox / RestoreFromSnapshot / ResumeSandbox / SendCommand
// respectively) outside any transaction.
func (a *Actor) planDispatch(ctx context.Context) (*spawnPlan, *dispatchPlan, error) {
	var spawn *spawnPlan
	var dispatch *dispatchPlan

	err := a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := time.Now()

		sessionRow, err := a.stores.session.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("sessionactor: get session: %w", err)
		}

		sandboxRow, sbErr := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		hasSandbox := true
		if sbErr != nil {
			if !errors.Is(sbErr, pgx.ErrNoRows) {
				return fmt.Errorf("sessionactor: get sandbox: %w", sbErr)
			}
			hasSandbox = false
		}

		turns, err := a.stores.turn.WithTx(tx).ListForSession(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("sessionactor: list turns: %w", err)
		}

		// NextToDispatch already encodes "no in-flight turn AND a Pending
		// turn exists" -- both branches (a) and (b) below need exactly
		// that same predicate, just gated on a different sandbox-status
		// condition.
		pendingID, hasPending := turn.NextToDispatch(toQueueEntries(turns))
		if !hasPending {
			return nil
		}

		// Branch (a): no sandbox row yet, or the existing one is dead
		// (Stopped/Stale/Failed) -- needs a fresh spawn, restore, or
		// resume before anything can be dispatched.
		if !hasSandbox || sandbox.IsDeadSandboxStatus(sandbox.State(sandboxRow.Status)) {
			sp, err := a.tryPlanSpawn(ctx, tx, sessionRow, sandboxRow, hasSandbox, now)
			if err != nil {
				return err
			}
			spawn = sp
			return nil
		}

		// Branch (b): a live sandbox already exists and is Ready (or
		// Suspect -- Step 24's own future recovery job either way) -- the
		// turn can be dispatched to it right now.
		//
		// Known, honestly-documented gap: this always routes a Ready
		// sandbox straight to tryPlanDispatch, regardless of whether it
		// actually has a live WebSocket connection right now.
		// EvaluateSpawnDecision already supports recovering a Ready sandbox
		// whose WebSocket never reconnected (past SpawnConfig.ReadyWait --
		// see tryPlanSpawn's own ForceRespawn carve-out), but nothing here
		// ever reaches that path for a Ready status: tryPlanSpawn's own
		// SpawnState construction has zero wiring for HasActiveWebSocket
		// today (it is never set to anything but its false zero value; grep
		// the repo -- no production caller sets it anywhere), so it cannot
		// yet safely distinguish "Ready with a dead connection, past
		// ReadyWait" from "Ready with a perfectly live one" here. Wiring
		// this branch to tryPlanSpawn before that detection exists (via the
		// sandbox-side connection registry/SandboxCommander) would risk
		// force-respawning a healthy, actively-connected Ready sandbox out
		// from under a live session -- worse than leaving this narrower gap
		// open and documented, matching this project's own established
		// practice elsewhere (e.g. Step 21/22's own documented, deliberate
		// gaps) of naming a real, known-open limitation rather than
		// silently leaving it unstated or attempting a half-solution.
		status := sandbox.State(sandboxRow.Status)
		if status == sandbox.StateReady || status == sandbox.StateSuspect {
			d, err := a.tryPlanDispatch(ctx, tx, sessionRow, sandboxRow, pendingID, turns, now)
			if err != nil {
				return err
			}
			dispatch = d
			return nil
		}

		// Branch (c): sandbox exists, is neither dead nor Ready/Suspect
		// (e.g. still Spawning/Connecting/Booting) -- defer to
		// EvaluateSpawnDecision's own judgment, exactly like branch (a)
		// does: for the vast majority of calls (a sandbox still genuinely
		// booting, well within SpawnStuckTimeout) this is a safe, cheap
		// no-op via EvaluateSpawnDecision's own Skip guard; only once the
		// sandbox is genuinely stuck past that window does it produce a
		// real SpawnActionSpawn -> TriggerForceRespawn -> actual respawn.
		// hasSandbox is always true by the time this branch is reached --
		// branch (a) above already handles the !hasSandbox case.
		sp, err := a.tryPlanSpawn(ctx, tx, sessionRow, sandboxRow, hasSandbox, now)
		if err != nil {
			return err
		}
		spawn = sp
		return nil
	})

	return spawn, dispatch, err
}

// tryPlanSpawn implements design decision 3a's own circuit-breaker-then-
// spawn-decision sequence, and -- on SpawnActionSpawn/SpawnActionRestore/
// SpawnActionResume -- performs the token-mint/upsert/arm-timer write, all
// still inside the caller's own transact (Step 23's own concurrency fix:
// resume now writes its own interim Spawning claim here too, exactly like
// spawn/restore already did -- see this file's own top comment for why
// that write is what closes the concurrent-double-ResumeSandbox-call race
// an adversarial review found in the OLD one-step shape).
func (a *Actor) tryPlanSpawn(
	ctx context.Context, tx pgx.Tx,
	sessionRow sqlcgen.Session, sandboxRow sqlcgen.Sandbox, hasSandbox bool,
	now time.Time,
) (*spawnPlan, error) {
	cbState := sandbox.CircuitBreakerState{}
	if hasSandbox {
		cbState.FailureCount = int(sandboxRow.SpawnFailureCount)
		if sandboxRow.LastSpawnFailureAt.Valid {
			cbState.LastFailureTime = sandboxRow.LastSpawnFailureAt.Time
		}
	}

	cbCfg := sandbox.CircuitBreakerConfig{
		Threshold: sandbox.CircuitBreakerThreshold,
		Window:    a.timeouts.CircuitBreakerWindow,
	}
	cbDecision := sandbox.EvaluateCircuitBreaker(cbState, cbCfg, now)

	if cbDecision.ShouldReset && hasSandbox && (sandboxRow.SpawnFailureCount != 0 || sandboxRow.LastSpawnFailureAt.Valid) {
		if _, err := a.stores.sandbox.WithTx(tx).UpdateCircuitBreaker(ctx, sqlcgen.UpdateSandboxCircuitBreakerParams{
			SessionID:          a.sessionID,
			SpawnFailureCount:  0,
			LastSpawnFailureAt: pgtype.Timestamptz{},
		}); err != nil {
			return nil, fmt.Errorf("sessionactor: reset circuit breaker: %w", err)
		}
		sandboxRow.SpawnFailureCount = 0
		sandboxRow.LastSpawnFailureAt = pgtype.Timestamptz{}
	}

	if !cbDecision.ShouldProceed {
		// An honest, documented limitation (per this Step's own brief):
		// nothing re-triggers EnsureDispatched on a plain timeout with no
		// other sandbox activity at all -- a later EnsureDispatched (the
		// next sandbox event, or a future session) tries again. A
		// genuinely timer-driven retry is Step 30's own resilience-suite
		// job (§9.3 #8), not this one's.
		a.logger.Warn("sessionactor: spawn circuit breaker open; skipping spawn this round",
			"wait", cbDecision.WaitTime.String())
		return nil, nil
	}

	if a.provider == nil {
		// Defensive: a Registry constructed without a SandboxProvider
		// (some tests, and any future caller that genuinely has none)
		// must not panic here -- log and no-op, exactly like the
		// nil-commander guard in tryPlanDispatch below.
		a.logger.Warn("sessionactor: spawn decision would proceed but no SandboxProvider is configured; skipping")
		return nil, nil
	}

	spawnState := sandbox.SpawnState{Status: sandbox.StatePending}
	if hasSandbox {
		spawnState = sandbox.SpawnState{
			Status:           sandbox.State(sandboxRow.Status),
			CreatedAt:        pgTimeOrZero(sandboxRow.CreatedAt),
			ProviderObjectID: stringOrEmpty(sandboxRow.ProviderID),
			// SnapshotImageID is read back from the sandbox row's own real
			// snapshot_id column (Step 22, "snapshots & restore", design
			// decision 6) -- "" (no snapshot machinery reached it yet, or
			// this sandbox was never snapshotted) unless a real
			// "snapshot_ready" event has previously persisted one (see
			// sandboxevent.go's own new branch).
			SnapshotImageID: stringOrEmpty(sandboxRow.SnapshotID),
			LastSeenAt:      pgTimeOrZero(sandboxRow.LastSeenAt),
		}
	}

	caps := a.provider.Capabilities()
	action := sandbox.EvaluateSpawnDecision(spawnState, sandbox.SpawnConfig{
		Cooldown:        a.timeouts.SpawnCooldown,
		ReadyWait:       a.timeouts.SpawnReadyWait,
		SpawningTimeout: a.timeouts.SpawnStuckTimeout,
	}, now, false, caps.Resume)

	switch action.Kind {
	case sandbox.SpawnActionSpawn:
		return a.planFreshSpawn(ctx, tx, sessionRow, hasSandbox, sandboxRow, now)
	case sandbox.SpawnActionRestore:
		// Only reachable when hasSandbox is true (EvaluateSpawnDecision's
		// own Restore branch requires SnapshotImageID != "" AND status in
		// {Stopped, Failed, Stale} -- none of which a brand-new,
		// no-sandbox-row session can have), so sandboxRow is a real,
		// already-loaded row here, not the zero value.
		return a.planRestore(ctx, tx, sessionRow, sandboxRow, action.SnapshotImageID, now)
	case sandbox.SpawnActionResume:
		// Only reachable when hasSandbox is true (EvaluateSpawnDecision's
		// own Resume branch requires ProviderObjectID != "" AND status in
		// {Stopped, Stale} -- none of which a brand-new, no-sandbox-row
		// session can have), mirroring SpawnActionRestore's own identical
		// reasoning above.
		return a.planResume(ctx, tx, sandboxRow, action.ProviderObjectID, now)
	default:
		// Skip/Wait are genuine, expected outcomes (cooldown, already in
		// progress, circuit breaker, ...). Spawn, Restore, and Resume are
		// all handled above (Step 21, Step 22, and Step 23 respectively) --
		// this default case exists so a future SpawnActionKind addition
		// fails safely into a no-op (logged) rather than being silently
		// mishandled by one of the cases above.
		a.logger.Info("sessionactor: spawn decision is not Spawn/Restore/Resume this round; no-op",
			"kind", action.Kind.String(), "reason", action.Reason)
		return nil, nil
	}
}

// planFreshSpawn implements design decision 3a's own write (token mint,
// upsert, arm connecting_deadline, assemble a Fresh-boot-mode
// SessionConfig) for the plain SpawnActionSpawn case -- pulled out of
// tryPlanSpawn's own body so planRestore (below) can share its structure
// without a blind copy-paste. Also validates the (from, trigger, gen) edge
// via sandbox.Transition before writing (§11's own hard rule, "every state
// transition goes through the machine's transition table") -- see
// planRestore's own doc comment below for why this validation is now
// shared by both paths.
func (a *Actor) planFreshSpawn(
	ctx context.Context, tx pgx.Tx,
	sessionRow sqlcgen.Session, hasSandbox bool, sandboxRow sqlcgen.Sandbox,
	now time.Time,
) (*spawnPlan, error) {
	// §11's own hard rule applies here just as much as anywhere else:
	// EvaluateSpawnDecision (tryPlanSpawn, above) is a pure decision
	// function, not the state machine's own authority on whether this
	// particular (from, trigger) edge is actually legal -- that authority
	// is sandbox.Transition alone. fromState/currentGen mirror exactly
	// what tryPlanSpawn's own spawnState was built from (never recomputed
	// a second, possibly-inconsistent way); newGen is computed Go-side
	// but must -- and, by construction, does -- match what
	// UpsertForSpawn's own SQL (gen = sandboxes.gen + 1, or 1 on a fresh
	// insert) independently computes for the exact same row, since both
	// run inside this same already-open transact, which already holds
	// the FOR UPDATE lock on the session row (transact's own
	// GetActorEpochForUpdate) that serializes every writer of this
	// session's sandbox row -- no concurrent writer can interleave
	// between this read and UpsertForSpawn's write below.
	fromState := sandbox.StatePending
	currentGen := 0
	if hasSandbox {
		fromState = sandbox.State(sandboxRow.Status)
		currentGen = int(sandboxRow.Gen)
	}
	newGen := currentGen + 1

	var chosenTrigger sandbox.Trigger
	switch fromState {
	case sandbox.StatePending, sandbox.StateStopped, sandbox.StateFailed, sandbox.StateStale:
		// A genuinely fresh/terminal-state respawn -- no snapshot/resume
		// available (see TriggerSpawn's own doc comment).
		chosenTrigger = sandbox.SpawnTrigger(newGen)
	case sandbox.StateSpawning, sandbox.StateConnecting, sandbox.StateBooting, sandbox.StateReady:
		// EvaluateSpawnDecision's own two recovery carve-outs: a spawn/
		// connect interrupted before the sandbox ever connected, or a
		// Ready sandbox whose WebSocket never reconnected. Abandoning a
		// stuck-but-live sandbox is worth a distinct, observable log line
		// -- unlike an ordinary spawn, this is giving up on something
		// that was (or claimed to be) alive.
		chosenTrigger = sandbox.ForceRespawnTrigger(newGen)
		a.logger.Warn("sessionactor: sandbox stuck in a live state past its recovery window; force-respawning",
			"session_id", a.sessionID.String(), "from_status", string(fromState),
			"gen", currentGen, "new_gen", newGen)
	default:
		// StateSuspect can never reach here: EvaluateSpawnDecision's own
		// Suspect branch always returns Skip (no staleness carve-out), so
		// action.Kind would already be SpawnActionSkip, not
		// SpawnActionSpawn, and this function would have returned above.
		// Any OTHER unrecognized status reaching here means
		// EvaluateSpawnDecision's own logic and this trigger-selection
		// switch have drifted out of sync -- fail loudly rather than fall
		// through to an unvalidated write.
		return nil, fmt.Errorf("sessionactor: spawn decision returned Spawn from unexpected status %q; no legal trigger for it", fromState)
	}

	if _, err := sandbox.Transition(fromState, currentGen, chosenTrigger); err != nil {
		return nil, fmt.Errorf("sessionactor: sandbox transition validation for spawn (from=%s trigger=%s gen=%d->%d): %w",
			fromState, chosenTrigger.Kind, currentGen, newGen, err)
	}

	token, err := platform.GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("sessionactor: generate sandbox token: %w", err)
	}
	tokenHash := hashSandboxToken(token)

	row, err := a.stores.sandbox.WithTx(tx).UpsertForSpawn(ctx, sqlcgen.UpsertSandboxForSpawnParams{
		SessionID: a.sessionID,
		TokenHash: &tokenHash,
	})
	if err != nil {
		return nil, fmt.Errorf("sessionactor: upsert sandbox for spawn: %w", err)
	}

	// Arm connecting_deadline now, at spawn-write time -- closing an
	// honest pre-existing gap (nothing armed this timer in production
	// before this Step, since nothing called CreateSandbox in production
	// before this Step either): without it, a spawn that never connects
	// would never be noticed by any watchdog.
	if err := a.armTimer(ctx, tx, TimerConnectingDeadline, now.Add(a.timeouts.FirstConnectBudget)); err != nil {
		return nil, err
	}

	cfg, err := a.assembleSessionConfig(sessionRow, int(row.Gen), token, row.ID.String(), sessionconfig.SessionConfigBootModeFresh)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: assemble session config: %w", err)
	}

	spec := ports.CreateSpec{Gen: int(row.Gen), SessionConfig: cfg, Image: placeholderBaseImage}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("sessionactor: invalid create spec: %w", err)
	}

	return &spawnPlan{gen: int(row.Gen), spec: spec}, nil
}

// planRestore implements design decision 6's own restore-specific write.
// It reuses the EXACT SAME UpsertSandboxForSpawn upsert planFreshSpawn
// uses above -- that query's own doc comment already documents this dual
// purpose ("every spawn/restore increments sandbox.gen"), so no second,
// parallel SQL write is built here. The one difference design decision 6
// calls out: BootMode passed to assembleSessionConfig is SnapshotRestore,
// not Fresh. Both paths validate their own (from, trigger, gen) edge via
// sandbox.Transition before writing -- planFreshSpawn via sandbox.
// SpawnTrigger/ForceRespawnTrigger, this path via sandbox.RestoreTrigger --
// proving each trigger's own FROM-state/gen-fencing legality genuinely
// gates the write, rather than trusting EvaluateSpawnDecision's own coarser
// status check alone.
func (a *Actor) planRestore(
	ctx context.Context, tx pgx.Tx,
	sessionRow sqlcgen.Session, sandboxRow sqlcgen.Sandbox, snapshotImageID string,
	now time.Time,
) (*spawnPlan, error) {
	// newGen mirrors exactly what UpsertSandboxForSpawn's own SQL is about
	// to compute (gen = gen + 1) -- predicted here, in Go, purely so
	// sandbox.Transition can validate the (from, trigger, gen) edge BEFORE
	// any write happens. Safe to predict rather than race: this actor is
	// the session's own single writer (§2), so no concurrent write can
	// have changed sandboxRow.Gen between planDispatch's own read and this
	// call.
	newGen := int(sandboxRow.Gen) + 1
	if _, err := sandbox.Transition(sandbox.State(sandboxRow.Status), int(sandboxRow.Gen), sandbox.RestoreTrigger(newGen)); err != nil {
		return nil, fmt.Errorf("sessionactor: sandbox transition restore (stopped/failed/stale->spawning): %w", err)
	}

	token, err := platform.GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("sessionactor: generate sandbox token: %w", err)
	}
	tokenHash := hashSandboxToken(token)

	row, err := a.stores.sandbox.WithTx(tx).UpsertForSpawn(ctx, sqlcgen.UpsertSandboxForSpawnParams{
		SessionID: a.sessionID,
		TokenHash: &tokenHash,
	})
	if err != nil {
		return nil, fmt.Errorf("sessionactor: upsert sandbox for restore: %w", err)
	}
	if int(row.Gen) != newGen {
		// Should be unreachable (this actor is this session's own single
		// writer, per §2 -- nothing else can have written to this row
		// between the Transition validation above and this same upsert) --
		// defensive, not an expected path.
		return nil, fmt.Errorf("sessionactor: restore gen mismatch: validated %d, upsert produced %d", newGen, row.Gen)
	}

	// Same reasoning as planFreshSpawn's own identical call: a restore
	// lands in the SAME Spawning state a fresh spawn does, so it needs the
	// SAME watchdog coverage while it waits to connect.
	if err := a.armTimer(ctx, tx, TimerConnectingDeadline, now.Add(a.timeouts.FirstConnectBudget)); err != nil {
		return nil, err
	}

	cfg, err := a.assembleSessionConfig(sessionRow, int(row.Gen), token, row.ID.String(), sessionconfig.SessionConfigBootModeSnapshotRestore)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: assemble session config: %w", err)
	}

	spec := ports.CreateSpec{Gen: int(row.Gen), SessionConfig: cfg, Image: placeholderBaseImage}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("sessionactor: invalid create spec: %w", err)
	}

	return &spawnPlan{gen: int(row.Gen), spec: spec, restore: true, snapshotID: ports.SnapshotID(snapshotImageID)}, nil
}

// planResume implements Step 23's own resume-specific write -- the FIRST
// step of the two-step shape sandbox.TriggerResume now has (state.go's
// own doc comment): an interim "claimed, in flight" write into Spawning,
// committed inside the caller's own transact, exactly like planFreshSpawn/
// planRestore's own identical first-step write, before any provider call
// happens. This is precisely the write whose ABSENCE the adversarial
// review this fix responds to exploited: without it, nothing marked a
// Stopped/Stale row as "a resume is already in flight," so a second actor
// instance for the same session could reach EvaluateSpawnDecision's own
// Resume branch a second time and call ResumeSandbox concurrently. Once
// this write commits, that same second actor's own EvaluateSpawnDecision
// call reads status==Spawning instead of Stopped/Stale and no-ops via its
// own existing SpawningTimeout-guarded Skip branch (spawndecision.go,
// untouched by this fix) -- the exact same protection a concurrent second
// spawn/restore attempt already gets.
//
// Reuses the EXACT SAME UpsertSandboxForSpawn upsert planFreshSpawn/
// planRestore already share -- that query's own doc comment ("every
// spawn/restore increments sandbox.gen") is now genuinely true of
// resume's own first step too: the Transition-validated target for
// TriggerResume IS Spawning now, so this upsert's hardcoded
// status='spawning' needs no post-hoc correction the way the OLD
// executeResume's single-transact shape used to need (that self-correcting
// UpdateStatus call is gone -- see this Step's own PR description for the
// now-resolved §11 "no ad-hoc status writes" nit this used to represent).
//
// Deliberately does NOT build a ports.CreateSpec/SessionConfig the way
// planFreshSpawn/planRestore do: ResumeSandbox's own port signature
// (§4.1) takes only (ctx, SandboxRef) -- no CreateSpec parameter -- so
// there is nothing for a spec to be validated or passed to here. The
// returned plan's own spec field is left at its zero value; executeResume
// never reads it. A fresh token/gen is still minted and persisted
// (sandbox tokens are hashed at rest, one per gen, per §5.2 -- paraphrased,
// not verbatim -- applying to a resume's own gen bump exactly as it does
// to a spawn/restore's), even though there is today no channel to deliver
// the plaintext token to the already-running provider instance (the
// honest, deliberately-deferred gap this file's own top comment and Step
// 23's PR description both already document -- unrelated to, and not
// solved by, this concurrency fix).
func (a *Actor) planResume(
	ctx context.Context, tx pgx.Tx,
	sandboxRow sqlcgen.Sandbox, providerObjectID string,
	now time.Time,
) (*spawnPlan, error) {
	// newGen mirrors exactly what UpsertSandboxForSpawn's own SQL is about
	// to compute (gen = gen + 1) -- predicted here, in Go, purely so
	// sandbox.Transition can validate the (from, trigger, gen) edge BEFORE
	// any write happens, mirroring planRestore's own identical reasoning:
	// this actor is the session's own single writer (§2), so no
	// concurrent write can have changed sandboxRow.Gen between
	// planDispatch's own read and this call.
	newGen := int(sandboxRow.Gen) + 1
	if _, err := sandbox.Transition(sandbox.State(sandboxRow.Status), int(sandboxRow.Gen), sandbox.ResumeTrigger(newGen)); err != nil {
		return nil, fmt.Errorf("sessionactor: sandbox transition resume (stopped/stale->spawning): %w", err)
	}

	token, err := platform.GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("sessionactor: generate sandbox token: %w", err)
	}
	tokenHash := hashSandboxToken(token)

	row, err := a.stores.sandbox.WithTx(tx).UpsertForSpawn(ctx, sqlcgen.UpsertSandboxForSpawnParams{
		SessionID: a.sessionID,
		TokenHash: &tokenHash,
	})
	if err != nil {
		return nil, fmt.Errorf("sessionactor: upsert sandbox for resume: %w", err)
	}
	if int(row.Gen) != newGen {
		// Should be unreachable -- this actor is this session's own single
		// writer, per §2 -- mirroring planRestore's own identical
		// defensive check.
		return nil, fmt.Errorf("sessionactor: resume gen mismatch: validated %d, upsert produced %d", newGen, row.Gen)
	}

	// Same reasoning as planFreshSpawn/planRestore's own identical call: a
	// resume now lands in the SAME Spawning state a fresh spawn/restore
	// does, so it needs the SAME watchdog coverage while it waits for
	// ResumeSandbox to return and the sandbox to reconnect its WS.
	if err := a.armTimer(ctx, tx, TimerConnectingDeadline, now.Add(a.timeouts.FirstConnectBudget)); err != nil {
		return nil, err
	}

	return &spawnPlan{gen: int(row.Gen), resume: true, providerObjectID: providerObjectID}, nil
}

// executeSpawn performs the actual (possibly slow, network-bound)
// CreateSandbox call OUTSIDE any transaction, then records the outcome in
// a SECOND, fresh transact -- design decision 3a's own required
// sequencing.
//
// Known, documented limitation pending Step 25's reconciler (docs/
// IMPLEMENTATION_PLAN.md row 25, "reconciler + GC ... orphan harvesting"):
// if CreateSandbox above genuinely succeeds (createErr == nil, a real
// cloud resource now exists under ref.ProviderID) but this actor's own
// epoch has gone stale by the time the transact below runs (a legitimate
// pod-handoff race -- a newer actor already took over and bumped
// actor_epoch), transact's own epoch-fencing check correctly rolls back
// this whole write, INCLUDING the UpdateProviderID call that would have
// been the only durable record of ref.ProviderID anywhere. Building a full
// reconciler (ports.SandboxProvider.List is shaped for exactly this, but
// has zero production callers yet) is explicitly Step 25's own job, not
// this one's -- the scoped-down fix here is to log loudly rather than
// silently swallow the leak, so it is at least observable/actionable by an
// operator until Step 25 lands.
func (a *Actor) executeSpawn(ctx context.Context, plan *spawnPlan) error {
	ref, createErr := a.provider.CreateSandbox(ctx, plan.spec)

	err := a.recordProviderOutcome(ctx, plan.gen, ref, createErr)

	if err != nil && createErr == nil && errors.Is(err, ErrStaleEpoch) {
		// See this function's own doc comment above: a real cloud sandbox
		// was just created but the write recording it just got rolled back
		// by a legitimate stale-epoch takeover -- log everything an
		// operator would need to find and manually clean this up until
		// Step 25's reconciler exists.
		a.logger.Warn("sessionactor: spawned sandbox orphaned by stale-epoch takeover; provider resource was never recorded and will leak until Step 25's reconciler exists",
			"session_id", a.sessionID.String(),
			"provider_id", ref.ProviderID,
			"gen", plan.gen,
		)
	}

	return err
}

// executeRestore mirrors executeSpawn exactly (Step 22, "snapshots &
// restore", design decision 6), except it calls RestoreFromSnapshot
// instead of CreateSandbox -- see recordProviderOutcome (below) for the
// second-transact outcome-recording half both now share, including the
// SAME recordSpawnFailure/circuit-breaker reuse a permanent
// RestoreFromSnapshot failure gets (that function's own doc comment
// explains why a restore failure is still fundamentally "we tried to get
// a live sandbox and a permanent provider error came back" -- the SAME
// concern a spawn failure is, not a different one). Shares executeSpawn's
// own known, documented stale-epoch-orphan limitation too: a real
// restored provider sandbox can equally leak until Step 25's reconciler
// exists.
func (a *Actor) executeRestore(ctx context.Context, plan *spawnPlan) error {
	ref, restoreErr := a.provider.RestoreFromSnapshot(ctx, plan.snapshotID, plan.spec)

	err := a.recordProviderOutcome(ctx, plan.gen, ref, restoreErr)

	if err != nil && restoreErr == nil && errors.Is(err, ErrStaleEpoch) {
		a.logger.Warn("sessionactor: restored sandbox orphaned by stale-epoch takeover; provider resource was never recorded and will leak until Step 25's reconciler exists",
			"session_id", a.sessionID.String(),
			"provider_id", ref.ProviderID,
			"gen", plan.gen,
		)
	}

	return err
}

// recordProviderOutcome is executeSpawn/executeRestore's own shared
// second-transact outcome-recording step (Step 22, design decision 6:
// "sharing whatever helper logic is genuinely common between the two...
// rather than duplicating it blindly") -- the exact same transact body
// Step 21's own executeSpawn used to run inline, unchanged in behavior:
// given the plan's own gen and the just-returned (ref, providerErr) from
// the real provider call, either records success (provider_id + the SAME
// Spawning->Connecting transition both a fresh spawn and a restore land
// in) or classifies+records a failure via recordSpawnFailure. Returns
// ErrStaleEpoch unchanged on a legitimate pod-handoff race, exactly like
// the inline transact this replaces already did.
func (a *Actor) recordProviderOutcome(ctx context.Context, gen int, ref ports.SandboxRef, providerErr error) error {
	return a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := time.Now()

		sandboxRow, err := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("sessionactor: get sandbox: %w", err)
		}

		if int(sandboxRow.Gen) != gen {
			// A newer spawn/restore attempt has already superseded this
			// one (e.g. a stale-spawn recovery elsewhere raced this call)
			// -- this result no longer applies to anything.
			a.logger.Warn("sessionactor: provider result for a superseded gen; ignoring",
				"result_gen", gen, "current_gen", sandboxRow.Gen)
			return nil
		}

		if providerErr != nil {
			return a.recordSpawnFailure(ctx, tx, sandboxRow, providerErr, now)
		}

		if _, err := a.stores.sandbox.WithTx(tx).UpdateProviderID(ctx, sqlcgen.UpdateSandboxProviderIDParams{
			SessionID:  a.sessionID,
			ProviderID: &ref.ProviderID,
		}); err != nil {
			return fmt.Errorf("sessionactor: record provider id: %w", err)
		}

		to, err := sandbox.Transition(sandbox.State(sandboxRow.Status), int(sandboxRow.Gen), sandbox.ProviderAckTrigger())
		if err != nil {
			return fmt.Errorf("sessionactor: sandbox transition spawning->connecting: %w", err)
		}
		if _, err := a.stores.sandbox.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
			SessionID: a.sessionID,
			Status:    sqlcgen.SandboxStatus(to),
		}); err != nil {
			return fmt.Errorf("sessionactor: update sandbox status to connecting: %w", err)
		}
		return nil
	})
}

// recordSpawnFailure classifies createErr (ports.IsTransient, §3.2's own
// "unknown provider errors default to transient" contract) and either
// leaves the sandbox in Spawning for a later retry (transient) or
// increments the persisted circuit breaker and transitions Spawning->
// Suspect (permanent) -- reusing transitionSandboxToSuspect (timerfired.go)
// so a permanent spawn failure resolves to Failed via the SAME terminal_
// grace machinery every watchdog timeout already uses (§3.2: "a watchdog
// never writes failed directly").
func (a *Actor) recordSpawnFailure(ctx context.Context, tx pgx.Tx, sandboxRow sqlcgen.Sandbox, createErr error, now time.Time) error {
	if ports.IsTransient(createErr) {
		a.logger.Warn("sessionactor: transient spawn failure; left in spawning for a later retry", "error", createErr)
		return nil
	}

	var code string
	var perr *ports.ProviderError
	if errors.As(createErr, &perr) {
		code = perr.Code
	}
	a.logger.Error("sessionactor: permanent spawn failure; transitioning to suspect pending grace",
		"error", createErr, "provider_error_code", code)

	newCount := sandboxRow.SpawnFailureCount + 1
	if _, err := a.stores.sandbox.WithTx(tx).UpdateCircuitBreaker(ctx, sqlcgen.UpdateSandboxCircuitBreakerParams{
		SessionID:          a.sessionID,
		SpawnFailureCount:  newCount,
		LastSpawnFailureAt: pgtype.Timestamptz{Time: now, Valid: true},
	}); err != nil {
		return fmt.Errorf("sessionactor: record spawn failure: %w", err)
	}

	if err := a.transitionSandboxToSuspect(ctx, tx, sandboxRow, now); err != nil {
		return err
	}
	// connecting_deadline no longer applies -- the sandbox is Suspect now,
	// awaiting terminal_grace's own resolution instead.
	return a.deleteTimer(ctx, tx, TimerConnectingDeadline)
}

// executeResume (Step 23, "resume") performs the actual (possibly slow,
// network-bound) SandboxProvider.ResumeSandbox call OUTSIDE any
// transaction, exactly like executeSpawn/executeRestore's own network
// call -- and, following this fix, the REST of its own shape now
// genuinely matches theirs too, rather than being a special case: by the
// time this function is ever called, plan's own interim Spawning claim
// (planResume, above) has ALREADY committed inside tryPlanSpawn's own
// transact -- exactly like planFreshSpawn/planRestore's own Spawning
// write already commits before executeSpawn/executeRestore ever call out
// to the provider. This is precisely what closes the concurrency bug an
// adversarial review found and empirically reproduced in the OLD shape
// (killing actor A's advisory-lock connection mid-resume-call, hydrating
// actor B for the same session, and getting a SECOND ResumeSandbox call
// while actor A's first call was still blocked): a second actor instance
// for the same session now reads status==Spawning, not Stopped/Stale, and
// EvaluateSpawnDecision's own existing guard (spawndecision.go,
// untouched by this fix) no-ops instead of returning SpawnActionResume a
// second time.
//
// ref.ProviderID is the EXISTING provider object recorded on plan (never
// a new one, unlike executeSpawn/executeRestore's own ref, which comes
// back FROM the provider call) -- ResumeSandbox's own port signature
// returns only an error, so there is no ref to overwrite here, and none
// is read from the return value.
func (a *Actor) executeResume(ctx context.Context, plan *spawnPlan) error {
	ref := ports.SandboxRef{ProviderID: plan.providerObjectID}
	resumeErr := a.provider.ResumeSandbox(ctx, ref)

	err := a.recordResumeOutcome(ctx, plan.gen, resumeErr)

	if err != nil && resumeErr == nil && errors.Is(err, ErrStaleEpoch) {
		// Mirrors executeSpawn/executeRestore's own identical stale-epoch
		// observability log: a real ResumeSandbox call just succeeded, but
		// the write recording its outcome (Connecting status) was rolled
		// back by a legitimate stale-epoch takeover -- the resumed
		// provider instance is real and live, but the control plane's own
		// bookkeeping for it never got recorded, and has no other channel
		// to reach it until Step 25's reconciler exists.
		a.logger.Warn("sessionactor: resumed sandbox's outcome orphaned by stale-epoch takeover; the resume itself succeeded at the provider but was never recorded and will be inconsistent until Step 25's reconciler exists",
			"session_id", a.sessionID.String(),
			"provider_id", plan.providerObjectID,
			"gen", plan.gen,
		)
	}

	return err
}

// recordResumeOutcome is executeResume's own second-transact outcome-
// recording step -- mirrors recordProviderOutcome (executeSpawn/
// executeRestore's own shared equivalent) almost exactly, with exactly
// ONE deliberate difference: it never calls UpdateProviderID. Resume
// reuses the SAME already-recorded provider object (never a new one), so
// there is nothing for that write to record that isn't already there --
// calling it anyway would be a no-op at best and, at worst, a lie about
// what actually happened (recordProviderOutcome's own UpdateProviderID
// call exists specifically to record a NEW object CreateSandbox/
// RestoreFromSnapshot just returned).
//
// On success: applies sandbox.ResumeAckTrigger() (Spawning -> Connecting)
// instead of recordProviderOutcome's sandbox.ProviderAckTrigger() --
// state.go's own doc comment on TriggerResumeAck explains why this is a
// distinct trigger kind, not a reuse of TriggerProviderAck, even though
// both share the exact same (from, to) edge: the transition LOG (§5.3)
// can tell "a spawn/restore's provider call acked" apart from "a resume's
// provider call acked" the same way TriggerForceRespawn is already kept
// distinct from TriggerSpawn despite sharing a target.
//
// On failure: reuses recordSpawnFailure UNCHANGED. This is now correct,
// not merely convenient: a permanent resume failure has, by this fix, had
// the exact same interim Spawning write a permanent spawn/restore failure
// has by the time its own outcome is recorded, so it now behaves
// IDENTICALLY -- same circuit-breaker increment, same
// transitionSandboxToSuspect path (Spawning -> Suspect, pending
// terminal_grace, §3.2's "a watchdog never writes failed directly" rule),
// same connecting_deadline cleanup. There is no longer a genuinely
// distinct resume-failure code path to maintain, so none is kept: the OLD
// recordResumeFailure (which left the row completely untouched on a
// permanent failure, because there was nothing yet to transition out of)
// is gone.
func (a *Actor) recordResumeOutcome(ctx context.Context, gen int, resumeErr error) error {
	return a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := time.Now()

		sandboxRow, err := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("sessionactor: get sandbox: %w", err)
		}

		if int(sandboxRow.Gen) != gen {
			// A newer spawn/restore/resume attempt has already superseded
			// this one -- mirrors recordProviderOutcome's own identical
			// guard.
			a.logger.Warn("sessionactor: resume result for a superseded gen; ignoring",
				"result_gen", gen, "current_gen", sandboxRow.Gen)
			return nil
		}

		if resumeErr != nil {
			return a.recordSpawnFailure(ctx, tx, sandboxRow, resumeErr, now)
		}

		to, err := sandbox.Transition(sandbox.State(sandboxRow.Status), int(sandboxRow.Gen), sandbox.ResumeAckTrigger())
		if err != nil {
			return fmt.Errorf("sessionactor: sandbox transition spawning->connecting (resume ack): %w", err)
		}
		if _, err := a.stores.sandbox.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
			SessionID: a.sessionID,
			Status:    sqlcgen.SandboxStatus(to),
		}); err != nil {
			return fmt.Errorf("sessionactor: update sandbox status to connecting (resume ack): %w", err)
		}
		return nil
	})
}

// tryPlanDispatch implements design decision 3b's own first half, entirely
// inside the caller's own transact: both turn transitions (Pending->
// Dispatched->Processing) and arming turn_deadline commit together. The
// actual SandboxCommander.SendCommand call is deliberately NOT made here
// -- see this file's own top comment for why a real WS frame write must
// never run while this transact's own FOR UPDATE lock on the session row
// is held -- it happens in executeDispatch, OUTSIDE any transaction, once
// this has committed.
func (a *Actor) tryPlanDispatch(
	ctx context.Context, tx pgx.Tx,
	sessionRow sqlcgen.Session, sandboxRow sqlcgen.Sandbox,
	turnID pgtype.UUID, turns []sqlcgen.Turn,
	now time.Time,
) (*dispatchPlan, error) {
	if a.commander == nil {
		// Defensive: mirrors tryPlanSpawn's own nil-provider guard exactly
		// -- some tests, and any future caller genuinely without one, must
		// not panic or half-write state here. A later EnsureDispatched
		// (once a commander IS configured) retries; the turn is left
		// untouched (still Pending).
		a.logger.Warn("sessionactor: dispatch decision would proceed but no SandboxCommander is configured; skipping")
		return nil, nil
	}

	target, ok := findTurnByID(turns, turnID)
	if !ok {
		return nil, fmt.Errorf("sessionactor: dispatch turn: turn %s not found among loaded turns", turnID.String())
	}

	toDispatched, err := turn.Transition(turn.StatePending, turn.TriggerDispatch)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: turn transition pending->dispatched: %w", err)
	}
	if _, err := a.stores.turn.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID:           turnID,
		Status:       sqlcgen.TurnStatus(toDispatched),
		DispatchedAt: pgtype.Timestamptz{Time: now, Valid: true},
	}); err != nil {
		return nil, fmt.Errorf("sessionactor: update turn status to dispatched: %w", err)
	}

	// §3.3: dispatched -> processing happens immediately here too -- this
	// Step's own scope has no separate "the sandbox acknowledged receipt"
	// signal to gate the second transition on (see design decision 3b's
	// own reasoning); "we successfully sent it" is treated as sufficient
	// for both. Note this now commits BEFORE SendCommand is ever attempted
	// (see this file's own top comment) -- if the send subsequently fails,
	// the turn is failed forward from here (executeDispatch/
	// failDispatchedTurn), never rolled back to Pending.
	toProcessing, err := turn.Transition(turn.StateDispatched, turn.TriggerStartProcessing)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: turn transition dispatched->processing: %w", err)
	}
	if _, err := a.stores.turn.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID:     turnID,
		Status: sqlcgen.TurnStatus(toProcessing),
	}); err != nil {
		return nil, fmt.Errorf("sessionactor: update turn status to processing: %w", err)
	}

	if err := a.armTimer(ctx, tx, TimerTurnDeadline, now.Add(a.timeouts.TurnDeadline)); err != nil {
		return nil, err
	}

	payload, err := buildPromptPayload(a.sessionID.String(), sessionRow, sandboxRow, target)
	if err != nil {
		return nil, fmt.Errorf("sessionactor: build prompt payload: %w", err)
	}

	return &dispatchPlan{turnID: turnID, payload: payload}, nil
}

// executeDispatch performs the actual SandboxCommander.SendCommand call
// OUTSIDE any transaction -- design decision 3b's own required sequencing,
// mirroring executeSpawn's own "network call, then (only if needed) a
// second small transact records the outcome" shape. The turn is already
// committed Processing by the time this runs (tryPlanDispatch's own
// transact, already committed) -- on success there is nothing further to
// do; on failure, failDispatchedTurn fails it forward.
func (a *Actor) executeDispatch(ctx context.Context, plan *dispatchPlan) error {
	if err := a.commander.SendCommand(a.sessionID.String(), plan.payload); err != nil {
		// Covers ports.ErrNoLiveSandboxConnection (the prompt genuinely
		// never reached a live connection) and every other send failure
		// identically.
		return a.failDispatchedTurn(ctx, plan.turnID, err)
	}
	return nil
}

// failDispatchedTurn transitions turnID -- already committed Processing by
// tryPlanDispatch's own transact -- to Failed, in its own small, fresh
// transact (mirroring executeSpawn's own "network call already happened
// outside any transaction; a separate, small transact now records its
// outcome" shape exactly). domain/turn's transition table has no reverse
// edge from Processing back to Pending, and none is added here
// (internal/domain/turn/state.go is explicitly off-limits this Step), so
// the only legal move is forward.
//
// This reuses the EXACT SAME domain/turn call and "append a synthetic
// execution_complete event" logic handleTurnDeadlineTimer (timerfired.go)
// already uses for its own turn_deadline expiry: turn.Transition(
// turn.StateProcessing, turn.TriggerTimeout), gated by
// turn.RequiresSyntheticExecutionComplete, then turn.DeriveFailureReason.
// TriggerTimeout, specifically, is the only one of domain/turn's two
// Processing->Failed forward edges that fits here: the OTHER one,
// TriggerFail, is documented (synthetic.go) to mean "a REAL terminal event
// reporting failure already arrived from the agent" and
// RequiresSyntheticExecutionComplete(TriggerFail) is false BECAUSE of that
// -- using it here would silently skip the synthetic execution_complete
// event entirely, since no real terminal event ever arrives for a prompt
// that never reached the sandbox at all, breaking §3.3's "clients always
// see one terminal event per turn" contract. TriggerTimeout is, like this
// failure, a pure control-plane-internal decision with no real wire event
// behind it, so it is the correct existing edge to reuse -- the resulting
// session-level FailureReason reads "timeout" even though the proximate
// cause here is a send failure, not a deadline expiry, which is an honest,
// documented consequence of domain/turn's existing trigger vocabulary
// (state.go off-limits this Step), not a new gap invented by this fix.
// sendErr's own real message is preserved, honestly, in the synthetic
// event's own "reason" field and the log line below.
func (a *Actor) failDispatchedTurn(ctx context.Context, turnID pgtype.UUID, sendErr error) error {
	reason := fmt.Sprintf("failed to deliver prompt to sandbox: %v", sendErr)
	a.logger.Error("sessionactor: dispatch turn: send prompt command failed; failing turn",
		"turn_id", turnID.String(), "error", sendErr)

	return a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		turns, err := a.stores.turn.WithTx(tx).ListForSession(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("sessionactor: list turns: %w", err)
		}

		target, ok := findTurnByID(turns, turnID)
		if !ok {
			// Gone via some other path entirely (e.g. session teardown)
			// by the time this runs -- nothing further to do.
			a.logger.Warn("sessionactor: fail dispatched turn: turn no longer found; ignoring", "turn_id", turnID.String())
			return nil
		}
		if turn.State(target.Status) != turn.StateProcessing {
			// Already resolved via some other path -- do not re-fail an
			// already-terminal (or otherwise no-longer-Processing) turn.
			a.logger.Warn("sessionactor: fail dispatched turn: turn no longer processing; ignoring",
				"turn_id", turnID.String(), "status", target.Status)
			return nil
		}

		to, err := turn.Transition(turn.StateProcessing, turn.TriggerTimeout)
		if err != nil {
			return fmt.Errorf("sessionactor: turn transition processing->failed (dispatch failure): %w", err)
		}

		now := time.Now()
		if _, err := a.stores.turn.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
			ID:          turnID,
			Status:      sqlcgen.TurnStatus(to),
			CompletedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}); err != nil {
			return fmt.Errorf("sessionactor: update turn status: %w", err)
		}

		if turn.RequiresSyntheticExecutionComplete(turn.TriggerTimeout) {
			if err := a.appendEvent(ctx, tx, "execution_complete", map[string]any{
				"turn_id":   turnID.String(),
				"synthetic": true,
				"reason":    reason,
			}); err != nil {
				return err
			}
		}

		failureReason, _ := turn.DeriveFailureReason(turn.StateProcessing, turn.TriggerTimeout)
		if err := a.persistDerivedSessionStatus(ctx, tx, summariesWithOverride(turns, turnID, to, failureReason)); err != nil {
			return err
		}
		return a.deleteTimer(ctx, tx, TimerTurnDeadline)
	})
}

// buildPromptPayload marshals a real, schema-valid sandboxws.Prompt for
// turnID (§3.3: "the turn records the OpenCode conversation id at turn
// start... so follow-up prompts on a fresh sandbox resume the same
// conversation").
func buildPromptPayload(sessionID string, sessionRow sqlcgen.Session, sandboxRow sqlcgen.Sandbox, target sqlcgen.Turn) (json.RawMessage, error) {
	prompt := sandboxws.Prompt{
		Type:      "prompt",
		MessageId: uuid.NewString(),
		SessionId: sessionID,
		Gen:       int(sandboxRow.Gen),
		// nil (first turn) or the session's own previously-recorded
		// conversation id (§3.3) -- sessions.opencode_conversation_id is
		// nil until the first heartbeat ever reports one.
		ConversationId: sessionRow.OpencodeConversationID,
		Text:           stringOrEmpty(target.Prompt),
		Model:          target.ModelID,
		Effort:         nil,
		ScmName:        scmCommitName,
		ScmEmail:       scmCommitEmail,
		PlanMode:       target.PlanMode,
	}
	return json.Marshal(prompt)
}

// toQueueEntries adapts stored turn rows into the generic
// []turn.QueueEntry[pgtype.UUID] shape HasInFlightTurn/NextToDispatch
// need (domain/turn has zero external dependencies, so it cannot know
// about pgtype.UUID or sqlcgen.Turn itself).
func toQueueEntries(turns []sqlcgen.Turn) []turn.QueueEntry[pgtype.UUID] {
	out := make([]turn.QueueEntry[pgtype.UUID], len(turns))
	for i, t := range turns {
		out[i] = turn.QueueEntry[pgtype.UUID]{ID: t.ID, Status: turn.State(t.Status)}
	}
	return out
}

// findTurnByID returns the turn in turns whose ID matches id, if any.
func findTurnByID(turns []sqlcgen.Turn, id pgtype.UUID) (sqlcgen.Turn, bool) {
	for _, t := range turns {
		if t.ID == id {
			return t, true
		}
	}
	return sqlcgen.Turn{}, false
}

// stringOrEmpty dereferences s, or returns "" if s is nil.
func stringOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
