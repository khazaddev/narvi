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

// reviewQueryKeys (§26.1/§12.2 item 2, §15.2/§15.3/§12.2 item 9) -- the
// code-review and release-review views' own data sources, both scoped by
// session id like sessionQueryKeys above.
export const reviewQueryKeys = {
  readout: (sessionId: string) => ['session', sessionId, 'review'] as const,
  releaseManifest: (sessionId: string) => ['session', sessionId, 'release-manifest'] as const,
}

// falsePositivePatternQueryKeys (§22.4) -- the per-repo audit view,
// parameterized by repoFullName (owner/repo combined, matching the
// server's own repo_full_name convention) rather than owner+repo
// separately, since every caller already has the combined form on hand.
export const falsePositivePatternQueryKeys = {
  list: (repoFullName: string) => ['false-positive-patterns', repoFullName] as const,
}

// planQueryKeys (§8.1/§12.2 item 3) -- the plan-mode view's own
// data source, scoped by session id like sessionQueryKeys above.
export const planQueryKeys = {
  list: (sessionId: string) => ['session', sessionId, 'plans'] as const,
}

// automationQueryKeys (§8.4/§12.2 item 4) -- the automations
// view's own list (parameterized by the SAME creator/status filter shape
// listAutomations accepts) and per-automation detail/invocations queries.
export const automationQueryKeys = {
  list: (filter: { createdBy?: string; status?: string }) => ['automations', 'list', filter.createdBy ?? null, filter.status ?? null] as const,
  detail: (automationId: string) => ['automations', automationId, 'detail'] as const,
  invocations: (automationId: string) => ['automations', automationId, 'invocations'] as const,
}
