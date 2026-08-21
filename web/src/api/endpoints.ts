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
// through this SAME request<T> + rest-dtos.ts pattern -- later views add
// them as each one needs a route, not speculatively here.
import type {
  ApplySuggestionResponse,
  Automation,
  ArtifactsResponse,
  AuditLogEntry,
  CloudIdentityBinding,
  ClusterBinding,
  ConfirmUploadResponse,
  CreateAutomationRequest,
  CreateAutomationResponse,
  CreateCloudIdentityBindingRequest,
  CreateProviderCredentialRequest,
  CreateSandboxSecretRequest,
  CreateSessionRequest,
  CreateTurnRequest,
  CreateTurnResponse,
  Environment,
  EventsResponse,
  FalsePositivePattern,
  LinkMemberIdentityRequest,
  ListAuditLogResponse,
  ListAutomationInvocationsResponse,
  ListAutomationsResponse,
  ListCloudIdentityBindingsResponse,
  ListDecisionInboxResponse,
  ListEnvironmentsResponse,
  ListFalsePositivePatternsResponse,
  ListMembersResponse,
  ListPlansResponse,
  ListPromptTemplatesResponse,
  ListProviderCredentialsResponse,
  ListSandboxSecretsResponse,
  ListSessionsResponse,
  Member,
  MergePullRequestRequest,
  MergePullRequestResponse,
  MintUploadRequest,
  MintUploadResponse,
  ModelCatalog,
  OpenCodeConfig,
  PlanActionResponse,
  PreviewIntentTemplateRequest,
  PreviewIntentTemplateResponse,
  ProviderCredential,
  PutClusterBindingRequest,
  PutOpenCodeConfigRequest,
  RebutFindingRequest,
  ReleaseManifestReadout,
  RepoDigestScope,
  ReviewAnalytics,
  ReviewFinding,
  ReviewReadout,
  RotateCloudIdentitySigningKeyResponse,
  SandboxSecret,
  Session,
  UpdateCloudIdentityBindingRequest,
  UpdateMemberRoleRequest,
  UpdateProviderCredentialRequest,
  UpdateSandboxSecretRequest,
  UpsertIntentTemplateRequest,
  WSTokenResponse,
} from '@narvi/contracts/rest-dtos'

import { request } from './http'

// -- decision inbox / home view (§16.2/§16.3, decisions 32-34). --

/** listDecisionInbox calls GET /api/decision-inbox -- the home view's own read model: every pending decision addressed to the signed-in caller (ready_to_merge/needs_review/awaiting_approval, plus needs_attention for an admin caller -- enforced server-side, decisioninbox.Build itself, never a client-side filter), already ranked by decision cost then age (§16.1) -- a caller renders this order as-is, never re-sorts. */
export function listDecisionInbox(signal?: AbortSignal): Promise<ListDecisionInboxResponse> {
  return request<ListDecisionInboxResponse>('/api/decision-inbox', { signal })
}

/**
 * mergePullRequest calls POST /api/decision-inbox/merge (§16.2's own Merge
 * endpoint, mockups.html decision 33: "Auto-approved still means
 * human-merged... re-validates CI, approval state, and RBAC server-side at
 * click time"). Sent unconditionally, regardless of the calling
 * component's own role/eligibility check -- RevalidateForMerge
 * (decisioninbox.go) re-checks CI/approval-state/Authorize against live
 * SCM data at click time; the rendered queue this request's own body was
 * built from is never trusted as authority. A 403/409 the server returns
 * surfaces as a genuine ApiError carrying the server's own message,
 * never a generic failure a caller could misattribute.
 */
export function mergePullRequest(body: MergePullRequestRequest, signal?: AbortSignal): Promise<MergePullRequestResponse> {
  return request<MergePullRequestResponse>('/api/decision-inbox/merge', { method: 'POST', body, signal })
}

export function createSession(body: CreateSessionRequest, signal?: AbortSignal): Promise<Session> {
  return request<Session>('/api/sessions', { method: 'POST', body, signal })
}

