# Service-level objectives and the alerts that back them

Five SLOs — each one a promise about **user-visible** behavior, derived
from a real budget already committed in `internal/platform/timeouts.go`
(never an invented number), backed by an alert already shipped in
`deploy/observability/alerts/reliability.json` (Step 77) that measures
the SAME quantity the objective is actually about. No new alert is added
by this Step — see "Why no new alert" at the bottom for why that is a
deliberate choice, not an oversight.

Every metric name below is checked against the real registered OTel
instruments by `internal/ops`'s own `TestNoMetricDrift`
(`go test ./internal/ops/...`, part of `make test`) — an SLO or an alert
naming a metric the code stops emitting fails that build, not just this
document.

## SLO 1 — A sandbox becomes usable quickly

**Objective.** A newly spawned sandbox reaches ready (first sign of life)
within the SAME budget the liveness watchdog itself uses to decide a
booting sandbox has gone quiet too long — not merely "eventually", a
concrete, user-felt ceiling: this is the time between "create a session"
and the agent actually being able to do anything.

**Metric.** `sandbox_agent_boot_duration_seconds` (histogram, p95) — the
sandbox-agent's own wall-clock measurement of one full boot-to-ready
sequence (`internal/sandboxagent/boot/telemetry.go`).

**Threshold and the arithmetic linking them.** `platform.Timeouts.
FirstConnectBudget` is **240s**, explicit in the plan (§3.2: "covers
provider cold start + boot") — the exact ceiling
`EvaluateConnectingTimeout` (`internal/domain/sandbox/liveness.go`) uses
to decide a booting sandbox has been silent too long and should be
suspected. A p95 boot time AT that budget means a material fraction of
ordinary, healthy boots are now at real risk of being watchdog-suspected
purely for being slow, not for being genuinely stuck — the objective is
therefore "p95 boot duration stays measurably below the watchdog's own
ceiling", not merely "below 240s with zero margin".

**Alert.** `BootDurationP95High` — `p95(sandbox_agent_boot_duration_seconds)
> 240s for 15m`, severity warning, runbook
[slow-boot-and-spawn.md](runbooks/slow-boot-and-spawn.md).

## SLO 2 — Sandbox spawn calls complete within the cold-start budget

**Objective.** A real provider `CreateSandbox`/`RestoreFromSnapshot`/
`ResumeSandbox` call completes within the worst-case cold-start latency
the rest of the timeout hierarchy was calibrated against — the
provider-facing half of the same "how long until my session is usable"
promise SLO 1 covers, but measured at the provider API boundary instead
of sandbox-agent's own boot sequence, so a provider-side regression is
visible independently of anything happening inside the sandbox itself.

**Metric.** `sandbox_spawn_duration_seconds` (histogram, p95) — wall-clock
duration of one real `SandboxProvider.CreateSandbox`/
`RestoreFromSnapshot`/`ResumeSandbox` call
(`internal/app/sessionactor/dispatch.go`).

**Threshold and the arithmetic linking them.** `platform.Timeouts.
ProviderWorstColdStart` is **220s** — §4.1's own stated floor ("Modal
cold scheduling alone can take 220s+"), the figure `platform.Timeouts.
ProviderHTTPClientTimeout` (5 min) is required to clear WITH MARGIN
(`Timeouts.Validate`'s own Chain B invariant: `ProviderHTTPClientTimeout
> ProviderWorstColdStart`). A p95 spawn latency AT OR ABOVE 220s means
the provider's real-world performance has drifted past what the rest of
the hierarchy assumes — not yet timing out (the HTTP client timeout still
has margin), but eating into that margin.

**Alert.** `SpawnLatencyP95High` —
`p95(sandbox_spawn_duration_seconds) > 220s for 15m`, severity warning,
runbook [slow-boot-and-spawn.md](runbooks/slow-boot-and-spawn.md).

## SLO 3 — A turn we tell the user failed genuinely failed

**Objective.** When Narvi reports a turn as `Failed` due to timing out, it
is actually telling the truth — the turn was genuinely stuck, not still
working and about to finish successfully. This is a correctness promise,
not a latency one: `platform.Timeouts.TurnDeadline` (**60 min**, "chosen
with margin below `SupervisorTurnCap`") is the CP's own persistent
`turn_deadline` timer, armed on every dispatch — the promise is that when
it fires and terminalizes a turn `Failed`, that verdict is correct.

**Metric.** `turn_false_failure_total` (counter) — incremented exactly
when a late, REAL `execution_complete{outcome:completed}` event arrives
for a session the control plane already terminalized `Failed` with
`failure_reason=timeout` (`internal/app/sessionactor`).

**Threshold and the arithmetic linking them.** Zero tolerance, not a
rate: unlike a watchdog false alarm (SLO 5 below), a
`turn_false_failure_total` increment has **no documented benign
single-race explanation** — every increment means `TurnDeadline` fired on
a turn that was, in fact, still genuinely working. This is the sharpest
possible version of "the metric measures the quantity the objective is
about": the objective is "TurnDeadline is never wrong", and this counter
increments *exactly* when it was wrong, nothing else.

**Alert.** `TurnFalseFailureAny` —
`increase(turn_false_failure_total[30m]) > 0`, severity warning, runbook
[turn-false-failures.md](runbooks/turn-false-failures.md).

## SLO 4 — A user-visible notification is delivered within its retry budget

**Objective.** A Slack/Linear/GitHub reply, a plan-approval-request
message, or a turn-completion notice reaches the user within the FULL
legitimate retry window the outbox's own backoff schedule allows — not
"instantly" (a transient third-party outage is expected to self-heal via
retry), but within a bounded, derivable ceiling, never indefinitely.

**Metric.** `outbox_lag_seconds` (gauge) — age, in seconds, of the oldest
still-due outbox row claimed at the start of the most recent pump tick
(`internal/app/outboxworker`). Deliberately NOT
`outbox_due_backlog_count` (a companion gauge, useful for confirming a
*sustained* outage where every pending row is mid-backoff and lag alone
reads misleadingly near zero — see the outbox runbook) — `outbox_lag_seconds`
is the one that actually measures elapsed wait time, the quantity this
objective is about.

**Threshold and the arithmetic linking them (full derivation, carried
verbatim from `deploy/observability/alerts/reliability.json`'s own
`thresholdDerivation`, re-verified against the current
`platform.DefaultTimeouts()` values as part of this Step).**
`outbox_lag_seconds` measures a row's CUMULATIVE age since creation, not
the delay of any single retry — comparing it against
`OutboxBackoffMax` (300s) alone, which bounds only ONE retry interval,
would be exactly Step 77's own review-caught arithmetic mistake (a
cumulative-age metric compared against a single-interval constant). The
correct ceiling is the cumulative age a row reaches after exhausting
`domain/outbox.MaxAttempts` (**10**) attempts:
`EvaluateBackoff`'s schedule with the shipped defaults
(`OutboxBackoffBase` 30s, `OutboxBackoffMax` 300s) produces retry delays,
per attempt 1 through 9 (attempt 10 is the dead-lettering attempt — no
further delay is ever scheduled after it): 30s, 60s, 120s, 240s, 300s,
300s, 300s, 300s, 300s. Sum = 30+60+120+240+(300×5) = **1950s** — the age
at which a row's own FINAL legitimate attempt becomes due. Add worst-case
real-world overhead per retry cycle — up to `OutboxPumpInterval` (5s,
waiting for a tick to claim a just-due row) plus `OutboxDeliveryTimeout`
(15s, the bounded worst case for the failing `Deliver` call itself) —
across the 9 cycles between attempts 1–10: 9 × (5s+15s) = 180s.
Worst-case legitimate age: 1950s + 180s = **2130s** (35.5 min). **2400s**
(40 min) sits comfortably above that with headroom.

**Alert.** `OutboxLagHigh` — `p95(outbox_lag_seconds) > 2400s for 10m`,
severity warning, runbook
[outbox-delivery.md](runbooks/outbox-delivery.md). Its sibling
`OutboxDeadLetterAny` (`increase(outbox_dead_letter_total[5m]) > 0`) backs
the same objective's own hard failure mode — a notification that
exhausted its entire retry budget and will never be delivered
automatically — see the same runbook.

## SLO 5 — Liveness detection is accurate

**Objective.** When the system suspects a sandbox is dead (a watchdog
activation), it is almost always right — the sandbox was actually dead,
not merely slow-but-alive and about to send its next heartbeat inside
`FirstConnectBudget`/`SteadyHeartbeatBudget`. A false suspicion is a
user-visible false failure: a working session gets treated as though it
crashed.

**Metric.** `watchdog_false_alarm_total` / `watchdog_activation_total`
(ratio of two counters) — the fraction of watchdog activations that
turned out to be wrong (the sandbox recovered during its own
`terminal_grace` window, proving it was alive the whole time)
(`internal/app/sessionactor`).

**Threshold and the arithmetic linking them.** This is the one SLO in
this set whose threshold is **not** derived from a single
`platform.Timeouts` constant — stated honestly rather than dressed up
with false precision: §5.3 states a target of "~0" but gives no numeric
figure, and no single `Timeouts` field governs a RATE (a behavioral
property, not a fixed duration). **10%** is an explicit judgment call,
following `platform/timeouts.go`'s own "not specified; chosen" convention
for exactly this situation — chosen because it sits well above the
occasional single-heartbeat-racing-the-boundary noise the two-budget
liveness model (`internal/domain/sandbox/liveness.go`,
`FirstConnectBudget`/`SteadyHeartbeatBudget`) can produce on its own. A
minimum-activation floor (5, in the same 30-minute window) avoids a
misleading ratio from a tiny sample.

**Alert.** `WatchdogFalseAlarmRateHigh` —
`ratio(watchdog_false_alarm_total, watchdog_activation_total) > 0.10 over
30m, minimum 5 activations in the window`, severity warning, runbook
[watchdog-false-alarms.md](runbooks/watchdog-false-alarms.md).

## Why no new alert

Step 78 deliberately adds **zero** new alerts to
`deploy/observability/alerts/*.json`. Every SLO above reuses an alert
Step 77 already shipped and already derived correctly against a real
`platform.Timeouts` budget — re-verified line-by-line above, not merely
copied. Two real gaps exist and are named here rather than papered over
with an invented alert:

- **No SLO covers real-time UI update latency** (how quickly a browser
  tab sees a new event over its WebSocket) or **notification-channel
  ingress health** (Slack/Linear/GitHub webhook processing latency) —
  no OTel instrument measures either quantity today. Inventing a
  threshold with no metric behind it would be exactly the "a threshold
  with no derivation is a guess someone will silence" failure mode
  `internal/ops.Alert.Validate` already refuses to let ship
  (`thresholdDerivation` is a required field for precisely this reason).
- `OrphanReapRateHigh` (`deploy/observability/alerts/reliability.json`)
  is a REAL, correctly-derived alert this Step deliberately does NOT
  promote to an SLO here: orphan-sandbox reaping is a platform cost/
  hygiene concern, not something a user directly experiences — an SLO is
  a promise about user-visible behavior (this document's own opening
  line), and stretching that definition to cover it would blur the one
  property that makes an SLO meaningful.

Building either of the first gap's own metrics is future work, not this
Step's — `internal/ops`'s own `TestNoMetricDrift` guarantees that if one
ever ships, the alert naming it can never silently drift from what the
code actually registers.
