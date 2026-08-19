# Sandbox substrate capability refusals (Docker / egress policy)

Step 74 ("sandbox substrate: docker, egress policy, toolchain", §27.5/§27.6).
No alert backs this today — see "No metric exists yet" below; this entry
exists because the symptom is real and worth an operator knowing how to
read, even without a dashboard panel pointing at it yet.

## Symptom

A session creation request is refused with HTTP 422 (`docker`/
`egress_policy` requirement the configured provider cannot honor), OR an
already-existing session's sandbox never spawns and stays stuck —
refused at every dispatch attempt rather than genuinely retrying.

## Confirm

Two SEPARATE, independent checks, run at two different times (§27.5's own
"fail-closed, twice" rule) — either one refusing is enough to block:

- **Session-creation time**: `checkSubstrateCapabilitiesUpFront`
  (`internal/adapters/inbound/httpapi/create.go`) — a `422` HTTP response
  body naming the mismatch directly (from
  `environment.CheckSubstrateCapabilities`). No log line of its own; the
  HTTP response IS the signal, visible to whichever client attempted the
  create.
- **Dispatch time** (every spawn/restore/resume attempt, not just the
  first): `refuseIfSubstrateUnsupported`
  (`internal/app/sessionactor/dispatch.go`) — log line
  `"sessionactor: refusing to spawn: configured provider cannot honor
  this Environment's substrate requirements (§27.5/§27.6 dispatch-time
  fail-closed re-check)"`, carrying `session_id` and the underlying
  `environment.CheckSubstrateCapabilities` error. This is the one that
  fires for an ALREADY-created session whose provider was reconfigured
  (or whose capabilities genuinely changed) after creation — the up-front
  check alone cannot catch that.

## No metric exists yet

Unlike `session_rollout_refused_total` (the rollout gate's own counter,
§32), neither refusal path above increments an OTel instrument today —
this is a genuine, currently-unbuilt gap, named honestly rather than
invented for this Step: IMPLEMENTATION_PLAN.md row 77 names four specific
dashboard subjects (false failures, outbox lag, orphans, boot p95) plus
§5.3's own list (spawn latency, boot phase durations, liveness gaps,
watchdog activations, outbox lag, orphan GC count) — a substrate-refusal
counter is not among either, so adding one is out of this Step's own
scope. The log line above is the only confirmable signal today.

## Remediation

1. **`docker: required` refused**: the configured `SandboxProvider`'s own
   `Capabilities().DockerInSandbox` is `false` — Modal's default gVisor
   sandboxes cannot run `dockerd` cleanly (§5.2); confirm the provider is
   actually configured for VM-runtime mode if Docker-in-sandbox is a real
   product requirement for this Environment, or unset `docker: required`
   on the Environment if it was enabled by mistake.
2. **`egress_policy: allowlist` refused**: the provider's own
   `Capabilities().EgressPolicy` does not support enforced egress —
   confirm the provider substrate actually has the allowlist mechanism
   wired (§27.6's own server-appended floor: control-plane host + git
   hosts), or relax the Environment's own `egress_policy` to `open` if
   enforcement was enabled by mistake.
3. **Refused only at dispatch time, not at creation** (the session was
   created successfully): the provider was reconfigured, or its real
   capability set changed, AFTER this session's Environment was set up.
   Either restore the provider's own prior capability, or accept this
   session's substrate requirement can no longer be honored and roll it
   back via the Environment's own settings.

## Resilience scenario

§9.3 scenario #17 ("restore-with-docker") proves an adjacent but distinct
property — that a Docker-required session's sandbox is NEVER restored
from a stale snapshot even if one exists on its row, always taking a
fresh spawn instead — not the capability-refusal gate itself:
`TestResilienceScenario17_RestoreWithDocker_NeverRestoresStaleSnapshot` —
`internal/app/sessionactor/snapshot_docker_integration_test.go`. No
catalogued §9.3 scenario reproduces the refusal gate above end to end; it
is covered directly by
`TestDispatch_RefusesDockerRequiredSessionWhenProviderUnsupported` /
`TestDispatch_RefusesEgressAllowlistSessionWhenProviderUnsupported` (the
dispatch-time re-check) — `internal/app/sessionactor/
dispatch_substrate_integration_test.go` — and the pure decision function
itself, `internal/domain/environment/substrate_test.go`.