export function getSession(sessionId: string, signal?: AbortSignal): Promise<Session> {
  return request<Session>(`/api/sessions/${encodeURIComponent(sessionId)}`, { signal })
}

// -- §12.2 item 1: the session workspace sidebar's own list. --

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

// -- §12.2 item 7, §13.1: the sign-in view's own two endpoints. --

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

// -- plan mode (§8.1/§12.2 item 3). --

/** listPlans calls GET /api/sessions/:id/plans -- every plan VERSION for the session, ordered by version, each carrying its own best-effort-extracted content (restdtos.Plan.content's own doc comment). */
export function listPlans(sessionId: string, signal?: AbortSignal): Promise<ListPlansResponse> {
  return request<ListPlansResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/plans`, { signal })
}

/**
 * approvePlan calls POST /api/sessions/:id/plans/:planId/approve
 * (§12.2 item 3's "Approve & build"). Own/joined-aware server-side
 * (authz.ActionApprovePlan, planauthz.go) -- this call is sent regardless
 * of what the client-side affordance decided to render; a caller not
 * actually authorized gets a real 403 back, never a client-side-only
 * refusal (see PlanModeView.tsx's own top doc comment).
 */
export function approvePlan(sessionId: string, planId: string, signal?: AbortSignal): Promise<PlanActionResponse> {
  return request<PlanActionResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/plans/${encodeURIComponent(planId)}/approve`, { method: 'POST', signal })
}

/** rejectPlan calls POST /api/sessions/:id/plans/:planId/reject (§12.2 item 3's "Reject") -- same server-side authorization as approvePlan above. */
export function rejectPlan(sessionId: string, planId: string, signal?: AbortSignal): Promise<PlanActionResponse> {
  return request<PlanActionResponse>(`/api/sessions/${encodeURIComponent(sessionId)}/plans/${encodeURIComponent(planId)}/reject`, { method: 'POST', signal })
}

// -- automations (§8.4/§12.2 item 4). --

/** listAutomations calls GET /api/automations, optionally filtered by creator/status (§8.4's own "creator/status filters" -- mockups.html's "My automations ▾ / All statuses ▾" toolbar). No extra RBAC beyond signed-in (automations.go's own doc comment). */
export function listAutomations(filter: { createdBy?: 'me' | string; status?: 'active' | 'paused' } = {}, signal?: AbortSignal): Promise<ListAutomationsResponse> {
  const params = new URLSearchParams()
  if (filter.createdBy !== undefined) params.set('createdBy', filter.createdBy)
  if (filter.status !== undefined) params.set('status', filter.status)
  const query = params.size > 0 ? `?${params.toString()}` : ''
  return request<ListAutomationsResponse>(`/api/automations${query}`, { signal })
}

/** createAutomation calls POST /api/automations -- admin/maintainer only server-side (authz.ActionManageAutomations); the button itself is rendered role-aware but the server is the real gate. */
export function createAutomation(body: CreateAutomationRequest, signal?: AbortSignal): Promise<CreateAutomationResponse> {
  return request<CreateAutomationResponse>('/api/automations', { method: 'POST', body, signal })
}

/** getAutomation calls GET /api/automations/:id. */
export function getAutomation(automationId: string, signal?: AbortSignal): Promise<Automation> {
  return request<Automation>(`/api/automations/${encodeURIComponent(automationId)}`, { signal })
}

/** listAutomationInvocations calls GET /api/automations/:id/invocations (the automations UI's own "runs table" addition) -- this automation's own most recent invocations, newest first, each with its own nested runs (automationinvocations.go's own doc comment). */
export function listAutomationInvocations(automationId: string, signal?: AbortSignal): Promise<ListAutomationInvocationsResponse> {
  return request<ListAutomationInvocationsResponse>(`/api/automations/${encodeURIComponent(automationId)}/invocations`, { signal })
}

/** pauseAutomation calls POST /api/automations/:id/pause -- admin/maintainer only server-side (authz.ActionManageAutomations). */
export function pauseAutomation(automationId: string, signal?: AbortSignal): Promise<Automation> {
  return request<Automation>(`/api/automations/${encodeURIComponent(automationId)}/pause`, { method: 'POST', signal })
}

