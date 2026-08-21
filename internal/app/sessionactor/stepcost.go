// This file (stepcost.go) implements §25.15's own "cost does need
// persistence" half: handleSandboxEvent's own "step_finish" case
// (sandboxevent.go), called from INSIDE that function's own transact,
// AFTER appendRawEvent has already persisted the raw event verbatim
// (that file's own top comment: "persist ALWAYS, for every recognized
// event type"). A decode failure here therefore never needs to
// retroactively touch that persisted row -- mirroring recordBootTiming's
// own identical decode-failure handling (boottiming.go): warn, record
// nothing, never return an error that would roll back the WHOLE
// transact.
//
// §25.15 is explicit about what this is NOT: it is not, and must never
// become, internal/adapters/outbound/opencode's own adapter-local
// turnState.spentUSD accumulator (§7.1/§26.7) -- that lives and dies
// with one sandbox-agent process's own in-memory turn state, answers one
// sandbox-local question, and is never read from here. This file sums
// the SAME wire signal (step_finish.cost.usd) into an independent,
// Postgres-durable running total for a different, control-plane-visible
// purpose: turns.cost_usd (migration 000098), joined into a workflow
// step's own cost via workflow_step_runs.turn_id (queries/workflows.sql,
// ListWorkflowStepRunsForRun) exactly as turns.model_id already is for a
// step's model.
package sessionactor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
)

// recordStepFinishCost adds this step_finish event's own cost.usd (when
// present) onto the running total of whichever turn is currently
// processing for this session -- see AddTurnCostUSD's own generated doc
// comment (sqlcgen/turns.sql.go) for why that accumulation is one
// guarded SQL UPDATE and never a Go-side read-modify-write.
//
// inserted is handleSandboxEvent's own appendRawEvent result for THIS
// event, threaded through for the SAME reason recordBootTiming's own
// inserted parameter is gated (boottiming.go): step_finish is not one of
// §6.1's 6 critical acked types, but the sender's own reconnect-resend
// buffer (§6.1: "buffers (1000 events, evict oldest non-critical) and
// re-sends on reconnect") can still replay an already-processed
// step_finish verbatim on a forced reconnect, and CreateEvent's own
// upsert-on-(session_id, message_id) dedup (queries/events.sql) is what
// tells this event apart from that replay. Recording cost unconditionally
// would double-count every buffered step_finish on such a replay.
func (a *Actor) recordStepFinishCost(ctx context.Context, tx pgx.Tx, raw json.RawMessage, inserted bool) error {
	if !inserted {
		// A wire-level redelivery of an already-processed step_finish (see
		// this function's own doc comment above) -- its cost was already
		// added to the running total once, on the first delivery; adding
		// it again here would double-count the same dollar figure.
		return nil
	}

	var evt sandboxws.StepFinish
	if err := json.Unmarshal(raw, &evt); err != nil {
		// Defensive, not fatal -- mirrors recordBootTiming/
		// completeProcessingTurn's own identical decode-failure handling:
		// wshub's own read loop only peeks a small, permissive envelope
		// struct before ever constructing this SandboxEvent (dispatch.go),
		// so a genuinely malformed-per-schema step_finish CAN reach this
		// point in production. Returning an error here would roll back
		// this function's own caller's WHOLE transact, including the raw
		// event this same transact already persisted moments ago.
		a.logger.Warn("sessionactor: step_finish failed schema decode; persisted verbatim, recording no cost",
			"error", err)
		return nil
	}

	usd := (*float64)(evt.Cost.Usd)
	if usd == nil {
		// §6.1: cost.usd is OPTIONAL/nullable on the wire. Absent/null
		// means this step_finish carries no dollar figure at all -- never
		// treated as "$0", and never touching turns.cost_usd: that column
		// must stay NULL until a REAL figure (even a genuine $0.00) has
		// arrived, so "no cost yet" never renders as "free" (§25.15).
		return nil
	}

	affected, err := a.stores.turn.WithTx(tx).AddCostUSD(ctx, a.sessionID, *usd)
	if err != nil {
		return fmt.Errorf("sessionactor: add turn cost: %w", err)
	}
	if affected == 0 {
		// Defensive only: a step_finish is only ever emitted mid-turn, by
		// construction of the OpenCode adapter's own event stream, so the
		// session's own turn should always still be Processing when this
		// arrives. Logged, not fatal -- exactly like
		// maybeEnqueueLinearProgress's own identical "no turn in
		// processing" defensive branch (progressnotify.go).
		a.logger.Warn("sessionactor: step_finish arrived with no turn in processing; cost not recorded")
	}
	return nil
}
