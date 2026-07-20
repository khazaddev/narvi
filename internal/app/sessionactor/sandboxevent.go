// This file (sandboxevent.go) implements handling a SandboxEvent command
// (command.go) -- the sandbox-WS-hub half of Step 18 (§3.2 liveness, §6.1
// ack receipt / event persistence, §9.3 scenario #6 stale-gen rejection).
// internal/adapters/inbound/wshub (this same Step) is the ONLY caller: its
// read loop delivers one SandboxEvent per inbound wire frame, once that
// connection's own handshake-time gen has already been validated (§6.1's
// "403 on id/gen mismatch") -- this handler enforces the SEPARATE
// per-message half of §3.2's gen-fencing rule ("stale-gen inputs are
// rejected and logged"), persists every recognized event (append-only),
// always bumps liveness (last_seen_at = max of all signals), and fires
// EXACTLY the two state transitions this Step's plan row scopes.
//
// # Explicitly out of scope (do not add a third case here)
//
// Suspect-state recovery-during-grace ("any liveness signal during grace
// returns to previous state", §3.2) is Step 24's own job ("two-phase
// terminalization"). A Suspect sandbox reconnecting through this handler is
// correctly ALLOWED (domain/sandbox.IsDeadSandboxStatus(Suspect) is false,
// so wshub's own handshake let the connection through) and its events DO
// get persisted and DO bump last_seen_at -- both genuinely useful and
// correct per this Step's own scope -- but no recovery TRANSITION fires:
// sandbox.TriggerRecover needs a Target (which previously-live state to
// return to) this handler has no mechanism to track/supply. Do not call
// sandbox.Transition speculatively for any event type/status combination
// beyond the two named below.

package sessionactor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
)

// ackIDPeek peeks just the "ackId" field a critical wire event carries
// (contracts/gen/go/sandboxws's own per-type doc comments: present and
// non-empty ONLY on the 6 critical types, by construction). This package
// decodes it directly from cmd.Raw rather than importing wshub's own
// envelope type (adapters/inbound depending on app/sessionactor is the only
// legal direction, never the reverse) or hardcoding a critical-type-name
// list -- exactly mirroring how wsbridge's own SendCritical(msg any, ackID
// string) is type-agnostic (see ports.ClassifyAgentEvent's doc comment for
// why that existing OUTBOUND-direction helper does not apply to this
// INBOUND-direction, raw-JSON-bytes input).
type ackIDPeek struct {
	AckID string `json:"ackId"`
}

// peekAckID extracts raw's own "ackId" field, or "" if raw is not a JSON
// object, has no such field, or the field is absent -- cmd.Raw is always
// well-formed JSON by construction (wshub already successfully unmarshaled
// it once to peek Type/Gen/LastBootPhase before ever constructing a
// SandboxEvent), so a decode failure here is defensive, not expected.
func peekAckID(raw json.RawMessage) string {
	var peek ackIDPeek
	if err := json.Unmarshal(raw, &peek); err != nil {
		return ""
	}
	return peek.AckID
}

// sandboxTransitionTrigger reports which Trigger (if any) applies for the
// given (event type, LastBootPhase, current status) combination -- the two
// (and only two) mappings this Step implements:
//
//	(a) "ready" arriving while status is Connecting -> WSConnectedTrigger
//	    (Connecting -> Booting).
//	(b) "heartbeat" with a nil LastBootPhase arriving while status is
//	    Booting -> BootCompleteTrigger (Booting -> Ready), grounded
//	    directly in Heartbeat.LastBootPhase's own doc comment: "Null once
//	    boot has completed (no more boot phases to report)".
//
// ok is false for every other combination -- including "ready" outside
// Connecting, or "heartbeat"/nil-LastBootPhase outside Booting -- which is
// NOT an error (these two events can legitimately arrive outside their
// exact expected phase: reconnects, replays). Callers must fall through to
// a liveness-only bump in that case, never attempt a speculative
// sandbox.Transition call for a combination this function does not name.
func sandboxTransitionTrigger(eventType string, lastBootPhase *string, status sandbox.State) (sandbox.Trigger, bool) {
	switch {
	case eventType == "ready" && status == sandbox.StateConnecting:
		return sandbox.WSConnectedTrigger(), true
	case eventType == "heartbeat" && lastBootPhase == nil && status == sandbox.StateBooting:
		return sandbox.BootCompleteTrigger(), true
	default:
		return sandbox.Trigger{}, false
	}
}

