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
// the state transitions this Step's plan row (and Step 22's, "snapshots &
// restore") scope: "ready"/Connecting, "heartbeat"-nil-phase/Booting
// (both Step 18), "snapshot_ready"/Snapshotting (Step 22, design decision
// 3 -- see handleSnapshotReadyEvent below), and now Suspect-recovery
// (Step 24, "two-phase terminalization" -- see the section right below).
//
// # Suspect-state recovery-during-grace (Step 24, "two-phase terminalization")
//
// §3.2: "Any liveness signal during grace returns to previous state." A
// Suspect sandbox reconnecting through this handler is correctly ALLOWED
// (domain/sandbox.IsDeadSandboxStatus(Suspect) is false, so wshub's own
// handshake let the connection through) -- this is exactly the moment
// that rule fires. Right after this event's own raw persistence
// (appendRawEvent, below) and BEFORE computing whatever further
// transition cmd.Type itself drives, handleSandboxEvent checks: is
// row.Status genuinely Suspect, AND does it still carry a
// pre_suspect_status (set by transitionSandboxToSuspect, timerfired.go,
// in the SAME write that entered Suspect -- migrations/000023_sandbox_
// pre_suspect_status.up.sql)? If so, ANY recognized inbound event counts
// as the liveness signal the rule names -- deliberately NOT narrowed to
// specific event types, since the rule's own wording is unconditional --
// and sandbox.Transition(StateSuspect, gen, sandbox.RecoverTrigger(
// preSuspectStatus)) is attempted. On success: the recovered status is
// written, pre_suspect_status is cleared back to NULL, and
// TimerTerminalGrace is deleted, all in one statement
// (RecoverSandboxFromSuspect, queries/sandboxes.sql, plus deleteTimer) --
// that grace window no longer applies once the sandbox is provably alive
// again. row itself is reassigned to the freshly-read recovered row, so
// every line below (the sandboxTransitionTrigger check, the
// snapshot_ready/execution_complete switch) sees the NOW-RECOVERED
// status, never the stale "suspect" one.
//
// This is deliberately NOT an early return: the SAME event that just
// recovered the sandbox is allowed to ALSO drive a further transition or
// turn-completion in the same pass -- e.g. a genuinely late
// execution_complete arriving while Suspect recovers the sandbox AND
// completes whichever turn is Processing, in ONE commit (§9.3 scenario
// #4: "execution_complete arrives AFTER terminalization -> state
// reconciled"; §3.2's own "a genuinely late success... reconciles: turn
// marked completed, session status re-derived, automation run counters
// corrected"). The turn/session half of that sentence needs no new code
// of its own: turn.EvaluateTurnDeadline's own turn_deadline budget vastly
// exceeds the sandbox's Suspect-grace window, so the turn is still
// Processing when this fires, and completeProcessingTurn (pushpr.go)
// already drives Processing->Completed via the already-legal edge once
// this recovery makes the sandbox live again -- see internal/domain/turn/
// state.go's own top comment for why that package needed no change to
// make this true. The THIRD clause -- "automation run counters
// corrected" -- is honestly NOT addressed by this Step: automations are
// not a built feature anywhere in this codebase yet (IMPLEMENTATION_PLAN.md
// row 31+, Phase 3) -- there is no automation_runs table, no automation
// domain package, nothing to correct. This is a genuine, currently-
// inapplicable gap in this Step's own delivery of §3.2's full sentence,
// not a silent omission: whichever future Step first builds automation
// run tracking will need its own equivalent late-success correction, the
// same way this Step's own turn/session correction was already free
// once Suspect-recovery existed.
//
// A Suspect row with no pre_suspect_status set is a defensive,
// practically-unreachable case (transitionSandboxToSuspect always sets it
// in the SAME write that enters Suspect) -- handled as a safe no-op,
// falling through to the pre-Step-24 behavior (persisted, liveness
// bumped, left Suspect, no recovery transition attempted). Likewise, a
// pre_suspect_status naming an illegal recovery target (should never
// happen: it is only ever written from a state TriggerSuspect's own
// transition table already validated) is logged and left Suspect, never
// treated as a fatal error for this event. Do not call sandbox.Transition
// speculatively for any OTHER event type/status combination beyond the
// ones named here and in sandboxTransitionTrigger's own doc comment
// below.
//
// Honest gap, out of this Step's own scope: a sandbox that recovers
// DIRECTLY back to Ready (pre_suspect_status was Ready) does not itself
// re-arm liveness_check/inactivity in this same pass -- both were
// deleted by their own respective timeout handlers (timerfired.go) on the
// way INTO Suspect, and the guard below that re-arms them only fires on
// a genuine before/after Booting->Ready transition within THIS event,
// which a direct-to-Ready recovery never produces. The sandbox stays
// fully live and functional either way (last_seen_at keeps advancing on
// every subsequent event) -- it simply has no ACTIVE watchdog re-armed
// until some later event happens to cross a boundary the existing guard
// already recognizes, or a future Step adds explicit re-arming for every
// recovery-landing state. Not fixed here: this Step's own brief scopes
// the recovery write itself, not a broader watchdog-rearming pass.

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
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

		// Step 24 ("two-phase terminalization"): Suspect-recovery-during-
		// grace -- see this file's own top comment for the full reasoning.
		// row is reassigned to the freshly-recovered row on success, so
		// every line below sees the NOW-RECOVERED status rather than the
		// stale "suspect" one.
		if sandbox.State(row.Status) == sandbox.StateSuspect && row.PreSuspectStatus != nil {
			recoveredTo, recErr := sandbox.Transition(sandbox.StateSuspect, int(row.Gen),
				sandbox.RecoverTrigger(sandbox.State(*row.PreSuspectStatus)))
			if recErr != nil {
				// Defensive only -- see this file's own top comment for why
				// this should be unreachable in practice. Logged, not
				// fatal: the sandbox is simply left Suspect for this event.
				a.logger.Warn("sessionactor: suspect recovery rejected; leaving sandbox suspect",
					"pre_suspect_status", *row.PreSuspectStatus, "error", recErr)
			} else {
				recovered, err := a.stores.sandbox.WithTx(tx).RecoverFromSuspect(ctx, sqlcgen.RecoverSandboxFromSuspectParams{
					SessionID:  a.sessionID,
					Status:     sqlcgen.SandboxStatus(recoveredTo),
					LastSeenAt: pgtype.Timestamptz{Time: now, Valid: true},
				})
				if err != nil {
					return fmt.Errorf("sessionactor: recover sandbox from suspect: %w", err)
				}
				row = recovered
				if err := a.deleteTimer(ctx, tx, TimerTerminalGrace); err != nil {
					return err
				}
			}
		}
		// If row.Status has no pre_suspect_status set (defensive-only, see
		// this file's own top comment): nothing above ran, row is
		// untouched, and this falls straight through to the liveness-only
		// bump below exactly like every other unrecognized-transition
		// event does.

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
		// the liveness-only bump below. This is also the path a sandbox
		// that just recovered above (or failed to, and is still Suspect)
		// takes when this SAME event names no further transition of its
		// own: still persisted, liveness bumped.

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

		// Step 21 ("e2e happy path")/Step 22 ("snapshots & restore"): per-
		// type post-persist handling. A tagged switch, not an if/else-if
		// chain (staticcheck QF1003), since this is a genuine dispatch on
		// cmd.Type's own value, not a chain of unrelated conditions.
		switch cmd.Type {
		case "execution_complete":
			// A REAL execution_complete completes whichever turn is
			// currently Processing (§3.3) -- see completeProcessingTurn's
			// own doc comment (pushpr.go) for the full reasoning, including
			// why no synthetic execution_complete is ever appended on this
			// path.
			sig, err := a.completeProcessingTurn(ctx, tx, row, cmd.Raw)
			if err != nil {
				return err
			}
			pushAfterCommit = sig
		case "snapshot_ready":
			// Step 22, design decision 3: a real snapshot_ready event
			// finalizes the Snapshotting->Ready transition
			// triggerSnapshotBestEffort (below) started, and persists the
			// sandbox's own confirmed snapshotId. Runs INSIDE this SAME
			// transact, right after appendRawEvent already persisted the
			// raw event above -- matching execution_complete's own "persist
			// first, then act" ordering exactly.
			if err := a.handleSnapshotReadyEvent(ctx, tx, row, cmd.Raw); err != nil {
				return err
			}
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

		// Step 22 ("snapshots & restore"), design decision 1 -- CORRECTED
		// per independent review: §3.3's own governing rule is "On
		// terminal event: complete turn, trigger snapshot, re-derive
		// session status, dispatch next pending" -- i.e. the snapshot
		// trigger is scoped to a real turn-terminal wire event, not to
		// every sandbox-WS frame. The CALL is gated on cmd.Type ==
		// "execution_complete" here, mirroring exactly how
		// sendPushBestEffort/createPRBestEffort are gated a few lines
		// below (pushAfterCommit != nil / cmd.Type == "push_complete").
		// The brief's original text ("do not gate the CALL itself on the
		// event type... internally a no-op when not applicable") was
		// wrong: triggerSnapshotBestEffort's own internal eligibility
		// check is only "status == Ready", which heartbeat (sent every
		// steady_heartbeat_budget's own 30s interval, §6.1, even on a
		// completely idle sandbox) also satisfies -- gating on Ready-
		// status alone let every routine heartbeat on an already-Ready
		// session re-trigger a full snapshot cycle indefinitely, calling
		// the real SandboxProvider.TakeSnapshot roughly every 30s forever
		// with zero turn activity. Gating on cmd.Type == "execution_complete"
		// fires this exactly once per real terminal event (completed,
		// failed, or cancelled outcome alike -- §3.3 does not restrict
		// "trigger snapshot" to successful completions only, unlike the
		// push signal below which IS success-only) and is naturally
		// idempotent against a redelivered/duplicate execution_complete:
		// the first delivery already moved the sandbox off Ready, so
		// triggerSnapshotBestEffort's own internal check no-ops on the
		// redelivery.
		if cmd.Type == "execution_complete" {
			a.triggerSnapshotBestEffort(ctx)
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

// handleSnapshotReadyEvent implements design decision 3's own
// snapshot_ready handling (Step 22, "snapshots & restore"): transitions
// Snapshotting -> Ready via sandbox.SnapshotCompleteTrigger() and persists
// the sandbox's own reported snapshotId onto sandboxes.snapshot_id.
// Called from INSIDE handleSandboxEvent's own transact, the SAME transact
// that already ran appendRawEvent for this wire event moments ago (row is
// the sandbox row as read at the very top of that transact, before this
// event's own generic status/liveness bump ran -- i.e. its Status field
// still reflects whatever the sandbox's status genuinely was when this
// event arrived).
//
// A late/duplicate snapshot_ready arriving after the sandbox is no longer
// Snapshotting (e.g. a liveness watchdog already suspected it in the
// meantime, or this is a wire-level redelivery of an already-handled
// critical event) is logged and treated as a no-op, never a transact
// failure -- matching this codebase's general "a stale or duplicate
// signal is logged, not fatal" posture (this same function's own
// stale-gen handling above; completeProcessingTurn's identical "no turn
// currently Processing" no-op in pushpr.go). This does NOT touch
// sandbox.gen -- a snapshot cycle happens within the SAME gen; only
// RestoreFromSnapshot bumps gen (design decision 6, dispatch.go).
//
// Fix (message-id correlation): neither TriggerSnapshotStart nor
// TriggerSnapshotComplete is gen-fenced (by design -- gen doesn't change
// within a snapshot cycle), so the "is the sandbox's CURRENT status
// Snapshotting" check alone is satisfiable by a LATER, unrelated snapshot
// attempt at the same gen -- an independent review constructed and
// confirmed this exact race against a real Postgres instance: attempt
// #1's SendCommand appears to fail (but the frame actually got through) ->
// revertSnapshotBestEffort reverts to Ready -> attempt #2 starts -> attempt
// #1's real, delayed snapshot_ready arrives and would otherwise be wrongly
// accepted as completing attempt #2, stamping the STALE snapshotId. So an
// event is only accepted as completing the CURRENT attempt when its own
// CommandMessageId matches row.PendingSnapshotMessageID (set by
// triggerSnapshotBestEffort's own first transact, atomically with the
// Ready->Snapshotting transition) -- a mismatch, or that column being nil
// (no attempt outstanding), is treated exactly like the late/duplicate
// case just above: logged, no-op, never a transact failure.
func (a *Actor) handleSnapshotReadyEvent(ctx context.Context, tx pgx.Tx, row sqlcgen.Sandbox, raw json.RawMessage) error {
	if sandbox.State(row.Status) != sandbox.StateSnapshotting {
		a.logger.Warn("sessionactor: snapshot_ready arrived while sandbox is not snapshotting; ignoring (late or duplicate delivery)",
			"status", row.Status)
		return nil
	}

	var evt sandboxws.SnapshotReady
	if err := json.Unmarshal(raw, &evt); err != nil {
		// Fix (was: log-only, leaving the sandbox permanently stuck
		// Snapshotting -- no watchdog covers that state, confirmed by both
		// the implementer and two independent reviewers): "we can't
		// understand this response" must be treated the same as "we don't
		// have a usable response," so revert exactly like
		// revertSnapshotBestEffort's own SendCommand-failure path does
		// (including clearing pending_snapshot_message_id) -- using the
		// SAME already-open tx this function was handed (not a second,
		// separate a.transact call: this function is already running
		// inside handleSandboxEvent's own transact, which already
		// persisted this raw event verbatim via appendRawEvent moments
		// ago; a decode failure here must never retroactively roll that
		// back, and reverting via a second, independent transaction here
		// would race the still-open outer one instead of composing with
		// it).
		a.logger.Warn("sessionactor: snapshot_ready failed schema decode; persisted verbatim, reverting sandbox to ready instead of leaving it stuck snapshotting",
			"error", err)
		return a.revertSnapshotToReady(ctx, tx, row)
	}

	pending := "<nil>"
	if row.PendingSnapshotMessageID != nil {
		pending = *row.PendingSnapshotMessageID
	}
	if row.PendingSnapshotMessageID == nil || evt.CommandMessageId == nil || *evt.CommandMessageId != *row.PendingSnapshotMessageID {
		a.logger.Warn("sessionactor: snapshot_ready commandMessageId does not match the sandbox's own currently-outstanding pending snapshot attempt; ignoring (stale/duplicate delivery from an earlier, already-completed-or-reverted attempt)",
			"pending_snapshot_message_id", pending)
		return nil
	}

	to, err := sandbox.Transition(sandbox.State(row.Status), int(row.Gen), sandbox.SnapshotCompleteTrigger())
	if err != nil {
		return fmt.Errorf("sessionactor: sandbox transition snapshotting->ready (snapshot_ready): %w", err)
	}
	if _, err := a.stores.sandbox.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: a.sessionID,
		Status:    sqlcgen.SandboxStatus(to),
	}); err != nil {
		return fmt.Errorf("sessionactor: update sandbox status to ready (snapshot complete): %w", err)
	}
	// Also clears pending_snapshot_message_id back to nil in this SAME
	// statement -- see UpdateSandboxSnapshotID's own generated doc
	// comment (queries/sandboxes.sql).
	if _, err := a.stores.sandbox.WithTx(tx).UpdateSnapshotID(ctx, sqlcgen.UpdateSandboxSnapshotIDParams{
		SessionID:  a.sessionID,
		SnapshotID: &evt.SnapshotId,
	}); err != nil {
		return fmt.Errorf("sessionactor: record snapshot id: %w", err)
	}
	return nil
}

