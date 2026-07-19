// This file (timerfired.go) implements handling a TimerFired command
// for each of the 5 named persistent timers (§2), using ONLY the
// already-built domain packages (internal/domain/{sandbox,turn,session})
// for every actual decision -- this file's own job is orchestration
// (reading current state, calling the right decision function, writing
// the result back transactionally) and never reimplementing a decision
// those packages already make.
//
// All 5 named timers are fully wired here -- none needed a SandboxProvider
// or AgentRuntime (neither exists until Step 12+):
//
//   - inactivity: domain/sandbox.EvaluateInactivityTimeout. One
//     deliberate, documented simplification: ConnectedClientCount is
//     always 0, since the client WS hub that will track connected
//     participants doesn't exist until Steps 18+ -- see
//     handleInactivityTimer. This does not stop the timer from being
//     fully wired; it just means the "clients connected -> extend + warn"
//     branch is unreachable until that later Step lands.
//   - connecting_deadline / liveness_check: domain/sandbox.
//     EvaluateConnectingTimeout / EvaluateHeartbeatHealth, respectively.
//   - terminal_grace: unconditionally treated as a genuine timeout (see
//     handleTerminalGraceTimer for why the Stopped/Stale peer outcomes
//     are not reachable yet, and why that is a documented absence of
//     their triggering conditions, not a shortcut taken here).
//   - turn_deadline: domain/turn.EvaluateTurnDeadline +
//     RequiresSyntheticExecutionComplete + DeriveFailureReason.
//
// Every handler ends by either re-arming (UpsertSessionTimer to a fresh,
// meaningful fires_at) or deleting the timer that fired. This is a hard
// contract, not a style preference: the timer pump's claim step (§2)
// pushes fires_at forward as a "delivery in progress" marker before this
// actor ever sees the command, precisely so a crash mid-handling
// self-heals once the claim window elapses. A handler that did neither --
// left the claimed row untouched -- would have that same claim window
// silently expire and redeliver the identical TimerFired command forever,
// even long after the condition it was watching stopped being relevant.

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/sandbox"
	"github.com/khazaddev/narvi/internal/domain/session"
	"github.com/khazaddev/narvi/internal/domain/turn"
)

// handle dispatches a Command to its handler. Command's isCommand method
// is unexported (command.go), so the default case below is unreachable
// dead-code protection, not a real possibility this package needs to
// handle gracefully.
func (a *Actor) handle(ctx context.Context, cmd Command) error {
	switch c := cmd.(type) {
	case TimerFired:
		return a.handleTimerFired(ctx, c)
	case SandboxEvent:
		return a.handleSandboxEvent(ctx, c)
	case EnsureDispatched:
		return a.handleEnsureDispatched(ctx)
	default:
		return fmt.Errorf("sessionactor: unhandled command type %T", cmd)
	}
}

func (a *Actor) handleTimerFired(ctx context.Context, cmd TimerFired) error {
	switch cmd.Name {
	case TimerInactivity:
		return a.handleInactivityTimer(ctx)
	case TimerConnectingDeadline:
		return a.handleConnectingDeadlineTimer(ctx)
	case TimerLivenessCheck:
		return a.handleLivenessCheckTimer(ctx)
	case TimerTerminalGrace:
		return a.handleTerminalGraceTimer(ctx)
	case TimerTurnDeadline:
		return a.handleTurnDeadlineTimer(ctx)
	default:
		// TEXT column, not an enum (§2) -- an unrecognized name is
		// handled defensively (deny-list-not-allow-list, same convention
		// as domain/sandbox.IsDeadSandboxStatus), never a fatal error.
		a.logger.Warn("sessionactor: ignoring TimerFired with unknown name", "name", cmd.Name)
		return nil
	}
}

