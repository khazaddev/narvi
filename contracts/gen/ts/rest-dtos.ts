/* eslint-disable */
/**
 * This file was automatically generated from /contracts JSON Schemas by
 * json-schema-to-typescript (contracts/scripts/generate-ts.mjs).
 * DO NOT EDIT IT BY HAND — edit the source .schema.json file and re-run
 * `npm run generate` instead.
 */

/**
 * REST DTOs (§6.3). SCOPE NOTE: §6.3 names the full BFF-facing route surface (sessions, events, artifacts, secrets, environments, automations, uploads, ws-token) but only sessions, events, artifacts, ws-token, and (as of Step 52) automations are specified in enough field-level detail anywhere in the technical plan to schema honestly today (Step 19's own plan row: 'REST endpoints the UI needs (create/get/events/artifacts)'). Secrets/environments/uploads DTOs are still deliberately NOT modeled here — they belong to the PRs that define those features (environments: PR-10/26/27; uploads: Step 58). This is a scope decision, not an oversight (see contracts/README.md). This schema also models the 8 members/audit-log shapes for §13.2/§13.3's own members API (Identity, Member, PendingLinkPrompt, ListMembersResponse, AuditLogEntry, ListAuditLogResponse, UpdateMemberRoleRequest, LinkMemberIdentityRequest) — promoted here as a pure migration off hand-written Go structs in internal/adapters/inbound/httpapi/members.go (see contracts/README.md's own 'Members/audit-log DTOs' section). It also models the 3 plan-mode shapes for GET/POST /api/sessions/:id/plans... (Plan, ListPlansResponse, PlanActionResponse) — audit-fix batch, closing findings M3 (a GET .../plans discoverability gap Step 37 left open) and L14/L16 (promoting planapprove.go's own hand-written planActionResponse now that this same area has a real DTO-consuming sibling endpoint). Step 52 ('automations: triggers & extras', §8.4) adds Automation/CreateAutomationRequest/CreateAutomationResponse/ListAutomationsResponse: the REST surface over migrations/000051_automations.up.sql + 000055_automations_triggers_and_extras.up.sql. triggerConfig is deliberately modeled as an opaque JSON object (mirroring AuditLogEntry.detail's own 'opaque raw-JSON passthrough' precedent immediately below), not a discriminated union keyed on triggerType -- its actual required sub-fields differ per trigger type (schedule for cron; event/action/label for github; eventType/action/teamKey for linear; empty for manual/webhook) and are validated at the application layer (internal/domain/automation's own ValidateCronTriggerConfig/ValidateGitHubTriggerConfig/ValidateLinearTriggerConfig), the same 'closed vocabulary enforced in Go, not in the schema' convention UpdateMemberRoleRequest.role/LinkMemberIdentityRequest.provider already establish. All of these shapes are independent named payloads, not a discriminated union, so there is deliberately no top-level oneOf. Field nullability convention: 'nullable' means a required key whose value may be JSON null. Enums here MUST match the Postgres enums in migrations/000004_sessions.up.sql exactly (plan-mode's own status enum instead matches migrations/000034_plan_mode.up.sql's plan_status; automation's own status/triggerType/lastRunStatus enums match migrations/000051_automations.up.sql/000055_automations_triggers_and_extras.up.sql/000052_automation_invocations.up.sql respectively). Step 53 ('provider credential injection', §25.1/§25.3) adds ProviderCredential/CreateProviderCredentialRequest/UpdateProviderCredentialRequest/ListProviderCredentialsResponse: the REST surface over this codebase's first secret-storage table (migrations/000056_provider_credentials.up.sql). ProviderCredential is deliberately write-only for its own underlying secret value -- the credential itself is accepted on create/update (CreateProviderCredentialRequest.value/UpdateProviderCredentialRequest.value) but NEVER appears in any response shape; ProviderCredential.maskedValue is a fixed, non-secret placeholder proving a value is configured, not a partial reveal of it. Step 54 ('domain/workflow + loopguard + schema', §25.4/§25.10) adds the workflow-engine DTO set over migrations/000057_workflows.up.sql: WorkflowDefinition/WorkflowStepDefinition/WorkflowEdge (the authorable graph -- WorkflowDefinition doubles as the eventual editing surface's PUT body, always the full desired state per UpdateRepoSettingsRequest's own 'never a partial patch' convention, and a PUT/DELETE against an isBuiltIn=true definition is refused unconditionally, a structural invariant, not an RBAC row -- §25.4), WorkflowBinding (which definition+version a (lane, repoFullName) resolves to; repoFullName null = the global binding, seeded per lane and never absent), the read-only WorkflowRun/WorkflowStepRun execution ledger, and WorkflowStepDecideRequest/Response (the §25.9 HITL verdict, same response shape as PlanActionResponse). All workflow enums match the Postgres workflow_* enums in migrations/000057 exactly. NO HTTP handler consumes any of these yet -- Step 54 is dark (schema/contracts/RBAC only); Steps 55-56 mount the first routes. Step 61 ('builder epistemic pre-action check', §20) adds CreateSessionRequest.epistemicCheckEnabled (this session's own optional override of the platform-wide default, mirroring buildModelId's own optional-key convention) and PostEpistemicOutcomeRequest/Response, the sandbox-bearer-authenticated tool POST /sessions/:id/turn/epistemic-outcome mints for the devil's-advocate preamble's own required structured signal (§20.2) -- outcome matches the Postgres turn_epistemic_outcome enum in migrations/000066 exactly, mirroring PostWorkflowStepOutcomeRequest's own shape and 'no run/step ids from the caller' convention one layer down, at the turn. Step 62 ('review verdict persistence, analytics, digest & automated approval', §21) extends RepoSettings with the auto-merge toggle, the eligibility engine's two per-repo criteria, and the contradiction-rate calibration triple; adds UpdateAutoApprovalSettingsRequest/UpdateAutoMergeToggleRequest, each its own separately-gated PUT endpoint (see RepoSettings/UpdateRepoSettingsRequest's own doc comments for why PutRepoSettings itself was not extended instead); and adds ReviewAnalytics, the read-only GET /api/repos/{owner}/{repo}/review-analytics response body over §21.1's three analytics rollups (timeseries, top-risk-driver breakdown, the 'Review finding outcomes' KPI) -- each carrying its own independent 'not yet computed' sentinel (internal/domain/reviewverdict's own Timeseries/TopRiskDrivers/FindingOutcomes), gated by the existing authz.ActionViewAnalytics (§13.3 row 1: every role, including viewer, may read analytics -- no new Action needed).
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
  /**
   * Step 59 (§29.8). Reasoning-effort override for this session's first turn; null means use the default. Required-nullable, mirroring modelId's own convention exactly -- valid values are owned per-model by OpenCode's own catalog `variants` maps (GET /api/models, this Step's own catalog endpoint), never a Narvi-side enum.
   */
  effort: string | null;
  planMode: boolean;
  /**
   * Optional (Step 37, 'plan mode, web', §12.2 item 3). Like pathScope/mockConfig below, this key is genuinely OPTIONAL (may be absent from the request body entirely) -- only meaningful when planMode is true: the model the eventual approval-dispatched IMPLEMENTATION turn should use, distinct from modelId (which names the PLAN turn's own model). Absent/null means 'use the default model catalog entry', the same convention modelId itself already establishes. Stored as sessions.build_model_id (migrations/000034_plan_mode.up.sql) -- a session-level, set-once value, unlike modelId/planMode which are per-turn (CreateTurnRequest does NOT carry this field: a 'request changes' turn never resubmits it).
   */
  buildModelId?: string | null;
  /**
   * Optional (Step 59, §29.8), mirroring buildModelId's own optional-key convention exactly, one field over: the reasoning effort the eventual approval-dispatched IMPLEMENTATION turn should use, distinct from effort (which names the PLAN turn's own effort). Absent/null means 'use the default'. Stored as sessions.build_effort (migrations/000063_turn_session_effort.up.sql).
   */
  buildEffort?: string | null;
  /**
   * Optional (Step 61, 'builder epistemic pre-action check', §20.4), mirroring buildModelId's own optional-key convention exactly: this session's own override of the platform-wide default for the devil's-advocate pre-action check on its own (non-plan-mode) build turns. Absent/null means 'use platform.Config's own global default' (off, unless an operator has turned the default on) -- a non-null value always wins regardless of that default. Stored as sessions.epistemic_check_enabled (migrations/000066_builder_epistemic_check.up.sql). Session-scoped, not turn-scoped, exactly like buildModelId/buildEffort -- CreateTurnRequest does NOT carry this field.
   */
  epistemicCheckEnabled?: boolean | null;
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
  /**
   * Step 59 (§29.8). Reasoning-effort override for this turn; null means use the default -- same convention and required-nullable shape as CreateSessionRequest.effort.
   */
  effort: string | null;
  planMode: boolean;
  /**
   * Optional (Step 58, 'uploads, blob storage & the in-sandbox download_file tool', §28.5). Genuinely OPTIONAL -- may be absent from the request body entirely, matching CreateSessionRequest.pathScope's own precedent, never merely an empty array. Each id must name a status='ready' upload artifact of THIS session -- validated at the turn-creation chokepoint; any unknown, foreign, or not-yet-ready id is refused with a structured 4xx before any turn is created. Absent (or an empty array) means no attachment block is rendered into the turn's own prompt -- a byte-for-byte no-op, not a degraded case.
   */
  attachmentIds?: string[];
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
 * GET /api/sessions/:id/artifacts (§6.3). Unbounded (no pagination) -- this list is expected to stay small. Each element's own status/failureReason fields (Step 58, §28.6) are additive here too, loosely typed like every element in this array already was -- see MintUploadResponse/ConfirmUploadResponse below for the strictly-typed upload-specific shapes this schema DOES pin.
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
 * POST /api/sessions/:id/uploads (Step 58, §28.4/§28.5): declares the file about to be uploaded. The control plane checks sizeBytes against MaxUploadBytes/MaxSessionUploadBytes and returns a presigned PUT URL; an over-limit request is refused with a structured 4xx and no artifact row is created. The sandbox-bearer twin of this same mint (POST /sessions/{sessionID}/uploads, outside /api and outside auth.Middleware) accepts an IDENTICAL JSON body shape but is deliberately not itself modeled here: contracts/rest is this codebase's browser-facing (/api) surface only -- every other sandbox-bearer endpoint (scm-credentials, snapshot, review/verdict, provider-credentials) has always used a plain, un-schema'd Go struct instead of a contracts/rest definition, and this endpoint follows that same established precedent.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "MintUploadRequest".
 */
export interface MintUploadRequest {
  filename: string;
  contentType: string;
  sizeBytes: number;
}
/**
 * 201 response to MintUploadRequest (§28.4): a presigned PUT URL, its own expiry, and the exact headers the uploader must send with the PUT for the signature to verify (ports.PresignedURL.Headers, forwarded verbatim -- e.g. Content-Type).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "MintUploadResponse".
 */
export interface MintUploadResponse {
  uploadId: string;
  putUrl: string;
  headers: {
    [k: string]: string;
  };
  expiresAt: string;
}
/**
 * Response to POST /api/sessions/:id/uploads/:uploadId/complete (§28.4/§28.6): tells the caller the RECORDED outcome -- the artifacts row and its broadcast/persisted event are the durable truth regardless of what this response says (§28.6: 'the confirm response tells the agent honestly whether verification passed... while the row and the event already carry the truth regardless'). Idempotent: a retried confirm of an already-resolved row returns this SAME recorded outcome, never re-verifies, never double-appends.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ConfirmUploadResponse".
 */
