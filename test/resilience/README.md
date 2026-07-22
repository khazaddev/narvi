# test/resilience

Automated replay of the known failure scenarios that are this design's
differentiator — §9.3 (`docs/TECHNICAL_PLAN.md`), the phase-2 exit gate,
not an afterthought:

> These run as automated scenarios against a real (or provider-faked)
> stack.

This is the definitive scenario-by-scenario index for that exit
criterion: for each of the 12 scenarios below, exactly one of the
following is true, and stated plainly rather than overstated —

- **covered** — a real test proves it, cited by exact function name and
  file path (verified by grepping the repo while writing this doc, not
  carried over from memory);
- **covered with an accepted gap** — a real test proves the reachable
  part of it; the remainder is a deliberate, named, user-confirmed
  decision, not an oversight;
- **reserved for a follow-up PR** — the harness this PR builds is ready
  for it, but the scenario itself isn't written yet;
- **deferred to a later phase** — it genuinely cannot be built yet,
  because what it exercises doesn't exist yet (named by exact Step/PR
  number).

## The harness (this PR)

`harness_test.go` builds a real, reusable "mini control plane" — one
throwaway Postgres (via testcontainers, every embedded migration
applied), the real Postgres store types, and a real `wshub.Hub` — wired
together with only exported APIs, the same way `cmd/control-plane/main.go`
itself wires them. `Harness.NewRegistry` constructs a real
`sessionactor.Registry` sharing that same pool/hub, callable more than
once per test to simulate more than one "pod" against the same database
(a rolling restart, a fresh replica picking a session back up). This is
deliberately a more realistic, less white-box style than the existing
internal-package integration tests — appropriate for scenarios that
genuinely span multiple packages, not one package's own internals in
isolation.

