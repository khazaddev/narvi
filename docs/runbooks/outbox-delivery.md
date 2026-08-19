# Outbox delivery stalled or dead-lettering

Backs alerts: `OutboxLagHigh`, `OutboxDeadLetterAny`
(`deploy/observability/alerts/reliability.json`). Dashboard:
[turns-and-delivery.json](../../deploy/observability/dashboards/turns-and-delivery.json).

## Symptom

Users stop seeing outbound notifications for events that should produce
one: a Slack/Linear thread reply, a GitHub check/comment update, a
plan-approval-request message, a turn-completion notice. The web UI (which
reads turn/session state directly, not via the outbox) still looks
correct — this is specifically an *outbound-channel* symptom.

## Confirm

- `outbox_lag_seconds` (gauge) rising and staying above its usual
  near-zero baseline. Zero only means nothing was *due and claimed* this
  tick — during a sustained outage every pending row can be mid-backoff at
  once, so also check:
- `outbox_due_backlog_count` (gauge) — total pending rows including
  mid-backoff ones. This is the one that stays honest during a backoff
  storm.
- `outbox_dead_letter_total` (counter) incrementing at all — each
  increment is one entry that exhausted `domain/outbox.MaxAttempts` (10)
  delivery attempts (`internal/domain/outbox/backoff.go`) and will never
  be retried automatically again.
- Logs: `"outboxworker: tick failed"` (a whole pump tick errored — check
  the `error` field; a single bad tick doesn't kill the loop, but a
  *repeated* one means something structural, e.g. the Postgres claim query
  itself failing) and, per dead-lettered entry, the log line at
  `internal/app/outboxworker/builder.go`'s own dead-letter branch (carries
  `max_attempts` and the last delivery error).

## Remediation

1. **Identify which notifier is failing.** `outboxworker.Builder` routes
   by `ports.NotificationKind` to one of the Slack/Linear/GitHub notifier
   adapters (`internal/adapters/outbound/{slackapi,linearapi,githubapi}`).
   The dead-letter log's own `lastError` field (or a direct read of
   `ListDeadLetter`) names the underlying HTTP failure — a 401/403 usually
   means a revoked/expired bot token or webhook credential; a sustained
   5xx means the third-party API itself is degraded (exactly §9.3
   scenario #9's own scripted case, below); a 4xx on every attempt for one
   channel and not others narrows it to that channel's own credential or
   payload.
2. **A transient third-party outage self-heals.** `domain/outbox.
   EvaluateBackoff`'s own schedule keeps retrying (`OutboxBackoffBase` 30s
   up to `OutboxBackoffMax` 5m, `internal/platform/timeouts.go`) until
   either delivery succeeds or `MaxAttempts` is exhausted — no operator
   action needed while `outbox_lag_seconds`/`outbox_due_backlog_count` are
   elevated but `outbox_dead_letter_total` is flat. `OutboxLagHigh`'s own
   300s threshold is deliberately set at `OutboxBackoffMax` specifically
   so it does NOT fire on ordinary backoff — if it's firing, a row has
   already exhausted that whole window without ever un-sticking.
3. **A credential problem needs a human fix.** Rotate/reissue the failing
   channel's own bot token or webhook secret (outside this codebase — the
   relevant provider's own admin console), then confirm new attempts on
   already-pending rows succeed (`outbox_lag_seconds`/backlog should drain
   within a few `OutboxPumpInterval` ticks, 5s each).
4. **Already dead-lettered rows are not automatically replayed.** This is
   a genuine, currently-unbuilt gap, not a hidden button: `OutboxStore`
   (`internal/adapters/outbound/postgres/outbox_store.go`) exposes
   `ListDeadLetter` to inspect them, but no requeue/replay method exists
   yet. Recovering a specific dead-lettered notification today means
   re-triggering whatever original action produced it (e.g. re-posting a
   review verdict, re-running the automation) through the product surface
   that creates that notification in the first place — not resurrecting
   the dead-lettered outbox row itself.

## Resilience scenario

§9.3 scenario #9 ("Outbox: Slack API 500s") proves the backoff-then-
recover half of this runbook end to end against a real
`outboxworker.Builder` and a fake-Slack `httptest.Server` scripted to
return 500 before recovering:
`TestResilienceScenario9_Outbox_SlackAPI500sThenRecovers_EventuallyDeliveredNoLoss`
— `test/resilience/scenario9_outbox_retry_test.go`.
