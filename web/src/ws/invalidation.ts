import type { QueryClient, QueryKey } from '@tanstack/react-query'

import { planQueryKeys, reviewQueryKeys, sessionQueryKeys } from '../api/queryKeys'
import type { EventEnvelope } from './types'

// invalidation.ts is the "-> query invalidation" stage of §12.1's
// pipeline: it maps a newly-folded EventEnvelope's own `type` to the
// TanStack Query keys (api/queryKeys.ts) that just went stale, and
// invalidates them. sessionStream.ts calls invalidateForEvents with only
// the NEWLY inserted events from one EventLog.appendMany call (never
// already-seen ones -- see that file's own top comment), so a redelivered
// replay can never cause a redundant invalidation storm either.
//
// The event-type strings below are real, not invented: confirmed by
// reading internal/app/sessionactor directly (dispatch.go/timerfired.go/
// sandboxevent.go), not guessed from the sandbox-ws schema's own $def
// names alone (those are PascalCase message names, e.g. "ToolCall"; the
// events.type STRING persisted and later replayed to the client is
// whatever cmd.Type/eventType the actor actually wrote, e.g. "tool_call"):
//   - "execution_complete", "warning": internal/app/sessionactor/
//     timerfired.go's own appendEvent(ctx, tx, "execution_complete", ...)
//     / appendEvent(ctx, tx, "warning", ...).
//   - "tool_call", "heartbeat": internal/app/sessionactor/sandboxevent.go
//     switches directly on cmd.Type == "tool_call" / == "heartbeat", and
//     appendRawEvent(ctx, tx, cmd.Type, ...) persists that same string
//     verbatim as the event's own type.
//   - "artifact": one of the same cmd.Type-sourced sandbox-ws event kinds
//     (contracts/sandbox-ws/v1/events.schema.json's own "Artifact" $def),
//     persisted the same way.
// This is a representative, not exhaustive, mapping -- it exists to prove
// the mechanism (a named event type drives a specific, narrow set of
// invalidations, not a blanket "invalidate everything" on every event);
// Each view extends this table as its own queries are added, the same
// way api/endpoints.ts's own route coverage grows.
type InvalidationRule = (sessionId: string) => QueryKey[]

const EVENT_TYPE_INVALIDATION: Record<string, InvalidationRule> = {
  // A review/manifest verdict is posted via a server-side REST tool call
  // mid-turn (§8.2/§26.1), never its own distinct sandbox-ws event type --
  // execution_complete (a turn boundary) is the closest real signal that
  // one MAY have landed, so the merge-readout/release-manifest queries
  // piggyback on it rather than polling. An occasional over-broad refetch
  // when no verdict actually posted this turn is the same accepted cost
  // DEFAULT_RULE's own doc comment already accepts below. planQueryKeys
  // piggybacks for the identical reason: a NEW plan version is
  // created exactly when a plan_mode=true turn completes -- there is no
  // dedicated "plan_created" sandbox-ws event type either (migrations/
  // 000034_plan_mode.up.sql's own doc comment: the plans row is written in
  // the SAME transaction as the producing turn's own terminal-state write,
  // internal/app/sessionactor/planrecord.go).
  execution_complete: (sessionId) => [
    sessionQueryKeys.detail(sessionId),
    sessionQueryKeys.events(sessionId),
    reviewQueryKeys.readout(sessionId),
    reviewQueryKeys.releaseManifest(sessionId),
    planQueryKeys.list(sessionId),
  ],
  tool_call: (sessionId) => [sessionQueryKeys.events(sessionId)],
  artifact: (sessionId) => [sessionQueryKeys.artifacts(sessionId)],
  warning: (sessionId) => [sessionQueryKeys.events(sessionId)],
  heartbeat: (sessionId) => [sessionQueryKeys.detail(sessionId)],
}

// An event type not in the table above still invalidates the session's
// own events query -- a genuinely NEW event of an unrecognized type is
// still new history, and rendering it as if it never arrived is a worse
// failure than an occasional over-broad refetch. Never "invalidate
// nothing" as the fallback.
const DEFAULT_RULE: InvalidationRule = (sessionId) => [sessionQueryKeys.events(sessionId)]

export function queryKeysForEvent(sessionId: string, event: EventEnvelope): QueryKey[] {
  const rule = EVENT_TYPE_INVALIDATION[event.type] ?? DEFAULT_RULE
  return rule(sessionId)
}

/** invalidateForEvents invalidates the de-duplicated union of query keys implied by `events`, and returns that union (test convenience -- callers do not need the return value). */
export function invalidateForEvents(queryClient: QueryClient, sessionId: string, events: readonly EventEnvelope[]): QueryKey[] {
  const seen = new Map<string, QueryKey>()
  for (const event of events) {
    for (const key of queryKeysForEvent(sessionId, event)) {
      seen.set(JSON.stringify(key), key)
    }
  }
  for (const key of seen.values()) {
    void queryClient.invalidateQueries({ queryKey: key })
  }
  return Array.from(seen.values())
}
