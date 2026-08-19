# Turn false failures

Backs alert: `TurnFalseFailureAny`
(`deploy/observability/alerts/reliability.json`). Dashboard:
[turns-and-delivery.json](../../deploy/observability/dashboards/turns-and-delivery.json)
(`turn-false-failure-rate` panel).

## Symptom

A user sees their turn reported as failed (`failure_reason: timeout`), but
the agent was, in fact, still genuinely working and later actually
finished — the failure was the control plane's own inference (its
`turn_deadline` fired with no terminal event yet), not a real agent
failure or crash.

## Confirm

- `turn_false_failure_total` (counter, no attributes — see the
  instrument's own doc comment, `internal/app/sessionactor/opsmetrics.go`,
  for why one narrow gate needs no label) — increments when a real, late,
  genuinely FIRST-seen `execution_complete{outcome:completed}` wire event
  arrives for a session whose own currently-derived state is ALREADY
  `Failed` with `failure_reason=timeout`.
- Log: `"sessionactor: execution_complete arrived with no turn in
  processing; ignoring"` — this line fires for EVERY late
  `execution_complete` regardless of outcome (a duplicate redelivery of an
  already-completed turn's own event looks identical in the log); only
  the metric distinguishes the specific "this one was a real success we
  wrongly called a failure" case from an ordinary, harmless redelivery.
- **A wire-level redelivery of the false-failure event itself does not
  re-increment.** §6.1's ack protocol buffers and resends the 6 critical
  event types (`execution_complete` included) until acked, and this is
  exactly the class of scenario where the connection is unhealthy enough
  for that to matter — so a false failure's own triggering event can
  itself arrive more than once. `completeProcessingTurn`
  (`internal/app/sessionactor/pushpr.go`) gates the counter on
  `appendRawEvent`'s own `inserted` result (true only the first time a
  given `execution_complete` messageID is ever seen), so a resend of the
  SAME event is persisted and acked like any other redelivery, but counts
  at most once. This is a distinct case from the ordinary-redelivery one in
  the paragraph above — that one describes a redelivery of a turn that was
  ALREADY reconciled normally (never a false failure at all); this one
  covers redelivery of the event that IS the false failure.
- The affected turn/session row itself: `turns.status = 'failed'`,
  `sessions.failure_reason = 'timeout'` — confirms which specific turn this
  was, but **the turn is never un-failed** by this event (see below).

## What this is NOT

This is observability only — `domain/turn`'s own state machine
(`internal/domain/turn/state.go`) has no `Failed -> Completed` edge, by
deliberate design (see that file's own top comment): a session already
showing `failed`/`timeout` stays exactly that in the product, even after
this metric records that the underlying agent run actually succeeded.
There is currently no automated remediation that resurrects the turn or
retroactively opens whatever PR/push the late completion implies —
building that is a real, separate design decision (a `Failed ->
Completed` edge, or a distinct "recovered" state) this Step deliberately
does not make.

## Remediation

1. **Per-occurrence**: tell the affected user their turn actually finished
   — the agent's own output/artifacts from that run are typically still
   recoverable from whatever the sandbox managed to push/report before the
   session tore down (check `push_complete`/`artifact` events already
   persisted for that session, `internal/adapters/inbound/wshub`'s own
   event log), even though the session's OWN status still reads `failed`.
2. **If this fires more than rarely**: `TurnDeadline` (60 minutes,
   `platform.Timeouts.TurnDeadline`, "chosen with margin below
   `SupervisorTurnCap`" 90 minutes) may be too tight for the workload
   actually running — a sustained pattern of false failures right around
   the 60-minute mark is the signal to reconsider that budget, not to
   silence the alert.
3. **If it never fires but you suspect it should be**: confirm the
   instrument itself is wired — `go test
   ./internal/app/sessionactor/... -run
   TestHandleTurnDeadlineTimer_ThenLateExecutionComplete_RecordsFalseFailure
   -tags=integration` reproduces the exact scenario end to end.

## Resilience scenario

§9.3 scenario #4 ("late `execution_complete`") is the closely related,
but NOT identical, mechanism — stated precisely rather than overclaimed:
scenario #4's own covered case is a real completion arriving while the
turn is STILL `Processing` (the Suspect-recovery-during-grace path,
`internal/app/sessionactor/sandboxevent.go`) — genuinely reconciled, no
false failure at all, since the turn never actually terminalized wrong.
`turn_false_failure_total` (this Step, 77) covers the OTHER half of that
same timing question: a completion arriving AFTER `turn_deadline` itself
already fired first. Both are proven by real tests:

- `TestHandleSandboxEvent_LateExecutionComplete_RecoversSandboxTurnAndSession`
  — `internal/app/sessionactor/suspectrecovery_integration_test.go`
  (scenario #4's own still-Processing case).
- `TestHandleTurnDeadlineTimer_ThenLateExecutionComplete_RecordsFalseFailure`
  — `internal/app/sessionactor/opsmetrics_integration_test.go` (this
  Step's own already-Failed case).