// handleInactivityTimer implements the `inactivity` named timer (§2).
// See this file's own top comment for the ConnectedClientCount
// simplification.
func (a *Actor) handleInactivityTimer(ctx context.Context) error {
	return a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := time.Now()

		sandboxRow, err := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// No sandbox yet -- nothing to watch. Re-arm at the
				// minimum check interval so the pump keeps polling until
				// one exists.
				return a.armTimer(ctx, tx, TimerInactivity, now.Add(a.timeouts.InactivityMinCheckInterval))
			}
			return fmt.Errorf("sessionactor: get sandbox: %w", err)
		}

		turns, err := a.stores.turn.WithTx(tx).ListForSession(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("sessionactor: list turns: %w", err)
		}

		state := sandbox.InactivityState{
			LastActivity: pgTimeOrZero(sandboxRow.LastSeenAt),
			Status:       sandbox.State(sandboxRow.Status),
			// ConnectedClientCount is always 0 for now: the client WS
			// hub that will actually track connected participants
			// doesn't exist until Steps 18+. Until then,
			// EvaluateInactivityTimeout can never take its "clients
			// still connected -> extend + warn" branch -- every genuine
			// inactivity timeout goes straight to
			// InactivityActionTimeout. This is a deliberate, documented
			// limitation of what THIS caller can observe, not a gap in
			// the decision function itself, which is used exactly as
			// built.
			ConnectedClientCount: 0,
			IsProcessing:         anyTurnProcessing(turns),
		}
		cfg := sandbox.InactivityConfig{
			Timeout:          a.timeouts.InactivityTimeout,
			Extension:        a.timeouts.InactivityExtension,
			MinCheckInterval: a.timeouts.InactivityMinCheckInterval,
		}
		action := sandbox.EvaluateInactivityTimeout(state, cfg, now)

		switch action.Kind {
		case sandbox.InactivityActionSchedule:
			return a.armTimer(ctx, tx, TimerInactivity, now.Add(action.NextCheck))

		case sandbox.InactivityActionExtend:
			if err := a.armTimer(ctx, tx, TimerInactivity, now.Add(action.Extension)); err != nil {
				return err
			}
			if !action.ShouldWarn {
				return nil
			}
			return a.appendEvent(ctx, tx, "warning", map[string]any{
				"reason": "inactivity_extended",
			})

		case sandbox.InactivityActionTimeout:
			// §3.2's two-phase design: the watchdog's only job is moving
			// the sandbox to Suspect and arming terminal_grace -- NOT
			// calling a provider to actually snapshot/stop it (that's
			// Step 12+'s SandboxProvider, out of scope here).
			// action.ShouldSnapshot is deliberately never consulted.
			if err := a.transitionSandboxToSuspect(ctx, tx, sandboxRow, now); err != nil {
				return err
			}
			// The sandbox is no longer Ready -- inactivity monitoring no
			// longer applies until (if ever) it recovers.
			return a.deleteTimer(ctx, tx, TimerInactivity)

		default:
			return fmt.Errorf("sessionactor: unknown inactivity action kind %v", action.Kind)
		}
	})
}

// handleConnectingDeadlineTimer implements the `connecting_deadline`
// named timer (§2), sharing EvaluateConnectingTimeout /
// EvaluateHeartbeatHealth's two-budget model with handleLivenessCheckTimer.
func (a *Actor) handleConnectingDeadlineTimer(ctx context.Context) error {
	return a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := time.Now()

		sandboxRow, err := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return a.deleteTimer(ctx, tx, TimerConnectingDeadline)
			}
			return fmt.Errorf("sessionactor: get sandbox: %w", err)
		}

		status := sandbox.State(sandboxRow.Status)
		if !isConnectingPhase(status) {
			// Sandbox has already moved past its boot phase (or into
			// Suspect/terminal via some other path) -- this timer no
			// longer applies; drop it rather than re-arming forever.
			return a.deleteTimer(ctx, tx, TimerConnectingDeadline)
		}

		cfg := sandbox.LivenessConfig{
			FirstConnectBudget:    a.timeouts.FirstConnectBudget,
			SteadyHeartbeatBudget: a.timeouts.SteadyHeartbeatBudget,
		}
		result := sandbox.EvaluateConnectingTimeout(status, pgTimeOrZero(sandboxRow.CreatedAt), pgTimeOrZero(sandboxRow.LastSeenAt), cfg, now)

		if !result.IsTimedOut {
			// Re-check at a short interval so the budget's expiry is
			// noticed promptly. platform.Timeouts has no dedicated
			// "connecting recheck interval" (not specified in the plan);
			// InactivityMinCheckInterval is reused here as the
			// platform's existing "how often is it OK to poll a
			// liveness-style condition" constant, rather than inventing
			// a near-duplicate field for the same judgment call.
			return a.armTimer(ctx, tx, TimerConnectingDeadline, now.Add(a.timeouts.InactivityMinCheckInterval))
		}

		if err := a.transitionSandboxToSuspect(ctx, tx, sandboxRow, now); err != nil {
			return err
		}
		return a.deleteTimer(ctx, tx, TimerConnectingDeadline)
	})
}