// handleSandboxEvent processes one inbound sandbox-WS event delivered by
// wshub's read loop. Whatever a.transact's own closure decides, the
// resulting outcome is ALWAYS sent on cmd.Reply (via a non-blocking
// select-with-default -- the buffered channel wshub constructs makes this
// always succeed immediately) the INSTANT that transact returns -- before
// handleEnsureDispatched or either of this event's own best-effort push/PR
// side effects ever run (see the reply's own inline comment below for
// why: those side effects can each individually exceed
// platform.Timeouts.SandboxEventAckTimeout on real network latency, and
// must never be able to delay a critical event's own ack past its 5s
// window) -- and this function then returns transact's own error upward
// unchanged, so run()'s existing ErrStaleEpoch-is-fatal /
// other-errors-are-logged-not-fatal behavior (actor.go) keeps working
// exactly as it does today.
func (a *Actor) handleSandboxEvent(ctx context.Context, cmd SandboxEvent) error {
	var outcome SandboxEventOutcome
	// pushAfterCommit is non-nil only when THIS event just completed a
	// turn successfully (Step 21, "e2e happy path", pushpr.go) -- acted on
	// (a real SandboxCommander.SendCommand call) only AFTER this
	// function's own transact below has committed, never inside it (see
	// pushpr.go's own top comment for why).
	var pushAfterCommit *pushSignal

	err := a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := time.Now()

		row, err := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("sessionactor: get sandbox: %w", err)
		}

		// Per-message gen-fencing (§3.2: "stale-gen inputs are rejected and
		// logged"; §9.3 scenario #6: "Stale sandbox from old gen reconnects
		// -> rejected 403, logged, session unaffected" -- the connection-
		// level half of that scenario is wshub's own handshake check; THIS
		// is the per-message half commands.schema.json's own doc comment
		// requires in addition to it). A stale-gen message here is an
		// EXPECTED occurrence -- an old-gen sandbox reconnecting/replaying
		// after a respawn -- never a failure: skip persisting it entirely,
		// never touch last_seen_at, never ack. outcome stays the zero value
		// (Persisted: false, AckID: "").
		if cmd.Gen != int(row.Gen) {
			a.logger.Warn("sessionactor: ignoring stale-gen sandbox event",
				"event_type", cmd.Type, "event_gen", cmd.Gen, "sandbox_gen", row.Gen)
			return nil
		}

		// Persist ALWAYS, for every recognized event type -- this is the
		// append-only per-session event log Step 19's client hub will
		// replay from, not limited to the 6 critical types.
		if err := a.appendRawEvent(ctx, tx, cmd.Type, cmd.MessageID, cmd.Raw); err != nil {
			return err
		}

		target := row.Status
		if trig, ok := sandboxTransitionTrigger(cmd.Type, cmd.LastBootPhase, sandbox.State(row.Status)); ok {
			to, err := sandbox.Transition(sandbox.State(row.Status), int(row.Gen), trig)
			if err != nil {
				return fmt.Errorf("sessionactor: sandbox transition via %s: %w", trig.Kind, err)
			}
			target = sqlcgen.SandboxStatus(to)
		}
		// If !ok: NOT an error (see sandboxTransitionTrigger's own doc) --
		// target stays row.Status unchanged, falling straight through to
		// the liveness-only bump below. This is also the path a Suspect
		// sandbox's own reconnect/replay traffic takes: persisted, liveness
		// bumped, no recovery transition attempted (Step 24's own job, see
		// this file's top comment).

		if _, err := a.stores.sandbox.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
			SessionID:  a.sessionID,
			Status:     target,
			LastSeenAt: pgtype.Timestamptz{Time: now, Valid: true},
		}); err != nil {
			return fmt.Errorf("sessionactor: update sandbox status/liveness: %w", err)
		}

		// First-time Booting->Ready transition, this event: arm BOTH
		// liveness_check and inactivity exactly once, here, at the real
		// moment the sandbox becomes Ready -- closing the confirmed gap
		// where neither timer was ever armed for the first time by any
		// production code path (they only ever re-arm themselves once
		// already firing; see handleLivenessCheckTimer/
		// handleInactivityTimer, timerfired.go). The guard is
		// before-vs-after on THIS event's own transition, not a bare
		// "is target Ready" check: on every later heartbeat while already
		// Ready, row.Status and target are both already Ready, so this is
		// false and neither timer is touched -- re-arming liveness_check on
		// every 30s heartbeat would keep pushing its own fires_at forward
		// and it would never get a real chance to fire. The exact same
		// constants handleLivenessCheckTimer/handleInactivityTimer already
		// use for their own self-re-arm are used here, so this initial arm
		// looks identical in shape to every subsequent one -- matching
		// TimerConnectingDeadline's own exactly-once-at-spawn precedent
		// (dispatch.go's tryPlanSpawn). Once armed, handleConnectingDeadlineTimer's
		// own already-correct delete-on-non-connecting-phase logic
		// (timerfired.go) hands off cleanly the next time that stale timer
		// fires: liveness_check is already live and watching by then.
		if sandbox.State(row.Status) != sandbox.State(target) && sandbox.State(target) == sandbox.StateReady {
			if err := a.armTimer(ctx, tx, TimerLivenessCheck, now.Add(a.timeouts.SteadyHeartbeatBudget)); err != nil {
				return err
			}
			if err := a.armTimer(ctx, tx, TimerInactivity, now.Add(a.timeouts.InactivityMinCheckInterval)); err != nil {
				return err
			}
		}

		// §3.3: "reported on every heartbeat" -- a heartbeat is a
		// sandbox-level liveness signal, not a turn-scoped one (it carries
		// no turn id), so this is a session-level write, independent of
		// whatever transition (if any) target computed above (Step 21,
		// "e2e happy path", design decision 6).
		if cmd.Type == "heartbeat" && cmd.ConversationID != nil {
			if _, err := a.stores.session.WithTx(tx).UpdateConversationID(ctx, sqlcgen.UpdateSessionConversationIDParams{
				ID:                     a.sessionID,
				OpencodeConversationID: cmd.ConversationID,
			}); err != nil {
				return fmt.Errorf("sessionactor: update session conversation id: %w", err)
			}
		}

		// Step 21 ("e2e happy path"): a REAL execution_complete completes
		// whichever turn is currently Processing (§3.3) -- see
		// completeProcessingTurn's own doc comment (pushpr.go) for the full
		// reasoning, including why no synthetic execution_complete is ever
		// appended on this path.
		if cmd.Type == "execution_complete" {
			sig, err := a.completeProcessingTurn(ctx, tx, row, cmd.Raw)
			if err != nil {
				return err
			}
			pushAfterCommit = sig
		}

		outcome.Persisted = true
		outcome.AckID = peekAckID(cmd.Raw)
		return nil
	})

	// Reply IMMEDIATELY once transact has committed (or deliberately
	// skipped persisting a stale-gen event) -- BEFORE any of the
	// best-effort post-commit side effects below ever run. This ordering
	// is deliberate and load-bearing: wshub's own readLoop
	// (internal/adapters/inbound/wshub/dispatch.go) is racing
	// platform.Timeouts.SandboxEventAckTimeout (5s) waiting on this exact
	// reply to write the ack back to the sandbox, and the side effects
	// below -- a full spawn/dispatch re-evaluation, a real git push, a
	// real GitHub API call -- can each individually take longer than
	// that budget on real network latency. Sending the reply here, before
	// any of them run, means a slow/failing side effect can never cause a
	// critical event's own ack to miss its window. The non-blocking
	// select-with-default is safe regardless: the buffered channel wshub
	// constructs makes this send always succeed immediately.
	select {
	case cmd.Reply <- outcome:
	default:
	}

	// Design decision 3: unconditionally re-evaluate spawn/dispatch state
	// right after this event's own transact commits successfully -- e.g.
	// a "ready"/heartbeat-driven transition to Booting/Ready is
	// immediately followed by a fresh dispatch evaluation, in case a
	// pending turn is now dispatchable. Calls the SAME handler function
	// EnsureDispatched itself invokes, directly (not via a.Send), since
	// this already runs on the actor's own single command-processing
	// goroutine -- see command.go's own EnsureDispatched doc comment.
	// Deliberately does NOT alter this function's own return value: a
	// failure here is logged, never treated as a failure of the sandbox
	// event itself (which already committed successfully).
	if err == nil {
		if dispatchErr := a.handleEnsureDispatched(ctx); dispatchErr != nil {
			a.logger.Warn("sessionactor: ensure-dispatched after sandbox event failed", "error", dispatchErr)
		}

		// Step 21 ("e2e happy path"): the two remaining best-effort side
		// effects this event may trigger, both deliberately run OUTSIDE
		// (i.e. after) the transact above committed, never inside it --
		// see pushpr.go's own top comment for why. Neither failure alters
		// this function's own return value: cmd.Type's own event already
		// committed successfully regardless of what either side effect
		// does next.
		if pushAfterCommit != nil {
			a.sendPushBestEffort(a.sessionID.String(), pushAfterCommit)
		}
		if cmd.Type == "push_complete" {
			a.createPRBestEffort(ctx, cmd.Raw)
		}
	}

	return err
}
