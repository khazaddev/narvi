/* eslint-disable */
/**
 * This file was automatically generated from /contracts JSON Schemas by
 * json-schema-to-typescript (contracts/scripts/generate-ts.mjs).
 * DO NOT EDIT IT BY HAND — edit the source .schema.json file and re-run
 * `npm run generate` instead.
 */

/**
 * sandbox-agent -> control-plane WS events (technical plan §6.1). Every event carries the common envelope (type, messageId, sessionId, gen). The 6 CRITICAL types (execution_complete, error, snapshot_ready, push_complete, push_error, sub_task_finish) additionally require ackId, deterministically formatted '{type}:{messageId}' (enforced here via both a description and a per-type 'pattern' anchored on the literal type prefix) so the ack protocol (§6.1: sender buffers 1000 events, evicts oldest non-critical, re-sends on reconnect until acked; receiver dedupes by upsert-on-messageId) can redeliver them exactly once. sub_task_finish (§7.1) joins the critical set because it closes an 'active' state the UI tracks (a live sub-lane count) exactly like execution_complete does at the turn level — a dropped, never-redelivered sub_task_finish would leave a sub-lane stuck active forever. Field nullability convention: a property documented as 'nullable' is a REQUIRED key whose value may be JSON null, EXCEPT step_finish's cost.tokens.cached and cost.usd — §6.1 only specifies that 'tokens is an object, not a number'; the {input, output, cached?, usd?} breakdown and which of those sub-fields are optional is this contract's own invention, not literally specified by the technical plan.
 */
export type SandboxEvent =
  | Ready
  | Heartbeat
  | BootProgress
  | Token
  | ToolCall
  | ToolResult
  | StepStart
  | StepFinish
  | GitSync
  | Artifact
  | ExecutionComplete
  | PushComplete
  | PushError
  | SessionTitle
  | Warning
  | SandboxErrorEvent
  | SnapshotReady
  | SubTaskStart
  | SubTaskFinish;

/**
 * First event on a fresh WS connection, once the agent is ready to receive commands.
 */
export interface Ready {
  type: 'ready';
  messageId: string;
  sessionId: string;
  gen: number;
  timestamp: string;
}
/**
 * §6.1: every 30s; carries conversation id + last_boot_phase so liveness (last_seen_at, §3.2) and turn-resume state stay current even with no other traffic.
 */
export interface Heartbeat {
  type: 'heartbeat';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * Null before the first turn has started a conversation.
   */
  conversationId: string | null;
  /**
   * Null once boot has completed (no more boot phases to report).
   */
  lastBootPhase: string | null;
  timestamp: string;
}
/**
 * Named boot phase report; re-arms the connecting deadline during long boots (§3.2).
 */
export interface BootProgress {
  type: 'boot_progress';
  messageId: string;
  sessionId: string;
  gen: number;
  phase: string;
  timestamp: string;
}
/**
 * §6.1: text is CUMULATIVE, not a delta. Consumers MUST treat this as a full replacement of the streamed text keyed by messageId (upsert-by-messageId), never append the text onto a running buffer.
 */
export interface Token {
  type: 'token';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * The full cumulative text so far for this messageId — replace, do not append.
   */
  text: string;
  /**
   * §6.1/§7.1 sub-task fan-out: OPTIONAL — absent or null means this event belongs to the turn's main lane; a non-null value is the subTaskId (same id sub_task_start/sub_task_finish carry) of the sub-task lane this event belongs to.
   */
  subTaskId?: string | null;
}
export interface ToolCall {
  type: 'tool_call';
  messageId: string;
  sessionId: string;
  gen: number;
  callId: string;
  toolName: string;
  /**
   * Freeform, tool-specific input.
   */
  input: {
    [k: string]: unknown;
  };
  /**
   * §6.1/§7.1 sub-task fan-out: OPTIONAL — absent or null means this event belongs to the turn's main lane; a non-null value is the subTaskId (same id sub_task_start/sub_task_finish carry) of the sub-task lane this event belongs to.
   */
  subTaskId?: string | null;
}
export interface ToolResult {
  type: 'tool_result';
  messageId: string;
  sessionId: string;
  gen: number;
  callId: string;
  /**
   * Freeform, tool-specific output.
   */
  output: {
    [k: string]: unknown;
  };
  isError: boolean;
  /**
   * §6.1/§7.1 sub-task fan-out: OPTIONAL — absent or null means this event belongs to the turn's main lane; a non-null value is the subTaskId (same id sub_task_start/sub_task_finish carry) of the sub-task lane this event belongs to.
   */
  subTaskId?: string | null;
}
export interface StepStart {
  type: 'step_start';
  messageId: string;
  sessionId: string;
  gen: number;
  stepId: string;
  /**
   * §6.1/§7.1 sub-task fan-out: OPTIONAL — absent or null means this event belongs to the turn's main lane; a non-null value is the subTaskId (same id sub_task_start/sub_task_finish carry) of the sub-task lane this event belongs to.
   */
  subTaskId?: string | null;
}
/**
 * THE CRITICAL SCHEMA (§6.1 / §9.1 dedicated test): cost.tokens is an OBJECT, never a bare number. A number-vs-object mismatch here silently zeroes cost tracking downstream, so this shape is pinned by a dedicated round-trip + rejection test in contracts/contractstest.
 */