// handleLivenessCheckTimer implements the `liveness_check` named timer
// (§2): the Ready-state counterpart of handleConnectingDeadlineTimer.
func (a *Actor) handleLivenessCheckTimer(ctx context.Context) error {
	return a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := time.Now()

		sandboxRow, err := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return a.deleteTimer(ctx, tx, TimerLivenessCheck)
			}
			return fmt.Errorf("sessionactor: get sandbox: %w", err)
		}

		if sandbox.State(sandboxRow.Status) != sandbox.StateReady {
			// liveness_check only monitors the steady Ready state (§3.2's
			// steady_heartbeat_budget); once the sandbox leaves Ready via
			// any path, this timer no longer applies.
			return a.deleteTimer(ctx, tx, TimerLivenessCheck)
		}

		cfg := sandbox.LivenessConfig{
			FirstConnectBudget:    a.timeouts.FirstConnectBudget,
			SteadyHeartbeatBudget: a.timeouts.SteadyHeartbeatBudget,
		}
		health := sandbox.EvaluateHeartbeatHealth(pgTimeOrZero(sandboxRow.LastSeenAt), cfg, now)

		if !health.IsStale {
			return a.armTimer(ctx, tx, TimerLivenessCheck, now.Add(a.timeouts.SteadyHeartbeatBudget))
		}

		if err := a.transitionSandboxToSuspect(ctx, tx, sandboxRow, now); err != nil {
			return err
		}
		return a.deleteTimer(ctx, tx, TimerLivenessCheck)
	})
}

// handleTerminalGraceTimer implements the `terminal_grace` named timer
// (§2, §3.2).
func (a *Actor) handleTerminalGraceTimer(ctx context.Context) error {
	return a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		sandboxRow, err := a.stores.sandbox.WithTx(tx).Get(ctx, a.sessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return a.deleteTimer(ctx, tx, TimerTerminalGrace)
			}
			return fmt.Errorf("sessionactor: get sandbox: %w", err)
		}

		if sandbox.State(sandboxRow.Status) != sandbox.StateSuspect {
			// Already recovered (or already terminal) via some other
			// path before this timer fired -- this specific grace period
			// is moot.
			return a.deleteTimer(ctx, tx, TimerTerminalGrace)
		}

		// §3.2: "Any liveness signal during grace returns to previous
		// state." This Step has no mechanism for a genuine external
		// liveness signal to arrive DURING grace and reach this actor as
		// a command (that requires the sandbox WS hub, Steps 16-18, to
		// exist and deliver one -- e.g. a future LivenessSignal command
		// driving TriggerRecover). Absent that, terminal_grace firing
		// here is UNCONDITIONALLY treated as a genuine timeout:
		// Suspect -> Failed. Stopped (an explicit stop request) and
		// Stale (a GC/reconciler classification) are the two OTHER peer
		// terminal outcomes domain/sandbox.Transition allows for
		// TriggerGraceExpired, but neither is reachable from this
		// handler yet -- Stopped needs an HTTP stop endpoint (a later
		// Step) to have recorded that a stop was explicitly requested,
		// and Stale needs Step 25's reconciler to have classified this
		// sandbox as orphaned. Both are genuinely absent from the
		// codebase today, not merely unwired here.
		to, err := sandbox.Transition(sandbox.StateSuspect, int(sandboxRow.Gen), sandbox.GraceExpiredTrigger(sandbox.StateFailed))
		if err != nil {
			return fmt.Errorf("sessionactor: sandbox transition suspect->failed: %w", err)
		}
		if _, err := a.stores.sandbox.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
			SessionID: a.sessionID,
			Status:    sqlcgen.SandboxStatus(to),
		}); err != nil {
			return fmt.Errorf("sessionactor: update sandbox status to failed: %w", err)
		}

		// The sandbox failing does not, on its own, fail whatever turn
		// might currently be Processing (this handler never touches
		// turns) -- that cascade is turn_deadline's own independent job
		// (handleTurnDeadlineTimer), armed and firing on its own separate
		// schedule. Re-deriving here is still correct and required: it
		// keeps the session row consistent with the turn history as it
		// stands right now, exactly as this Step scopes it.
		if err := a.rederiveSessionStatusUnchanged(ctx, tx); err != nil {
			return err
		}
		return a.deleteTimer(ctx, tx, TimerTerminalGrace)
	})
}

