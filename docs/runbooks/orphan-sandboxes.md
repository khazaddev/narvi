# Orphan sandboxes reaped at a sustained rate

Backs alert: `OrphanReapRateHigh` (`deploy/observability/alerts/reliability.json`).
Dashboard: [sandbox-lifecycle.json](../../deploy/observability/dashboards/sandbox-lifecycle.json).

## Symptom

Cloud spend on sandbox instances looks higher than the number of live
sessions would explain, or the provider's own console shows more running
instances than `sandboxes` rows in a live status
(Spawning/Connecting/Booting/Ready/Snapshotting/Suspect). Individually,
this is invisible to users — a reaped orphan was never doing real work for
anyone — but a *sustained* rate points at something upstream leaking real
cloud resources.

## Confirm

- `orphans_reaped` (counter) — increments only when
  `internal/app/reconciler.Reconciler.ReconcileOnce` finds a real
  provider-side sandbox with no matching live Postgres row and
  successfully stops it. **A single, occasional reap is expected, not a
  bug** (see below) — the alert's own 10-minute/5-reap threshold is
  calibrated to sit above that noise floor.
- Log: `"reconciler: stop orphaned sandbox failed"` — a failed stop
  attempt does NOT increment `orphans_reaped` and stays tracked for retry
  on the reconciler's own next tick (`ReconcilerInterval`, 60s); repeated
  failures for the SAME `provider_id` point at a provider-side problem
  stopping that specific instance, not a Narvi bug.

## Why an occasional single reap is not itself a problem

`internal/app/reconciler/reconciler.go`'s own doc comment names the
inherent race: `dispatch.go`'s three-step spawn sequencing commits a
`sandboxes` row already in a live status (`spawning`) with `provider_id`
still `NULL`, calls the real, network-bound `SandboxProvider.CreateSandbox`
**outside** any transaction, then commits a second transact that finally
records `provider_id`. A reconcile tick landing in that exact window sees
a real, wanted cloud object with no match in Postgres yet — indistinguishable,
for that one tick, from a genuine orphan.
`ReconcilerOrphanConfirmationPeriod` (30s, `internal/platform/timeouts.go`)
exists precisely to not reap on first sighting; `OrphanReapRateHigh`'s own
threshold (>5 in 10 minutes) is set well above what that single-race
window alone would ever produce.

## Remediation

1. **Confirm it's sustained, not a blip.** Check the dashboard panel
   (`orphan-gc-rate`) over the last hour, not just whether the alert
   fired once.
2. **Look for a spawn-path bug leaking real resources.** A sustained
   orphan rate usually means something is calling
   `SandboxProvider.CreateSandbox`/`RestoreFromSnapshot`/`ResumeSandbox`
   successfully and then failing to commit the corresponding Postgres
   write on a wider scale than the single-race window above — e.g. a
   stale-epoch takeover racing a real provider call
   (`executeSpawn`/`executeRestore`/`executeResume`'s own doc comments in
   `internal/app/sessionactor/dispatch.go` name this exact case and log
   `"...orphaned by stale-epoch takeover..."` when it happens; check
   whether THAT log line's own rate correlates with the `orphans_reaped`
   rate). A correlated spike in actor-epoch churn (frequent pod
   restarts/rescheduling) is the most likely proximate cause, not a
   reconciler bug.
3. **Check the provider's own health.** A provider returning malformed or
   delayed `CreateSandbox` responses (success recorded provider-side, but
   the response Narvi receives errors or times out) produces the exact
   same orphan signature.
4. **No manual reap is needed** — the reconciler's own next tick
   (`ReconcilerInterval`, 60s) retries automatically; this runbook is
   about finding and fixing the upstream cause, not draining a queue.

## Resilience scenario

§9.3 scenario #5 ("two concurrent spawns") proves the reap half of this
mechanism directly against a real Postgres `sandboxes` row with no live
owner:
`TestReconcileOnce_ReapsOrphansLeavesLiveRowAlone` —
`internal/app/reconciler/reconciler_integration_test.go`. The
concurrent-spawn race that CREATES a loser sandbox in the first place is
covered by the same scenario's other two tests
(`TestResilience_ConcurrentResumeAcrossActors_ResumeSandboxCalledAtMostOnce`,
`TestResilience_ConcurrentPlainSpawnAcrossActors_CreateSandboxCalledAtMostOnce`,
both `internal/app/sessionactor/dispatch_integration_test.go`) — see
`test/resilience/README.md`'s own scenario #5 entry for the full picture.