// revertSnapshotToReady performs the Snapshotting->Ready compensating
// transition and clears pending_snapshot_message_id back to nil, using
// the CALLER's own tx (never opening its own transact) -- shared by
// revertSnapshotBestEffort (below, which opens its own fresh transact and
// passes it here) and handleSnapshotReadyEvent's decode-failure path
// (above, which passes the SAME tx it was itself handed, already open
// inside handleSandboxEvent's own transact). row is read (and, in the
// decode-failure caller's case, already known to be Snapshotting) by the
// caller; this function re-confirms that itself so it is safe to call
// from either context without a caller-side duplicate check.
func (a *Actor) revertSnapshotToReady(ctx context.Context, tx pgx.Tx, row sqlcgen.Sandbox) error {
	if sandbox.State(row.Status) != sandbox.StateSnapshotting {
		// Already moved on via some other path (e.g. a liveness watchdog
		// already suspected it in the meantime) -- nothing to revert.
		a.logger.Warn("sessionactor: revert snapshot to ready: sandbox no longer snapshotting; ignoring",
			"status", row.Status)
		return nil
	}
	to, err := sandbox.Transition(sandbox.State(row.Status), int(row.Gen), sandbox.SnapshotCompleteTrigger())
	if err != nil {
		return fmt.Errorf("sessionactor: sandbox transition snapshotting->ready (revert): %w", err)
	}
	if _, err := a.stores.sandbox.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: a.sessionID,
		Status:    sqlcgen.SandboxStatus(to),
	}); err != nil {
		return fmt.Errorf("sessionactor: update sandbox status to ready (revert): %w", err)
	}
	if _, err := a.stores.sandbox.WithTx(tx).UpdatePendingSnapshotMessageID(ctx, sqlcgen.UpdateSandboxPendingSnapshotMessageIDParams{
		SessionID:                a.sessionID,
		PendingSnapshotMessageID: nil,
	}); err != nil {
		return fmt.Errorf("sessionactor: clear pending snapshot message id (revert): %w", err)
	}
	return nil
}

