// Typed endpoint functions over http.ts's generic request<T> -- the
// "typed API client" half of §12.1's data layer. Every request/response
// shape below is imported from contracts/gen/ts/rest-dtos.ts (via the
// "@narvi/contracts" alias); this file adds no field, renames no key, and
// declares no interface of its own for any of them -- see http.ts's own
// top comment for the full "why a thin wrapper, not a bigger generator"
// writeup.
//
// Routes mirrored 1:1 from cmd/control-plane/main.go's own
// router.Route("/api/sessions", ...) block (§6.3) -- only the slice
// web/src/ws (this Step's own pipeline demo/tests) actually needs today:
// create a session, read it back, mint a client WS token, and page
// through its REST event history. The remaining ~15 routes under
// /api/sessions (uploads, plans, review, ...) are exactly as typeable
// through this SAME request<T> + rest-dtos.ts pattern -- Steps 81+ add
// them as each view needs one, not speculatively here.
import type {
  ApplySuggestionResponse,
  ArtifactsResponse,
  ConfirmUploadResponse,
  CreateSessionRequest,
  CreateTurnRequest,
  CreateTurnResponse,
  EventsResponse,
  FalsePositivePattern,
  ListFalsePositivePatternsResponse,
  ListSessionsResponse,
  Member,
  MintUploadRequest,
  MintUploadResponse,
  ModelCatalog,
  RebutFindingRequest,
  ReleaseManifestReadout,
  ReviewFinding,
  ReviewReadout,
  Session,
  WSTokenResponse,
} from '@narvi/contracts/rest-dtos'

import { request } from './http'

export function createSession(body: CreateSessionRequest, signal?: AbortSignal): Promise<Session> {
  return request<Session>('/api/sessions', { method: 'POST', body, signal })
}

export function getSession(sessionId: string, signal?: AbortSignal): Promise<Session> {
  return request<Session>(`/api/sessions/${encodeURIComponent(sessionId)}`, { signal })
}

// -- Step 82 (§12.2 item 1): the session workspace sidebar's own list. --

/**
 * listSessions calls GET /api/sessions?filter=mine|all -- the sidebar's
 * own data source (status chips, My sessions/All filter). filter mirrors
 * the REST route's own two accepted values exactly (listsessions.go);
 * this wrapper does not default it itself, so the query key a caller
 * builds always names the actual filter in effect.
 */
export function listSessions(filter: 'mine' | 'all', signal?: AbortSignal): Promise<ListSessionsResponse> {
  return request<ListSessionsResponse>(`/api/sessions?filter=${filter}`, { signal })
}

export function listSessionEvents(sessionId: string, options: { cursor?: string; limit?: number } = {}, signal?: AbortSignal): Promise<EventsResponse> {
  const params = new URLSearchParams()
  if (options.cursor !== undefined) params.set('cursor', options.cursor)
  if (options.limit !== undefined) params.set('limit', String(options.limit))
  const query = params.size > 0 ? `?${params.toString()}` : ''
  return request<EventsResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/events${query}`, { signal })
}

export function mintWsToken(sessionId: string, signal?: AbortSignal): Promise<WSTokenResponse> {
  return request<WSTokenResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/ws-token`, { method: 'POST', signal })
}

export function createTurn(sessionId: string, body: CreateTurnRequest, signal?: AbortSignal): Promise<CreateTurnResponse> {
  return request<CreateTurnResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/turns`, { method: 'POST', body, signal })
}

// -- Step 81 (§12.2 item 7, §13.1): sign-in view's own two endpoints. --

/**
 * getMe calls GET /api/me -- the authenticated caller's own role +
 * currently-linked identities (internal/adapters/inbound/httpapi/me.go),
 * reusing the SAME generated Member shape GET /api/members returns for
 * each row. Resolves to a 401 ApiError (via http.ts's own request<T>)
 * when no valid session cookie is present -- callers distinguish "not
 * signed in" from a genuine failure by checking `error instanceof
 * ApiError && error.status === 401`, never by string-matching the
 * message.
 */
export function getMe(signal?: AbortSignal): Promise<Member> {
  return request<Member>('/api/me', { signal })
}

/**
 * logout calls POST /auth/logout (internal/adapters/inbound/auth/
 * logout.go) -- revokes the real user_sessions row server-side and clears
 * the narvi_auth_session cookie in the response. Idempotent (see that
 * handler's own doc comment); the 204 it returns carries no body, so
 * http.ts's own request<T> resolves this to undefined -- there is nothing
 * for a caller to do with the result beyond knowing the call completed.
 */
export function logout(signal?: AbortSignal): Promise<undefined> {
  return request<undefined>('/auth/logout', { method: 'POST', signal })
}

// -- §12.2 item 1 / §28: the composer's model/effort selector,
// file attachment, and the rail's own artifacts panel. --

/** listArtifacts calls GET /api/sessions/:id/artifacts (§6.3) -- the rail's own Artifacts panel data source, invalidated automatically on every 'artifact' WS event (ws/invalidation.ts's own existing EVENT_TYPE_INVALIDATION entry, wired before this Step ever needed it). */
export function listArtifacts(sessionId: string, signal?: AbortSignal): Promise<ArtifactsResponse> {
  return request<ArtifactsResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/artifacts`, { signal })
}

