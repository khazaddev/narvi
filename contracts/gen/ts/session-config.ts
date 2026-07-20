/* eslint-disable */
/**
 * This file was automatically generated from /contracts JSON Schemas by
 * json-schema-to-typescript (contracts/scripts/generate-ts.mjs).
 * DO NOT EDIT IT BY HAND — edit the source .schema.json file and re-run
 * `npm run generate` instead.
 */

/**
 * The SESSION_CONFIG document (§4.1: 'sandbox env passed as one SESSION_CONFIG JSON document — the provider never assembles env fragments'). This shape is NOT fully enumerated anywhere in the technical plan; it is built here from what §4.1/§3.4/§5.2/§5.3/§6.4 explicitly reference. It is expected to GROW when PR-12 (Modal provider) actually consumes it — treat this as the honest floor, not a final shape. Field nullability convention: 'nullable' means a required key whose value may be JSON null.
 */
export interface SessionConfig {
  sessionId: string;
  /**
   * This sandbox instance's own stable identity (sandboxes.id), delivered to sandbox-agent so it can present itself correctly as the X-Sandbox-ID header on the sandbox WS handshake (§6.1).
   */
  sandboxId: string;
  /**
   * Spawn generation (§3.2 fencing).
   */
  gen: number;
  /**
   * §5.2: sandbox tokens are hashed at rest control-plane-side; one per gen. This is the plaintext bearer value handed to the sandbox at spawn time.
   */
  sandboxToken: string;
  /**
   * §6.4. Delivered to the sandbox as the NARVI_BOOT_MODE env var.
   */
  bootMode: 'build' | 'fresh' | 'repo_image' | 'snapshot_restore';
  /**
   * Where sandbox-agent connects back for the sandbox WS (§6.1).
   */
  controlPlaneWsUrl: string;
  /**
   * §3.4: position 0 = primary; repos are always a list, never a scalar single-repo mirror.
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
   * §5.3 propagation chain: webhook -> CP -> provider -> sandbox-agent -> OpenCode wrapper -> back. Null only when no upstream correlation id exists (e.g. session created without an ingress webhook).
   */
  correlationId: string | null;
}
