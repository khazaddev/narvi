// This file (planapprovalcontent.go) implements Step 38's ("plan mode,
// cross-channel", §8.1/§13.3) own best-effort extraction of a plan's
// rendered content (the numbered steps/scope the producing turn's own
// assistant text laid out) for use in the Slack/Linear plan-approval-
// request notifications (outboxenqueue.go).
//
// There is no structured plan schema anywhere in this codebase (§12.2 item
// 3's own "numbered steps with file refs, scope estimate" is the WEB UI's
// own future rendering of a plan turn's freeform assistant text -- no
// Step, including this one, has built a frontend yet, this Step's own
// explicit out-of-scope note). So this does not attempt to parse steps out
// of the model's own prose into any structured shape -- it extracts the
// producing turn's own final streamed assistant text VERBATIM (the plan
// document's own actual content, whatever shape the model rendered it in,
// almost always already a numbered list given how plan-mode turns are
// prompted) and lets the caller (Slack Block Kit / Linear activity text)
// truncate/render it as plain text.

package sessionactor

import (
	"context"
	"encoding/json"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// planContentFallbackText is what planContentText returns when no
// assistant text could be recovered at all (an empty/failed events list) --
// never leaves a notification with literally nothing describing the plan.
const planContentFallbackText = "(plan content unavailable -- see the session's own event log)"

// planContentEventFetchLimit bounds how many of the session's own MOST
// RECENT events this reads back (a.stores.event.ListRecentForSession,
// newest-first) -- generous for a single plan-mode turn's own streamed
// output (a turn's own text/tool-call event count is overwhelmingly never
// anywhere near this many), while still being a fixed, safe upper bound
// rather than an unbounded full-table scan. Deliberately anchored to the
// TAIL of the session's event log, not the beginning: a long-lived session
// (many prior turns) can easily have accumulated more than this many
// events in total by the time a LATER plan-mode turn completes, and this
// turn's own token events -- the most recent activity in the session --
// must still be found within the fetched window regardless of how much
// earlier history precedes them.
const planContentEventFetchLimit = 2000

// tokenEventPayload is the minimal shape this function reads out of a
// "token" event's own raw payload (contracts/sandbox-ws/v1/events.schema.
// json's own Token shape) -- Text is CUMULATIVE per messageId (§6.1: "text
// is CUMULATIVE, not a delta"), so the LAST token event (by event id, i.e.
// arrival order) for the producing turn's own span is already the full,
// final rendered text of whichever assistant message it belongs to.
type tokenEventPayload struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// planContentText best-effort recovers processing's own final streamed
// assistant text -- the plan document's own actual content -- by scanning
// the session's own event log, NEWEST FIRST (a.stores.event.
// ListRecentForSession, pool-based per EventStore's own doc comment, so
// this is always a plain read of already-committed rows, safe to call from
// inside completeProcessingTurn's own transact), for "token" events whose
// own CreatedAt falls at/after processing's own DispatchedAt. Since the
// scan is newest-first, the FIRST such token event found is already the
// LAST one by arrival order (§6.1: token text is cumulative per messageId,
// so that one event's own Text is already the full, final rendered text) --
// no need to keep scanning for a "latest so far" the way an oldest-first
// scan would. The scan also stops as soon as it reaches an event older than
// processing's own DispatchedAt: everything from there on (lower ids, in
// this descending walk) belongs to an EARLIER turn's own streamed output,
// not this plan-mode turn's own (events carries no turn_id column, see
// migrations/000008_events.up.sql's own doc comment) -- there is nothing
// left to find. Never fails the caller: any read/decode error, or finding
// nothing at all, returns planContentFallbackText rather than propagating
// an error -- a best-effort notification enrichment must never block or
// fail the turn-completion transaction it runs inside.
func (a *Actor) planContentText(ctx context.Context, processing sqlcgen.Turn) string {
	events, err := a.stores.event.ListRecentForSession(ctx, a.sessionID, planContentEventFetchLimit)
	if err != nil {
		a.logger.Warn("sessionactor: list events for plan content extraction failed", "error", err)
		return planContentFallbackText
	}

	for _, e := range events {
		if processing.DispatchedAt.Valid && e.CreatedAt.Valid && e.CreatedAt.Time.Before(processing.DispatchedAt.Time) {
			break
		}
		if e.Type != "token" {
			continue
		}

		var tok tokenEventPayload
		if err := json.Unmarshal(e.Payload, &tok); err != nil {
			continue
		}
		if tok.Text != "" {
			return tok.Text
		}
	}

	return planContentFallbackText
}
