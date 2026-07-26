/* eslint-disable */
/**
 * This file was automatically generated from /contracts JSON Schemas by
 * json-schema-to-typescript (contracts/scripts/generate-ts.mjs).
 * DO NOT EDIT IT BY HAND — edit the source .schema.json file and re-run
 * `npm run generate` instead.
 */

/**
 * REST DTOs (§6.3). SCOPE NOTE: §6.3 names the full BFF-facing route surface (sessions, events, artifacts, secrets, environments, automations, uploads, ws-token) but only sessions, events, artifacts, and ws-token are specified in enough field-level detail anywhere in the technical plan to schema honestly today (Step 19's own plan row: 'REST endpoints the UI needs (create/get/events/artifacts)'). Secrets/environments/automations/uploads DTOs are deliberately NOT modeled here — they belong to the PRs that define those features (environments: PR-10/26/27; automations: PR-46/47; uploads: PR-49). This is a scope decision, not an oversight (see contracts/README.md). This schema also models the 8 members/audit-log shapes for §13.2/§13.3's own members API (Identity, Member, PendingLinkPrompt, ListMembersResponse, AuditLogEntry, ListAuditLogResponse, UpdateMemberRoleRequest, LinkMemberIdentityRequest) — promoted here as a pure migration off hand-written Go structs in internal/adapters/inbound/httpapi/members.go (see contracts/README.md's own 'Members/audit-log DTOs' section). All of these shapes are independent named payloads, not a discriminated union, so there is deliberately no top-level oneOf. Field nullability convention: 'nullable' means a required key whose value may be JSON null. Enums here MUST match the Postgres enums in migrations/000004_sessions.up.sql exactly.
 */
export interface RestDtos {
  [k: string]: unknown;
}
/**
 * Mirrors the sessions table (migrations/000004_sessions.up.sql). status/failureReason/spawnSource enums match session_status/session_failure_reason/session_spawn_source exactly.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "Session".
 */
export interface Session {
  id: string;
  /**
   * Null until the session_title WS event (§6.1) sets it.
   */
  title: string | null;
  /**
   * Matches Postgres session_status exactly.
   */
  status: 'created' | 'active' | 'completed' | 'failed' | 'cancelled';
  /**
   * Matches Postgres session_failure_reason exactly. Null except on a terminal non-completed path.
   */
  failureReason: 'cancelled' | 'failed' | 'timeout' | 'never_started' | null;
  archived: boolean;
  /**
   * Matches Postgres session_spawn_source exactly.
   */
  spawnSource: 'web' | 'slack' | 'linear' | 'github';
  /**
   * Null for bot/automation-created sessions with no direct human user.
   */
  createdBy: string | null;
  createdAt: string;
  updatedAt: string;
}
/**
 * The one CreateSessionRequest shape used by every ingress surface (§10 Phase-3 milestone: 'atomic dedupe, one CreateSessionRequest').
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "CreateSessionRequest".
 */
