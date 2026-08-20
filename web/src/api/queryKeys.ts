// Centralized TanStack Query key factory (§12.1's own "-> query
// invalidation" pipeline stage, ws/invalidation.ts, needs a single source
// of truth for these keys -- a view's own useQuery call and a reducer's
// own invalidateQueries call independently constructing "the same" key by
// hand is exactly the kind of drift this factory exists to make
// impossible). No views consume this yet (Steps 81+ do); it lands here
// because the WS pipeline (ws/invalidation.ts) is this Step's own first
// consumer.
//
// Keys are plain arrays, not objects, and namespaced by the REST resource
// they mirror (api/endpoints.ts) -- `sessionQueryKeys.detail(id)` names
// exactly the same conceptual resource GET /api/sessions/:id returns.
export const sessionQueryKeys = {
  /** All session-scoped queries for one session -- invalidating this key invalidates every query below it too (TanStack Query's own prefix-matching). */
  all: (sessionId: string) => ['session', sessionId] as const,
  detail: (sessionId: string) => ['session', sessionId, 'detail'] as const,
  events: (sessionId: string) => ['session', sessionId, 'events'] as const,
  artifacts: (sessionId: string) => ['session', sessionId, 'artifacts'] as const,
}