/** resumeAutomation calls POST /api/automations/:id/resume -- same server-side gate as pauseAutomation above; this is the mockup's own "auto-paused chip + Resume" action. */
export function resumeAutomation(automationId: string, signal?: AbortSignal): Promise<Automation> {
  return request<Automation>(`/api/automations/${encodeURIComponent(automationId)}/resume`, { method: 'POST', signal })
}

// -- members & access, audit log (§13.2/§13.3, §12.2 item 5). --

/** listMembers calls GET /api/members -- every user with role/disabled and their own currently-linked identities, plus every system-wide still-pending link prompt. Admin-only server-side (authz.ActionManageMembers). */
export function listMembers(signal?: AbortSignal): Promise<ListMembersResponse> {
  return request<ListMembersResponse>('/api/members', { signal })
}

/** updateMemberRole calls PATCH /api/members/:userId/role -- admin-only server-side. */
export function updateMemberRole(userId: string, body: UpdateMemberRoleRequest, signal?: AbortSignal): Promise<Member> {
  return request<Member>(`/api/members/${encodeURIComponent(userId)}/role`, { method: 'PATCH', body, signal })
}

/** linkMemberIdentity calls POST /api/members/:userId/identities -- admin manual link, admin-only server-side. */
export function linkMemberIdentity(userId: string, body: LinkMemberIdentityRequest, signal?: AbortSignal): Promise<Member> {
  return request<Member>(`/api/members/${encodeURIComponent(userId)}/identities`, { method: 'POST', body, signal })
}

/** unlinkMemberIdentity calls DELETE /api/members/:userId/identities/:identityId -- admin-only server-side. */
export function unlinkMemberIdentity(userId: string, identityId: string, signal?: AbortSignal): Promise<Member> {
  return request<Member>(`/api/members/${encodeURIComponent(userId)}/identities/${encodeURIComponent(identityId)}`, { method: 'DELETE', signal })
}

/**
 * listAuditLog calls GET /api/audit-log -- ONE PAGE of audit_log rows,
 * newest first, never the whole log (members.go's own ListAuditLog:
 * defaultAuditLogPageSize 50, maxAuditLogPageSize 200, plus ?offset=).
 *
 * The page size is passed explicitly rather than defaulted so the caller
 * cannot mistake the response for the complete table: this comment used to
 * say "every audit_log row", and the members screen was built on that
 * reading -- filtering a 50-row page client-side and then rendering "No
 * entries." as though it were a statement about a member's entire history.
 * On an audit surface that is the worst possible thing to get wrong, so the
 * shape of this signature now makes the paging impossible to overlook.
 */
export function listAuditLog(params: { limit: number; offset: number }, signal?: AbortSignal): Promise<ListAuditLogResponse> {
  const query = new URLSearchParams({ limit: String(params.limit), offset: String(params.offset) })
  return request<ListAuditLogResponse>(`/api/audit-log?${query.toString()}`, { signal })
}

// -- environments (§14.1) --

/** listEnvironments calls GET /api/environments -- every environments row, newest-first. Maintainer+ only server-side (authz.ActionManageEnvironments); see httpapi/environments.go's own doc comment for why this is list-only, no create/update. */
export function listEnvironments(signal?: AbortSignal): Promise<ListEnvironmentsResponse> {
  return request<ListEnvironmentsResponse>('/api/environments', { signal })
}

// -- prompt templates (§18.6, §12.2 item 5). --

/** listPromptTemplates calls GET /api/intent-templates -- every prompt_templates row, ordered by name. Admin-only server-side (authz.ActionActivatePromptTemplate). */
export function listPromptTemplates(signal?: AbortSignal): Promise<ListPromptTemplatesResponse> {
  return request<ListPromptTemplatesResponse>('/api/intent-templates', { signal })
}

/** previewIntentTemplate calls POST /api/intent-templates/preview -- assembles a DRAFT template's text against real variable values, never touching Postgres. Admin-only server-side. */
export function previewIntentTemplate(body: PreviewIntentTemplateRequest, signal?: AbortSignal): Promise<PreviewIntentTemplateResponse> {
  return request<PreviewIntentTemplateResponse>('/api/intent-templates/preview', { method: 'POST', body, signal })
}