export interface StepFinish {
  type: 'step_finish';
  messageId: string;
  sessionId: string;
  gen: number;
  stepId: string;
  /**
   * §6.1/§7.1 sub-task fan-out: OPTIONAL — absent or null means this event belongs to the turn's main lane; a non-null value is the subTaskId (same id sub_task_start/sub_task_finish carry) of the sub-task lane this event belongs to.
   */
  subTaskId?: string | null;
  cost: {
    /**
     * NOTE: tokens is an object, not a number (§6.1 explicit warning).
     */
    tokens: {
      input: number;
      output: number;
      cached?: number | null;
    };
    usd?: number | null;
  };
}
/**
 * §3.4 stash -> checkout -> pop sequence, reported as it happens. NOTE on "repo" in required: this tightens an already-merged def (repo did not exist at all in the earlier scaffolding-only shape) rather than staying purely additive. That is deliberate and safe here, not an oversight: git_sync had zero real producers or consumers before the same Step that added "repo" also wired the one producer that sets it (cmd/sandbox-agent) and the field is read nowhere else, so no already-running wire party could have been depending on its absence. Tightening a field that is actually in use elsewhere would need a BREAKING marker instead.
 */
export interface GitSync {
  type: 'git_sync';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * §3.4: repos are always a list -- names which repo (SessionConfig.repos[].name) this phase is about, so a multi-repo session reconciling several repos concurrently/sequentially can be disambiguated.
   */
  repo: string;
  status: 'stash' | 'checkout' | 'pop';
  branch: string;
}
/**
 * artifactType MUST match the Postgres artifact_type enum (migrations/000012_artifacts.up.sql) exactly. status/failureReason (Step 58, §28.6) are additive and OPTIONAL: absent status means "ready" (the same zero-producers-today additive reasoning SnapshotReady.commandMessageId used) -- every pr/preview artifact event emitted before this Step, and every future one that never sets them, stays a valid, unchanged shape. Both fields are CP-SYNTHESIZED ONLY (mirrors GitSync's own "repo" field note above and the general subTaskId-population convention this file's own top-level description states): the sandbox never emits an artifact event for an upload at all -- the control plane already owns the row before any bytes exist, so a sandbox-reported completion would be a second writer over a fact Postgres already owns (§5.1). failureReason MUST match the Postgres artifact_failure_reason enum (migrations/000060_artifacts_upload_lifecycle.up.sql) exactly, and is only ever non-null when status is "failed".
 */
export interface Artifact {
  type: 'artifact';
  messageId: string;
  sessionId: string;
  gen: number;
  artifactType: 'pr' | 'preview' | 'upload';
  /**
   * format is deliberately "uri-reference", not the stricter "uri" (Step 58 relaxation -- backward compatible: every absolute URL a plain "uri" ever accepted still validates, so no pr/preview producer's existing behavior changes). pr/preview artifacts always carry an ABSOLUTE external link (a GitHub PR/preview URL); upload artifacts carry the artifacts row's own STABLE, RELATIVE /api/sessions/{id}/uploads/{uploadId}/content path (§28.5: "the artifact row's url column stores this stable /api/... content path, never a presigned URL") -- a relative reference a browser client resolves against its own current origin, which "uri" alone would have rejected.
   */
  url: string;
  metadata: {
    [k: string]: unknown;
  };
  /**
   * Absent means "ready" -- see this definition's own top-level description for the additive/CP-synthesized-only contract.
   */
  status?: 'ready' | 'failed';
  /**
   * Matches Postgres artifact_failure_reason exactly. Null/absent except when status is "failed".
   */
  failureReason?: 'size_exceeded' | 'quota_exceeded' | 'verification_failed' | 'abandoned' | null;
}
/**
 * CRITICAL (requires ackId). outcome MUST match the turn_status terminal states (migrations/000005_turns.up.sql).
 */