export interface CreateSessionRequest {
  /**
   * Matches Postgres session_spawn_source exactly.
   */
  spawnSource: 'web' | 'slack' | 'linear' | 'github';
  title: string | null;
  /**
   * Initial prompt text; null to create the session without dispatching a first turn.
   */
  prompt: string | null;
  /**
   * Same shape as SESSION_CONFIG's repos (contracts/session-config/v1) — mirrored here, not $ref'd, since REST and SESSION_CONFIG are independently versioned contracts.
   *
   * @minItems 1
   */
  repos: [
    {
      name: string;
      url: string;
      /**
       * Null means create the session branch from the repo's default base branch.
       */
      branch: string | null;
    },
    ...{
      name: string;
      url: string;
      /**
       * Null means create the session branch from the repo's default base branch.
       */
      branch: string | null;
    }[]
  ];
  /**
   * Null means use the default model catalog entry.
   */
  modelId: string | null;
  planMode: boolean;
  /**
   * Optional (Step 37, 'plan mode, web', §12.2 item 3). Like pathScope/mockConfig below, this key is genuinely OPTIONAL (may be absent from the request body entirely) -- only meaningful when planMode is true: the model the eventual approval-dispatched IMPLEMENTATION turn should use, distinct from modelId (which names the PLAN turn's own model). Absent/null means 'use the default model catalog entry', the same convention modelId itself already establishes. Stored as sessions.build_model_id (migrations/000034_plan_mode.up.sql) -- a session-level, set-once value, unlike modelId/planMode which are per-turn (CreateTurnRequest does NOT carry this field: a 'request changes' turn never resubmits it).
   */
  buildModelId?: string | null;
  /**
   * Optional (row 10, 'domain: Environment scoping', §14.1). Absent or null means today's exact unscoped behavior, unchanged: no environments row is created and the session's environmentId/provenanceTag stay null. A non-empty list of sparse-checkout glob patterns creates a new, session-scoped Environment row (internal/domain/environment.ValidatePathScope validates each pattern; the first invalid pattern is rejected with 400 before any Postgres write). Unlike every other field on this DTO, this key is genuinely OPTIONAL (may be absent from the request body entirely), not merely nullable -- there is no separately-managed, ID-referenced Environment entity to reference here yet (see this schema's own top-level SCOPE NOTE above).
   */
  pathScope?: string[] | null;
  /**
   * Optional (row 27, 'mocking + contract drift', §14.3). Like pathScope above, this key is genuinely OPTIONAL (may be absent from the request body entirely) and independent of it -- an Environment can be path-scoped, mock-configured, both, or neither (§14.1: 'an optional path_scope ... and an optional mock_config'). Presence of this key in the request body -- even as {} with contractsPath absent/null -- means: mark this session's Environment mock_configured=true, and store a contracts path, resolved as literal "contracts/api" when contractsPath is absent/null, otherwise the caller's own exact value. Absence of this key entirely leaves mock_configured=false and contracts_path=NULL, today's exact behavior, unchanged. A non-empty pathScope is NOT required for mockConfig to be accepted -- either optional attribute alone is sufficient to create a new, session-scoped Environment row.
   */
  mockConfig?: {
    /**
     * Repo-relative path to the contract-driven mock spec directory (§14.3: 'a shared contracts/api/*.{yaml,json} spec'). Absent or null means the literal default "contracts/api"; a non-null value is stored verbatim, with no validation beyond what mockConfig's own containing object already requires.
     */
    contractsPath?: string | null;
  } | null;
}
/**
 * POST /api/sessions/:id/turns (Step 28, 'turn recovery', §8.7 'relaunch-and-resume: conversation id replay'). Enqueues a new turn on an EXISTING session -- mirrors CreateSessionRequest's own prompt/modelId/planMode fields exactly (same shape, not reinvented) for the turn's own dispatch-time inputs. Deliberately has NO 'resume'/'conversationId' field of its own: sessions.opencode_conversation_id (already persisted across turns, §3.3) is threaded into every dispatched Prompt automatically by the control plane's own dispatch logic, so a new turn on a session that already has one continues that same OpenCode conversation with no separate request field needed.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "CreateTurnRequest".
 */
export interface CreateTurnRequest {
  /**
   * The turn's prompt text. Unlike CreateSessionRequest.prompt, this is required and non-null: this request's entire purpose is enqueuing one new turn, so there is no 'no turn' case to support here.
   */
  prompt: string;
  /**
   * Null means use the default model catalog entry -- same convention as CreateSessionRequest.modelId.
   */
  modelId: string | null;
  planMode: boolean;
}
/**
 * 201 response body for POST /api/sessions/:id/turns: the newly created turn's own id/status only -- callers already have the full session state via GET /api/sessions/:id or the WS stream, so this endpoint's own job is confirming the enqueue, not re-describing the whole session.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "CreateTurnResponse".
 */
export interface CreateTurnResponse {
  id: string;
  /**
   * Matches Postgres turn_status exactly (migrations/000005_turns.up.sql) -- always "pending" for a freshly created turn today, but modeled as the full enum for forward-compatibility rather than a literal, matching Session.status's own precedent above.
   */
  status: 'pending' | 'dispatched' | 'processing' | 'completed' | 'failed' | 'cancelled';
}
/**
 * §6.2: per-participant, hashed at rest, 24h TTL, minted via POST /api/sessions/:id/ws-token.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "WSTokenResponse".
 */
export interface WSTokenResponse {
  token: string;
  expiresAt: string;
}
/**
 * GET /api/sessions/:id/events (§6.3). Mirrors client-ws/v1's own FetchHistoryResponse shape exactly, for the same reason that schema gives: the full event-payload shape is assembled by later PRs, and REST/WS should not diverge on this envelope.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "EventsResponse".
 */
export interface EventsResponse {
  events: {
    [k: string]: unknown;
  }[];
  /**
   * Null when there are no more pages.
   */
  nextCursor: string | null;
}
/**
 * GET /api/sessions/:id/artifacts (§6.3). Unbounded (no pagination) -- this list is expected to stay small.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ArtifactsResponse".
 */
