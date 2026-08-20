# Watchdog false-alarm rate elevated

Backs alert: `WatchdogFalseAlarmRateHigh`
(`deploy/observability/alerts/reliability.json`). Dashboard:
[sandbox-lifecycle.json](../../deploy/observability/dashboards/sandbox-lifecycle.json)
(`watchdog-activation-rate`, `watchdog-false-alarm-rate`,
`liveness-gap-p95` panels).

## Symptom

Sandboxes that were never actually stuck get transitioned to `Suspect` and
respawned mid-turn — visible to a user as a turn that appears to restart
or "reconnect" partway through for no obvious reason, even though the
agent was making real progress.

## Confirm

```json narvi-metrics
{"metrics": ["watchdog_activation_total", "watchdog_false_alarm_total", "sandbox_liveness_gap_seconds"]}
```

- `watchdog_activation_total` (counter, tagged `watchdog`:
  `inactivity`/`connecting_deadline`/`liveness_check`) — every time
  `transitionSandboxToSuspect` (`internal/app/sessionactor/timerfired.go`)
  fires for one of the three watchdog-style timers.
- `watchdog_false_alarm_total` (counter, tagged `recovered_from`) — every
  time a `Suspect` sandbox recovers DURING its own `terminal_grace` window
  (§3.2: "any liveness signal during grace returns to previous state") —
  proof, after the fact, that the watchdog that suspected it was wrong.
  The **ratio** of this to `watchdog_activation_total` is what the alert
  actually watches.
- `sandbox_liveness_gap_seconds` (histogram) — how long the sandbox had
  actually shown no sign of life by the moment the watchdog fired. This is
  bounded BELOW by whichever budget fired it
  (`FirstConnectBudget`/`SteadyHeartbeatBudget`/`InactivityTimeout`,
  `internal/platform/timeouts.go`) by construction — a p95 sitting only
  slightly above that budget (the healthy case) vs. one sitting well above
  it (the timer pump itself falling behind, a different problem — see
  step 3 below) look different on this panel.
- Log: `"sessionactor: suspect recovery rejected; leaving sandbox
  suspect"` is the DEFENSIVE-only failure path (should not fire in
  practice per `sandboxevent.go`'s own top comment) — its presence at all
  is itself worth investigating, separately from the false-alarm rate
  itself.

## Remediation

1. **Break down `watchdog_activation_total` by `watchdog`.** A
   `liveness_check`/`inactivity` spike (both only ever fire from `Ready`)
   points at heartbeat delivery itself being unreliable — check whether
   it correlates with WS reconnects or a network path between the
   sandbox and control plane. A `connecting_deadline` spike points at
   slow boots — see [slow-boot-and-spawn.md](slow-boot-and-spawn.md)
   first, since `BootDurationP95High` firing at the same time is the more
   direct signal.
2. **A single false alarm is not itself actionable** — the two-budget
   liveness model (`internal/domain/sandbox/liveness.go`) has an
   inherent boundary case: a heartbeat that lands just barely on the wrong
   side of `SteadyHeartbeatBudget` (90s) due to ordinary network jitter
   will occasionally trip a watchdog that then immediately recovers. The
   alert's own 10% threshold (over a 30-minute window, minimum 5
   activations — see the alert file's own `thresholdDerivation`) is
   deliberately above that noise floor.
3. **A SUSTAINED elevated rate, especially with `sandbox_liveness_gap_seconds`
   sitting well above the firing watchdog's own budget**, points at the
   `TimerFired` delivery path itself lagging — check
   `platform.Timeouts.TimerPumpInterval` (5s)/`TimerClaimDuration` (30s)
   against actual timer-pump tick latency (no dedicated metric exists for
   this specific lag today — this is a genuine, currently-unbuilt gap in
   what this Step instruments; a correlated spike across MANY sessions'
   own `sandbox_liveness_gap_seconds` values, all sitting well above their
   respective budgets at the same wall-clock time, is the closest
   available signal).

## Resilience scenario

No §9.3-catalogued scenario reproduces THIS specific property (a false
alarm's own rate) end to end — stated honestly rather than pointing at an
adjacent one that doesn't actually prove it. The Suspect-recovery
mechanism `watchdog_false_alarm_total` counts is proven correct in
isolation by Step 24's own tests
(`TestHandleSandboxEvent_SuspectRecovery_ReturnsToPreSuspectStatus`,
`internal/app/sessionactor/suspectrecovery_integration_test.go`) and by
this Step's own new instrument test
(`TestHandleSandboxEvent_SuspectRecovery_RecordsWatchdogFalseAlarm`,
`internal/app/sessionactor/opsmetrics_integration_test.go`) — both prove
the MECHANISM fires correctly for one recovery, not a false-alarm RATE
under sustained load, which is not something a resilience scenario is
well-suited to reproduce deterministically in the first place.