// handleTurnDeadlineTimer implements the `turn_deadline` named timer (§2,
// §3.3).
func (a *Actor) handleTurnDeadlineTimer(ctx context.Context) error {
	return a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		now := time.Now()

		turns, err := a.stores.turn.WithTx(tx).ListForSession(ctx, a.sessionID)
		if err != nil {
			return fmt.Errorf("sessionactor: list turns: %w", err)
		}

		processing, ok := findProcessingTurn(turns)
		if !ok {
			// No turn currently Processing: it already reached a
			// terminal state via some other path before this timer
			// fired, or the timer is simply stale. turn_deadline is
			// armed exactly once, at TriggerDispatch (see
			// domain/turn.EvaluateTurnDeadline's own doc), so unlike the
			// liveness timers there is no "reschedule and keep watching"
			// case here -- just drop it.
			return a.deleteTimer(ctx, tx, TimerTurnDeadline)
		}

		result := turn.EvaluateTurnDeadline(pgTimeOrZero(processing.DispatchedAt), now, turn.DeadlineConfig{
			Deadline: a.timeouts.TurnDeadline,
		})

		if !result.IsTimedOut {
			// Re-arm at the remaining time. turn_deadline is
			// conceptually a one-shot deadline, not a recheck-interval
			// timer like the liveness ones -- but re-arming defensively
			// here correctly handles the timer pump's claim-and-
			// redeliver semantics if this ever fires "early" (e.g. a
			// claim-window edge case), by pushing it out to the real
			// remaining deadline instead of treating an early wakeup as
			// the genuine one.
			remaining := a.timeouts.TurnDeadline - result.Elapsed
			return a.armTimer(ctx, tx, TimerTurnDeadline, now.Add(remaining))
		}

		to, err := turn.Transition(turn.StateProcessing, turn.TriggerTimeout)
		if err != nil {
			return fmt.Errorf("sessionactor: turn transition processing->failed: %w", err)
		}

		if _, err := a.stores.turn.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
			ID:          processing.ID,
			Status:      sqlcgen.TurnStatus(to),
			CompletedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}); err != nil {
			return fmt.Errorf("sessionactor: update turn status: %w", err)
		}

		// §3.3: "Stop/failure paths emit a synthetic execution_complete
		// event so clients always see one terminal event per turn" --
		// exactly the machinery Step 08 built for this caller (see
		// domain/turn's own doc.go). RequiresSyntheticExecutionComplete
		// and DeriveFailureReason are used exactly as built, not
		// reimplemented.
		if turn.RequiresSyntheticExecutionComplete(turn.TriggerTimeout) {
			if err := a.appendEvent(ctx, tx, "execution_complete", map[string]any{
				"turn_id":   processing.ID.String(),
				"synthetic": true,
				"reason":    "timeout",
			}); err != nil {
				return err
			}
		}

		failureReason, _ := turn.DeriveFailureReason(turn.StateProcessing, turn.TriggerTimeout)

		if err := a.persistDerivedSessionStatus(ctx, tx, summariesWithOverride(turns, processing.ID, to, failureReason)); err != nil {
			return err
		}
		return a.deleteTimer(ctx, tx, TimerTurnDeadline)
	})
}

