package reviewverdict

import "github.com/khazaddev/narvi/internal/domain/review"

// This file implements §26.4's own named residual, closed by §26.4:
// "Corroborating the claim against the persisted sub_task_finish trace is
// what would make the heading 'structural'... until it ships this field is
// trusted, not verified." A schema-required `CounterReview: done`
// self-report (review.CounterReviewStatus, counterreview.go) is
// presence-verified by reviewpost.ValidateVerdictInput and truth-verified
// by nothing at all -- a primary reviewer that never actually dispatched
// the `counter-reviewer` sub-task (§7.1's engine-native fan-out) can still
// self-report "done" and receive CounterReviewFloor's permissive
// ShippableAuto. §26.4's own opening paragraph already establishes that
// the trace to corroborate against exists and is durable: sub_task_finish
// is one of the six ack-guaranteed critical event types
// (ports/agentruntime.go), and sandbox-event persistence is unconditional
// for every recognized type (sessionactor/sandboxevent.go's own
// appendRawEvent, called before the switch on cmd.Type) -- a sub-agent
// that actually ran leaves a durable, queryable trace whether or not the
// primary reviewer's own self-report is honest. CounterReviewCorroborated
// (below) is the ONE pure function that reads that trace back.
//
// # Why this lives here, not in internal/domain/review
//
// Mirrors record.go's own precedent immediately above (a reviewverdict
// type wrapping review.Verdict, rather than a new field ON review.Verdict
// itself): review's own doc.go "zero external imports" convention (this
// package already imports review, see record.go/rollup.go) means review
// itself cannot depend on anything shaped like a persisted event-log
// query result, however loosely typed. This function's own two input
// slices are the SAME "already-fetched, plain typed data, zero I/O"
// discipline driftcanary.go's own FilesChangedDrifted already commits to
// -- the caller (internal/adapters/inbound/httpapi/reviewverdict.go) does
// every Postgres read and JSONB payload decode; this function does one
// thing, a pure comparison, and returns a plain bool.
//
// # Field shapes match the wire, not the DB row
//
// SubTaskStartRecord/SubTaskFinishRecord below deliberately carry only
// the two fields this comparison actually needs from each event's own
// wire shape (sandboxws.SubTaskStart.SubAgentType/SubTaskId and
// sandboxws.SubTaskFinish.SubTaskId/Outcome) -- never a sqlcgen.Event row,
// never raw JSON. The caller decodes each persisted event's own `payload`
// JSONB column into these shapes; this function never sees a database
// type or a byte slice.

// SubTaskStartRecord is the one slice of a persisted sub_task_start
// event's own wire payload (contracts/sandbox-ws/v1/events.schema.json)
// this comparison needs: which sub-task (SubTaskID) was announced as
// which named sub-agent (SubAgentType, §26.4's own new wire field,
// sourced from the task tool's own "subagent_type" dispatch parameter --
// see internal/adapters/outbound/opencode/translate.go's
// taskInputSubAgentType for the extraction, and that field's own doc
// comment for why THIS field, never the freeform Label, is what
// corroboration keys off).
type SubTaskStartRecord struct {
	SubTaskID    string
	SubAgentType string
}

// SubTaskFinishRecord is the one slice of a persisted sub_task_finish
// event's own wire payload this comparison needs: which sub-task
// (SubTaskID, the SAME correlator its own sub_task_start carried) ended
// with which Outcome -- one of sandboxws's own ExecutionCompleteOutcome
// wire strings ("completed"/"failed"/"cancelled", §7.1's own "reuses the
// turn's own outcome taxonomy").
type SubTaskFinishRecord struct {
	SubTaskID string
	Outcome   string
}

// counterReviewFinishOutcomeCompleted mirrors sandboxws.
// ExecutionCompleteOutcomeCompleted's own wire string value ("completed")
// exactly -- checked against contracts/gen/go/sandboxws/sandboxws.go
// rather than guessed, per this Step's own instruction. Named rather than
// inlined so a future SubTaskFinish outcome-taxonomy change (§7.1's own
// "reuses the turn's own outcome taxonomy" -- a taxonomy that could grow)
// only ever needs updating in one place. Deliberately a plain string, not
// an import of the sandboxws-generated type: this package's own sibling
// files (driftcanary.go, record.go) take plain typed inputs from their
// callers rather than reaching into contracts/gen themselves, and this
// function's own SubTaskFinishRecord.Outcome field follows that same
// discipline -- the caller (httpapi) is what actually touches the wire
// type when decoding a persisted payload.
const counterReviewFinishOutcomeCompleted = "completed"

// CounterReviewCorroborated reports whether starts/finishes -- BOTH
// already scoped by the caller to the SAME session, the SAME sandbox gen
// the turn being verdicted was actually dispatched at, AND a created_at
// lower bound at that same turn's own dispatched_at (see queries/
// events.sql's own ListSubTaskStartEventsForTurn/
// ListSubTaskFinishEventsForTurn doc comment for why gen-scoping ALONE was
// found insufficient -- a real cross-turn contamination gap caught by
// adversarial review -- and why the dispatched_at bound is required
// alongside it, not merely session-scoping) -- together contain real, durable
// evidence that the `counter-reviewer` sub-agent (review.
// CounterReviewerAgentName, "counter-reviewer") was both dispatched and
// actually completed.
//
// True iff there exists a starts record whose SubAgentType equals
// review.CounterReviewerAgentName AND whose OWN SubTaskID also appears in
// finishes with Outcome == "completed" -- both halves of that AND
// matched to the SAME subTaskId, never "some counter-reviewer start
// exists somewhere" independently paired with "some completed finish
// exists somewhere". A session's own trace can carry sub-tasks for other
// named sub-agents in the SAME turn (architecture-scribe, fact-check,
// §26.4/§26.6) -- this function must find the RIGHT pair, not merely ANY
// start+completed-finish pair, which is exactly why it correlates by
// SubTaskID rather than counting starts and finishes independently.
//
// false for every other case: no starts at all; a counter-reviewer start
// with no matching finish at all (still "active" from the trace's own
// point of view, or the finish event simply has not landed yet -- see
// this function's own caller in httpapi/reviewverdict.go for the accepted
// race this covers); a matching finish whose Outcome is "failed" or
// "cancelled" rather than "completed"; or a finish that exists for a
// DIFFERENT subTaskId than any counter-reviewer start (never
// cross-matched to a start it does not actually belong to).
//
// Zero I/O, zero time.Now(), zero randomness (CLAUDE.md/§11) -- a pure
// function of its two plain-typed slice arguments, exactly like
// driftcanary.go's own FilesChangedDrifted immediately alongside it in
// this same package.
func CounterReviewCorroborated(starts []SubTaskStartRecord, finishes []SubTaskFinishRecord) bool {
	completedSubTaskIDs := make(map[string]bool, len(finishes))
	for _, f := range finishes {
		if f.Outcome == counterReviewFinishOutcomeCompleted {
			completedSubTaskIDs[f.SubTaskID] = true
		}
	}

	for _, s := range starts {
		if s.SubAgentType != review.CounterReviewerAgentName {
			continue
		}
		if completedSubTaskIDs[s.SubTaskID] {
			return true
		}
	}
	return false
}
