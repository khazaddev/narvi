/* eslint-disable */
/**
 * This file was automatically generated from /contracts JSON Schemas by
 * json-schema-to-typescript (contracts/scripts/generate-ts.mjs).
 * DO NOT EDIT IT BY HAND — edit the source .schema.json file and re-run
 * `npm run generate` instead.
 */

/**
 * sandbox-agent -> control-plane WS events (technical plan §6.1). Every event carries the common envelope (type, messageId, sessionId, gen). The 5 CRITICAL types (execution_complete, error, snapshot_ready, push_complete, push_error) additionally require ackId, deterministically formatted '{type}:{messageId}' (enforced here via both a description and a per-type 'pattern' anchored on the literal type prefix) so the ack protocol (§6.1: sender buffers 1000 events, evicts oldest non-critical, re-sends on reconnect until acked; receiver dedupes by upsert-on-messageId) can redeliver them exactly once. Field nullability convention: a property documented as 'nullable' is a REQUIRED key whose value may be JSON null, EXCEPT step_finish's cost.tokens.cached and cost.usd — §6.1 only specifies that 'tokens is an object, not a number'; the {input, output, cached?, usd?} breakdown and which of those sub-fields are optional is this contract's own invention, not literally specified by the technical plan.
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
  | SnapshotReady;

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
}
export interface StepStart {
  type: 'step_start';
  messageId: string;
  sessionId: string;
  gen: number;
  stepId: string;
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
 * §3.4 stash -> checkout -> pop sequence, reported as it happens.
 */
export interface GitSync {
  type: 'git_sync';
  messageId: string;
  sessionId: string;
  gen: number;
  status: 'stash' | 'checkout' | 'pop';
  branch: string;
}
/**
 * artifactType MUST match the Postgres artifact_type enum (migrations/000012_artifacts.up.sql) exactly.
 */
export interface Artifact {
  type: 'artifact';
  messageId: string;
  sessionId: string;
  gen: number;
  artifactType: 'pr' | 'preview' | 'upload';
  url: string;
  metadata: {
    [k: string]: unknown;
  };
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
}