/** getModelCatalog calls GET /api/models (§8.8) -- the composer's own model/effort selector data source. Available to every authenticated role including viewer (modelcatalog.go's own doc comment). */
export function getModelCatalog(signal?: AbortSignal): Promise<ModelCatalog> {
  return request<ModelCatalog>('/api/models', { signal })
}

/** mintUpload calls POST /api/sessions/:id/uploads (§28.4/§28.5) -- the browser-facing mint variant (attachments.ts's own runUpload calls this as step 1 of mint -> PUT -> confirm). */
export function mintUpload(sessionId: string, body: MintUploadRequest, signal?: AbortSignal): Promise<MintUploadResponse> {
  return request<MintUploadResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/uploads`, { method: 'POST', body, signal })
}

/** confirmUpload calls POST /api/sessions/:id/uploads/:uploadId/complete (§28.4/§28.6) -- attachments.ts's own runUpload calls this as step 3, after the direct-to-storage PUT. */
export function confirmUpload(sessionId: string, uploadId: string, signal?: AbortSignal): Promise<ConfirmUploadResponse> {
  return request<ConfirmUploadResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/uploads/${encodeURIComponent(uploadId)}/complete`, { method: 'POST', signal })
}

// -- code review + release review (§26.1/§12.2 item 2, §15.2/§15.3/§12.2 item 9). --

/** getReviewReadout calls GET /api/sessions/:id/review -- the code-review view's own merge-readout data source (digest, findings, history, epistemic heads-up). */
export function getReviewReadout(sessionId: string, signal?: AbortSignal): Promise<ReviewReadout> {
  return request<ReviewReadout>(`/api/sessions/${encodeURIComponent(sessionId)}/review`, { signal })
}

/** getReleaseManifestReadout calls GET /api/sessions/:id/release-manifest -- the dedicated release-review screen's own data source. */
export function getReleaseManifestReadout(sessionId: string, signal?: AbortSignal): Promise<ReleaseManifestReadout> {
  return request<ReleaseManifestReadout>(`/api/sessions/${encodeURIComponent(sessionId)}/release-manifest`, { signal })
}

/** retriggerReview calls POST /api/sessions/:id/review/retrigger (§12.2 item 2's "re-run action") -- admin/maintainer only server-side (authz.ActionRetriggerReview); the button itself is rendered role-aware but the server is the real gate. */
export function retriggerReview(sessionId: string, signal?: AbortSignal): Promise<CreateTurnResponse> {
  return request<CreateTurnResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/review/retrigger`, { method: 'POST', signal })
}

/** rebutReviewFinding calls POST /api/sessions/:id/review/findings/:identityHash/rebut (§22.1) -- maintainer+ only server-side. */
export function rebutReviewFinding(sessionId: string, identityHash: string, body: RebutFindingRequest, signal?: AbortSignal): Promise<ReviewFinding> {
  return request<ReviewFinding>(`/api/sessions/${encodeURIComponent(sessionId)}/review/findings/${encodeURIComponent(identityHash)}/rebut`, { method: 'POST', body, signal })
}

/** applySuggestion calls POST /api/sessions/:id/review/findings/:identityHash/apply-suggestion (§12.2 item 2) -- maintainer+ only server-side. */
export function applySuggestion(sessionId: string, identityHash: string, signal?: AbortSignal): Promise<ApplySuggestionResponse> {
  return request<ApplySuggestionResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/review/findings/${encodeURIComponent(identityHash)}/apply-suggestion`, { method: 'POST', signal })
}

/** listFalsePositivePatterns calls GET /api/repos/:owner/:repo/false-positive-patterns (§22.4) -- the per-repo audit view, maintainer+ only server-side (authz.ActionManageFalsePositivePatterns). */
export function listFalsePositivePatterns(owner: string, repo: string, signal?: AbortSignal): Promise<ListFalsePositivePatternsResponse> {
  return request<ListFalsePositivePatternsResponse>(`/api/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/false-positive-patterns`, { signal })
}

/** retireFalsePositivePattern calls POST /api/repos/:owner/:repo/false-positive-patterns/:patternId/retire (§22.4) -- maintainer+ only server-side. */
export function retireFalsePositivePattern(owner: string, repo: string, patternId: string, signal?: AbortSignal): Promise<FalsePositivePattern> {
  return request<FalsePositivePattern>(`/api/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/false-positive-patterns/${encodeURIComponent(patternId)}/retire`, { method: 'POST', signal })
}