export interface ExecutionComplete {
  type: 'execution_complete';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * Deterministic ackId = 'execution_complete:{messageId}' (§6.1).
   */
  ackId: string;
  outcome: 'completed' | 'failed' | 'cancelled';
  reason: string | null;
  /**
   * §6.1/§7.1 sub-task fan-out: OPTIONAL — absent or null means this event belongs to the turn's main lane; a non-null value is the subTaskId (same id sub_task_start/sub_task_finish carry) of the sub-task lane this event belongs to.
   */
  subTaskId?: string | null;
}
/**
 * CRITICAL (requires ackId).
 */
export interface PushComplete {
  type: 'push_complete';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * Deterministic ackId = 'push_complete:{messageId}' (§6.1).
   */
  ackId: string;
  repos: {
    name: string;
    branch: string;
    sha: string;
  }[];
}
/**
 * CRITICAL (requires ackId).
 */
export interface PushError {
  type: 'push_error';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * Deterministic ackId = 'push_error:{messageId}' (§6.1).
   */
  ackId: string;
  error: string;
}
export interface SessionTitle {
  type: 'session_title';
  messageId: string;
  sessionId: string;
  gen: number;
  title: string;
}
export interface Warning {
  type: 'warning';
  messageId: string;
  sessionId: string;
  gen: number;
  message: string;
}
/**
 * CRITICAL (requires ackId). Named SandboxErrorEvent rather than Error in this schema — a $def literally named Error generates a TS interface that shadows the built-in Error class for any consumer that imports it unaliased.
 */
export interface SandboxErrorEvent {
  type: 'error';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * Deterministic ackId = 'error:{messageId}' (§6.1).
   */
  ackId: string;
  message: string;
  fatal: boolean;
}
/**
 * CRITICAL (requires ackId).
 */
export interface SnapshotReady {
  type: 'snapshot_ready';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * Deterministic ackId = 'snapshot_ready:{messageId}' (§6.1).
   */
  ackId: string;
  snapshotId: string;
  /**
   * Echoes the messageId of the Snapshot command this event is completing (sandbox-agent sets this to the exact MessageId of the Snapshot command it received). Optional and additive: this event has zero real production consumers before its own first implementation, so adding this field now is not a breaking wire-contract change. Exists so the control plane can correlate a snapshot_ready back to the specific attempt it answers -- gen alone cannot, since neither the snapshot-start nor snapshot-complete transition is gen-fenced (a snapshot cycle happens within the same gen). Absent/omitted on any producer that predates this field.
   */
  commandMessageId?: string;
}
/**
 * §7.1: brackets a spawned sub-task's lifetime. NOT critical (no ackId) — only sub_task_finish closes an 'active' state the ack protocol must guarantee delivery of.
 */
export interface SubTaskStart {
  type: 'sub_task_start';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * Stable correlator for this sub-task's lifetime (§7.1), derived from whatever correlator the engine itself exposes (OpenCode's own nested-task id today).
   */
  subTaskId: string;
  /**
   * Human-readable sub-task label (e.g. OpenCode's own subtask part 'description' field).
   */
  label: string;
  /**
   * The messageId of the enclosing main-lane message whose invocation spawned this sub-task.
   */
  parentMessageId: string;
  /**
   * Step 71 (§26.4/§7.1): the task tool's own 'subagent_type' dispatch parameter -- the literal named sub-agent (e.g. 'counter-reviewer', 'architecture-scribe', 'fact-check') the engine was actually told to invoke, VERIFIED LIVE as one of the task tool's own real input fields ({"description","prompt","subagent_type"}). Unlike label above (freeform, explicitly documented there as 'not a correctness-bearing value'), this is the engine's own reliable dispatch parameter, which is why post-hoc sub-task corroboration (reviewverdict.CounterReviewCorroborated) keys off this field, never off label. Optional and additive: this event has real, already-shipped production consumers that predate this field, so adding it now is not a breaking wire-contract change. Absent/omitted on any producer that predates this field, and always absent on the legacy/unverified-live subtaskPart fallback translation path (translateSubTaskStart), which has no task-tool input to extract this from at all.
   */
  subAgentType?: string;
}
/**
 * CRITICAL (requires ackId). §7.1: closes an 'active' state the UI tracks (a live sub-lane count), the same criticality reasoning as execution_complete at the turn level.
 */
export interface SubTaskFinish {
  type: 'sub_task_finish';
  messageId: string;
  sessionId: string;
  gen: number;
  /**
   * Deterministic ackId = 'sub_task_finish:{messageId}' (§6.1).
   */
  ackId: string;
  /**
   * Same subTaskId this sub-task's own sub_task_start carried.
   */
  subTaskId: string;
  /**
   * Reuses the turn's own outcome taxonomy (§3.3, §7.1).
   */
  outcome: 'completed' | 'failed' | 'cancelled';
}