// transitionSandboxToSuspect is the shared Suspect-transition + arm-grace
// step every watchdog-style timer (inactivity, connecting_deadline,
// liveness_check) performs identically on timeout, per §3.2's two-phase
// design: "a watchdog never writes failed directly. It writes suspect and
// arms terminal_grace."
func (a *Actor) transitionSandboxToSuspect(ctx context.Context, tx pgx.Tx, row sqlcgen.Sandbox, now time.Time) error {
	to, err := sandbox.Transition(sandbox.State(row.Status), int(row.Gen), sandbox.SuspectTrigger())
	if err != nil {
		return fmt.Errorf("sessionactor: sandbox transition to suspect: %w", err)
	}
	if _, err := a.stores.sandbox.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: a.sessionID,
		Status:    sqlcgen.SandboxStatus(to),
	}); err != nil {
		return fmt.Errorf("sessionactor: update sandbox status to suspect: %w", err)
	}
	return a.armTimer(ctx, tx, TimerTerminalGrace, now.Add(a.timeouts.TerminalGracePeriod))
}

// rederiveSessionStatusUnchanged re-derives and persists session status
// from the CURRENT (unmodified by this call) turn history -- used by
// handleTerminalGraceTimer, which changes the SANDBOX's status but no
// turn's. Since domain/session.DeriveStatus is a pure function of turn
// history alone, and that history hasn't changed, its result here is
// necessarily identical to whatever it was the last time it was
// legitimately derived and persisted (by whichever call DID change a
// turn's status -- in this Step, only handleTurnDeadlineTimer). turns
// carry no failure_reason column by design (see turn.FailureReason's own
// doc: only the (from, trigger) pair that PRODUCED a terminal transition
// knows the reason, and only the caller making that transition can derive
// it) -- so the last turn's failure reason, if it is terminal-failed, is
// read back from the session row's OWN currently-stored failure_reason
// rather than reconstructed from a trigger this call was never given.
func (a *Actor) rederiveSessionStatusUnchanged(ctx context.Context, tx pgx.Tx) error {
	sessionRow, err := a.stores.session.WithTx(tx).Get(ctx, a.sessionID)
	if err != nil {
		return fmt.Errorf("sessionactor: get session: %w", err)
	}
	turns, err := a.stores.turn.WithTx(tx).ListForSession(ctx, a.sessionID)
	if err != nil {
		return fmt.Errorf("sessionactor: list turns: %w", err)
	}

	var currentReason turn.FailureReason
	if sessionRow.FailureReason != nil {
		currentReason = turn.FailureReason(*sessionRow.FailureReason)
	}

	return a.persistDerivedSessionStatus(ctx, tx, summariesForRederive(turns, currentReason))
}

// persistDerivedSessionStatus runs domain/session.DeriveStatus over
// summaries and persists the result -- the ONLY way this package ever
// writes sessions.status/failure_reason (§11: "every state transition
// goes through the machine's transition table").
func (a *Actor) persistDerivedSessionStatus(ctx context.Context, tx pgx.Tx, summaries []turn.Summary) error {
	derived := session.DeriveStatus(summaries)

	var failureReason *sqlcgen.SessionFailureReason
	if derived.FailureReason != "" {
		v := sqlcgen.SessionFailureReason(derived.FailureReason)
		failureReason = &v
	}

	if _, err := a.stores.session.WithTx(tx).UpdateStatus(ctx, sqlcgen.UpdateSessionStatusParams{
		ID:            a.sessionID,
		Status:        sqlcgen.SessionStatus(derived.Status),
		FailureReason: failureReason,
	}); err != nil {
		return fmt.Errorf("sessionactor: update session status: %w", err)
	}
	return nil
}

// armTimer upserts (arms or re-arms) the named timer to fire at fireAt.
func (a *Actor) armTimer(ctx context.Context, tx pgx.Tx, name string, fireAt time.Time) error {
	_, err := a.stores.timer.WithTx(tx).Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: a.sessionID,
		Name:      name,
		FiresAt:   pgtype.Timestamptz{Time: fireAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("sessionactor: arm timer %q: %w", name, err)
	}
	return nil
}

// deleteTimer removes the named timer -- see this file's top comment for
// why every handler must end in either this or armTimer.
func (a *Actor) deleteTimer(ctx context.Context, tx pgx.Tx, name string) error {
	if err := a.stores.timer.WithTx(tx).Delete(ctx, sqlcgen.DeleteSessionTimerParams{
		SessionID: a.sessionID,
		Name:      name,
	}); err != nil {
		return fmt.Errorf("sessionactor: delete timer %q: %w", name, err)
	}
	return nil
}

