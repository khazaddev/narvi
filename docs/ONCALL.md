# On-call entry point

Start here. This page tells you how to figure out what's broken, which of
the seven existing runbooks to open, and what to do when none of them
fit. It does not duplicate any runbook's own content — every link below
goes to the single source of truth for that procedure.

## 1. Is the control plane even up?

`GET /health` — a non-`200` (or no response at all) means this is an infrastructure/
deploy problem, not one of the seven failure modes below — check the
deployment platform's own status (pod restarts, recent deploys, resource
limits) before looking at anything else on this page. A `200` here only
proves the process is up and can reach Postgres (`healthHandler` pings
the pool, `platform.Timeouts.HealthCheckTimeout` bound) — it says nothing
about whether sessions are actually working.

## 2. What does the dashboard say?

Two dashboards, both under `deploy/observability/`:
[`sandbox-lifecycle.json`](../deploy/observability/dashboards/sandbox-lifecycle.json)
(spawn/boot latency, watchdog health, orphan GC) and
[`turns-and-delivery.json`](../deploy/observability/dashboards/turns-and-delivery.json)
(turn false-failure rate, outbox delivery lag). If an alert paged you,
its own name below tells you exactly which runbook to open — no need to
read the dashboard first. If you're investigating a REPORTED symptom with
no alert yet (a user says "my session is stuck", say), the dashboards are
where to look for which of the five things below is actually happening.

## 3. Alert (or symptom) → runbook

This table is the ONE place this mapping lives —
`docs/runbooks/README.md` carries the identical table; if you edit one,
edit both, or better, point one at the other.

| Alert (or symptom) | Runbook |
|---|---|
| `OutboxLagHigh`, `OutboxDeadLetterAny` — notifications not reaching Slack/Linear/GitHub | [outbox-delivery.md](runbooks/outbox-delivery.md) |
| `OrphanReapRateHigh` — provider-side sandboxes being reaped unusually fast | [orphan-sandboxes.md](runbooks/orphan-sandboxes.md) |
| `BootDurationP95High`, `SpawnLatencyP95High` — sessions slow to become usable | [slow-boot-and-spawn.md](runbooks/slow-boot-and-spawn.md) |
| `WatchdogFalseAlarmRateHigh` — sandboxes wrongly suspected dead | [watchdog-false-alarms.md](runbooks/watchdog-false-alarms.md) |
| `TurnFalseFailureAny` — a turn reported `Failed` that actually finished | [turn-false-failures.md](runbooks/turn-false-failures.md) |
| A session refuses to spawn with `docker`/`egress_policy` errors | [sandbox-capability-refusals.md](runbooks/sandbox-capability-refusals.md) |
| Rotating the webhook-signing key | [signing-key-rotation.md](runbooks/signing-key-rotation.md) |

Each SLO in [`docs/SLOS.md`](SLOS.md) names the exact alert (and
therefore the exact runbook) that backs it, plus the arithmetic behind
its threshold — useful when you need to judge whether a borderline metric
value is actually a problem before a formal alert fires.

## 4. A cohort-rollout incident (repo refusing, or refusing to STOP)

This is the one class of incident that is not one of the seven runbooks
above, and it is not duplicated here either — the full operator
procedure (enroll, arm/disarm cohort mode, roll back one repo or
platform-wide, and — critically — how to verify a rollback actually took
effect) already lives at `docs/TECHNICAL_PLAN.md` §32.9. Start there
directly if:

- A repository that should be enrolled is being refused (check for
  `session_rollout_refused_total{spawn_source=...}` incrementing and a
  `"...refused, repo not enrolled"` log line — §32.7).
- You need to roll a repository (or the whole platform) BACK OFF cohort
  rollout during an incident — §32.9's own "Roll back one repository" /
  "Roll back platform-wide" sections.
- A refusal you're seeing might actually be a transient database blip
  rather than a genuine policy refusal — §32.9's own "Distinguishing a
  policy refusal from a database blip" section tells you exactly which
  log line/metric distinguishes the two.

**One thing §32.9 states plainly and worth repeating here because it
surprises people mid-incident**: rolling a repo back is a
**stop-new-work** guarantee, never an instant kill switch. A turn already
`Processing` at the moment you flip the flag keeps running until it
completes or `platform.Timeouts.TurnDeadline` (60 min) fires — there is
no faster path for that one case. Don't assume the rollback is broken
just because one already-running turn is still going.

## 5. When none of the above fits

The seven runbooks and §32.9 above cover every failure mode this system
is currently known to have a real signal for. If your symptom genuinely
matches none of them:

1. **Check the per-surface guide for the surface involved**
   ([web](guides/web.md) / [Slack](guides/slack.md) /
   [Linear](guides/linear.md) / [GitHub](guides/github.md)) — the
   "honest negatives" section on each one documents real, deliberate
   refusals and silent-drop behaviors (a busy session dropping a Slack
   message, GitHub's own total silence on an unenrolled repo, ...) that
   look like bugs from the outside but are shipped, correct behavior.
   Rule this out before treating something as an incident.
2. **Search structured logs by `correlation_id`** — every request/event
   this codebase processes carries one through `ctx`
   (`platform.Logger(ctx)`), so a single problematic session/turn/webhook
   delivery's own full trail is greppable by that one id across every
   package it touched.
3. **Check `docs/PRODUCTION_CHECKLIST.md`** — several of the sharper
   incident classes (rollout mode silently defaulting to `open`, a
   webhook subscription pointed at the wrong URL, alerts not actually
   wired to a real alerting backend) are exactly the gaps that checklist
   exists to catch BEFORE launch; if one of them turns out to be live in
   production, that checklist item was missed, not a new kind of bug.
4. **Check what changed recently** — `git log --oneline -20` against
   `main`, and whether a config value (`NARVI_*`) changed in the
   deployment platform's own recent history. Most incidents this system
   does not already have a runbook for are a regression from something
   that changed, not a novel failure mode.
5. **If it's a genuinely new failure mode**: once resolved, it earns its
   own runbook — see `docs/runbooks/README.md`'s own "fewer, real
   entries" discipline for the bar a new entry should clear, and
   `docs/TECHNICAL_PLAN.md` §9.3 for the resilience-scenario catalog a
   genuinely new class of failure should ideally also get added to.
