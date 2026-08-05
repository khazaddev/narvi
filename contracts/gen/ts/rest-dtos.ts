/* eslint-disable */
/**
 * This file was automatically generated from /contracts JSON Schemas by
 * json-schema-to-typescript (contracts/scripts/generate-ts.mjs).
 * DO NOT EDIT IT BY HAND — edit the source .schema.json file and re-run
 * `npm run generate` instead.
 */

/**
 * REST DTOs (§6.3). SCOPE NOTE: §6.3 names the full BFF-facing route surface (sessions, events, artifacts, secrets, environments, automations, uploads, ws-token) but only sessions, events, artifacts, ws-token, and (as of Step 52) automations are specified in enough field-level detail anywhere in the technical plan to schema honestly today (Step 19's own plan row: 'REST endpoints the UI needs (create/get/events/artifacts)'). Secrets/environments/uploads DTOs are still deliberately NOT modeled here — they belong to the PRs that define those features (environments: PR-10/26/27; uploads: PR-49). This is a scope decision, not an oversight (see contracts/README.md). This schema also models the 8 members/audit-log shapes for §13.2/§13.3's own members API (Identity, Member, PendingLinkPrompt, ListMembersResponse, AuditLogEntry, ListAuditLogResponse, UpdateMemberRoleRequest, LinkMemberIdentityRequest) — promoted here as a pure migration off hand-written Go structs in internal/adapters/inbound/httpapi/members.go (see contracts/README.md's own 'Members/audit-log DTOs' section). It also models the 3 plan-mode shapes for GET/POST /api/sessions/:id/plans... (Plan, ListPlansResponse, PlanActionResponse) — audit-fix batch, closing findings M3 (a GET .../plans discoverability gap Step 37 left open) and L14/L16 (promoting planapprove.go's own hand-written planActionResponse now that this same area has a real DTO-consuming sibling endpoint). Step 52 ('automations: triggers & extras', §8.4) adds Automation/CreateAutomationRequest/CreateAutomationResponse/ListAutomationsResponse: the REST surface over migrations/000051_automations.up.sql + 000055_automations_triggers_and_extras.up.sql. triggerConfig is deliberately modeled as an opaque JSON object (mirroring AuditLogEntry.detail's own 'opaque raw-JSON passthrough' precedent immediately below), not a discriminated union keyed on triggerType -- its actual required sub-fields differ per trigger type (schedule for cron; event/action/label for github; eventType/action/teamKey for linear; empty for manual/webhook) and are validated at the application layer (internal/domain/automation's own ValidateCronTriggerConfig/ValidateGitHubTriggerConfig/ValidateLinearTriggerConfig), the same 'closed vocabulary enforced in Go, not in the schema' convention UpdateMemberRoleRequest.role/LinkMemberIdentityRequest.provider already establish. All of these shapes are independent named payloads, not a discriminated union, so there is deliberately no top-level oneOf. Field nullability convention: 'nullable' means a required key whose value may be JSON null. Enums here MUST match the Postgres enums in migrations/000004_sessions.up.sql exactly (plan-mode's own status enum instead matches migrations/000034_plan_mode.up.sql's plan_status; automation's own status/triggerType/lastRunStatus enums match migrations/000051_automations.up.sql/000055_automations_triggers_and_extras.up.sql/000052_automation_invocations.up.sql respectively). Step 53 ('provider credential injection', §25.1/§25.3) adds ProviderCredential/CreateProviderCredentialRequest/UpdateProviderCredentialRequest/ListProviderCredentialsResponse: the REST surface over this codebase's first secret-storage table (migrations/000056_provider_credentials.up.sql). ProviderCredential is deliberately write-only for its own underlying secret value -- the credential itself is accepted on create/update (CreateProviderCredentialRequest.value/UpdateProviderCredentialRequest.value) but NEVER appears in any response shape; ProviderCredential.maskedValue is a fixed, non-secret placeholder proving a value is configured, not a partial reveal of it. Step 54 ('domain/workflow + loopguard + schema', §25.4/§25.10) adds the workflow-engine DTO set over migrations/000057_workflows.up.sql: WorkflowDefinition/WorkflowStepDefinition/WorkflowEdge (the authorable graph -- WorkflowDefinition doubles as the eventual editing surface's PUT body, always the full desired state per UpdateRepoSettingsRequest's own 'never a partial patch' convention, and a PUT/DELETE against an isBuiltIn=true definition is refused unconditionally, a structural invariant, not an RBAC row -- §25.4), WorkflowBinding (which definition+version a (lane, repoFullName) resolves to; repoFullName null = the global binding, seeded per lane and never absent), the read-only WorkflowRun/WorkflowStepRun execution ledger, and WorkflowStepDecideRequest/Response (the §25.9 HITL verdict, same response shape as PlanActionResponse). All workflow enums match the Postgres workflow_* enums in migrations/000057 exactly. NO HTTP handler consumes any of these yet -- Step 54 is dark (schema/contracts/RBAC only); Steps 55-56 mount the first routes.
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
 * Request body for POST /sessions/:id/review/verdict (Step 47, 'server-side verdict', §8.2/§5.2) -- the verdict-posting tool's own typed-fields call, validated server-side (internal/domain/reviewpost.ValidateVerdictInput). Mirrors internal/domain/review.Verdict's own fields exactly, EXCEPT Shippable itself, which this endpoint always recomputes server-side (review.ComputeShippable) and NEVER accepts from a caller -- see that package's own Verdict.Shippable doc comment (verdict.go) for why.
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
 * GET/PUT /api/repos/{owner}/{repo}/settings response body (Step 47, §8.2/§21.2) -- an admin, per-repo policy-flag row (migrations/000044_repo_settings.up.sql). Deliberately a small, extensible shape: future Steps (58's auto-merge toggle, 61's automatic-re-review opt-in) are each expected to add a further boolean property here, never a bespoke DTO of their own.
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
}
/**
 * Request body for PUT /api/repos/{owner}/{repo}/settings -- always the full, current desired state (never a partial patch), matching RepoSettings' own shape. sentinelAutofixEnabled (Step 48) is deliberately OPTIONAL, not required, exactly like every other additive field this schema has ever grown (e.g. CreateSessionRequest.buildModelId) -- an old caller that only ever knew about blockOnHighRisk keeps compiling/working unchanged; PutRepoSettings' own 'always the full desired state' semantics mean an old caller that omits this key simply (re)sets it to its own safe default (false) alongside whatever it DOES specify, never a partial-patch surprise.
 *
 * This interface was referenced by `RestDtos`'s JSON-Schema
 * via the `definition` "UpdateRepoSettingsRequest".
 */
export interface UpdateRepoSettingsRequest {
  blockOnHighRisk: boolean;
  sentinelAutofixEnabled?: boolean;
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
 * One workflow_step_definitions row plus its outgoing edges (Step 54, §25.10). order is 1-based and unique per definition, not required contiguous. modelId null means inherit exactly what the session would use today (turns.model_id/sessions.build_model_id -- §25.8's zero-config proof); non-null is the same opaque 'provider/model' passthrough convention modelId fields already use (§25.1/§25.7, no Narvi-side allowlist). promptTemplate uses the established '{{variable_name}}' placeholder syntax (§18.6); '{{prompt}}' is the caller's own text.
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
   * §25.10's optional canvas-layout attachment: this step's node position on the visual editor's canvas (Step 79, §25.12). OPAQUE server-side -- stored verbatim (workflow_step_definitions.canvas_position JSONB), round-tripped, never interpreted for any behavior. Genuinely OPTIONAL (may be absent entirely, like CreateSessionRequest.pathScope) AND nullable: absent/null means no layout has ever been saved for this step (true for every built-in and every API-authored definition until a canvas first saves one).
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
