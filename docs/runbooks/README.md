# Runbooks

Operator runbooks for the failure modes Narvi's own dashboards/alerts
(`deploy/observability/`) surface, drawn from the resilience catalog
(`docs/TECHNICAL_PLAN.md` §9.3, indexed scenario-by-scenario in
`test/resilience/README.md`) plus the operational surfaces Steps 73a/74/76
added. Each entry below states the symptom an operator sees, the metric or
log line that confirms it, the remediation, and — where one exists — the
resilience scenario that reproduces it. **Fewer, real entries, not a
complete-looking table**: a failure mode this system cannot actually have
gets no entry here, and a gap in what's actually buildable (e.g. no
automated outbox dead-letter replay) is stated as a gap, not glossed over.

| Runbook | Alert(s) it backs | §9.3 scenario |
|---|---|---|
| [outbox-delivery.md](outbox-delivery.md) | `OutboxLagHigh`, `OutboxDeadLetterAny` | #9 (Slack API 500s) |
| [orphan-sandboxes.md](orphan-sandboxes.md) | `OrphanReapRateHigh` | #5 (concurrent spawns / orphan GC) |
| [slow-boot-and-spawn.md](slow-boot-and-spawn.md) | `BootDurationP95High`, `SpawnLatencyP95High` | #3 (slow boot) |
| [watchdog-false-alarms.md](watchdog-false-alarms.md) | `WatchdogFalseAlarmRateHigh` | none (see entry) |
| [turn-false-failures.md](turn-false-failures.md) | `TurnFalseFailureAny` | #4 (late `execution_complete`), adjacent not identical — see entry |
| [sandbox-capability-refusals.md](sandbox-capability-refusals.md) | none (no metric exists yet — see entry) | #17 (restore-with-docker) |
| [signing-key-rotation.md](signing-key-rotation.md) | none (routine admin procedure, not a failure) | n/a |

## Cross-references, not duplicated here

- **Cohort-rollout rollback** (Step 76, §32): the full operator runbook —
  enrolling/de-enrolling a repository, arming/disarming cohort mode
  platform-wide, and verifying a rollback took effect — already lives at
  `docs/TECHNICAL_PLAN.md` §32.9. That is the single source of truth for
  rollback; this directory does not restate it. `session_rollout_refused_total`
  (tagged `spawn_source`) is the metric §32.9 itself already names.
- **Docker/egress substrate refusals** (Step 74, §27.5/§27.6): see
  [sandbox-capability-refusals.md](sandbox-capability-refusals.md) below —
  short enough not to need its own cross-reference note, but distinct from
  rollout refusals above (a substrate refusal means the *provider* can't
  honor what the *Environment* requires; a rollout refusal means the repo
  simply isn't enrolled yet).
- **Signing-key rotation** (Step 73a, §27.3): see
  [signing-key-rotation.md](signing-key-rotation.md) below.

## Instruments these runbooks rely on

Every metric name mentioned in this directory is checked against the real
registered OTel instruments by `internal/ops`'s own CI-enforced drift test
(`go test ./internal/ops/...`, part of `make test`) — see that package's
own `doc.go` for why. A metric named here that the code stops emitting
fails that build, not just this documentation.