export interface ConfirmUploadResponse {
  /**
   * Matches Postgres artifact_status exactly, restricted to the two terminal outcomes confirm itself can ever report (never 'pending').
   */
  status: 'ready' | 'failed';
  /**
   * Matches Postgres artifact_failure_reason exactly. Null when status is 'ready'.
   */
  failureReason: 'size_exceeded' | 'quota_exceeded' | 'verification_failed' | 'abandoned' | null;
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
/**
 * One plan-mode VERSION's own REST wire shape (migrations/000034_plan_mode.up.sql), returned by GET /api/sessions/:id/plans (audit finding M3, completeness: Step 37 shipped approve/reject with no way for a web client to ever discover a planId to approve). Deliberately omits turnId and slack_channel_id/slack_message_ts, both present on the underlying plans row: turnId is an internal linkage to the producing turn's own event stream (where the plan's actual text/steps live, per that migration's own doc comment), not needed for a client whose job here is discovering/approving a planId; slack_channel_id/slack_message_ts (migrations/000035_plan_mode_cross_channel.up.sql) are Slack cross-channel-notify plumbing that should never leak into a REST response, mirroring PlanActionResponse's own equally minimal shape below.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "Plan".
 */
export interface Plan {
  id: string;
  sessionId: string;
  /**
   * 1-based, monotonically increasing per session (internal/domain/plan.NextVersion) -- v1 is the first plan proposed, v2 a 'request changes' revision, and so on.
   */
  version: number;
  /**
   * Matches Postgres plan_status exactly (migrations/000034_plan_mode.up.sql).
   */
  status: 'awaiting_approval' | 'approved' | 'rejected' | 'superseded';
  /**
   * The model that produced this plan version, copied from the producing turn's own model_id AT CREATION TIME (migrations/000034_plan_mode.up.sql's own doc comment on plan_model_id). Null means that turn had no explicit model id (the default model catalog entry).
   */
  planModelId: string | null;
  createdAt: string;
  /**
   * Null while status is 'awaiting_approval'; set the moment a decision (approve/reject, from any entry point) is recorded. goJSONSchema forces the literal *time.Time type (rather than go-jsonschema's own default generated named-pointer-type wrapper, e.g. PlanModelId's own PlanPlanModelId *string above): a NAMED type whose underlying type is *time.Time (e.g. 'type PlanDecidedAt *time.Time') does NOT inherit time.Time's own UnmarshalJSON/MarshalJSON method set in Go (methods attach to the exact named type they're declared on, never promoted across a distinct named-pointer-type indirection), so encoding/json falls through to its generic struct decoder and fails on a date-time STRING value with 'cannot unmarshal string into Go struct field ... of type time.Time' -- this is the first nullable date-time field this schema has ever needed (no prior nullable-date-time property existed to surface this), caught by this batch's own new Plan round-trip test.
   */
  decidedAt: string | null;
  /**
   * The user who decided this plan's verdict. Null while status is 'awaiting_approval', or for a decision attributed to no direct human user.
   */
  decidedBy: string | null;
}
/**
 * GET /api/sessions/:id/plans's own response body (audit finding M3, completeness) -- every plan VERSION for the session, ordered by version, so a web client can render v1->v2 history and find the currently awaiting_approval version's own id to approve/reject. Deliberately minimal: no pagination (a session's own plan history is expected to stay small, matching ArtifactsResponse's own identical 'unbounded' precedent above) and no new WS/event notification on plan creation -- later Steps (decision inbox, plan-mode UI) are already planned to build richer surfaces; this endpoint only closes the discoverability gap.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ListPlansResponse".
 */
export interface ListPlansResponse {
  plans: Plan[];
}
/**
 * 200 response body for POST /api/sessions/:id/plans/:planId/approve and its reject twin (§12.2 item 3) -- promoted from a hand-written Go struct (internal/adapters/inbound/httpapi/planapprove.go's own planActionResponse) now that GET .../plans above gives this same area a real DTO-consuming sibling endpoint, the exact condition that struct's own doc comment named as the trigger to eventually promote it.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PlanActionResponse".
 */
export interface PlanActionResponse {
  planId: string;
  /**
   * The plan's own real, current status after this call -- always 'approved' on a winning approve and 'rejected' on a winning reject in practice (a losing/conflicting call never reaches this response body at all, see DecidePlanOnTx's own doc comment), but modeled as the full plan_status enum for forward-compatibility rather than a literal, matching CreateTurnResponse.status's own identical precedent above.
   */
  status: 'awaiting_approval' | 'approved' | 'rejected' | 'superseded';
  /**
   * The newly enqueued implementation turn's id, set iff this call was ApprovePlan and it won. Always null for RejectPlan (reject never dispatches a new turn).
   */
  turnId: string | null;
}
/**
 * Request body for POST /sessions/:id/review/verdict (Step 47, 'server-side verdict', §8.2/§5.2) -- the verdict-posting tool's own typed-fields call, validated server-side (internal/domain/reviewpost.ValidateVerdictInput). Mirrors internal/domain/review.Verdict's own fields exactly, EXCEPT Shippable itself, which this endpoint always recomputes server-side (review.ComputeShippable) and NEVER accepts from a caller -- see that package's own Verdict.Shippable doc comment (verdict.go) for why. factCheck/factCheckKilled and counterReview are Step 69's own additions (§26.4/§26.6, 'review deep path: adversarial counter-review + readout measurement') -- see those two properties' own doc comments below.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PostReviewVerdictRequest".
 */
export interface PostReviewVerdictRequest {
  /**
   * Matches internal/domain/review.RiskLevel's own three values exactly.
   */
  riskLevel: 'low' | 'medium' | 'high';
  /**
   * Matches internal/domain/review.PremiseState's own three values exactly.
   */
  premise: 'ok' | 'questionable' | 'not_a_pr';
  /**
   * Matches internal/domain/review.Tag's own fixed, closed vocabulary exactly -- an empty array is legal (the reviewer found no tagged area touched).
   */
  blastRadius: (
    'auth' | 'migrations' | 'contracts' | 'secrets' | 'infra' | 'public_api' | 'data_layer' | 'dependencies'
  )[];
  filesChanged: number;
  /**
   * Matches internal/domain/review.TestsCoverageState's own three values exactly.
   */
  testsCoverage: 'adequate' | 'insufficient' | 'skipped';
  /**
   * Matches internal/domain/review.DocsDriftState's own three values exactly.
   */
  docsDrift: 'none' | 'found' | 'skipped';
  /**
   * The MODEL's own self-report (internal/domain/review.ProposedShippable) -- advisory only, carried for audit/transparency, and structurally incapable of influencing the server-computed Shippable this endpoint returns (review.ComputeShippable's own signature does not accept it).
   */
  proposedShippable: 'auto' | 'needs_human' | 'block';
  /**
   * The agent's own free-text narrative explaining the verdict -- required, never re-parsed back out as structured data once posted (review/doc.go's own 'nothing here even imports a markdown parser, on principle' stance).
   */
  summary: string;
  /**
   * Step 48's own additive extension (§8.2/§17/§22.1): zero or more per-finding typed fields, alongside the verdict's own aggregate fields above. OPTIONAL -- absent/empty means this verdict reports no individual findings, exactly like every verdict posted before this Step. See internal/domain/reviewpost/finding.go's own doc comment for why identityHash is NEVER accepted here (server-computed only, from sentinelKind+filePath+description).
   */
  findings?: PostedFinding[];
  digest: Digest;
  /**
   * Step 69's own addition (§26.6, 'diff-only fact-check pass, both paths'): whether the primary reviewer's orchestration spawned the diff-only fact-check sub-task (§7.1's engine-native fan-out, no tool access) before posting this verdict. Matches internal/domain/reviewpost.FactCheckStatus's own two values exactly. REQUIRED UNCONDITIONALLY -- both paths, not merely deep, since the fact-check pass itself runs on every review regardless of depth. Never an input to the server-computed Shippable -- 'skipped' can only mean published findings were not additionally pruned of provably-wrong ones, never that a real defect went unverified (the deliberate, load-bearing difference from counterReview below).
   */
  factCheck: 'done' | 'skipped';
  /**
   * Step 69's own addition (§26.6): the count of findings the fact-check pass actually removed as provably wrong from the diff alone. REQUIRED UNCONDITIONALLY, alongside factCheck above -- MUST be 0 when factCheck is 'skipped' (a skipped pass, by construction, removed nothing; internal/domain/reviewpost.ValidateVerdictInput's own ErrFactCheckKilledOnSkip rejects any other combination).
   */
  factCheckKilled: number;
  /**
   * Step 69's own addition (§26.4, 'the deep path: adversarial counter-review'): whether the primary reviewer's orchestration spawned and adjudicated the counter-reviewer sub-task (§7.1's engine-native fan-out) before posting this verdict. One of 'done'/'skipped' when present, matching internal/domain/review.CounterReviewStatus's own two values -- deliberately modeled as an unconstrained nullable string here, not a schema-level enum (mirroring PostedFinding.sentinelKind's own identical precedent immediately above, itself mirroring UpdateMemberRoleRequest.role's precedent): null/absent is legal on every path (the light path never runs a counter-reviewer at all, §26.9), and the closed vocabulary plus the CONDITIONAL requirement (application-level REQUIRED whenever this session's own server-resolved review-depth is 'deep') are both enforced at the application layer (internal/domain/reviewpost.ValidateVerdictInput's own ErrInvalidCounterReview), which this JSON Schema cannot express (review-depth lives on the turn, not on this payload -- mirrors digest.archDecisions/stackRisks/unverifiedLimits' own identical conditional-requirement shape, Step 68). 'skipped' raises the server-computed Shippable floor to needs_human (review.CounterReviewFloor) -- the deliberate, load-bearing difference from factCheck above, which never raises anything.
   */
  counterReview?: string | null;
}
/**
 * One finding's own typed fields, as posted by the verdict-posting tool call (Step 48) -- NEVER carries an identity hash (server-computed, internal/domain/reviewpost.ComputeFindingIdentity, never client-supplied -- the same 'don't trust the model with anything authoritative' discipline as PostReviewVerdictRequest.proposedShippable).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PostedFinding".
 */
export interface PostedFinding {
  /**
   * Null for an ordinary (non-sentinel) risk-map finding. One of 'coverage'/'docs_drift' when present (§17.1: only these two sentinels can ever trigger the sentinel-auto-fix flow) -- deliberately modeled as an unconstrained nullable string here, not a schema-level enum (mirroring UpdateMemberRoleRequest.role's own identical precedent): the closed vocabulary is enforced at the application layer (internal/domain/reviewpost.ValidateFindingInput), which owns the specific 'unrecognized sentinel kind' 400 message.
   */
  sentinelKind?: string | null;
  /**
   * Reuses review.RiskLevel's own three-tier vocabulary -- one finding's own severity, independent of the verdict's overall riskLevel.
   */
  severity: 'low' | 'medium' | 'high';
  filePath: string;
  /**
   * Informational only -- NEVER part of this finding's own identity hash, so a finding re-reported at a shifted line number is still recognized as the same finding (§22.1).
   */
  line?: number | null;
  description: string;
  /**
   * An optional unified-diff/patch text the apply-suggestion endpoint (§12.2 item 2) can attempt to apply.
   */
  suggestedFix?: string | null;
}
/**
 * Step 66's own additive extension (§26.1, 'review digest: verdict as merge readout'): the merge-readout's typed content -- 'what this PR does', architecture choices, and risks to the stack -- that fronts the rendered verdict, ahead of the pre-existing findings/coverage/docs-drift content (now collapsed into an appendix, internal/domain/reviewpost.RenderVerdictComment). Extended by Step 67 (§26.2, 'description adequacy + graduated remediation') with descriptionAdequacy/adequacyExplanation/proposedBody below, and by Step 68 (§26.3, light/deep review-depth triage) with a CONDITIONAL requirement on three more fields. REQUIRED on the request as a whole (unlike findings above): summary/descriptionAdequacy/adequacyExplanation within it are ALWAYS hard-required (internal/domain/reviewpost.ValidateVerdictInput); archDecisions/stackRisks/unverifiedLimits are requested on every review (the review-turn prompt asks the agent to fill them, internal/domain/review.RenderTurnPrompt) and are ADDITIONALLY hard-required -- rejected when empty/blank -- whenever this session's own review-depth routing decision (turns.review_depth, resolved server-side, never a field on this request body) is 'deep'. This JSON Schema cannot express that condition itself (review-depth lives on the turn, not on this payload) -- see ValidateVerdictInput's own doc comment (validate.go) for the exact, application-level enforced rule. proposedBody remains requested but never required, on every path.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "Digest".
 */
export interface Digest {
  /**
   * 'What this PR does' -- 2-4 sentences written FROM THE DIFF, never copied from the PR's own title/body (untrusted input, §5.2). Distinct from the top-level summary above (this request's own pre-existing free-text narrative explaining the VERDICT) -- see internal/domain/reviewpost.Digest's own doc comment for the full 'why two summaries' explanation.
   */
  summary: string;
  /**
   * Zero or more structural decisions the diff makes -- schema-optional at this JSON Schema level (absent/empty always decodes fine), but application-level REQUIRED (at least one entry with real, non-blank content) whenever this session's own server-resolved review-depth is 'deep' (Step 68, §26.3) -- see this object's own description, and internal/domain/reviewpost.ValidateVerdictInput's own doc comment, for the exact conditional rule this schema alone cannot express.
   */
  archDecisions?: ArchDecision[];
  /**
   * Free-text prose: coupling and deployment risks (migrations, multi-phase deploys, image rebuilds) and reversibility -- rendered alongside the verdict's own existing blastRadius. Schema-optional; application-level REQUIRED non-blank on a 'deep' review-depth turn, mirroring archDecisions above (Step 68, §26.3).
   */
  stackRisks?: string | null;
  /**
   * Free-text prose: what was explicitly NOT verified (honest limits). Schema-optional; application-level REQUIRED non-blank on a 'deep' review-depth turn, mirroring archDecisions above (Step 68, §26.3).
   */
  unverifiedLimits?: string | null;
  /**
   * Step 67's own addition (§26.2): the agent's own comparison of THIS SAME digest's summary (written from the diff) against the pull request's own title+body -- which stay untrusted input throughout, consumed by this comparison, never obeyed by it. Matches internal/domain/review.DescriptionAdequacy's own three values exactly. REQUIRED -- directly feeds review.ComputeShippable's own third raise-only floor (review.AdequacyFloor): 'misleading' floors Shippable at needs_human, 'ok'/'drift' impose no floor of their own.
   */
  descriptionAdequacy: 'ok' | 'drift' | 'misleading';
  /**
   * Step 67's own addition (§26.2): the tri-state's own required one-line explanation of WHY descriptionAdequacy is what it is -- REQUIRED non-blank, mirroring summary's own 'a verdict with no human-readable explanation at all defeats the point' treatment.
   */
  adequacyExplanation: string;
  /**
   * Step 67's own addition (§26.2): the agent's OWN optional rewrite proposal for the pull request's body -- 'the agent MAY rewrite the PR body'. OPTIONAL, not validation-enforced: most reviews propose no rewrite at all. Rendered as a suggestion in the digest for every PR regardless of authorship; ALSO delivered as a real write only for a Narvi-authored PR with this repo's own descriptionAutofix flag on, both checks re-verified server-side at delivery time (never trusted from this payload alone). The PR's title is never rewritten automatically, in either case -- this field carries body content only.
   */
  proposedBody?: string | null;
  /**
   * Step 69's own addition (§26.4, 'the deep path: adversarial counter-review'): free-text prose naming where the primary reviewer's own findings/digest and the counter-reviewer sub-task's own adjudication genuinely disagreed -- 'inter-agent disagreement is precisely the signal that a human must decide'. OPTIONAL, not validation-enforced, on every path -- most deep reviews produce no disagreement at all, and there is no counter-reviewer on the light path to disagree with anything (§26.9).
   */
  contestedPoints?: string | null;
}
/**
 * One structural decision the diff makes (Digest.archDecisions, §26.1's own 'Architecture choices' section): what was decided, the alternative implicitly rejected, and conformance to the repo's own conventions (its agent instructions file -- CLAUDE.md/AGENTS.md -- and its established patterns, already visible to the reviewing agent via its own sandbox checkout, never fetched or injected by this endpoint). No individual field here is REQUIRED at the schema level (no minLength, no 'required' array on THIS object) -- internal/domain/reviewpost's own doc comment (digest.go) is explicit that a submitted-but-incomplete ArchDecision is rendered honestly (its blank field(s) render as empty, never silently dropped) rather than rejected. Digest.archDecisions as a WHOLE, however, is application-level required to contain at least one entry with real, non-blank content in ANY of these three fields whenever this session's own review-depth is 'deep' (Step 68, §26.3, now implemented) -- see Digest.archDecisions' own description, and internal/domain/reviewpost.ValidateVerdictInput's own hasNonBlankArchDecision check, for the exact rule this per-object schema cannot itself express.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ArchDecision".
 */
export interface ArchDecision {
  /**
   * What the diff actually decided.
   */
  decision?: string | null;
  /**
   * The alternative this decision implicitly passed over.
   */
  rejectedAlternative?: string | null;
  /**
   * How this decision conforms to (or diverges from) the repo's own established conventions.
   */
  conventionConformance?: string | null;
}
/**
 * One review_findings row's own REST wire shape (migrations/000046_review_findings.up.sql) -- returned by the rebut and apply-suggestion endpoints (Step 48) so a caller can confirm the resulting state.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ReviewFinding".
 */
export interface ReviewFinding {
  identityHash: string;
  sentinelKind: string | null;
  severity: 'low' | 'medium' | 'high';
  filePath: string;
  line: number | null;
  description: string;
  suggestedFix: string | null;
  /**
   * Matches internal/domain/reviewpost.FindingStatus exactly.
   */
  status: 'open' | 'rebutted' | 'fix_pending' | 'fix_open' | 'fix_merged' | 'fix_applied';
  rebuttalText: string | null;
}
/**
 * Request body for POST /api/sessions/:id/review/findings/:identityHash/rebut (Step 48, §22.1) -- maintainer+ only (authz.ActionEditReviewVerdict).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "RebutFindingRequest".
 */
export interface RebutFindingRequest {
  /**
   * The maintainer's own reason this finding is not a genuine issue.
   */
  rebuttalText: string;
}
/**
 * 200 response body for POST /api/sessions/:id/review/findings/:identityHash/apply-suggestion (Step 48, §12.2 item 2).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ApplySuggestionResponse".
 */
export interface ApplySuggestionResponse {
  identityHash: string;
  /**
   * The new commit this call created on the PR's own head branch, applying the finding's suggestedFix.
   */
  commitSha: string;
}
/**
 * 201 response body for POST /sessions/:id/review/verdict -- the server-computed authoritative results the caller cannot itself derive, so a review agent can log/confirm what actually happened.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PostReviewVerdictResponse".
 */
export interface PostReviewVerdictResponse {
  /**
   * The AUTHORITATIVE, server-computed classification (review.ComputeShippable's own result) -- never the request's own proposedShippable, converted or otherwise.
   */
  shippable: 'auto' | 'needs_human' | 'block';
  /**
   * Which GitHub pull-request-review event this call submitted (internal/domain/reviewpost.ComputeFormalReviewEvent's own result) -- APPROVE is never a legal value here, see that function's own doc comment for why.
   */
  formalReviewEvent: 'COMMENT' | 'REQUEST_CHANGES';
  /**
   * The review:*-risk label (internal/domain/reviewpost.RiskLabel) now reflecting this verdict's own RiskLevel on the pull request.
   */
  syncedLabel: string;
  /**
   * Step 48's own additive extension: the server-computed identityHash for each posted finding, in the SAME order as the request's own findings array -- so a caller can log/correlate them. Absent/empty when the request posted no findings.
   */
  findingIdentityHashes?: string[];
}
/**
 * Request body for POST /sessions/:id/workflow/step-outcome (Step 55, 'workflow execution engine', §25.6) -- the generic step-outcome-posting tool, mirroring PostReviewVerdictRequest's own sandbox-bearer-authenticated-endpoint shape (see reviewverdict.go's doc comment for the full 'why an HTTP endpoint, not a genuine OpenCode/LLM tool-call' reasoning, which applies identically here) but structurally generic rather than review-specific -- internal/domain/reviewpost's existing verdict-posting shape is what this mirrors structurally, per §25.6. Posts onto whichever workflow_step_runs attempt is CURRENTLY the calling session's own live (status='running') one; the caller names no run/step ids at all -- the endpoint resolves that itself from the sandbox-authenticated session id alone.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PostWorkflowStepOutcomeRequest".
 */
export interface PostWorkflowStepOutcomeRequest {
  /**
   * Matches Postgres workflow_step_outcome_status exactly (internal/domain/workflow.StepOutcomeStatus) -- the ONLY vocabulary an Edge may condition on (§25.4), a DISTINCT axis from review.Shippable (§25.8): never routed through it, never inferred from it.
   */
  status: 'ok' | 'needs_fix' | 'blocked';
  /**
   * The agent's own free-text narrative explaining the outcome -- advisory only, required, never re-parsed back out as structured data once posted (§25.6, the same discipline PostReviewVerdictRequest.summary already establishes).
   */
  summary: string;
  /**
   * Optional opaque per-step typed handoff data (§25.6's structuredPayload -- e.g. a future audit step's own review.Verdict + reviewpost.Finding[] payload, out of this Step's own scope). Stored verbatim (workflow_step_runs.outcome_payload JSONB) for whichever later step reads it back -- never interpreted or re-parsed here. Modeled as an opaque raw-JSON passthrough (goJSONSchema -> encoding/json.RawMessage), mirroring AuditLogEntry.detail's own identical precedent, so the stored byte stream round-trips exactly. Absent means this outcome carries no structured handoff data at all.
   */
  structuredPayload?: {
    [k: string]: unknown;
  };
}
/**
 * 201 response body for POST /sessions/:id/workflow/step-outcome (Step 55, §25.6) -- confirms which attempt/run actually recorded the posted outcome.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PostWorkflowStepOutcomeResponse".
 */
export interface PostWorkflowStepOutcomeResponse {
  stepRunId: string;
  workflowRunId: string;
}
/**
 * Request body for POST /sessions/:id/turn/epistemic-outcome (Step 61, 'builder epistemic pre-action check', §20.2) -- the devil's-advocate preamble's own reporting tool, mirroring PostWorkflowStepOutcomeRequest/PostReviewVerdictRequest's own sandbox-bearer-authenticated-endpoint shape exactly (see reviewverdict.go's doc comment for the full 'why an HTTP endpoint, not a genuine OpenCode/LLM tool-call' reasoning, which applies identically here). Posts onto whichever turn is CURRENTLY the calling session's own live (status='processing') one; the caller names no turn id at all -- the endpoint resolves that itself from the sandbox-authenticated session id alone, exactly like the workflow-step-outcome endpoint's own identical convention.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PostEpistemicOutcomeRequest".
 */
export interface PostEpistemicOutcomeRequest {
  /**
   * Matches Postgres turn_epistemic_outcome exactly (internal/domain/turn.EpistemicOutcome) -- §20.1's own two-tier taxonomy (minor/strong) plus none (the check ran and found nothing worth even a MINOR mention). Never inferred from the agent's own natural-language reply (§20.2's own 'never prompt-only' requirement) -- this typed field is the outcome of record.
   */
  outcome: 'none' | 'minor' | 'strong';
}
/**
 * 201 response body for POST /sessions/:id/turn/epistemic-outcome (Step 61, §20.2) -- confirms which turn actually recorded the posted outcome.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PostEpistemicOutcomeResponse".
 */
export interface PostEpistemicOutcomeResponse {
  turnId: string;
}
/**
 * GET/PUT /api/repos/{owner}/{repo}/settings response body (Step 47, §8.2/§21.2) -- an admin, per-repo policy-flag row (migrations/000044_repo_settings.up.sql). Deliberately a small, extensible shape: Step 62's auto-merge toggle, Step 65's automatic-re-review opt-in (§24.5), and Step 67's description-autofix toggle (§26.2) each added a further boolean property here, never a bespoke DTO of their own -- future toggles are expected to follow the same pattern.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "RepoSettings".
 */
export interface RepoSettings {
  /**
   * The natural 'owner/repo' key, matching github_pr_sessions.repo_full_name's own shape.
   */
  repoFullName: string;
  /**
   * §21.2: an admin, per-repo, strict-boolean setting that reuses the verdict-posting tool's SAME formal-review submission path and carries no independent permission of its own -- see internal/domain/reviewpost.ComputeFormalReviewEvent's own doc comment for its exact effect.
   */
  blockOnHighRisk: boolean;
  /**
   * §17.1: admin-only, per-repo, off by default -- enables the sentinel-auto-fix flow (coverage/doc-drift findings spawn a child session that opens its own merge-gated follow-up PR). A stricter gate than blockOnHighRisk/the criteria-driven auto-approval config, since it ends in an unattended merge.
   */
  sentinelAutofixEnabled: boolean;
  /**
   * Step 62, §21.2 stage 2: admin-only, per-repo, off by default -- once armed, an auto-approved PR merges unattended (internal/app/automerge.Worker) instead of surfacing in the decision inbox for a human's 1-click confirm. Gated by authz.ActionToggleAutoMerge, the SAME admin-only row as sentinelAutofixEnabled, never maintainer-level ActionConfigureAutoApprove.
   */
  autoMergeEnabled: boolean;
  /**
   * Step 62, §21.2 stage 1: this repo's own configured diff-size eligibility threshold. Null means 'not configured -- the auto-approval engine's own built-in default applies' (internal/domain/autoapproval.DefaultEligibilityConfig), never a magic sentinel number. Gated by authz.ActionConfigureAutoApprove (maintainer+).
   */
  maxAutoApproveFilesChanged: number | null;
  /**
   * Step 62, §21.2 stage 1: this repo's own configured sensitive-path tag list (internal/domain/review.Tag's own fixed vocabulary). Null means 'not configured -- the auto-approval engine's own default list applies (migrations, auth, contracts)', never an empty-list claim that this repo deliberately has zero sensitive tags. Gated by authz.ActionConfigureAutoApprove (maintainer+).
   */
  sensitiveBlastRadiusTags:
    ('auth' | 'migrations' | 'contracts' | 'secrets' | 'infra' | 'public_api' | 'data_layer' | 'dependencies')[] | null;
  /**
   * Step 62, §21.2 stage 2: false means no auto-approval outcome (confirmed or overridden) has been recorded for this repo yet in the calibration window -- distinct from a real, computed 0% rate (§21.1's own 'not yet computed' sentinel discipline, mirroring ListDecisionInboxResponse.decisionLatencyComputed's own identical triple below).
   */
  contradictionRateComputed: boolean;
  /**
   * Null iff contradictionRateComputed is false. The fraction of auto-approved PRs a human later disagreed with, as a 0-100 percentage -- the figure an admin uses to decide whether to arm autoMergeEnabled.
   */
  contradictionRatePercent: number | null;
  /**
   * How many auto-approval outcomes contradictionRatePercent was computed over -- 0 whenever contradictionRateComputed is false.
   */
  contradictionSampleSize: number;
  /**
   * Step 65, §24.5: admin-only, per-repo, off by default -- once armed, a new commit pushed to a PR with an existing review session automatically enqueues a fresh review turn (after a trailing-edge debounce quiet period, §24.2) instead of waiting for a human's manual re-trigger. Gated by authz.ActionToggleAutoRetriggerReview, the SAME admin-only row as sentinelAutofixEnabled/autoMergeEnabled -- this automation never auto-approves anything on its own, it only ever enqueues an ordinary review turn.
   */
  autoRetriggerReviewEnabled: boolean;
  /**
   * Step 67, §26.2: admin-only, per-repo, off by default -- once armed, a Narvi-authored PR's own description-adequacy floor firing (drift/misleading) may result in this repo's own PR bodies being automatically rewritten (original preserved in a collapsed block), delivered via the outbox. The drift/misleading precondition is enforced at ENQUEUE time (a verdict reporting descriptionAdequacy "ok" never enqueues a rewrite candidate at all, regardless of proposedBody); Narvi-authorship and this flag are independently re-verified FRESH server-side at DELIVERY time, with descriptionAdequacy itself re-asserted a third time from the same verdict (a fact fixed at verdict time, carried rather than re-derived) -- never trusted from the posting agent alone. Gated by authz.ActionToggleDescriptionAutofix, the SAME admin-only row as sentinelAutofixEnabled/autoMergeEnabled/autoRetriggerReviewEnabled -- arming this changes what runs UNATTENDED on a repo's own PRs (an automatic body rewrite, no human in the loop), the same reasoning every sibling toggle in this row already carries. Human-authored PRs are never affected regardless of this flag -- they only ever get a rendered suggestion (Digest.proposedBody), never a write.
   */
  descriptionAutofixEnabled: boolean;
  /**
   * Step 68, §26.3: this repo's own reviewDepth routing mode -- one of "auto"/"always_light"/"always_deep" when set (validated application-side against internal/domain/reviewtriage.Mode's own closed vocabulary, never enforced at the schema level to avoid a nullable-enum's own awkward generated wrapper type). Null means 'not configured -- the engine's own built-in default applies' (internal/domain/reviewtriage.DefaultConfig, mode "auto"), never a magic sentinel string. Gated by authz.ActionConfigureReviewDepth (admin only, §13.3 row 6) -- arming always_deep/always_light changes what runs UNATTENDED on every future PR review (which model/effort tier, and how much cost, every automated review incurs), the same reasoning every sibling toggle in this row already carries.
   */
  reviewDepthMode: string | null;
  /**
   * Step 68, §26.3: this repo's own additional deep-routing glob patterns, layered on top of (never replacing) the engine's own fixed sensitive-glob set (migrations/auth/infra-as-code/CI-workflow). Null means 'no repo-specific deep paths configured'. Gated by authz.ActionConfigureReviewDepth (admin only, same row as reviewDepthMode).
   */
  reviewDepthDeepPaths: string[] | null;
  /**
   * Step 69, §26.7: this repo's own light-path per-review cost ceiling, in USD. Null means 'not configured -- the engine's own built-in default applies' (internal/domain/reviewtriage.DefaultCostBudget, $0.50), never a magic sentinel number. Gated by authz.ActionConfigureReviewCostBudget (admin only, §13.3 row 6, same row as reviewDepthMode) -- arming a non-default ceiling changes the dollar figure a review turn's own prompt renders as a query parameter on its GET to this sandbox's own loopback review-cost-budget endpoint (Step 70; internal/domain/review's own subAgentOrchestrationInstructions renders 'GET {{REVIEW_COST_BUDGET_TOOL_URL}}?ceilingUsd=<this value>'; cmd/sandbox-agent's reviewcostbudgetserver.go answers with a real internal/domain/reviewtriage.ShouldSkipOptionalPass(spentUSD, ceilingUSD) decision, spentUSD read from this turn's own live cost accumulator, internal/adapters/outbound/opencode's turnState.spentUSD). The reviewing agent still has to make that GET and obey the answer -- this control plane has no channel to intervene inside an already-dispatched turn -- but the answer itself is a real, server-computed fact as of Step 70, not the agent's own self-estimate of spend it was asked for before. The same 'changes what an unattended review is TOLD, admin-gated' reasoning every sibling toggle in this row already carries.
   */
  reviewCostBudgetLightUsd: number | null;
  /**
   * Step 69, §26.7: this repo's own deep-path per-review cost ceiling, in USD. Null means 'not configured -- the engine's own built-in default applies' (internal/domain/reviewtriage.DefaultCostBudget, $5.00). Gated by authz.ActionConfigureReviewCostBudget, same row as reviewCostBudgetLightUsd -- see that field's own description for how this ceiling is checked as of Step 70 (a real GET to this sandbox's own loopback review-cost-budget endpoint, answered from a real running spend total, not a self-estimate).
   */
  reviewCostBudgetDeepUsd: number | null;
}
/**
 * Request body for PUT /api/repos/{owner}/{repo}/settings -- always the full, current desired state (never a partial patch), matching RepoSettings' own shape. sentinelAutofixEnabled (Step 48) is deliberately OPTIONAL, not required, exactly like every other additive field this schema has ever grown (e.g. CreateSessionRequest.buildModelId) -- an old caller that only ever knew about blockOnHighRisk keeps compiling/working unchanged; PutRepoSettings' own 'always the full desired state' semantics mean an old caller that omits this key simply (re)sets it to its own safe default (false) alongside whatever it DOES specify, never a partial-patch surprise. Step 62's own §21.2 fields (autoMergeEnabled/maxAutoApproveFilesChanged/sensitiveBlastRadiusTags) are DELIBERATELY NOT on this shared request: this endpoint's own handler requires EVERY permission its fields collectively need (PutRepoSettings' own doc comment, httpapi/reposettings.go), which would force a maintainer authorized only for the auto-approval-config row (§13.3 row 5) through this endpoint's admin-only gates (row 6) too -- see UpdateAutoApprovalSettingsRequest/UpdateAutoMergeToggleRequest below, each its own endpoint with its own single, correctly-scoped gate.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateRepoSettingsRequest".
 */
export interface UpdateRepoSettingsRequest {
  blockOnHighRisk: boolean;
  sentinelAutofixEnabled?: boolean;
}
/**
 * Request body for PUT /api/repos/{owner}/{repo}/auto-approval-settings (Step 62, §21.2 stage 1) -- the auto-approval eligibility engine's own two per-repo-tunable criteria. A SEPARATE endpoint from UpdateRepoSettingsRequest's own PUT /settings, gated SOLELY by authz.ActionConfigureAutoApprove (maintainer+, §13.3 row 5) -- see that DTO's own doc comment for why. Always the full, current desired state for these two fields specifically (never a partial patch) -- the handler read-modify-writes repo_settings.auto_merge_enabled (a DIFFERENT row, gated by a DIFFERENT action) unchanged alongside whichever of these two this call sets.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateAutoApprovalSettingsRequest".
 */
export interface UpdateAutoApprovalSettingsRequest {
  /**
   * Null means 'use the auto-approval engine's own built-in default'.
   */
  maxAutoApproveFilesChanged: number | null;
  /**
   * Null means 'use the auto-approval engine's own built-in default list (migrations, auth, contracts)'.
   */
  sensitiveBlastRadiusTags:
    ('auth' | 'migrations' | 'contracts' | 'secrets' | 'infra' | 'public_api' | 'data_layer' | 'dependencies')[] | null;
}
/**
 * Request body for PUT /api/repos/{owner}/{repo}/auto-merge (Step 62, §21.2 stage 2) -- arms/disarms the per-repo unattended-merge toggle. A SEPARATE endpoint, gated SOLELY by authz.ActionToggleAutoMerge (admin only, §13.3 row 6) -- see UpdateRepoSettingsRequest's own doc comment for why this is not folded into the shared PUT /settings endpoint.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateAutoMergeToggleRequest".
 */
export interface UpdateAutoMergeToggleRequest {
  enabled: boolean;
}
/**
 * Request body for PUT /api/repos/{owner}/{repo}/auto-retrigger-review (Step 65, §24.5) -- arms/disarms the per-repo automatic-re-review-on-new-commits opt-in. A SEPARATE endpoint, gated SOLELY by authz.ActionToggleAutoRetriggerReview (admin only, §13.3 row 6) -- see UpdateRepoSettingsRequest's own doc comment for why this is not folded into the shared PUT /settings endpoint.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateAutoRetriggerReviewToggleRequest".
 */
export interface UpdateAutoRetriggerReviewToggleRequest {
  enabled: boolean;
}
/**
 * Request body for PUT /api/repos/{owner}/{repo}/description-autofix (Step 67, §26.2) -- arms/disarms the per-repo Narvi-authored-PR description-autofix toggle. A SEPARATE endpoint, gated SOLELY by authz.ActionToggleDescriptionAutofix (admin only, §13.3 row 6) -- see UpdateRepoSettingsRequest's own doc comment for why this is not folded into the shared PUT /settings endpoint.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateDescriptionAutofixToggleRequest".
 */
export interface UpdateDescriptionAutofixToggleRequest {
  enabled: boolean;
}
/**
 * Request body for PUT /api/repos/{owner}/{repo}/review-depth (Step 68, §26.3) -- (re)configures this repo's own reviewDepth mode/deepPaths. A SEPARATE endpoint, gated SOLELY by authz.ActionConfigureReviewDepth (admin only, §13.3 row 6) -- see UpdateRepoSettingsRequest's own doc comment for why this is not folded into the shared PUT /settings endpoint. Always the full, current desired state for these two fields specifically (never a partial patch).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateReviewDepthConfigRequest".
 */
export interface UpdateReviewDepthConfigRequest {
  /**
   * One of "auto"/"always_light"/"always_deep" when set (validated application-side, see RepoSettings.reviewDepthMode's own doc comment for why not at the schema level). Null means 'use the engine's own built-in default (auto)'.
   */
  mode: string | null;
  /**
   * Null means 'no repo-specific deep paths'.
   */
  deepPaths: string[] | null;
}
/**
 * Request body for PUT /api/repos/{owner}/{repo}/review-cost-budget (Step 69, §26.7) -- (re)configures this repo's own per-path cost ceilings. A SEPARATE endpoint, gated SOLELY by authz.ActionConfigureReviewCostBudget (admin only, §13.3 row 6) -- see UpdateRepoSettingsRequest's own doc comment for why this is not folded into the shared PUT /settings endpoint. Always the full, current desired state for these two fields specifically (never a partial patch).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateReviewCostBudgetRequest".
 */
export interface UpdateReviewCostBudgetRequest {
  /**
   * Null means 'use the engine's own built-in default ($0.50)'. Validated application-side as strictly POSITIVE -- an explicit 0 is rejected 400, never silently stored: internal/domain/reviewtriage.CostBudget's own zero value means 'no ceiling configured', so a stored 0 here would collide with that sentinel and resolve to unlimited spend, the opposite of an explicit-zero operator's likely intent.
   */
  lightUsd: number | null;
  /**
   * Null means 'use the engine's own built-in default ($5.00)'. Validated application-side as strictly positive, the SAME 'zero collides with the unconfigured sentinel' reasoning as lightUsd above.
   */
  deepUsd: number | null;
}
/**
 * GET /api/repos/{owner}/{repo}/review-analytics response body (Step 62, §21.1) -- the three analytics rollups named in that section's own scope, each bounded to platform.Timeouts.ReviewVerdictAnalyticsWindow (never an unbounded scan) and carrying its OWN independent 'not yet computed' sentinel: 'a repo with a real 0% dismiss rate and a repo with no data yet must never render identically' (§21.1). Step 69, §26.5 adds a fourth rollup, digestContestationRatePercent -- the 'digest precision (contestation rate)' KPI that section names, the SAME 'own independent not-yet-computed sentinel' discipline as the original three. Gated by the existing authz.ActionViewAnalytics (§13.3 row 1) -- every role including viewer.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ReviewAnalytics".
 */
export interface ReviewAnalytics {
  /**
   * The natural 'owner/repo' key, matching github_pr_sessions.repo_full_name's own shape.
   */
  repoFullName: string;
  /**
   * False iff no review_verdicts row exists for this repo within the window -- timeseries is then empty. Unlike topRiskDriversComputed below, this can never be true with an empty timeseries: every posted verdict belongs to some UTC calendar day, so any real data produces at least one bucket (internal/domain/reviewverdict.Timeseries' own doc comment).
   */
  timeseriesComputed: boolean;
  /**
   * One entry per UTC calendar day that had at least one posted verdict within the window, oldest first. Null iff timeseriesComputed is false -- a real, computed timeseries can never itself be an empty array (every posted verdict belongs to some day, so any real data produces at least one bucket), so null is an unambiguous, redundant-with-the-boolean signal here, never a magic empty-vs-absent distinction a client must reason about on its own.
   */
  timeseries: ReviewAnalyticsDayBucket[] | null;
  /**
   * False iff no review_verdicts row exists for this repo within the window. UNLIKE timeseriesComputed, true with an EMPTY (but non-null) topRiskDrivers array below is itself a real, distinct, computed answer -- verdicts exist in the window but none tagged any BlastRadius risk driver (internal/domain/reviewverdict.TopRiskDrivers' own doc comment on why this is never confused with the ok=false sentinel above it).
   */
  topRiskDriversComputed: boolean;
  /**
   * Every review.Tag that appeared in at least one verdict's BlastRadius within the window, sorted by count descending then tag ascending (a fixed, deterministic order -- never Go map iteration order). Null iff topRiskDriversComputed is false. A non-null EMPTY array with topRiskDriversComputed true is the one genuinely ambiguous-looking case this DTO can render, and it is deliberate: real verdicts exist, none tagged a risk driver -- the boolean, never array nullness/emptiness alone, is what a client must branch on.
   */
  topRiskDrivers: ReviewAnalyticsTagCount[] | null;
  /**
   * False iff no review_findings row exists for this repo within the window -- reads a DIFFERENT table than the two rollups above (review_findings, Step 48's mutable per-finding status history, never review_verdicts' own append-only rows -- internal/domain/reviewverdict.FindingOutcomes' own doc comment).
   */
  findingOutcomesComputed: boolean;
  /**
   * Every reviewpost.FindingStatus present in the window, sorted by count descending then status ascending. Null iff findingOutcomesComputed is false -- like timeseries above, a real, computed result can never itself be an empty array (every counted status is non-empty by construction), so null is unambiguous here too.
   */
  findingOutcomes: ReviewAnalyticsFindingStatusCount[] | null;
  /**
   * Step 69, §26.5: false means zero deep-path verdicts have been posted for this repo within the window (only a deep-path review ever produces an arch recap at all, §26.4/§26.9) -- distinct from a real, computed 0% rate, the SAME 'not yet computed' sentinel discipline as RepoSettings.contradictionRateComputed (reposettings.go).
   */
  digestContestationRateComputed: boolean;
  /**
   * Null iff digestContestationRateComputed is false. The fraction of this window's deep-path arch-recap digest sections a maintainer contested (via the §26.5 'arch recap wrong: <reason>' command), as a 0-100 percentage -- §26.5's own 'digest precision (contestation rate)' KPI.
   */
  digestContestationRatePercent: number | null;
}
/**
 * One UTC calendar day's own Shippable-classification counts -- ReviewAnalytics.timeseries' own per-day row (internal/domain/reviewverdict.DayBucket).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ReviewAnalyticsDayBucket".
 */
export interface ReviewAnalyticsDayBucket {
  /**
   * Midnight UTC of this bucket's own calendar day.
   */
  day: string;
  /**
   * How many verdicts posted this day carried Shippable == auto.
   */
  autoCount: number;
  /**
   * How many verdicts posted this day carried Shippable == needs_human.
   */
  needsHumanCount: number;
  /**
   * How many verdicts posted this day carried Shippable == block.
   */
  blockCount: number;
}
/**
 * One review.Tag's own occurrence count across the window's verdicts -- ReviewAnalytics.topRiskDrivers' own per-tag row (internal/domain/reviewverdict.TagCount).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ReviewAnalyticsTagCount".
 */
export interface ReviewAnalyticsTagCount {
  tag: 'auth' | 'migrations' | 'contracts' | 'secrets' | 'infra' | 'public_api' | 'data_layer' | 'dependencies';
  count: number;
}
/**
 * One reviewpost.FindingStatus's own occurrence count across the window's review_findings rows -- ReviewAnalytics.findingOutcomes' own per-status row (internal/domain/reviewverdict.FindingStatusCount).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ReviewAnalyticsFindingStatusCount".
 */
export interface ReviewAnalyticsFindingStatusCount {
  status: 'open' | 'rebutted' | 'fix_pending' | 'fix_open' | 'fix_merged' | 'fix_applied';
  count: number;
}
/**
 * Same shape as CreateSessionRequest's own inline repos item (name/url/branch) -- a REAL top-level $def here (unlike CreateSessionRequest.repos' own inline item schema, which go-jsonschema cannot be $ref'd across sibling $defs) so Automation/CreateAutomationRequest can both reference it directly.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "AutomationReposElem".
 */
export interface AutomationReposElem {
  name: string;
  url: string;
  /**
   * Null means create runs from the repo's default base branch.
   */
  branch: string | null;
}
/**
 * One entry of an automation's own env_vars (Step 52, §8.4's own 'per-automation env vars') -- plain, non-secret configuration only (internal/domain/automation.EnvVar). See internal/domain/automation/doc.go's own writeup for why per-automation SECRETS are a deliberately different, unbuilt thing (deferred to Step 53).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "AutomationEnvVarElem".
 */
export interface AutomationEnvVarElem {
  /**
   * A POSIX shell/environment-variable-legal identifier (internal/domain/automation.ValidateEnvVars) -- letters/digits/underscore, not starting with a digit.
   */
  name: string;
  /**
   * An empty string is a legitimate value.
   */
  value: string;
}
/**
 * One automations row's own REST wire shape (migrations/000051_automations.up.sql, extended by migrations/000055_automations_triggers_and_extras.up.sql, Step 52 '§8.4'). Returned by POST/GET/list.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "Automation".
 */
export interface Automation {
  id: string;
  name: string;
  prompt: string | null;
  /**
   * Same shape as CreateSessionRequest.repos -- the set of repos this automation's own runs fan out against.
   *
   * @minItems 1
   */
  repos: [AutomationReposElem, ...AutomationReposElem[]];
  /**
   * Matches Postgres automation_status exactly.
   */
  status: 'active' | 'paused';
  consecutiveFailures: number;
  /**
   * Null for a system-attributed automation with no direct human creator.
   */
  createdBy: string | null;
  createdAt: string;
  updatedAt: string;
  /**
   * Matches Postgres automation_trigger_type exactly. 'manual' means this automation fires only via a direct, out-of-band invocation (no automatic trigger of its own).
   */
  triggerType: 'manual' | 'cron' | 'github' | 'linear' | 'webhook';
  /**
   * Opaque, type-specific trigger configuration -- see this schema's own top-level description for why this is not a discriminated union. {} for triggerType manual/webhook; {"schedule": "<5-field cron expr>"} for cron; {"event": ..., "action": ..., "label": ...} for github (action/label optional); {"eventType": ..., "action": ..., "teamKey": ...} for linear (action/teamKey optional).
   */
  triggerConfig: {
    [k: string]: unknown;
  };
  /**
   * §8.4's own 'sandboxSettings honored on automation sessions' -- same shape/semantics as CreateSessionRequest.pathScope, applied to every run this automation fans out.
   */
  sandboxPathScope: string[] | null;
  sandboxMockConfigured: boolean;
  /**
   * Meaningful only when sandboxMockConfigured is true; null means the default "contracts/api".
   */
  sandboxContractsPath: string | null;
  envVars: AutomationEnvVarElem[];
  /**
   * Null until this automation's first invocation ever closes.
   */
  lastRunAt: string | null;
  /**
   * The most recently CLOSED invocation's own outcome -- matches Postgres automation_invocation_status, excluding 'pending' (a closed invocation is never pending). Null until lastRunAt is first set.
   */
  lastRunStatus: 'succeeded' | 'failed' | null;
  /**
   * A short, one-sentence, mechanically generated description of the most recently closed invocation's own outcome (internal/domain/automation.BuildArtifactSummary). Null until lastRunAt is first set.
   */
  artifactSummary: string | null;
}
/**
 * POST /api/automations's own request body (Step 52, §8.4). Admin/maintainer only (authz.ActionManageAutomations).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "CreateAutomationRequest".
 */
export interface CreateAutomationRequest {
  name: string;
  prompt: string | null;
  /**
   * @minItems 1
   */
  repos: [AutomationReposElem, ...AutomationReposElem[]];
  triggerType: 'manual' | 'cron' | 'github' | 'linear' | 'webhook';
  /**
   * Required sub-fields depend on triggerType -- see Automation.triggerConfig's own doc comment. Absent/{} is only valid for triggerType manual/webhook.
   */
  triggerConfig?: {
    [k: string]: unknown;
  };
  /**
   * Optional, like CreateSessionRequest.pathScope -- absent/null means unscoped.
   */
  sandboxPathScope?: string[] | null;
  /**
   * Optional, like CreateSessionRequest.mockConfig -- presence (even as {}) means mock_configured=true for every run this automation fans out.
   */
  sandboxMockConfig?: {
    contractsPath?: string | null;
  } | null;
  /**
   * Optional -- absent/empty means no per-automation env vars.
   */
  envVars?: AutomationEnvVarElem[];
}
/**
 * 201 response body for POST /api/automations.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "CreateAutomationResponse".
 */
export interface CreateAutomationResponse {
  automation: Automation;
  /**
   * The PLAINTEXT inbound-webhook bearer token, set iff triggerType is 'webhook' -- returned ONLY this once (mirrors WSTokenResponse's own identical 'hashed at rest, plaintext returned exactly once' convention, platform.HashToken/GenerateToken); null for every other triggerType.
   */
  webhookToken: string | null;
}
/**
 * 200 response body for POST /api/automations/{automationID}/webhook-token (review fix: 'webhook token has no rotation/revocation/expiry'). Same 'hashed at rest, plaintext returned exactly once' convention as CreateAutomationResponse.webhookToken -- unlike that field, webhookToken here is never null: this route only ever succeeds for a triggerType 'webhook' automation, and a successful rotation always mints and returns a real fresh token.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "RotateAutomationWebhookTokenResponse".
 */
export interface RotateAutomationWebhookTokenResponse {
  automation: Automation;
  /**
   * The PLAINTEXT, freshly rotated inbound-webhook bearer token -- returned ONLY this once. The OLD token is invalidated immediately: its own hash no longer matches any automation, with no grace period.
   */
  webhookToken: string;
}
/**
 * GET /api/automations's own response body (Step 52, §8.4's own 'creator/status filters', applied as ?createdBy=<uuid|me>&status=<active|paused> query params). Unbounded (no pagination), matching ListMembersResponse's own identical precedent.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ListAutomationsResponse".
 */
export interface ListAutomationsResponse {
  automations: Automation[];
}
/**
 * One provider_credentials row's own REST wire shape (Step 53, §25.1/§25.3, migrations/000056_provider_credentials.up.sql). Returned by the create/get/list routes mounted at /api/repos/{owner}/{repo}/provider-credentials, /api/environments/{environmentID}/provider-credentials, and /api/provider-credentials (global) -- scope/scopeTarget are always implied by WHICH of the 3 route groups a request hit, never accepted as a separate request field, so there is no risk of a caller's URL and body disagreeing about scope. The underlying secret value is NEVER included here -- see maskedValue.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ProviderCredential".
 */
export interface ProviderCredential {
  id: string;
  /**
   * Matches Postgres provider_credential_scope exactly.
   */
  scope: 'repo' | 'environment' | 'global';
  /**
   * The repo_full_name ('owner/repo') for scope=repo, the environments.id (stringified) for scope=environment, or null for scope=global.
   */
  scopeTarget: string | null;
  /**
   * Matches Postgres provider_credential_provider exactly.
   */
  provider: 'google' | 'anthropic' | 'openai';
  /**
   * A FIXED, non-secret placeholder (never a partial reveal of the real value, never derived from it) proving a credential is configured for this (scope, scopeTarget, provider) -- the real value is write-only from this API's own perspective and is never returned by any route, ever.
   */
  maskedValue: string;
  createdAt: string;
  updatedAt: string;
}
/**
 * POST request body for all 3 provider-credentials route groups (repo/environment/global -- see ProviderCredential's own doc comment for why scope/scopeTarget are never body fields). Gated by authz.ActionManageRepoSecrets/ActionManageEnvSecrets/ActionManageGlobalSecrets respectively (admin+maintainer for repo/environment, admin-only for global, §13.3's own already-reserved matrix rows). A duplicate (scope, scopeTarget, provider) is rejected 409 -- rotate the existing credential via PUT instead of creating a second row for it.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "CreateProviderCredentialRequest".
 */
export interface CreateProviderCredentialRequest {
  provider: 'google' | 'anthropic' | 'openai';
  /**
   * The plaintext credential value -- encrypted at rest (platform.EncryptToken, AES-256-GCM) immediately server-side, never logged, never echoed back in any response. Must not contain a NUL byte (U+0000) -- an embedded NUL later breaks os/exec when the resolved value is written into a spawned sandbox's cmd.Env (cmd/sandbox-agent/main.go's own fetchProviderCredentialSpawnEnv); the httpapi handler enforces this same rule server-side regardless of this pattern.
   */
  value: string;
}
/**
 * PUT request body for /{scope-route}/provider-credentials/{id} -- rotates ONLY the encrypted value. scope/scopeTarget/provider are immutable once a row is created (delete-then-create if a different scope/target/provider is actually wanted) -- this DTO deliberately carries no fields for any of the three.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateProviderCredentialRequest".
 */
export interface UpdateProviderCredentialRequest {
  /**
   * The new plaintext credential value, replacing the old one -- same encrypt-immediately, never-logged, never-echoed handling as CreateProviderCredentialRequest.value, and the same NUL-byte (U+0000) exclusion -- see CreateProviderCredentialRequest.value's own description for why.
   */
  value: string;
}
/**
 * GET response body for all 3 provider-credentials route groups -- every row at that one (scope, scopeTarget) pair, one per configured provider. Unbounded (no pagination, matching ListAutomationsResponse's own identical precedent) -- bounded in practice to at most 3 rows (one per Provider) per (scope, scopeTarget).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ListProviderCredentialsResponse".
 */
export interface ListProviderCredentialsResponse {
  providerCredentials: ProviderCredential[];
}
/**
 * One sandbox_secrets row's own REST wire shape (Step 72, §27.1, migrations/000090_sandbox_secrets.up.sql). Returned by the create/get/list routes mounted at /api/repos/{owner}/{repo}/sandbox-secrets, /api/environments/{environmentID}/sandbox-secrets, and /api/sandbox-secrets (global) -- scope/scopeTarget are always implied by WHICH of the 3 route groups a request hit, never accepted as a separate request field, mirroring ProviderCredential's own identical posture exactly. The underlying secret value is NEVER included here -- see maskedValue.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "SandboxSecret".
 */
export interface SandboxSecret {
  id: string;
  /**
   * Matches Postgres sandbox_secret_scope, EXCLUDING 'automation' -- that scope is schema-only as of Step 72 (§27.1: no CRUD endpoint reaches it yet, mirroring how ProviderCredentialScope's own DTO excludes 'user', a scope managed through a completely separate flow).
   */
  scope: 'repo' | 'environment' | 'global';
  /**
   * The repo_full_name ('owner/repo') for scope=repo, the environments.id (stringified) for scope=environment, or null for scope=global.
   */
  scopeTarget: string | null;
  /**
   * The POSIX-shaped environment variable name this secret is injected as into every hook/service/opencode serve process a session spawns -- validated server-side (internal/domain/sandboxsecret.ValidateName): rejects the reserved NARVI_* namespace and every name providercredential.EnvVarNames already owns.
   */
  name: string;
  /**
   * A FIXED, non-secret placeholder (never a partial reveal of the real value, never derived from it) proving a secret is configured for this (scope, scopeTarget, name) -- the real value is write-only from this API's own perspective and is never returned by any route, ever.
   */
  maskedValue: string;
  createdAt: string;
  updatedAt: string;
}
/**
 * POST request body for all 3 sandbox-secrets route groups (repo/environment/global -- see SandboxSecret's own doc comment for why scope/scopeTarget are never body fields). Gated by authz.ActionManageRepoSecrets/ActionManageEnvSecrets/ActionManageGlobalSecrets respectively -- the SAME 3 already-reserved actions ProviderCredential's own routes use (§27.1: 'Step 53's idioms reused throughout'). A duplicate (scope, scopeTarget, name) is rejected 409 -- rotate the existing secret via PUT instead of creating a second row for it.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "CreateSandboxSecretRequest".
 */
export interface CreateSandboxSecretRequest {
  /**
   * POSIX env-var-shaped name (uppercase letters, digits, underscore; must not start with a digit), rejected if it starts with NARVI_ or exactly matches a name providercredential.EnvVarNames already owns -- the httpapi handler enforces this server-side (internal/domain/sandboxsecret.ValidateName) regardless of this field's own bare string type.
   */
  name: string;
  /**
   * The plaintext secret value -- encrypted at rest (platform.EncryptToken, AES-256-GCM) immediately server-side, never logged, never echoed back in any response. Must not contain a NUL byte (U+0000), mirroring CreateProviderCredentialRequest.value's own identical rule and rationale.
   */
  value: string;
}
/**
 * PUT request body for /{scope-route}/sandbox-secrets/{id} -- rotates ONLY the encrypted value. scope/scopeTarget/name are immutable once a row is created (delete-then-create if a different scope/target/name is actually wanted) -- this DTO deliberately carries no fields for any of the three, mirroring UpdateProviderCredentialRequest exactly.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateSandboxSecretRequest".
 */
export interface UpdateSandboxSecretRequest {
  /**
   * The new plaintext secret value, replacing the old one -- same encrypt-immediately, never-logged, never-echoed handling as CreateSandboxSecretRequest.value, and the same NUL-byte (U+0000) exclusion.
   */
  value: string;
}
/**
 * GET response body for all 3 sandbox-secrets route groups -- every row at that one (scope, scopeTarget) pair. Unbounded (no pagination, matching ListProviderCredentialsResponse's own identical precedent) -- in practice bounded by however many distinct secret names an admin/maintainer has configured at that one scope target.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ListSandboxSecretsResponse".
 */
export interface ListSandboxSecretsResponse {
  sandboxSecrets: SandboxSecret[];
}
/**
 * One opencode_configs row's own REST wire shape (Step 72, §27.2, migrations/000091_opencode_configs.up.sql). Returned by GET/PUT /api/environments/{environmentID}/opencode-config and GET/PUT /api/opencode-config (global). UNLIKE SandboxSecret/ProviderCredential, document is returned in FULL, plaintext -- this is configuration a human reads and edits in Settings, not secret material (that table's own top migration comment); anything secret-shaped belongs in sandbox_secrets and is referenced from document via OpenCode's own {env:VAR} substitution.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "OpenCodeConfig".
 */
export interface OpenCodeConfig {
  /**
   * Matches Postgres opencode_config_scope exactly -- deliberately no 'repo' (a repo's own committed opencode.json already occupies OpenCode's native 'project' slot, no Narvi table needed) and no 'automation' (no per-automation OpenCode config concept exists).
   */
  scope: 'environment' | 'global';
  /**
   * The environments.id (stringified) for scope=environment, or null for scope=global.
   */
  scopeTarget: string | null;
  /**
   * The raw OpenCode config document (opencode.json shape) -- validated at save only as 'parses as a JSON object, bounded size' (§27.2: OpenCode's own schema drifts with its version, so a Narvi-side copy of it would be a second, staler validator). May reference a sandbox_secrets name via OpenCode's own {env:VAR} substitution syntax; never contains a secret value directly.
   */
  document: {
    [k: string]: unknown;
  };
  createdAt: string;
  updatedAt: string;
}
/**
 * PUT request body for /api/environments/{environmentID}/opencode-config and /api/opencode-config (global) -- create-or-replace (§27.2's own 'at most one row per scope target' singleton, upserted rather than a separate POST/PUT-by-id pair the way ProviderCredential/SandboxSecret use, since there is no id for a caller to ever learn or pass). Gated by authz.ActionManageEnvSecrets (environment scope, maintainer+ -- the §13.3 row that owns environments/env secrets) / authz.ActionManageGlobalSecrets (global scope, admin only -- the §13.3 row that owns integrations/global secrets), reusing the SAME 2 already-reserved actions rather than a new OpenCode-config-specific action (§27.1's 'Step 53's idioms reused throughout' extended to §27.2).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "PutOpenCodeConfigRequest".
 */
export interface PutOpenCodeConfigRequest {
  /**
   * Same validation as OpenCodeConfig.document: must parse as a JSON object; nothing deeper is checked server-side.
   */
  document: {
    [k: string]: unknown;
  };
}
/**
 * One explicit (from step, outcome) -> to step routing rule (Step 54, §25.10's 'Edges' entity; one workflow_edges row). Named WorkflowEdge rather than the plan's bare 'Edges'/'Edge': restdtos is a flat namespace and an unprefixed generated 'Edge' type would be needlessly generic -- AutomationReposElem's own entity-prefixed-helper precedent. onStatus is the ONLY thing an edge may condition on (§25.4): the closed 3-value step-outcome vocabulary, a DISTINCT axis from review's Shippable (which is never routed through it). With no explicit edge, 'ok' advances to the next step in order and 'needs_fix'/'blocked' escalate -- fail-conservative; a retry loop is always wired explicitly (internal/domain/workflow.NextStep owns these semantics).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "WorkflowEdge".
 */
export interface WorkflowEdge {
  fromStepId: string;
  /**
   * Matches Postgres workflow_step_outcome_status exactly.
   */
  onStatus: 'ok' | 'needs_fix' | 'blocked';
  /**
   * May equal fromStepId (a wired same-step retry loop) or an earlier step (a backward loop, e.g. §25.9's fix -> audit edge).
   */
  toStepId: string;
}
/**
 * One workflow_step_definitions row plus its outgoing edges (Step 54, §25.10). order is 1-based and unique per definition, not required contiguous. modelId null means inherit exactly what the session would use today (turns.model_id/sessions.build_model_id -- §25.8's zero-config proof); non-null is the same opaque 'provider/model' passthrough convention modelId fields already use (§25.1/§25.7, no Narvi-side allowlist). effort (Step 59, §29.8) mirrors modelId's own shape and inherit-when-null semantics exactly, one field over. promptTemplate uses the established '{{variable_name}}' placeholder syntax (§18.6); '{{prompt}}' is the caller's own text.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "WorkflowStepDefinition".
 */
export interface WorkflowStepDefinition {
  id: string;
  order: number;
  /**
   * Matches Postgres workflow_step_kind exactly -- a single-value closed enum as of Step 54 (every §25.8 shape is an ordinary agent turn); modeled as an enum, not a literal, so a later phase can add a kind without a shape change.
   */
  kind: 'agent';
  modelId: string | null;
  /**
   * Step 59, §29.8's 'workflow engine echo'. Null means inherit exactly what the session would use today (turns.effort/sessions.build_effort).
   */
  effort: string | null;
  promptTemplate: string;
  /**
   * Matches Postgres workflow_execution_scope exactly (§25.6: child_session is reserved for steps needing real isolation; same_session is the default and what every built-in step uses).
   */
  executionScope: 'same_session' | 'child_session';
  /**
   * Matches Postgres workflow_conversation_continuity exactly (§25.6: fresh is a new OpenCode conversation on the SAME session, never a child session).
   */
  conversationContinuity: 'continue' | 'fresh';
  hitlBefore: boolean;
  hitlAfter: boolean;
  /**
   * This step's own explicit outgoing edges -- empty means pure default routing (§25.4). At most one edge per onStatus value (workflow_edges_from_status_uniq).
   */
  edges: WorkflowEdge[];
  /**
   * §25.10's optional canvas-layout attachment: this step's node position on the visual editor's canvas (Step 88, §25.12). OPAQUE server-side -- stored verbatim (workflow_step_definitions.canvas_position JSONB), round-tripped, never interpreted for any behavior. Genuinely OPTIONAL (may be absent entirely, like CreateSessionRequest.pathScope) AND nullable: absent/null means no layout has ever been saved for this step (true for every built-in and every API-authored definition until a canvas first saves one).
   */
  canvasPosition?: {
    x: number;
    y: number;
  } | null;
}
/**
 * One workflow_definitions row plus its ordered steps (Step 54, §25.10; mirrors internal/domain/workflow.Definition). Doubles as the eventual editing surface's PUT body -- always the full, current desired state (steps and edges included), never a partial patch, matching UpdateRepoSettingsRequest's own convention -- but NO handler consumes it yet (Step 54 is dark). isBuiltIn marks one of the three seeded system templates; a PUT/DELETE against an isBuiltIn=true definition is refused unconditionally, even for an admin -- a structural invariant (§25.4), not an RBAC row, enforced by the store/handler layer Steps 55-56 add. version is a 1-based edit counter (provenance a binding/run pins), not a versioned-content archive.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "WorkflowDefinition".
 */
export interface WorkflowDefinition {
  id: string;
  /**
   * Matches Postgres workflow_lane exactly -- the closed 3-value Lane enum (§25.4, internal/domain/workflow.Lane).
   */
  lane: 'review' | 'request' | 'plan';
  /**
   * Unique per lane (workflow_definitions_lane_name_uniq).
   */
  name: string;
  isBuiltIn: boolean;
  version: number;
  /**
   * Every step, in order. A definition with zero steps is not executable and is rejected (internal/domain/workflow.ValidateDefinition).
   *
   * @minItems 1
   */
  steps: [WorkflowStepDefinition, ...WorkflowStepDefinition[]];
  createdAt: string;
  updatedAt: string;
}
/**
 * One workflow_bindings row (Step 54, §25.10): which definition, at which version, a (lane, repoFullName) pair resolves to. repoFullName null is the GLOBAL binding for that lane -- §25.4: exactly one per lane, seeded by migration 000057 to point at the lane's system template, and from then on an ordinary, independently-repointable setting that is NEVER absent (so resolution is repo row if present, else the guaranteed global row -- never an 'absent row -> default' branch). A non-null repoFullName ('owner/repo', repo_settings.repo_full_name's exact shape) is a repo override shadowing the global binding for that one repo only. Activation is admin-only (authz.ActionActivateWorkflowBinding, §25.11) -- the same single action gates both scopes.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "WorkflowBinding".
 */
export interface WorkflowBinding {
  id: string;
  /**
   * Matches Postgres workflow_lane exactly. Always equals the bound definition's own lane -- structurally guaranteed (workflow_bindings_definition_lane_fk).
   */
  lane: 'review' | 'request' | 'plan';
  repoFullName: string | null;
  workflowDefinitionId: string;
  /**
   * The definition's version at binding/activation time -- provenance for 'what was active when', alongside WorkflowRun's own start-time pin.
   */
  definitionVersion: number;
  createdAt: string;
  updatedAt: string;
}
/**
 * One workflow_runs row (Step 54, §25.10) -- READ-ONLY on the wire: runs are created and advanced exclusively by the execution engine (Step 55, §25.6), never via any request DTO. lane/workflowDefinitionId/definitionVersion are pinned at start time as provenance. 'needs_review' is §25.9's escalation parking state (circuit breaker tripped, or an unrouted needs_fix/blocked outcome): non-terminal, one notice, waiting on a human decision.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "WorkflowRun".
 */
export interface WorkflowRun {
  id: string;
  sessionId: string;
  /**
   * Matches Postgres workflow_lane exactly.
   */
  lane: 'review' | 'request' | 'plan';
  workflowDefinitionId: string;
  definitionVersion: number;
  /**
   * Matches Postgres workflow_run_status exactly. The owning transition table ships with Step 55's engine (§11: every state transition through the machine's table) -- the vocabulary is fixed here so the wire contract never has to change under it.
   */
  status: 'running' | 'needs_review' | 'completed' | 'failed' | 'cancelled';
  createdAt: string;
  updatedAt: string;
  /**
   * Null while the run is non-terminal. goJSONSchema forces the literal *time.Time for the same named-pointer-type UnmarshalJSON reason Plan.decidedAt documents in full.
   */
  finishedAt: string | null;
}
/**
 * One workflow_step_runs row (Step 54, §25.10) -- READ-ONLY on the wire, one row per ATTEMPT of one step within a run (a retry/revise re-execution is a NEW row, never an update-in-place -- §25.5's COUNT(*) iteration read depends on exactly that). Deliberately omits two persisted columns, mirroring Plan's own documented omissions: outcome_payload (the §25.6 typed step-to-step handoff, internal plumbing the engine consumes -- never re-parsed presentation data) and decision_text (write-side input, carried by WorkflowStepDecideRequest.text and folded into the NEXT attempt's re-execution).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "WorkflowStepRun".
 */
export interface WorkflowStepRun {
  id: string;
  workflowRunId: string;
  stepDefinitionId: string;
  /**
   * The ordinary turn this attempt dispatched as (§25.6: 'every step is an ordinary sequential turn'). Null while an awaiting_decision (hitlBefore-gated) attempt exists before any turn does.
   */
  turnId: string | null;
  /**
   * Matches Postgres workflow_step_run_status exactly. Same dark-vocabulary note as WorkflowRun.status: the owning transition table is Step 55's.
   */
  status: 'awaiting_decision' | 'running' | 'completed' | 'failed' | 'cancelled';
  /**
   * Matches Postgres workflow_step_outcome_status exactly. Null until this attempt's own typed outcome is posted (§25.6).
   */
  outcomeStatus: 'ok' | 'needs_fix' | 'blocked' | null;
  /**
   * The posted outcome's advisory free-text summary -- never re-parsed as structured data once posted (§25.6), same discipline as PostReviewVerdictRequest.summary.
   */
  outcomeSummary: string | null;
  /**
   * Matches Postgres workflow_step_decision exactly. Null unless a HITL verdict has been rendered on this attempt (§25.9).
   */
  decision: 'approve' | 'reject' | 'revise' | null;
  /**
   * Null until a HITL verdict is recorded -- mirrors Plan.decidedAt exactly, goJSONSchema *time.Time included (see that field's own doc comment for the full named-pointer-type reason).
   */
  decidedAt: string | null;
  /**
   * The user who decided this attempt's HITL verdict. Null until decided, or for a decision attributed to no direct human user -- mirrors Plan.decidedBy.
   */
  decidedBy: string | null;
  createdAt: string;
  /**
   * Null while this attempt is live (running/awaiting_decision).
   */
  finishedAt: string | null;
}
/**
 * Request body for POST /api/workflow-runs/:runId/steps/:stepRunId/decide (§25.9/§25.10) -- the same shape discipline as decideplan.go's approve/reject. NO handler is registered for this route yet: Step 54 ships the contract only (dark); Step 56 mounts the endpoint, gated by authz.ActionDecideWorkflowStep (own/joined-aware, same row as plan approval, §25.11). verdict is a schema-level enum (matching Postgres workflow_step_decision exactly) because the vocabulary is a closed domain enum, the same modeling choice as PostReviewVerdictRequest.riskLevel -- not the deliberately-unconstrained UpdateMemberRoleRequest.role shape.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "WorkflowStepDecideRequest".
 */
export interface WorkflowStepDecideRequest {
  /**
   * approve continues the run; reject ends it; revise ALWAYS re-executes the same step with text folded in as an additional instruction -- never a direct substitution of a structured artifact (§25.9, mirroring plan mode's own 'revise:' handling). Human-revision loops are exempt from the circuit breaker (§25.9).
   */
  verdict: 'approve' | 'reject' | 'revise';
  /**
   * The human's instruction. Required non-empty for verdict 'revise' (enforced at the application layer by Step 56's handler, which owns the specific 400 message); optional context for 'reject'; ignored for 'approve'.
   */
  text: string | null;
}
/**
 * 200 response body for the decide endpoint (§25.9/§25.10) -- mirrors PlanActionResponse's own minimal confirm-what-happened shape: the decided attempt, the run's resulting status, and the follow-up turn if the verdict dispatched one.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "WorkflowStepDecideResponse".
 */
export interface WorkflowStepDecideResponse {
  stepRunId: string;
  /**
   * The decided attempt's own status after this call -- the full workflow_step_run_status enum for forward-compatibility, matching PlanActionResponse.status's own precedent.
   */
  stepRunStatus: 'awaiting_decision' | 'running' | 'completed' | 'failed' | 'cancelled';
  /**
   * The owning run's status after this call -- e.g. 'failed' after a winning reject ends the run, 'running' after an approve/revise continues it.
   */
  runStatus: 'running' | 'needs_review' | 'completed' | 'failed' | 'cancelled';
  /**
   * The newly enqueued turn's id when this verdict dispatched one (an approve advancing to the next step, a revise re-executing the same step); null when it did not (a reject) -- mirrors PlanActionResponse.turnId.
   */
  turnId: string | null;
}
/**
 * Response body for POST/GET /api/me/chatgpt-link (Step 59, §29.3/§29.9): the ChatGPT-account-OAuth link flow's own status, read by the Settings page's own poll loop -- there is no background worker driving this flow forward; the human sitting on the page IS the polling loop (§29.3 point 2), so GET simply reports the current chatgpt_link_attempts/provider_credentials state and, at most, performs ONE throttled upstream poll attempt per call. DELETE /api/me/chatgpt-link (unlink) returns 204 with no body, not this shape.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ChatGPTLinkStatus".
 */
export interface ChatGPTLinkStatus {
  /**
   * unlinked: no attempt in progress and no linked account. pending: a device-flow attempt is in progress (verificationUrl/userCode/expiresAt are populated). linked: a ChatGPT account is linked and its stored token is healthy. needs_relink: was linked, but the refresh pump's own terminal failure (§29.5: invalid_grant/refresh_token_reused) means it is no longer served to any sandbox -- the Settings card's own 'reconnect your ChatGPT account' case.
   */
  status: 'unlinked' | 'pending' | 'linked' | 'needs_relink';
  /**
   * Populated only when status is 'pending' -- always the literal https://auth.openai.com/codex/device (§29.2/§29.3), included so the UI never hardcodes it independently.
   */
  verificationUrl?: string | null;
  /**
   * Populated only when status is 'pending' -- the short code the human enters at verificationUrl.
   */
  userCode?: string | null;
  /**
   * Populated only when status is 'pending' -- this device-flow attempt's own expiry (chatgpt_link_attempts.expires_at); past this time a fresh POST starts a new attempt. goJSONSchema forces the literal time.Time type (no leading '*' here, unlike Plan.decidedAt's own required-field precedent -- this key is OPTIONAL, so go-jsonschema's own omitempty pointer-wrapping already adds the one pointer level this field needs; combining that wrapping with an already-pointer goJSONSchema.type produced a double pointer, **time.Time, verified directly against a real regen during this Step).
   */
  expiresAt?: string | null;
}
/**
 * Response body for GET /api/models -- Step 59's own 'Catalog' deliverable (IMPLEMENTATION_PLAN.md Step 59 row; §8 item 8; §29; §25.2). STRUCTURAL DECISION, named here since §29 leaves it open: sourced from a control-plane-embedded snapshot of OpenCode's own GET /provider catalog (live-verified against the pinned OpenCode 1.17.15 binary during this Step's own implementation), NOT a live per-sandbox proxy -- the control-plane image does not ship the OpenCode binary (§29.9's own identical reasoning for why the ChatGPT device-flow client is a direct CP-side adapter rather than brokered through a spawned sandbox), so there is no running OpenCode server this endpoint could query live even if it wanted to. This is the SAME 'pinned known-good set' convention §7 already established for the sandbox-side per-turn fallback (the opencode adapter's own resolveProviderModel/fallbackModel), applied here as the control plane's ONLY source rather than a fallback of last resort -- refreshed by hand whenever the pinned OpenCode version bumps, exactly like that fallback constant already is. Scope: the 3 providers Step 53 already wires credential injection for (google/anthropic/openai) -- every model id is the exact catalog id OpenCode itself recognizes, usable verbatim as the '<providerId>/<modelId>' string modelId/buildModelId/effort/buildEffort already accept end to end today (§25.1's own 'no Narvi-side allowlist' passthrough, unchanged by this catalog's existence -- it is a discovery aid, never a validating allowlist).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ModelCatalog".
 */
export interface ModelCatalog {
  providers: ModelCatalogProvider[];
}
/**
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ModelCatalogProvider".
 */
export interface ModelCatalogProvider {
  /**
   * The catalog provider id, e.g. "openai" -- the exact string used as the '<providerId>/...' prefix, matching internal/domain/providercredential.Provider's own values.
   */
  id: string;
  models: ModelCatalogModel[];
}
/**
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ModelCatalogModel".
 */
export interface ModelCatalogModel {
  /**
   * The catalog model id, e.g. "gpt-5.3-codex-spark" -- combine with the owning ModelCatalogProvider.id and a "/" to get the exact modelId/buildModelId string this Step's own generic passthrough already accepts.
   */
  id: string;
  /**
   * Human-readable display name.
   */
  name: string;
  /**
   * Total context window, in tokens.
   */
  contextWindow: number;
  /**
   * Whether this model supports tool calling -- OpenCode's own catalog capabilities.toolcall.
   */
  toolCall: boolean;
  /**
   * Whether this model exposes a reasoning-effort dial at all -- OpenCode's own catalog capabilities.reasoning. False means variants is always empty for this model: effort has nothing to override.
   */
  reasoning: boolean;
  /**
   * The valid effort/buildEffort override values for THIS model, e.g. ["none","low","medium","high","xhigh"] for an OpenAI reasoning model, or ["high","max"] for anthropic/claude-sonnet-4-5 (both live-verified during this Step) -- owned per-model by OpenCode's own catalog (§29.8), never a Narvi-side enum. The composer's own model/effort selector (§12.2 item 1, Phase 7) reads this list to populate its effort dropdown once a model is chosen.
   */
  variants: string[];
  cost: ModelCatalogCost;
}
/**
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ModelCatalogCost".
 */
export interface ModelCatalogCost {
  /**
   * USD per million input tokens.
   */
  input: number;
  /**
   * USD per million output tokens.
   */
  output: number;
  /**
   * USD per million cache-read tokens when this model supports prompt caching; null otherwise.
   */
  cacheRead?: number | null;
  /**
   * USD per million cache-write tokens when this model supports prompt caching; null otherwise.
   */
  cacheWrite?: number | null;
}
/**
 * Response body for GET /api/admin/shadow-compare (Step 59's own 'shadow-comparison tooling for review' deliverable, IMPLEMENTATION_PLAN.md Step 59 row, reusing §9.4/§18.5's shadow-mode discipline: 'the same mechanism is used again for every future model swap'). §29 has no dedicated design subsection for this piece -- this is a deliberately minimal, from-scratch interpretation, named as such: a READ-ONLY, side-effect-free comparison of two ALREADY-COMPLETED turns (e.g. the same PR/prompt dispatched once on the active model and once on a shadow/candidate model or effort, or a session's own two differently-configured re-runs), never a re-execution orchestrator -- 'shadow' here means 'never affects either compared turn or its session', the same never-act-only-observe posture §18.5 requires stay permanent, applied to model/effort evaluation rather than classifier routing. Cost is deliberately NOT included: no per-turn cost column exists anywhere in this schema today (§7.1's own named, unclosed debt -- 'per-model cost attribution ... is not designed here'), and inventing one for this endpoint alone would be exactly the kind of shape invention this Step's own hard constraints forbid.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ShadowComparisonReport".
 */
export interface ShadowComparisonReport {
  turnA: ShadowComparisonTurn;
  turnB: ShadowComparisonTurn;
}
/**
 * One side of a ShadowComparisonReport -- mirrors the subset of the turns table (migrations/000005_turns.up.sql, 000018_session_repos.up.sql, 000063_turn_session_effort.up.sql) this comparison actually reads. No turn-level failureReason: unlike sessions.failure_reason, there is no such column on turns itself (that taxonomy is derived at the SESSION level from a turn's own outcome, internal/domain/turn/failurereason.go) -- status alone is this DTO's own outcome signal.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ShadowComparisonTurn".
 */
export interface ShadowComparisonTurn {
  turnId: string;
  sessionId: string;
  modelId: string | null;
  effort: string | null;
  /**
   * Matches Postgres turn_status exactly, same enum as CreateTurnResponse.status.
   */
  status: 'pending' | 'dispatched' | 'processing' | 'completed' | 'failed' | 'cancelled';
  createdAt: string;
  /**
   * goJSONSchema forces the literal *time.Time type -- see Plan.decidedAt's own doc comment.
   */
  dispatchedAt: string | null;
  /**
   * goJSONSchema forces the literal *time.Time type -- see Plan.decidedAt's own doc comment.
   */
  completedAt: string | null;
  /**
   * completedAt minus dispatchedAt, in seconds; null until both are set.
   */
  durationSeconds: number | null;
}
/**
 * Step 60 ('decision inbox: read model + API', §16): one decision-inbox row. Only the fields relevant to `kind` are populated -- every OTHER field is present but null, matching this schema's own established nullability convention (this file's own top doc comment: 'nullable means a required key whose value may be JSON null'). Mirrors internal/app/decisioninbox.Item 1:1 -- see that type's own doc comment for the full per-kind field mapping (PR fields for ready_to_merge/needs_review, plan fields for awaiting_approval, session/automation/outbox fields for needs_attention). provenanceKind/provenanceRepoFullName/provenancePattern are a FLATTENED nested object (mirrors internal/domain/decisioninbox.Provenance's own three fields) rather than a nullable $ref: this schema's own codegen tooling (go-jsonschema) has no established precedent anywhere else in this file for a nullable object-typed field via oneOf/$ref, and produces an untyped interface{} for one -- flattening keeps every field here a plain, typed, nullable scalar, consistent with every other kind-conditional field on this same object.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "DecisionInboxItem".
 */
export interface DecisionInboxItem {
  /**
   * Matches internal/domain/decisioninbox.Kind's own four values exactly (§16.1). needs_attention is only ever present for an admin caller (§16.1's own parenthetical) -- enforced server-side, never a client-side filter.
   */
  kind: 'ready_to_merge' | 'needs_review' | 'awaiting_approval' | 'needs_attention';
  title: string;
  /**
   * When this row first became a pending decision -- the ranking (§16.1: 'by decision cost then age') and staleness reference point. For a PR row this is an APPROXIMATION (the PR's own GitHub creation time, not the instant it became assigned/eligible for this specific user) -- see internal/app/decisioninbox.Item.EnteredQueueAt's own doc comment for why, and for the direction this approximation errs in (it can only ever UNDER-state how recently a PR became a decision, so `stale` below can over-fire on an old-but-recently-assigned PR).
   */
  enteredQueueAt: string;
  /**
   * The response's own generation instant minus enteredQueueAt, in seconds.
   */
  ageSeconds: number;
  /**
   * True once age exceeds the configured staleness threshold (§16.1: '>48h, configurable').
   */
  stale: boolean;
  /**
   * Set for any PR-shaped row -- kind=ready_to_merge/needs_review, AND the handoff sub-case of kind=awaiting_approval (a PR carrying the handoff label rides awaiting_approval instead of an ordinary code-review kind, but is still a PR row); null for a plan/session/automation/outbox row.
   */
  repoFullName: string | null;
  prNumber: number | null;
  htmlUrl: string | null;
  /**
   * The PR's current head SHA at the moment this response's own scmAsOf snapshot was taken -- what a Merge click must still match server-side at click time (§16.2, §5.2).
   */
  headSha: string | null;
  /**
   * How this PR reached the user (§16.1: 'a first-class field, not a UI nicety') -- matches internal/domain/decisioninbox.ProvenanceKind's own three values exactly. Set for any PR-shaped row, exactly like repoFullName above (ready_to_merge/needs_review AND the handoff sub-case of awaiting_approval); null otherwise.
   */
  provenanceKind: 'assigned_directly' | 'requested_reviewer' | 'codeowners' | null;
  /**
   * Set iff provenanceKind=requested_reviewer (e.g. 'acme/payroll-api').
   */
  provenanceRepoFullName: string | null;
  /**
   * The winning CODEOWNERS pattern -- set iff provenanceKind=codeowners (e.g. 'internal/app/scheduler/**').
   */
  provenancePattern: string | null;
  /**
   * The PR's own current review:*-risk label, or null if never risk-labeled.
   */
  riskLabel: string | null;
  /**
   * Set for any PR-shaped row, exactly like repoFullName above (§60 review finding C4 -- this field, findings, and isHandoff used to be nulled out for the handoff sub-case of kind=awaiting_approval, the one row isHandoff exists to identify).
   */
  ciGreen: boolean | null;
  /**
   * Count of still-open (never rebutted/fixed) review findings on this PR. Set for any PR-shaped row -- see ciGreen's own description -- UNLESS the count itself could not be determined (a store error): §60 review finding P3-3 (second round) -- a transient failure fails the *eligibility computation* closed (treated internally as though a blocking finding were present) but that fail-closed sentinel must never be presented on the wire as an honest, real count, so this is null in that case instead, never the synthetic value used internally.
   */
  findings: number | null;
  /**
   * True for a handoff-labeled PR (§14.4) riding kind=awaiting_approval instead of an ordinary code-review kind. Set (to true or false) for any PR-shaped row -- see ciGreen's own description; this is the field a client checks to tell a handoff PR apart from a plan awaiting approval within the SAME kind=awaiting_approval bucket.
   */
  isHandoff: boolean | null;
  /**
   * The PR's own current GitHub review-decision fact -- display only, NEVER what kind=ready_to_merge's own 'approved' means (§16.1 defines that as auto-approval by the deterministic eligibility engine, re-checked at merge time by re-validating CI/risk-label/open-findings/HasChangesRequested, never a human GitHub review). Set for any PR-shaped row, exactly like ciGreen above.
   */
  hasApprovingReview: boolean | null;
  /**
   * The PR's own current GitHub review-decision fact, reduced to each reviewer's LATEST review (§60 review finding P1-1, second round) so a reviewer who requested changes and has since re-reviewed and approved no longer counts here. UNLIKE hasApprovingReview above, this DOES gate an action: RevalidateForMerge treats a true value as a hard block on the Merge endpoint (§60 review finding A4, first round) -- a client should use this field, not hasApprovingReview, to pre-disable/explain a disabled Merge action (§60 review finding P1-4, second round: this field previously did not exist on the wire at all, even though the fact it surfaces already hard-blocked the merge server-side). Set for any PR-shaped row, exactly like ciGreen above.
   */
  hasChangesRequested: boolean | null;
  /**
   * kind=awaiting_approval, a plan (not a handoff PR) only.
   */
  planId: string | null;
  /**
   * Set for a plan (kind=awaiting_approval) or a failed session (kind=needs_attention).
   */
  sessionId: string | null;
  /**
   * Matches Postgres session_failure_reason -- kind=needs_attention, a failed session, only.
   */
  failureReason: string | null;
  /**
   * kind=needs_attention, an auto-paused automation, only.
   */
  automationId: string | null;
  /**
   * The automation's own last deterministic run summary (§8.4).
   */
  artifactSummary: string | null;
  /**
   * kind=needs_attention, a dead-lettered outbox delivery, only.
   */
  outboxId: string | null;
  outboxKind: string | null;
  lastError: string | null;
}
/**
 * GET /api/decision-inbox's own response body (Step 60, §16.2/§16.3 -- Phase 5 half: read model + endpoints; the UI is Phase 7).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ListDecisionInboxResponse".
 */
export interface ListDecisionInboxResponse {
  /**
   * Already ranked server-side (§16.1: decision cost then age) -- a client renders this order as-is, never re-sorts.
   */
  items: DecisionInboxItem[];
  /**
   * When the PR-derived rows (ready_to_merge/needs_review) were actually fetched from GitHub (§16.2: 'the response carries its as-of timestamp... never presented as live truth') -- null iff the caller has no linked GitHub identity, so no SCM read was attempted AT ALL. Distinct from scmFetchFailed below (§60 review finding C1): scmAsOf==null alone used to be the ONLY signal here, which meant a GitHub outage or a revoked token (a read that WAS attempted and failed) was indistinguishable from never having linked GitHub in the first place -- a contract-abiding client would render 'no GitHub linked' for what was actually a transient failure. goJSONSchema forces the literal *time.Time type -- see Plan.decidedAt's own doc comment for why a named pointer-type wrapper silently breaks encoding/json here.
   */
  scmAsOf: string | null;
  /**
   * True iff the caller's PR-derived rows (ready_to_merge/needs_review) are a known-incomplete or degraded picture -- ONE channel fed by several independent producers (§60 review findings P1-2/P1-3/P2-1, second round, extending §60 review finding C1, first round): the live PR fetch failing outright (a revoked token, a GitHub incident, a timeout, or a linked-identity lookup/decrypt failure -- scmAsOf stays null in these cases, no fetch was attempted or it never returned); one of GitHub's own underlying discovery queries failing while the other still returned a real, if partial, result (scmAsOf IS set here -- a genuine, if partial, fetch happened); or an individual PR's own §17 sentinel-fix exclusion check erroring (that one row is dropped, fail-closed, but the overall read is no longer complete). UNLIKE this field's own previous doc comment claimed, scmAsOf non-null and scmFetchFailed true are NOT mutually exclusive -- a partial-but-real fetch legitimately carries both a real as-of instant and a flag telling the caller not to trust the rows present as complete. Always false alongside scmAsOf==null when no linked identity exists at all (a legitimate, non-degraded empty state). A client should render a distinct 'temporarily unable to load your pull requests, try again shortly' state whenever this is true -- never the same 'no GitHub linked' empty state scmAsOf==null with scmFetchFailed==false means, and never silently trust the rows present as a complete queue.
   */
  scmFetchFailed: boolean;
  /**
   * §16.2's own decision-latency metric -- null iff decisionLatencyComputed is false (§21.1's own 'not yet computed' sentinel, distinct from a real zero: a repo with a real 0-second median and one with no decisions yet in the window must never render identically).
   */
  decisionLatencyMedianSeconds: number | null;
  /**
   * How many already-decided items fed decisionLatencyMedianSeconds -- 0 whenever decisionLatencyComputed is false.
   */
  decisionLatencySampleSize: number;
  decisionLatencyComputed: boolean;
}
/**
 * POST /api/decision-inbox/merge's own request body (Step 60, §16.2's own Merge endpoint, mockups.html decision 33: 'Auto-approved still means human-merged... re-validates CI, approval state, and RBAC server-side at click time').
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "MergePullRequestRequest".
 */
export interface MergePullRequestRequest {
  repoFullName: string;
  prNumber: number;
}
/**
 * 200 response body for POST /api/decision-inbox/merge -- returned only once the endpoint's own server-side re-validation (CI green, approval state, Authorize) passed AND the SourceControl.MergePR call itself succeeded.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "MergePullRequestResponse".
 */
export interface MergePullRequestResponse {
  merged: boolean;
  mergeCommitSha: string;
  message: string;
}
/**
 * One review_false_positive_patterns row's own REST wire shape (Step 63, 'review: learned false-positive patterns', §22.2/§22.4, migrations/000073_review_false_positive_patterns.up.sql) -- returned by the audit-view GET and the retire POST so a caller can confirm the resulting state. Capture itself has no REST shape at all: it happens exclusively via the `false positive: <reason>` PR-thread command (§22.2, internal/adapters/inbound/github's own dispatch-before-router capture handler), never through this API.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "FalsePositivePattern".
 */
export interface FalsePositivePattern {
  id: string;
  repoFullName: string;
  /**
   * The maintainer's own free-text pattern description, captured verbatim at teach time.
   */
  reason: string;
  createdAt: string;
  /**
   * §22.4's own usage-signal bookkeeping: how many review passes have included this pattern in their own advisory block since it was taught (or since it was last retired-and-untouched -- retiring does not reset this count). Never a claim that any of those passes actually acted on it (§22.3: advisory, never a filter).
   */
  hitCount: number;
  /**
   * Null iff hitCount is 0 -- this pattern has never yet been injected into a review pass. goJSONSchema forces the literal *time.Time type -- see Plan.decidedAt's own doc comment for why a named pointer-type wrapper silently breaks encoding/json here.
   */
  lastHitAt: string | null;
  /**
   * Null means active (still injected into future review passes); non-null means a maintainer+ has explicitly retired it (§22.4) -- excluded from injection from that point on, but never deleted. goJSONSchema forces the literal *time.Time type -- see Plan.decidedAt's own doc comment for why a named pointer-type wrapper silently breaks encoding/json here.
   */
  retiredAt: string | null;
}
/**
 * GET /api/repos/{owner}/{repo}/false-positive-patterns's own response body (Step 63, §22.4's own audit view) -- EVERY pattern for this repo, active or retired, newest-first.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ListFalsePositivePatternsResponse".
 */
export interface ListFalsePositivePatternsResponse {
  patterns: FalsePositivePattern[];
}
/**
 * One cloud_identity_bindings row's own REST wire shape (Step 73a, §27.3, migrations/000093_cloud_identity_bindings.up.sql). Returned by the create/list/update routes mounted at /api/environments/{environmentID}/cloud-identity-bindings and /api/cloud-identity-bindings (global) -- scope/scopeTarget are always implied by WHICH of the 2 route groups a request hit, never accepted as a separate request field, mirroring ProviderCredential's own identical convention. params carries no secret material (identifiers only -- a role ARN, a client id, an env-var name -- never a credential value), unlike ProviderCredential.maskedValue/SandboxSecret, so it is returned in full, not masked.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "CloudIdentityBinding".
 */
export interface CloudIdentityBinding {
  id: string;
  /**
   * Matches Postgres cloud_identity_binding_scope exactly -- deliberately narrower than ProviderCredentialScope/SandboxSecretScope (no repo scope: §27.3, "a deployment target is an Environment property, not a repo property").
   */
  scope: 'environment' | 'global';
  /**
   * The environments.id (stringified, IMMUTABLE id -- environments has no name column) for scope=environment, or null for scope=global.
   */
  scopeTarget: string | null;
  /**
   * Matches Postgres cloud_identity_binding_kind exactly. A binding with kind=azure can never have scope=global -- refused at creation (400) -- see CreateCloudIdentityBindingRequest's own description for why.
   */
  kind: 'aws' | 'gcp' | 'azure' | 'generic';
  /**
   * The `aud` claim value a minted token carries when this binding is the one that matched a POST /sessions/{id}/cloud-identity-token request -- customer-set, per-binding, whatever the target cloud/consumer documents it expects (§27.3).
   */
  audience: string;
  /**
   * Identifiers, never secrets (§27.3: "params are identifiers not secrets... stored plaintext, readable") -- AWS: role ARN; GCP: workload-identity-provider resource name + optional service-account email; Azure: client id + tenant id; generic: the env-var name to publish the token path under. Modeled as an opaque raw-JSON passthrough (goJSONSchema -> encoding/json.RawMessage), the SAME precision-preserving convention AuditLogEntry.detail already establishes in this schema, rather than a decoded map[string]interface{} -- this codebase does not itself enforce a fixed key set per kind (each cloud's own expected shape is documented, not schema-validated, exactly like ProviderCredential's own precedent of not validating a value's own internal shape).
   */
  params: {
    [k: string]: unknown;
  };
  /**
   * The EXACT `sub` claim string (narvi:environment:<environment_id>) a customer must paste into their cloud-side trust policy for this binding to take effect -- Step 73a's own gap-4 resolution: the management API surfaces this directly rather than making the customer construct the string format themselves. Non-null only for scope=environment (a single, fixed, well-defined Environment); null for scope=global, since a global-scope binding's own token carries a DIFFERENT sub per Environment it is ever minted for -- there is no single string to surface (see this Step's own cloudidentity package doc comment for the full "what global scope means for sub" discussion, gap 3).
   */
  sub: string | null;
  createdAt: string;
  updatedAt: string;
}
/**
 * POST request body for both cloud-identity-bindings route groups (environment/global -- see CloudIdentityBinding's own doc comment for why scope/scopeTarget are never body fields). Gated by authz.ActionManageCloudIdentityBindings (maintainer+, §13.3's own environments row). A duplicate (scope, scopeTarget, kind) is rejected 409 -- rotate the existing binding via PUT instead of creating a second row for it. kind=azure at the global route group is rejected 400 (ErrAzureGlobalScopeForbidden, internal/domain/cloudidentity) -- Azure's federated-credential matching is exact-match only on `sub`, which is always per-Environment, so a single global-scope azure binding cannot honestly promise to trust every Environment (this Step's own gap-3 resolution -- see internal/domain/cloudidentity's own doc comment for the full reasoning).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "CreateCloudIdentityBindingRequest".
 */
export interface CreateCloudIdentityBindingRequest {
  kind: 'aws' | 'gcp' | 'azure' | 'generic';
  /**
   * See CloudIdentityBinding.audience's own description.
   */
  audience: string;
  /**
   * Optional -- defaults to {} when omitted. See CloudIdentityBinding.params' own description.
   */
  params?: {
    [k: string]: unknown;
  };
}
/**
 * PUT request body for /{scope-route}/cloud-identity-bindings/{id} -- rotates ONLY audience/params. scope/scopeTarget/kind are immutable once a row is created (delete-then-create if a different scope/target/kind is actually wanted), mirroring UpdateProviderCredentialRequest's own identical "identity fields immutable, payload fields rotate in place" posture.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateCloudIdentityBindingRequest".
 */
export interface UpdateCloudIdentityBindingRequest {
  /**
   * The new audience value, replacing the old one.
   */
  audience: string;
  /**
   * Optional -- omitted means "leave params unchanged" is NOT supported; an omitted value is treated as {} (matching CreateCloudIdentityBindingRequest.params' own default), since this is a full-replace PUT, not a partial PATCH.
   */
  params?: {
    [k: string]: unknown;
  };
}
/**
 * GET response body for both cloud-identity-bindings route groups -- every row at that one (scope, scopeTarget) pair, one per configured kind. Unbounded (no pagination, matching ListProviderCredentialsResponse's own identical precedent) -- bounded in practice to at most 4 rows (one per Kind) per (scope, scopeTarget).
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "ListCloudIdentityBindingsResponse".
 */
export interface ListCloudIdentityBindingsResponse {
  cloudIdentityBindings: CloudIdentityBinding[];
}
/**
 * 200 response body for POST /api/cloud-identity/signing-keys/rotate (Step 73a, §27.3/§27.8: "manual, admin-triggered rotation with the overlap window is v1" -- this Step's own gap-2 resolution, internal/domain/oidckey's own doc comment). Gated by authz.ActionManageCloudIdentityKeys (admin only). Never returns any key MATERIAL (private or public) -- only kid/timestamp metadata, proving a rotation happened and telling the caller exactly when the just-retired key (if any) stops verifying.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "RotateCloudIdentitySigningKeyResponse".
 */
export interface RotateCloudIdentitySigningKeyResponse {
  /**
   * The freshly created signing key's own kid -- the one every NEW token is minted with from this point forward.
   */
  activeKid: string;
  activeCreatedAt: string;
  /**
   * The PREVIOUSLY active key's own kid, now retired -- null only on the very first-ever rotation (bootstrapping the first key, nothing to retire).
   */
  retiredKid: string | null;
  /**
   * When retiredKid stopped signing NEW tokens -- null iff retiredKid is null. goJSONSchema forces the literal *time.Time type -- see Plan.decidedAt's own doc comment for why a named pointer-type wrapper silently breaks encoding/json here.
   */
  retiredAt: string | null;
  /**
   * retiredAt + platform.Timeouts.CloudIdentitySigningKeyOverlapWindow -- the instant retiredKid drops out of the JWKS response and stops verifying ANY token, even one already minted. Null iff retiredKid is null.
   */
  publishableUntil: string | null;
}
