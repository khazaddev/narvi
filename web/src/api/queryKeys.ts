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

// sessionListQueryKeys (Step 82, §12.2 item 1) -- the sidebar's own list
// query, namespaced separately from sessionQueryKeys above (which is
// always parameterized per single session id): the list is parameterized
// per FILTER instead ("mine"/"all"), a completely different axis.
export const sessionListQueryKeys = {
  list: (filter: 'mine' | 'all') => ['sessions', 'list', filter] as const,
}

// authQueryKeys (Step 81, §13.1) -- the sign-in view's own "am I signed
// in, and as whom" query (GET /api/me). One key, no params: there is only
// ever one meaningful "current caller" per browser session, unlike
// sessionQueryKeys above (which is parameterized per session id).
export const authQueryKeys = {
  me: () => ['auth', 'me'] as const,
}

// modelCatalogQueryKeys (§8.8) -- the composer's own model/effort
// selector. One key, no params: GET /api/models returns one deployment-
// wide catalog (a compiled-in snapshot, modelcatalog.go's own doc
// comment), not something parameterized per session.
export const modelCatalogQueryKeys = {
  all: () => ['models'] as const,
}
