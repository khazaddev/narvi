# Slow sandbox spawn/boot

Backs alerts: `BootDurationP95High`, `SpawnLatencyP95High`
(`deploy/observability/alerts/reliability.json`). Dashboard:
[sandbox-lifecycle.json](../../deploy/observability/dashboards/sandbox-lifecycle.json).

## Symptom

Users see a session sit in a "starting" state longer than usual before the
agent begins working, or (in the worst case) a slow-but-alive sandbox gets
watchdog-suspected and respawned even though nothing was actually stuck —
see [watchdog-false-alarms.md](watchdog-false-alarms.md) for that specific
downstream symptom.

## Confirm — which half is slow

```json narvi-metrics
{"metrics": ["sandbox_spawn_duration_seconds", "sandbox_agent_boot_duration_seconds", "sandbox_agent_hook_rerun_duration_seconds", "sandbox_agent_git_fetch_duration_seconds", "sandbox_agent_git_checkout_duration_seconds"]}
```

Two genuinely separate phases, each with its own panel/alert, so check
both before assuming which one regressed:

- **Provider-side spawn latency** — `sandbox_spawn_duration_seconds`
  (p95). This is the real `SandboxProvider.CreateSandbox`/
  `RestoreFromSnapshot`/`ResumeSandbox` call's own wall-clock duration
  (`internal/app/sessionactor/dispatch.go`'s `executeSpawn`/
  `executeRestore`/`executeResume`) — time spent BEFORE the sandbox even
  connects. Tagged by `action` (spawn/restore/resume) and `outcome` — break
  out by `action` first: a slow `restore` and a slow fresh `spawn` point
  at different provider-side subsystems.
- **In-sandbox boot sequence** — `sandbox_agent_boot_duration_seconds`
  (p95, recorded sandbox-agent-side, `internal/sandboxagent/boot/
  telemetry.go`) — repo clone/sync through hook/service startup, AFTER
  the sandbox has already connected. Break down further with
  `sandbox_agent_hook_rerun_duration_seconds` (one hook's own time —
  tagged `repo`/`hook`/`boot_mode`/`workspace_moved`),
  `sandbox_agent_git_fetch_duration_seconds`, and
  `sandbox_agent_git_checkout_duration_seconds` to isolate which boot
  phase actually regressed.
- Log: `sandbox-agent` emits a **boot fingerprint** first on every boot
  (binary version, image digest, repo SHAs, boot mode, §5.3) — check
  whether a slow boot correlates with a specific image digest (a bad base
  image) or boot mode (`build`/`fresh` are expected to be slower than
  `repo_image`/`snapshot_restore`; only a regression WITHIN one boot mode
  is actionable).

## Remediation

1. **Provider-side latency (`SpawnLatencyP95High`):** check the
   provider's own status page/console first — this metric measures a real
   third-party API call, and the alert's own 220s threshold
   (`platform.Timeouts.ProviderWorstColdStart`) is already calibrated to
   the worst realistic cold start this system's timeout hierarchy assumes
   (§4.1, §5.4's own invariant chain: `providerHTTPClientTimeout > provider
   worst cold start`) — firing means the provider is now materially slower
   than that assumption. If sustained, the invariant chain itself may need
   revisiting (a config change, not a code bug) rather than accepting a
   growing gap between assumption and reality.
2. **Boot-sequence latency (`BootDurationP95High`):** break down by boot
   mode first — a `build`/`fresh` regression usually means a slower base
   image or a growing dependency-install step (`setup.sh`); a
   `repo_image`/`snapshot_restore` regression is more concerning, since
   those modes exist specifically to be fast (§19's warm-boot design) —
   check `sandbox_agent_hook_rerun_duration_seconds` tagged
   `workspace_moved=true` first, since a non-idempotent `setup.sh` rerun
   firing on every boot (rather than only on a genuinely moved workspace)
   is the most common cause of an otherwise-fast boot mode regressing.
3. Neither of these has an automated remediation — they are capacity/
   configuration signals for a human to act on (provider tier, base image
   contents, dependency-install caching), not a bug this codebase's own
   supervision loop can self-heal.

## Resilience scenario

§9.3 scenario #3 ("slow boot") proves the SURVIVAL half of this — that a
genuinely slow boot does not get itself falsely killed by the connecting-
deadline watchdog as long as `boot_progress` pings keep arriving — end to
end:
`TestResilienceScenario3_SlowBoot_SurvivesRepeatedBootProgressPings_NeverFalselyKilled`
— `test/resilience/scenario3_slow_boot_test.go`. It does not itself measure
or assert on `sandbox_agent_boot_duration_seconds`/`sandbox_spawn_duration_seconds`
(both postdate that scenario, Step 77) — this runbook's own dashboard panels
are the way to observe REGRESSION in boot/spawn speed; the resilience
scenario is the way to confirm slowness alone never causes a false kill.
