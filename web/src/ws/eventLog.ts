import type { EventEnvelope } from './types'

// EventLog is the "event log" stage of §12.1's "WS transport -> event log
// -> reducer -> query invalidation" pipeline: an append-only, deduplicated,
// id-ordered store of EventEnvelopes (types.ts).
//
// # Why dedup is load-bearing here, not defensive-programming boilerplate
//
// Every (re)subscribe over the client WS (contracts/client-ws/v1/
// protocol.schema.json) re-sends up to initialReplayLimit=200 of a
// session's OLDEST events (internal/adapters/inbound/wshub/client.go:
// `events.ListForSession(ctx, sessionID, 0, initialReplayLimit)` --
// afterID=0, i.e. from the very start of history, every single time,
// including on reconnect -- confirmed by reading that file directly, not
// assumed). fetch_history backfill (sessionStream.ts) walks the same
// already-seen range forward again after every reconnect for the same
// reason. A reducer that folds this log by naive arrival/insertion order
// would double-count every one of those events on every reconnect --
// exactly the "double counters" defect class this repo has already paid
// to learn twice (see this Step's own PR description). Dedup by `id` here
// is what makes appendMany idempotent under that redelivery, so the
// reducer (reducer.ts) never has to know redelivery happens at all.
//
// # Why NOT dedup live-broadcast frames the same way
//
// A live broadcast frame (internal/app/ports/eventbroadcaster.go's own
// doc: "sent exactly as stored... there is no separate wrapper envelope
// for it") carries NEITHER an id NOR, in general, a type -- it is the
// bare events.payload column, verbatim (internal/app/sessionactor/
// actor.go: `a.pendingBroadcast = append(a.pendingBroadcast, raw)` where
// `raw` is the SAME bytes written to the payload column, nothing else).
// There is structurally no key to dedup a raw broadcast frame against an
// EventEnvelope by. This module therefore never accepts a raw broadcast
// frame as an entry at all -- see sessionStream.ts's own top comment for
// how a live broadcast is used instead (as a low-latency "go re-fetch"
// signal, not as log data).
export class EventLog {
  private readonly byId = new Map<number, EventEnvelope>()
  // Kept sorted by id ascending at all times, so entries() never has to
  // re-sort on every read -- appends are O(log n) find + O(n) splice,
  // which is fine at the event volumes a single session's log holds
  // (client.go's own initialReplayLimit=200 gives a sense of scale: this
  // is not a data structure built for millions of rows).
  private ordered: EventEnvelope[] = []

  /** append inserts event if event.id has not been seen before. Returns true iff it was newly inserted (false = deduped). */
  append(event: EventEnvelope): boolean {
    if (this.byId.has(event.id)) {
      return false
    }
    this.byId.set(event.id, event)
    const index = lowerBound(this.ordered, event.id)
    this.ordered.splice(index, 0, event)
    return true
  }

  /** appendMany applies append to each event in turn and returns the subset that was newly inserted, in the SAME relative order they were passed in (not necessarily id order) -- callers (sessionStream.ts) use exactly this list to decide what to feed the reducer/invalidation, so a duplicate never reaches either. */
  appendMany(events: readonly EventEnvelope[]): EventEnvelope[] {
    const inserted: EventEnvelope[] = []
    for (const event of events) {
      if (this.append(event)) {
        inserted.push(event)
      }
    }
    return inserted
  }

  has(id: number): boolean {
    return this.byId.has(id)
  }

  get size(): number {
    return this.ordered.length
  }

  /** highestId returns the greatest event id currently in the log, or null when empty -- the cursor sessionStream.ts's backfill loop resumes fetch_history from. */
  highestId(): number | null {
    return this.ordered.length === 0 ? null : this.ordered[this.ordered.length - 1].id
  }

  /** entries returns the full id-ordered log as a fresh array (safe for a caller to hold onto -- this class never mutates a previously-returned array in place). */
  entries(): EventEnvelope[] {
    return this.ordered.slice()
  }

  /** reset clears the log entirely. Not called by sessionStream.ts on an ordinary reconnect (see that file's own comment for why re-merging, not resetting, is the deliberate choice there) -- provided for callers that genuinely need to start over (e.g. switching which session is being viewed). */
  reset(): void {
    this.byId.clear()
    this.ordered = []
  }
}

// lowerBound returns the index of the first entry whose id is >= target,
// i.e. where target should be spliced in to keep `arr` sorted ascending.
function lowerBound(arr: readonly EventEnvelope[], targetId: number): number {
  let lo = 0
  let hi = arr.length
  while (lo < hi) {
    const mid = (lo + hi) >>> 1
    if (arr[mid].id < targetId) {
      lo = mid + 1
    } else {
      hi = mid
    }
  }
  return lo
}
