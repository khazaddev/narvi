/* eslint-disable */
/**
 * This file was automatically generated from /contracts JSON Schemas by
 * json-schema-to-typescript (contracts/scripts/generate-ts.mjs).
 * DO NOT EDIT IT BY HAND — edit the source .schema.json file and re-run
 * `npm run generate` instead.
 */

/**
 * REST DTOs (§6.3). SCOPE NOTE: §6.3 names the full BFF-facing route surface (sessions, events, artifacts, secrets, environments, automations, uploads, ws-token) but only sessions, events, artifacts, and ws-token are specified in enough field-level detail anywhere in the technical plan to schema honestly today (Step 19's own plan row: 'REST endpoints the UI needs (create/get/events/artifacts)'). Secrets/environments/automations/uploads DTOs are deliberately NOT modeled here — they belong to the PRs that define those features (environments: PR-10/26; automations: PR-46/47; uploads: PR-49). This is a scope decision, not an oversight (see contracts/README.md). These 5 shapes are independent named payloads, not a discriminated union, so there is deliberately no top-level oneOf. Field nullability convention: 'nullable' means a required key whose value may be JSON null. Enums here MUST match the Postgres enums in migrations/000004_sessions.up.sql exactly.
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
   * Optional (row 10, 'domain: Environment scoping', §14.1). Absent or null means today's exact unscoped behavior, unchanged: no environments row is created and the session's environmentId/provenanceTag stay null. A non-empty list of sparse-checkout glob patterns creates a new, session-scoped Environment row (internal/domain/environment.ValidatePathScope validates each pattern; the first invalid pattern is rejected with 400 before any Postgres write). Unlike every other field on this DTO, this key is genuinely OPTIONAL (may be absent from the request body entirely), not merely nullable -- there is no separately-managed, ID-referenced Environment entity to reference here yet (see this schema's own top-level SCOPE NOTE above).
   */
  pathScope?: string[] | null;
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