/** upsertIntentTemplate calls POST /api/intent-templates -- creates or overwrites a named template's text. Admin-only server-side. */
export function upsertIntentTemplate(body: UpsertIntentTemplateRequest, signal?: AbortSignal) {
  return request<{ name: string; template: string; updatedAt: string }>('/api/intent-templates', { method: 'POST', body, signal })
}

// -- digest scope, review analytics (§21.3/§21.1, §12.2 item 5). --

/** getRepoDigestScope calls GET /api/repos/:owner/:repo/digest-scope -- which Slack channels/Linear organizations would receive this repo's own next daily digest (derived, read-only -- see httpapi/digestscope.go's own doc comment for why there is no editable cadence/scope setting). Every role including viewer (authz.ActionViewAnalytics). */
export function getRepoDigestScope(owner: string, repo: string, signal?: AbortSignal): Promise<RepoDigestScope> {
  return request<RepoDigestScope>(`/api/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/digest-scope`, { signal })
}

/** getReviewAnalytics calls GET /api/repos/:owner/:repo/review-analytics -- the review-risk analytics section's own read model (§21.1), each rollup carrying its own independent "not yet computed" sentinel. Every role including viewer. */
export function getReviewAnalytics(owner: string, repo: string, signal?: AbortSignal): Promise<ReviewAnalytics> {
  return request<ReviewAnalytics>(`/api/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}/review-analytics`, { signal })
}

// -- secrets scope resolution (§27.1/§25.1, §12.2 item 5). --
//
// sandbox-secrets and provider-credentials both partition into the SAME
// 3 scopes (repo/environment/global), each its own separately-gated REST
// route group on the server (environments.go's own doc comment on why
// this mirrors that split). SecretScope + secretScopePath below name the
// ONE place that partition is expressed client-side, rather than 6
// independently-hand-written base-path strings (3 scopes x 2 resources)
// drifting from each other over time.

export type SecretScope = { kind: 'repo'; owner: string; repo: string } | { kind: 'environment'; environmentId: string } | { kind: 'global' }

function secretScopePath(resource: 'sandbox-secrets' | 'provider-credentials', scope: SecretScope): string {
  switch (scope.kind) {
    case 'repo':
      return `/api/repos/${encodeURIComponent(scope.owner)}/${encodeURIComponent(scope.repo)}/${resource}`
    case 'environment':
      return `/api/environments/${encodeURIComponent(scope.environmentId)}/${resource}`
    case 'global':
      return `/api/${resource}`
  }
}

/** listSandboxSecrets calls GET on the sandbox-secrets route group matching scope -- every row at that (scope, scopeTarget), NEVER the secret value itself (only SandboxSecret.maskedValue, a fixed non-secret placeholder). */
export function listSandboxSecrets(scope: SecretScope, signal?: AbortSignal): Promise<ListSandboxSecretsResponse> {
  return request<ListSandboxSecretsResponse>(secretScopePath('sandbox-secrets', scope), { signal })
}

/** createSandboxSecret calls POST on the sandbox-secrets route group matching scope. body.value is sent once, over this one request, and is never echoed back by any response this client ever reads. */
export function createSandboxSecret(scope: SecretScope, body: CreateSandboxSecretRequest, signal?: AbortSignal): Promise<SandboxSecret> {
  return request<SandboxSecret>(secretScopePath('sandbox-secrets', scope), { method: 'POST', body, signal })
}

/** updateSandboxSecretValue calls PUT on the sandbox-secrets route group matching scope -- rotates ONLY the value; name/scope are immutable once created. */
export function updateSandboxSecretValue(scope: SecretScope, secretId: string, body: UpdateSandboxSecretRequest, signal?: AbortSignal): Promise<SandboxSecret> {
  return request<SandboxSecret>(`${secretScopePath('sandbox-secrets', scope)}/${encodeURIComponent(secretId)}`, { method: 'PUT', body, signal })
}

