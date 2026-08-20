import type { QueryClient, QueryKey } from '@tanstack/react-query'

import { sessionQueryKeys } from '../api/queryKeys'
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
// Steps 81+ extend this table as each view's own queries are added, the
// same way api/endpoints.ts's own route coverage grows.
type InvalidationRule = (sessionId: string) => QueryKey[]

const EVENT_TYPE_INVALIDATION: Record<string, InvalidationRule> = {
  execution_complete: (sessionId) => [sessionQueryKeys.detail(sessionId), sessionQueryKeys.events(sessionId)],
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