It is kept intentionally minimal: built only as far as scenario #12 (the
one scenario this PR adds) actually needs. A follow-up PR is expected to
extend it once scenarios #3, #5's plain-SPAWN race variant, and #7's own
real needs are known — see those entries below for what that will likely
require: a way to inject boot delay (#3); a real/fake
`ports.SandboxProvider` (#5, mirroring `dispatch_integration_test.go`'s
own `fakeSpawnProvider` precedent); or a live commander plus a way to
simulate a dropped WS connection against this same rig (#7).
`Harness.NewRegistry` currently wires commander/provider/sourceControl as
nil, which is only sufficient for scenario #12 and a future #3 — #5 and
#7 will need a sibling constructor (or a direct `sessionactor.NewRegistry`
call reusing this harness's pool/timeouts/hub) rather than reusing
`NewRegistry` as-is.

## The 12 scenarios

### #1 — kill the CP pod mid-turn

> Kill the CP pod mid-turn → actor rehydrates, turn resumes or
> fails-with-reason; no stuck `processing`.

**Status: covered, with a scoped-and-documented half.** The "or" in the
scenario's own wording is a real disjunction, and only one branch is
built: turn resume onto a still-live sandbox mid-turn is real machinery
only as of scenario #2 below, not a rehydration-after-pod-kill path — no
turn-resume-across-a-pod-kill mechanism exists anywhere in this codebase
(confirmed: `domain/turn`'s own transition table has no edge out of
`Processing` except `Completed`/`Failed`/`Cancelled`). This is an
existing, already-documented scoping decision from when the test was
written (Step 21), not something reopened here.

- `TestResilience_KillPodMidTurn_TurnFailsWithReason_NoStuckProcessing`
  — `internal/app/sessionactor/resilience_killpod_integration_test.go`

### #2 — kill the sandbox mid-turn

> Kill the sandbox mid-turn → suspect → grace → respawn+resume with same
> conversation id.

**Status: covered**, end to end, literally as described: dispatch onto a
Ready sandbox at gen 1, force it through `Suspect`, let `terminal_grace`
elapse without recovery (`Suspect` → `Failed` → immediate respawn at gen
2), the new sandbox connects and boots, and the SAME still-`Processing`
turn is re-dispatched to gen 2 carrying the session's own recorded
`opencode_conversation_id`.

- `TestResilience_KillSandboxMidTurn_SuspectGraceRespawnReenqueueSameConversation`
  — `internal/app/sessionactor/resilience_turnrecovery_integration_test.go`

### #3 — slow boot

> Slow boot (inject 5-min delay in deps install) → boot_progress keeps
> session alive; no false kill.

**Status: reserved for a follow-up PR.** Only narrow, domain-decision-level
coverage exists today — no real end-to-end "a session survives a slow
boot because boot_progress events keep resetting its liveness deadline"
test exists anywhere in this repo:

- `TestEvaluateConnectingTimeout` —
  `internal/domain/sandbox/liveness_test.go` (proves the pure timeout
  decision function in isolation)
- `TestConnectingDeadlineHandoff_ToLivenessCheck` —
  `internal/app/sessionactor/timerfired_integration_test.go` (proves the
  handoff from the connecting-deadline timer to the steady-state liveness
  check, not a real sustained slow-boot survival)

The harness this PR builds (`harness_test.go`) is intended to carry this:
a fresh scenario file driving a sandbox through repeated `boot_progress`
events with injected delays between them, asserting the session is never
falsely killed.

### #4 — late `execution_complete`

> `execution_complete` arrives AFTER terminalization → state reconciled,
> automation counters corrected.

**Status: covered, with an accepted, self-documented gap.** State
reconciliation itself (a late `execution_complete` arriving after the
turn/session already terminalized some other way) is real and tested.
Automation-counter correction is not — `handleSandboxEvent`'s own doc
comment (`internal/app/sessionactor/sandboxevent.go`, not the test file
below) says so directly ("automations are not a built feature anywhere
in this codebase yet ... there is no automation_runs table, no
automation domain package, nothing to correct"), and that remains
accurate: it is genuinely Phase 3+ work, not something this PR's audit
found freshly.

- `TestHandleSandboxEvent_LateExecutionComplete_RecoversSandboxTurnAndSession`
  — `internal/app/sessionactor/suspectrecovery_integration_test.go`

### #5 — two concurrent spawns

> Two concurrent spawns (double-click / retry race) → single winner by
> gen fencing; loser sandbox reaped by GC.

**Status: covered for RESUME; the plain-SPAWN race variant is reserved for
a follow-up PR.** A genuine two-actor race is proven for the RESUME path
(two actors, same session, concurrently attempting to resume the same
sandbox — only one call to the provider's `ResumeSandbox` actually
happens), plus the epoch-fencing primitive that makes any such race safe
and the reconciler's own orphan-reaping half. What is NOT yet proven is
the same double-actor race for a plain SPAWN (as opposed to resume) —
that variant is reserved for the follow-up PR, not attempted here.

- `TestResilience_ConcurrentResumeAcrossActors_ResumeSandboxCalledAtMostOnce`
  — `internal/app/sessionactor/dispatch_integration_test.go`
- `TestExecuteSpawn_StaleEpochOnRecord_PropagatesErrStaleEpoch` —
  `internal/app/sessionactor/dispatch_integration_test.go` (the fencing
  primitive both spawn and resume rely on)
- `TestReconcileOnce_ReapsOrphansLeavesLiveRowAlone` —
  `internal/app/reconciler/reconciler_integration_test.go` (the loser's
  sandbox actually gets reaped)

### #6 — stale sandbox from an old gen reconnects

> Stale sandbox from old gen reconnects → rejected 403, logged, session
> unaffected.

**Status: covered**, across the two places a stale-gen reconnect is
actually rejected: the sandbox-side WS upgrade itself, and the
scm-credentials bearer-token endpoint a sandbox agent separately calls.

- `TestSandboxHandler_GenMismatch` —
  `internal/adapters/inbound/wshub/sandbox_test.go`
- `TestHandleSandboxEvent_FullRoundTrip` (its stale-gen sub-case) —
  `internal/app/sessionactor/sandboxevent_integration_test.go`
- `TestScmCredentials_GenMismatch_Rejected` —
  `internal/adapters/inbound/httpapi/scmcredentials_integration_test.go`

### #7 — WS drop during event stream

> WS drop during event stream → ack protocol redelivers the 6 critical
> events exactly once.

**Status: reserved for a follow-up PR.** The 6 critical types
(`execution_complete`, `error`, `snapshot_ready`, `push_complete`,
`push_error`, `sub_task_finish` — `contracts/sandbox-ws/v1/events.schema.json`'s
own description) are real, and each half of the ack protocol has *some*
coverage today, but not the full inbound pipeline end to end:

- `TestSendCritical_ResendUntilAckedThenNeverAgain` —
  `internal/sandboxagent/wsbridge/bridge_test.go` (sender-side: proves
  resend-until-acked, but only for ONE of the 6 critical types,
  `execution_complete`, against a fake `httptest` WS server, not a real
  control-plane inbound handler)
- `TestEventStore_Create_DedupesOnSessionIDAndMessageID` —
  `internal/adapters/outbound/postgres/event_artifact_wstoken_integration_test.go`
  (receiver-side dedupe, but only at the Postgres-store level — an
  upsert-on-`(session_id, message_id)` unit, not exercised through the
  full inbound WS handler pipeline)

Genuinely proving this scenario means a harness-driven test that drops a
real WS connection mid-stream and confirms redelivery-exactly-once
through the FULL pipeline (sandbox agent → control plane → Postgres),
for more than the one critical type already covered piecemeal — reserved
for the follow-up PR.

### #8 — provider API down during spawn

> Provider API down during spawn → typed transient error, retry with
> backoff, circuit breaker only on permanent.

**Status: covered, with an accepted, user-confirmed gap.** The
transient/permanent classification and the circuit breaker's own
permanent-only-increments behavior are genuinely tested against a real
fake provider. "Retry with backoff" specifically is NOT a real, distinct
mechanism anywhere in this codebase: spawn retries use a fixed
`SpawnStuckTimeout`-based force-respawn (`platform/timeouts.go`), not
exponential backoff — unlike `domain/imagebuild.EvaluateBackoff`
(`internal/domain/imagebuild/backoff.go`), which is a real backoff
mechanism, but for a completely different feature (image builds, not
spawn retries).

The user was asked and explicitly chose: document this as an accepted,
deliberate gap — no new backoff mechanism for spawn retries is being
built to satisfy this scenario's literal wording. This is a conscious
product decision, not an oversight this PR is hiding.

- `TestHandleEnsureDispatched_PermanentProviderError_IncrementsCircuitBreaker`
  — `internal/app/sessionactor/dispatch_integration_test.go`
- `TestHandleEnsureDispatched_TransientProviderError_DoesNotIncrementCircuitBreaker`
  — `internal/app/sessionactor/dispatch_integration_test.go`

### #9 — Outbox: Slack API 500s

> Outbox: Slack API 500s for 10 min → notification eventually delivered,
> no loss.

**Status: deferred to a later phase.** This scenario depends on
functionality that does not exist yet:

- **Step 35, "outbox delivery" (Phase 3)** — the outbox delivery worker
  itself. `internal/adapters/outbound/postgres/outbox_store.go`'s own doc
  comment: "No caching, no retries, no business rules — the delivery
  worker lands in PR-35."
- **Step 33, "Slack ingress" (Phase 3)** — the Slack Notifier adapter.
  `internal/adapters/outbound/slackapi/doc.go`: "Package slackapi will
  hold the Slack Notifier adapter, consumed via the outbox only —
  implemented in PR-35."

Nothing here is built or faked to simulate coverage; there is genuinely
nothing yet to test.

### #10 — concurrent @mentions on one PR

> Concurrent @mentions on one PR → exactly one review session (atomic
> claim).

**Status: deferred to a later phase.** This scenario depends on
functionality that does not exist yet:

- **Step 40/41, "domain/review" / "review sessions" (Phase 4)** — the
  review-session domain logic and its atomic-claim mechanism.
  `internal/domain/review/doc.go`: "Package review will hold code-review
  domain logic: risk-map verdicts, sentinels, and the verdict floor —
  implemented in PR-40."
- **Step 32, "GitHub ingress" (Phase 3)** — the inbound webhook adapter
  that would even observe an `@mention`. `internal/adapters/inbound/github/doc.go`:
  "Package github will hold the GitHub webhook ingress adapter,
  normalizing events into the shared CreateSessionRequest — implemented
  in PR-32."

Nothing here is built or faked to simulate coverage; there is genuinely
nothing yet to test.

### #11 — dirty working tree at relaunch

> Dirty working tree at relaunch → stash → checkout session branch → pop;
> zero lost user edits.

**Status: covered**, and the strongest, most literal match of all 12
scenarios against its own §9.3 wording — a real dirty tree, a real
relaunch onto a different branch, a real stash → checkout → pop sequence,
asserted against real git state with zero lost edits.

- `TestResilienceScenario11_DirtyWorkingTree_RelaunchWithDifferentBranch_ZeroLostEdits`
  — `internal/sandboxagent/gitclone/sync_test.go`

### #12 — deploy rollout (rolling restart)

> Deploy rollout (rolling restart) → zero sessions marked failed.

**Status: covered — this PR's own new scenario**, the harness's first
proof-of-concept. A real session with a Ready sandbox and a turn
genuinely `Processing` (dispatched moments ago, every timer armed
comfortably in the future — none of the machinery scenario #1 relies on
is anywhere near overdue), owned by a real actor via the harness's
`Registry.GetOrSpawn`. `Registry.Shutdown()` — the REAL graceful path
`cmd/control-plane/main.go` itself uses on `SIGTERM`, not
`pg_terminate_backend`-style connection-severing the way scenario #1's
test simulates a hard pod kill — is called while the turn is still
genuinely in flight. The turn and session are read back from Postgres
afterward and confirmed NOT force-failed by the shutdown itself; a second
`Registry` (a fresh "pod" against the same database) is then confirmed
able to take ownership of the same session again, proving it is truly
resumable, not merely "not yet marked failed".

- `TestResilienceScenario12_GracefulRollingRestart_ZeroSessionsMarkedFailed`
  — `test/resilience/scenario12_rolling_restart_test.go`

## Summary

| # | Scenario | Status |
|---|---|---|
| 1 | Kill CP pod mid-turn | Covered (fails-with-reason half only, documented since Step 21) |
| 2 | Kill sandbox mid-turn | Covered |
| 3 | Slow boot | Reserved for follow-up PR |
| 4 | Late `execution_complete` | Covered (automation-counter correction deferred to Phase 3+) |
| 5 | Concurrent spawns | Covered for RESUME; plain-SPAWN race reserved for follow-up PR |
| 6 | Stale-gen reconnect | Covered |
| 7 | WS-drop ack redelivery | Reserved for follow-up PR |
| 8 | Provider down during spawn | Covered (backoff-retry mechanism an accepted, user-confirmed gap) |
| 9 | Outbox: Slack API 500s | Deferred — needs Step 33 + Step 35 (Phase 3) |
| 10 | Concurrent @mentions | Deferred — needs Step 32 (Phase 3) + Step 40/41 (Phase 4) |
| 11 | Dirty working tree at relaunch | Covered |
| 12 | Deploy rollout (rolling restart) | Covered — this PR |