// snapshotPlan is what triggerSnapshotBestEffort's own first transact
// hands to itself when (and only when) it actually transitioned the
// sandbox Ready->Snapshotting -- the real SandboxCommander.SendCommand
// call happens OUTSIDE that transact, using exactly this plan, mirroring
// dispatch.go's own spawnPlan/dispatchPlan "commit state, THEN make the
// real call" shape.
type snapshotPlan struct {
	gen              int
	commandMessageID string
}

// triggerSnapshotBestEffort implements design decision 1's own post-turn
// snapshot trigger (Step 22, "snapshots & restore", docs/IMPLEMENTATION_
// PLAN.md row 22's own "post-turn snapshot" bullet, and §3.3's own "On
// terminal event: complete turn, trigger snapshot..."). Called from
// handleSandboxEvent's own post-commit block ONLY when cmd.Type ==
// "execution_complete" (a real turn-terminal wire event) -- corrected per
// independent review from this file's own earlier, unconditional-call
// design, which let every routine heartbeat on an already-Ready session
// re-trigger a full snapshot cycle indefinitely (see the call site's own
// comment in handleSandboxEvent for the full reasoning). Its own logic,
// still worth keeping defensive/idempotent in its own right (a redelivered
// execution_complete, or any other future caller, must never double-fire
// a snapshot):
//
//  1. Mint the Snapshot command's own MessageId FIRST, before any
//     transact -- needed both to persist it (step 2) and to build the
//     real command (step 3).
//  2. Read the current sandbox row, in a NEW transact. If status != Ready,
//     no-op: a sandbox that's Suspect/Snapshotting/Booting/etc. is not
//     eligible (exactly one live sandbox per session, and it must be
//     idle-and-ready to snapshot). Otherwise transition Ready ->
//     Snapshotting via sandbox.SnapshotStartTrigger() AND persist the
//     minted MessageId onto sandboxes.pending_snapshot_message_id, in this
//     SAME transact (message-id correlation fix, below), commit.
//  3. OUTSIDE that transact: send a real sandboxws.Snapshot command via
//     ports.SandboxCommander.SendCommand (the SAME port Step 21 built for
//     Prompt commands -- reused, not a second sandbox-command channel),
//     carrying the SAME MessageId just persisted.
//  4. If SendCommand fails (including ports.ErrNoLiveSandboxConnection):
//     SendCommand is a context-bounded conn.Write, a classic ambiguous-
//     write case -- the frame can already have been flushed to the OS/TCP
//     layer before the local call ever times out or errors, so "SendCommand
//     failed" does NOT reliably mean "the sandbox will never complete
//     this." revertSnapshotBestEffort (below) runs a SECOND, small
//     transact reverting Snapshotting back to Ready via
//     sandbox.SnapshotCompleteTrigger() AND clearing
//     pending_snapshot_message_id back to nil (a compensating write; no
//     gen-fencing concern since neither trigger is gen-fenced) -- so that
//     IF the frame actually did get through and a real snapshot_ready for
//     THIS attempt eventually arrives late (e.g. after a second attempt
//     has since started), handleSnapshotReadyEvent's own commandMessageId
//     match against the CURRENT pending id (now either nil or a later
//     attempt's own different id) correctly discards it as stale instead
//     of wrongly completing whatever attempt happens to be outstanding
//     when it finally arrives -- this closes a real race an independent
//     review confirmed against a real Postgres instance (see
//     migrations/000022_sandbox_snapshot_id.up.sql's own
//     pending_snapshot_message_id doc comment for the full scenario).
//     Logged at Warn, never treated as fatal to the turn-completion flow
//     that triggered it -- matches sendPushBestEffort/createPRBestEffort's
//     own "BestEffort" naming and never-alters-caller-return-value
//     discipline exactly (this function returns nothing at all).
//  5. If SendCommand succeeds: nothing more to do here -- wait for the
//     snapshot_ready event (handleSnapshotReadyEvent, above). HONEST GAP
//     (documented, not fixed by this Step -- see cmd/sandbox-agent/
//     main.go's own HandleSnapshot doc comment for the matching statement
//     of the same fact): if the command is genuinely lost on the wire
//     despite SendCommand reporting success (rather than merely delayed),
//     or the sandbox process itself dies mid-snapshot before ever
//     reporting snapshot_ready, the sandbox is left stuck Snapshotting --
//     no watchdog covers that state today, and building one (a dedicated
//     snapshot-timeout timer, or a broader NACK mechanism) is explicitly
//     out of this Step's own scope.
func (a *Actor) triggerSnapshotBestEffort(ctx context.Context) {
	messageID := uuid.NewString()

	var plan *snapshotPlan
	err := a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		row, err := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// No sandbox row at all -- defensive only: the ONLY
				// current caller (handleSandboxEvent) already read this
				// same row successfully moments ago in this very
				// invocation, so this branch is unreachable in practice
				// today, but a future caller invoking this unconditionally
				// with no guaranteed-existing row must not panic or error
				// loudly here.
				return nil
			}
			return fmt.Errorf("sessionactor: get sandbox: %w", err)
		}
		if sandbox.State(row.Status) != sandbox.StateReady {
			return nil
		}

		to, err := sandbox.Transition(sandbox.State(row.Status), int(row.Gen), sandbox.SnapshotStartTrigger())
		if err != nil {
			return fmt.Errorf("sessionactor: sandbox transition ready->snapshotting: %w", err)
		}
		if _, err := a.stores.sandbox.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
			SessionID: a.sessionID,
			Status:    sqlcgen.SandboxStatus(to),
		}); err != nil {
			return fmt.Errorf("sessionactor: update sandbox status to snapshotting: %w", err)
		}
		// Message-id correlation fix: persist the freshly-minted command
		// MessageId in this SAME transact (no extra round trip) --
		// handleSnapshotReadyEvent only accepts a later snapshot_ready as
		// completing THIS attempt if its own commandMessageId matches this
		// column's value.
		if _, err := a.stores.sandbox.WithTx(tx).UpdatePendingSnapshotMessageID(ctx, sqlcgen.UpdateSandboxPendingSnapshotMessageIDParams{
			SessionID:                a.sessionID,
			PendingSnapshotMessageID: &messageID,
		}); err != nil {
			return fmt.Errorf("sessionactor: record pending snapshot message id: %w", err)
		}
		plan = &snapshotPlan{gen: int(row.Gen), commandMessageID: messageID}
		return nil
	})
	if err != nil {
		a.logger.Warn("sessionactor: trigger snapshot: transact failed", "error", err)
		return
	}
	if plan == nil {
		// Not Ready (or no sandbox row at all) -- no snapshot warranted
		// this round; the common, expected case for most sandbox events.
		return
	}

	if a.commander == nil {
		// Defensive: mirrors tryPlanDispatch/tryPlanSpawn's own identical
		// nil-port guards exactly -- some tests, and any future caller
		// genuinely without one, must not panic here. The sandbox is
		// already committed Snapshotting at this point, so it must be
		// reverted back to Ready -- there is no live channel to ever
		// deliver the snapshot command it's waiting for.
		a.logger.Warn("sessionactor: sandbox is ready to snapshot but no SandboxCommander is configured; reverting to ready")
		a.revertSnapshotBestEffort(ctx)
		return
	}

	cmd := sandboxws.Snapshot{
		Type:      "snapshot",
		MessageId: plan.commandMessageID,
		SessionId: a.sessionID.String(),
		Gen:       plan.gen,
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		a.logger.Error("sessionactor: marshal snapshot command failed", "error", err)
		a.revertSnapshotBestEffort(ctx)
		return
	}

	if err := a.commander.SendCommand(a.sessionID.String(), payload); err != nil {
		// Covers ports.ErrNoLiveSandboxConnection and every other send
		// failure identically -- see this function's own doc comment,
		// point 4.
		a.logger.Warn("sessionactor: send snapshot command failed; reverting sandbox to ready", "error", err)
		a.revertSnapshotBestEffort(ctx)
	}
}

// revertSnapshotBestEffort is triggerSnapshotBestEffort's own compensating
// write for when the snapshot command never reached the sandbox: opens
// its OWN fresh transact (unlike handleSnapshotReadyEvent's own decode-
// failure revert, which reuses an already-open tx it was handed -- see
// revertSnapshotToReady's own doc comment for why the two calling contexts
// need different transaction-acquisition strategies), reads the current
// row, then delegates the actual revert-to-Ready-and-clear-pending-id
// logic to revertSnapshotToReady so both callers share exactly one
// implementation of it. A failure of THIS revert is logged at Warn too,
// never escalated -- there is nothing further this best-effort path can
// do about it (matches sendPushBestEffort/createPRBestEffort's own
// terminal, log-only failure handling).
func (a *Actor) revertSnapshotBestEffort(ctx context.Context) {
	err := a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		row, err := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("sessionactor: get sandbox: %w", err)
		}
		return a.revertSnapshotToReady(ctx, tx, row)
	})
	if err != nil {
		a.logger.Warn("sessionactor: revert snapshot to ready failed", "error", err)
	}
}