/** deleteSandboxSecret calls DELETE on the sandbox-secrets route group matching scope. */
export function deleteSandboxSecret(scope: SecretScope, secretId: string, signal?: AbortSignal): Promise<undefined> {
  return request<undefined>(`${secretScopePath('sandbox-secrets', scope)}/${encodeURIComponent(secretId)}`, { method: 'DELETE', signal })
}

/** listProviderCredentials calls GET on the provider-credentials route group matching scope -- every row at that (scope, scopeTarget), one per configured AI provider, NEVER the credential value itself (only ProviderCredential.maskedValue). */
export function listProviderCredentials(scope: SecretScope, signal?: AbortSignal): Promise<ListProviderCredentialsResponse> {
  return request<ListProviderCredentialsResponse>(secretScopePath('provider-credentials', scope), { signal })
}

/** createProviderCredential calls POST on the provider-credentials route group matching scope. */
export function createProviderCredential(scope: SecretScope, body: CreateProviderCredentialRequest, signal?: AbortSignal): Promise<ProviderCredential> {
  return request<ProviderCredential>(secretScopePath('provider-credentials', scope), { method: 'POST', body, signal })
}

/** updateProviderCredentialValue calls PUT on the provider-credentials route group matching scope -- rotates ONLY the value. */
export function updateProviderCredentialValue(scope: SecretScope, credentialId: string, body: UpdateProviderCredentialRequest, signal?: AbortSignal): Promise<ProviderCredential> {
  return request<ProviderCredential>(`${secretScopePath('provider-credentials', scope)}/${encodeURIComponent(credentialId)}`, { method: 'PUT', body, signal })
}

/** deleteProviderCredential calls DELETE on the provider-credentials route group matching scope. */
export function deleteProviderCredential(scope: SecretScope, credentialId: string, signal?: AbortSignal): Promise<undefined> {
  return request<undefined>(`${secretScopePath('provider-credentials', scope)}/${encodeURIComponent(credentialId)}`, { method: 'DELETE', signal })
}

// -- cloud identity: OIDC bindings, cluster binding, signing-key rotation (§27.3/§27.4, §12.2 item 5). --

export type CloudIdentityBindingScope = { kind: 'environment'; environmentId: string } | { kind: 'global' }

function cloudIdentityBindingsPath(scope: CloudIdentityBindingScope): string {
  return scope.kind === 'environment' ? `/api/environments/${encodeURIComponent(scope.environmentId)}/cloud-identity-bindings` : '/api/cloud-identity-bindings'
}

/** listCloudIdentityBindings calls GET on the cloud-identity-bindings route group matching scope. A 503 ApiError means the capability is unconfigured (RequireCloudIdentityCapability, fail-closed) -- callers should render "no rotation/binding affordance at all" for that case, never retry it as a transient error. */
export function listCloudIdentityBindings(scope: CloudIdentityBindingScope, signal?: AbortSignal): Promise<ListCloudIdentityBindingsResponse> {
  return request<ListCloudIdentityBindingsResponse>(cloudIdentityBindingsPath(scope), { signal })
}

/** createCloudIdentityBinding calls POST on the cloud-identity-bindings route group matching scope. params carries identifiers only, never a secret (CloudIdentityBinding.params' own doc comment). */
export function createCloudIdentityBinding(scope: CloudIdentityBindingScope, body: CreateCloudIdentityBindingRequest, signal?: AbortSignal): Promise<CloudIdentityBinding> {
  return request<CloudIdentityBinding>(cloudIdentityBindingsPath(scope), { method: 'POST', body, signal })
}

/** updateCloudIdentityBinding calls PUT on the cloud-identity-bindings route group matching scope -- rotates audience/params; kind is immutable once created. */
export function updateCloudIdentityBinding(scope: CloudIdentityBindingScope, bindingId: string, body: UpdateCloudIdentityBindingRequest, signal?: AbortSignal): Promise<CloudIdentityBinding> {
  return request<CloudIdentityBinding>(`${cloudIdentityBindingsPath(scope)}/${encodeURIComponent(bindingId)}`, { method: 'PUT', body, signal })
}