// appendEvent inserts a session event row inside tx (§2: appended events
// always commit in the same transaction as the state change they
// describe).
func (a *Actor) appendEvent(ctx context.Context, tx pgx.Tx, eventType string, payload map[string]any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("sessionactor: marshal %s event payload: %w", eventType, err)
	}
	if _, err := a.stores.event.WithTx(tx).Create(ctx, sqlcgen.CreateEventParams{
		SessionID: a.sessionID,
		Type:      eventType,
		Payload:   raw,
	}); err != nil {
		return fmt.Errorf("sessionactor: append %s event: %w", eventType, err)
	}
	// Queue for broadcast AFTER commit -- see actor.go's transact/
	// broadcastPending doc comments for the full commit-then-broadcast,
	// discard-on-rollback ordering this is part of.
	a.pendingBroadcast = append(a.pendingBroadcast, raw)
	return nil
}

// pgTimeOrZero converts a nullable Postgres timestamp to time.Time,
// time.Time{} (the zero value) when it is NULL -- matching how the
// domain decision functions in internal/domain/sandbox already represent
// "no signal yet" (e.g. InactivityState.LastActivity's own doc: "Zero
// (time.Time{}) means never active").
func pgTimeOrZero(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}

// isConnectingPhase reports whether status is one of the three boot-phase
// states domain/sandbox.EvaluateConnectingTimeout actually evaluates
// (Spawning, Connecting, Booting) -- used here to decide whether
// connecting_deadline still applies at all, distinct from
// EvaluateConnectingTimeout's own IsTimedOut (which only says whether the
// budget has elapsed, not whether the phase is still relevant).
func isConnectingPhase(status sandbox.State) bool {
	switch status {
	case sandbox.StateSpawning, sandbox.StateConnecting, sandbox.StateBooting:
		return true
	default:
		return false
	}
}

// findProcessingTurn returns the turn currently in StateProcessing, if
// any. At most one such turn can exist per session (domain/turn's own
// single-in-flight invariant, enforced additionally at the DB level by
// turns_one_processing_per_session).
func findProcessingTurn(turns []sqlcgen.Turn) (sqlcgen.Turn, bool) {
	for _, t := range turns {
		if turn.State(t.Status) == turn.StateProcessing {
			return t, true
		}
	}
	return sqlcgen.Turn{}, false
}

// anyTurnProcessing reports whether any turn in turns is currently
// StateProcessing -- the IsProcessing input
// domain/sandbox.EvaluateInactivityTimeout needs.
func anyTurnProcessing(turns []sqlcgen.Turn) bool {
	_, ok := findProcessingTurn(turns)
	return ok
}

// summariesWithOverride builds the []turn.Summary domain/session.
// DeriveStatus needs from stored turn rows, substituting overrideStatus/
// overrideReason for the one row whose ID matches overrideID -- used
// right after this actor itself just transitioned that one turn (so its
// new status/reason is known directly, not yet reflected in the turns
// slice fetched at the top of the same transaction).
func summariesWithOverride(turns []sqlcgen.Turn, overrideID pgtype.UUID, overrideStatus turn.State, overrideReason turn.FailureReason) []turn.Summary {
	out := make([]turn.Summary, len(turns))
	for i, t := range turns {
		if t.ID == overrideID {
			out[i] = turn.Summary{Status: overrideStatus, FailureReason: overrideReason}
			continue
		}
		out[i] = turn.Summary{Status: turn.State(t.Status)}
	}
	return out
}

// summariesForRederive builds the []turn.Summary domain/session.
// DeriveStatus needs from stored turn rows, for a re-derivation NOT
// accompanied by a turn status change of this call's own (see
// rederiveSessionStatusUnchanged's doc for why currentFailureReason,
// applied only to the last entry, is the correct source of truth here).
func summariesForRederive(turns []sqlcgen.Turn, currentFailureReason turn.FailureReason) []turn.Summary {
	out := make([]turn.Summary, len(turns))
	for i, t := range turns {
		out[i] = turn.Summary{Status: turn.State(t.Status)}
	}
	if n := len(out); n > 0 {
		out[n-1].FailureReason = currentFailureReason
	}
	return out
}