export interface ArtifactsResponse {
  artifacts: {
    [k: string]: unknown;
  }[];
}
/**
 * One linked-identity row's own REST wire shape (§13.2/§13.3 members API) -- returned both standalone (POST/DELETE .../identities) and nested inside Member.identities. provider/linkedVia enums match the Postgres identity_provider/identity_linked_via enums (migrations/000003_identities.up.sql) exactly.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "Identity".
 */
export interface Identity {
  id: string;
  /**
   * Matches Postgres identity_provider exactly.
   */
  provider: 'github' | 'slack' | 'linear' | 'google';
  externalId: string;
  /**
   * Matches Postgres identity_linked_via exactly.
   */
  linkedVia: 'auto_email' | 'prompt' | 'admin';
  createdAt: string;
}
/**
 * One member's own REST wire shape -- role + every identity currently linked to them (§13.3: 'linked identity chips'). role matches the Postgres user_role enum exactly. Every endpoint that returns a Member (ListMembers, UpdateMemberRole) populates identities with the target's own actual currently-linked identities -- never null, empty only when the member genuinely has none linked.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "Member".
 */
export interface Member {
  id: string;
  email: string;
  displayName: string;
  /**
   * Matches Postgres user_role exactly.
   */
  role: 'admin' | 'maintainer' | 'member' | 'viewer';
  disabled: boolean;
  createdAt: string;
  identities: Identity[];
}
/**
 * One still-present identity_link_prompts row's own REST wire shape -- deliberately carries NO nonce/nonce hash (a bearer secret, never surfaced over this read endpoint), just enough for an admin-facing view to show 'someone from Slack/Linear is waiting to connect their account' (§13.2: 'pending-link state'). provider matches the Postgres identity_provider enum exactly.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PendingLinkPrompt".
 */
export interface PendingLinkPrompt {
  /**
   * Matches Postgres identity_provider exactly.
   */
  provider: 'github' | 'slack' | 'linear' | 'google';
  externalId: string;
  expiresAt: string;
  createdAt: string;
}
/**
 * GET /api/members's own response body (§13.2/§13.3): every user with role/disabled and their own currently-linked identities, plus every system-wide still-pending link prompt.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ListMembersResponse".
 */
export interface ListMembersResponse {
  members: Member[];
  pendingLinkPrompts: PendingLinkPrompt[];
}
/**
 * One audit_log row's own REST wire shape (migrations/000013_audit_log.up.sql).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "AuditLogEntry".
 */
export interface AuditLogEntry {
  id: string;
  /**
   * Null for a system/automation-attributed entry with no direct actor user.
   */
  actorUserId: string | null;
  action: string;
  resourceType: string;
  resourceId: string;
  /**
   * Arbitrary per-action JSON detail (audit_log.detail_json) -- always an object, defaults to {} at the DB layer. Modeled as an opaque raw-JSON passthrough (goJSONSchema -> encoding/json.RawMessage), not a decoded map[string]interface{}, so the response byte stream reproduces the stored jsonb verbatim -- a decode-into-map-then-re-marshal step would risk silently converting any integer beyond 2^53 to a lossy float64 (audit finding: LOW, decode-then-re-encode precision loss). This is still validated as a JSON object at the handler layer (members.go's own ListAuditLog) before being accepted verbatim -- see that handler's own doc comment on the per-row degrade-gracefully behavior for a malformed legacy row.
   */
  detail: {
    [k: string]: unknown;
  };
  correlationId: string | null;
  createdAt: string;
}
/**
 * GET /api/audit-log's own response body.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ListAuditLogResponse".
 */
export interface ListAuditLogResponse {
  entries: AuditLogEntry[];
}
/**
 * PATCH /api/members/{userID}/role's own request body. role is deliberately modeled as an unconstrained string, not an enum matching user_role -- it is validated against that closed set at the application layer instead (members.go's own validRoles map), so an unrecognized value surfaces the handler's own specific 'unrecognized role' 400 rather than a generic schema-decode error.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateMemberRoleRequest".
 */
export interface UpdateMemberRoleRequest {
  /**
   * One of admin/maintainer/member/viewer at the application layer (matches Postgres user_role); not enforced here, see this shape's own description.
   */
  role: string;
}
/**
 * POST /api/members/{userID}/identities's own request body. provider is deliberately modeled as an unconstrained string, not an enum matching identity_provider, for the same reason UpdateMemberRoleRequest.role is: application-layer validation (members.go's own validProviders map) owns the closed set and its own 'unrecognized provider' 400 message.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "LinkMemberIdentityRequest".
 */
export interface LinkMemberIdentityRequest {
  /**
   * One of github/slack/linear/google at the application layer (matches Postgres identity_provider); not enforced here, see this shape's own description.
   */
  provider: string;
  externalId: string;
}