/** deleteCloudIdentityBinding calls DELETE on the cloud-identity-bindings route group matching scope. */
export function deleteCloudIdentityBinding(scope: CloudIdentityBindingScope, bindingId: string, signal?: AbortSignal): Promise<undefined> {
  return request<undefined>(`${cloudIdentityBindingsPath(scope)}/${encodeURIComponent(bindingId)}`, { method: 'DELETE', signal })
}

/** getEnvironmentClusterBinding calls GET /api/environments/:id/cluster-binding -- the (at most one, per-Environment) cluster binding. A 404 ApiError means no binding exists for this Environment yet. */
export function getEnvironmentClusterBinding(environmentId: string, signal?: AbortSignal): Promise<ClusterBinding> {
  return request<ClusterBinding>(`/api/environments/${encodeURIComponent(environmentId)}/cluster-binding`, { signal })
}

/** putEnvironmentClusterBinding calls PUT /api/environments/:id/cluster-binding -- create-or-replace (upsert), the singleton-resource convention every §27 config table here uses. */
export function putEnvironmentClusterBinding(environmentId: string, body: PutClusterBindingRequest, signal?: AbortSignal): Promise<ClusterBinding> {
  return request<ClusterBinding>(`/api/environments/${encodeURIComponent(environmentId)}/cluster-binding`, { method: 'PUT', body, signal })
}

/** deleteEnvironmentClusterBinding calls DELETE /api/environments/:id/cluster-binding. */
export function deleteEnvironmentClusterBinding(environmentId: string, signal?: AbortSignal): Promise<undefined> {
  return request<undefined>(`/api/environments/${encodeURIComponent(environmentId)}/cluster-binding`, { method: 'DELETE', signal })
}

/** rotateCloudIdentitySigningKey calls POST /api/cloud-identity/signing-keys/rotate -- admin-only, destructive-adjacent (§27.3/§27.8): mints a fresh signing key and retires the previous one after the JWKS overlap window. Never call this without an explicit user confirmation first -- see the Settings view's own confirm-before-rotate UI. */
export function rotateCloudIdentitySigningKey(signal?: AbortSignal): Promise<RotateCloudIdentitySigningKeyResponse> {
  return request<RotateCloudIdentitySigningKeyResponse>('/api/cloud-identity/signing-keys/rotate', { method: 'POST', signal })
}

// -- OpenCode config (§27.2, §12.2 item 5). --

export type OpenCodeConfigScope = { kind: 'environment'; environmentId: string } | { kind: 'global' }

function openCodeConfigPath(scope: OpenCodeConfigScope): string {
  return scope.kind === 'environment' ? `/api/environments/${encodeURIComponent(scope.environmentId)}/opencode-config` : '/api/opencode-config'
}

/** getOpenCodeConfig calls GET on the opencode-config route matching scope -- returned in FULL, plaintext (this is configuration, not secret material -- OpenCodeConfig's own doc comment). A 404 ApiError means no document has been saved for this scope yet. */
export function getOpenCodeConfig(scope: OpenCodeConfigScope, signal?: AbortSignal): Promise<OpenCodeConfig> {
  return request<OpenCodeConfig>(openCodeConfigPath(scope), { signal })
}

/** putOpenCodeConfig calls PUT on the opencode-config route matching scope -- create-or-replace. */
export function putOpenCodeConfig(scope: OpenCodeConfigScope, body: PutOpenCodeConfigRequest, signal?: AbortSignal): Promise<OpenCodeConfig> {
  return request<OpenCodeConfig>(openCodeConfigPath(scope), { method: 'PUT', body, signal })
}

/** deleteOpenCodeConfig calls DELETE on the opencode-config route matching scope. */
export function deleteOpenCodeConfig(scope: OpenCodeConfigScope, signal?: AbortSignal): Promise<undefined> {
  return request<undefined>(openCodeConfigPath(scope), { method: 'DELETE', signal })
}

export type { Environment, Member, AuditLogEntry, SandboxSecret, ProviderCredential, CloudIdentityBinding, ClusterBinding, OpenCodeConfig, ReviewAnalytics, RepoDigestScope }
