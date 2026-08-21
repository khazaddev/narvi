# Production launch checklist

Every item here is grounded in something real and checkable — a config
value, a CI check, a running process's own observable state, or a drill
someone actually performed. Fewer, real items, not a complete-looking
table (the same discipline `docs/runbooks/README.md` already states for
runbooks): an item nobody can independently verify is decoration and does
not belong here.

## 1. `NARVI_ROLLOUT_MODE` is explicitly `cohort`

**Why this is item 1.** `platform.Config.RolloutMode`
(`NARVI_ROLLOUT_MODE`) defaults to `rollout.ModeOpen` when unset
(`internal/platform/config.go`) — deliberately, so Step 76 could land as
a behavior-preserving no-op. That default is exactly wrong for
production: `open` mode admits every repository named on any incoming
session-creation request unconditionally, the moment the deployment goes
live, with none of Step 76's per-repo enrollment gate (§32) actually
engaged. `docs/TECHNICAL_PLAN.md` §32 names this Step (78) as the one
that owns verifying the flip actually happened.

**Check.** On the running production process: `NARVI_ROLLOUT_MODE=cohort`
is set in its own environment (not merely in a config template file
nobody applied). Confirm behaviorally, not just by reading the env file:
attempt a session-creation request for a repository that is deliberately
NOT enrolled and confirm the channel-appropriate refusal from §32.5 (REST:
`403` "repository not enrolled"; GitHub: silent `200`, nothing posted;
Slack/Linear: an honest in-thread/agent-session denial). A boot with an
invalid value never reaches this point at all —
`platform.InvalidRolloutModeError` fails startup outright — so "the
process is running" already proves the value is either `open` or
`cohort`; this check is what distinguishes which one.

**Also confirm**: every repository that must keep working the moment this
flips has already been enrolled (§32.6, seed-manifest-only in v1) —
arming cohort mode with zero repos enrolled refuses every single new
session platform-wide, GitHub included (§32.9's own explicit warning).
Check via `repo_settings.sessions_enabled` for each repository in the
launch cohort, or the `seed.repo_setting_upserted` audit-log row the seed
tool wrote for each.

## 2. CI is green on the exact commit being deployed

Not "CI passed at some point on `main`" — the specific commit SHA this
deployment is built from. Confirm on GitHub: the `checks` job (`go vet`,
`golangci-lint`, `narvichecks`, `go test -race ./...`, `make
contracts-check`) and the `test-integration` job (all four matrix groups)
both green for that SHA. `go test -race ./...` is where
`internal/ops`'s own `TestNoMetricDrift` and `TestNoGuideDrift` run — a
green `checks` job is simultaneously proof that every
`deploy/observability/{dashboards,alerts}` entry names a metric the code
actually emits, and that every `docs/guides/*.md` command maps to a real
route or classifier-routing value, for THIS exact commit.

## 3. The control plane boots cleanly against production config

**Check.** `GET /health` returns `200` from the deployed process, in the
target environment, with real production values for every `NARVI_*`
variable `platform.Config.Load` requires (`internal/platform/config.go`
— roughly 30 required variables: database URL, all three HMAC secrets,
GitHub/Slack/Linear app credentials and webhook secrets, the token
encryption key, the allowlist, the Modal credentials, the intent
classifier's own provider/model, ...). This single check is deliberately
NOT decomposed into one line per variable: `Load` already fails closed on
the first missing/invalid one (`errors.Join`-ing every problem found), so
a clean boot is simultaneously proof every one of those ~30 checks
passed — decomposing it further would be exactly the "an item nobody can
verify independently of the others" decoration this checklist avoids.
A clean boot also proves the embedded migrations applied successfully
(`applyMigrations` runs, advisory-locked, on every boot,
`cmd/control-plane/main.go`) — there is no separate manual migration
step to check.

**Specifically confirm `NARVI_STAGE=production`** (not `staging` left
over from a template) — `Load` accepts all three valid values equally, so
a clean boot alone does not prove this one; check it directly. Getting it
wrong weakens every auth cookie's own `Secure` attribute
(`internal/adapters/inbound/auth`'s own stage-gated cookie policy).

## 4. Each ingress surface's own webhook subscription actually reaches this deployment

A required `NARVI_*` webhook secret being set only proves the control
plane is READY to verify a real webhook — it does not prove GitHub's
App, the Slack App's Events API Request URL, or Linear's own webhook
config actually POINTS at this deployment's real
`NARVI_PUBLIC_BASE_URL`. Check each provider's own delivery log/test
button: GitHub App → recent webhook deliveries show `200`s; Slack App →
Event Subscriptions shows the URL verified (the one-time
`url_verification` handshake, `internal/adapters/inbound/slack/doc.go`);
Linear → the webhook shows recent successful deliveries. See
[docs/guides/slack.md](guides/slack.md#what-starts-a-new-session)/
[linear.md](guides/linear.md#starting-a-session)/
[github.md](guides/github.md#what-triggers-a-session-or-a-turn) for what
a real inbound event from each looks like once this is wired correctly.

## 5. The alerts in `deploy/observability/alerts/*.json` are actually evaluated somewhere

`internal/platform.SetupOTel` exports metrics and traces to **stdout
only** by default — an unset `NARVI_OTLP_ENDPOINT` is a fully supported,
byte-identical-to-before state, not a gap (Step 110, §33). Since Step
110, `cmd/control-plane/main.go` (never `cmd/sandbox-agent`, which cannot
reach one even if it wanted to — see `platform.Config.OTLPEndpoint`'s own
doc comment for why that is structural, not incidental) CAN point
`SetupOTel` at a real OTLP/HTTP collector by setting that var, in which
case both a `TracerProvider` and a `MeterProvider` export there instead of
stdout. Either way, `deploy/observability/alerts/reliability.json`'s own
schema stays deliberately backend-agnostic (`internal/ops/schema.go`'s
`PanelType`/`Alert` doc comments, precisely because no backend is
pinned) — the seven alert rules committed to this repo are correctly
derived (see [`docs/SLOS.md`](SLOS.md)) and CI-checked against real
metric names (`TestNoMetricDrift`), but **evaluating** each rule's own
`condition` and paging someone is still the operator's own collector/
alerting backend's job, never this codebase's (§33.5 keeps §1's refusal
to pin a vendor).

**Check.** Two things, not one: (1) `NARVI_OTLP_ENDPOINT` actually points
this deployment's control plane at a real, reachable collector — an unset
value here silently leaves this whole item unmet, exactly as before Step
108, just no longer for lack of a code path; (2) the alerting backend
that collector feeds lists all seven alert names from
`deploy/observability/alerts/reliability.json` (`OutboxLagHigh`,
`OutboxDeadLetterAny`, `OrphanReapRateHigh`, `BootDurationP95High`,
`SpawnLatencyP95High`, `WatchdogFalseAlarmRateHigh`,
`TurnFalseFailureAny`), each pointed at the correct routed on-call
schedule (see [`docs/ONCALL.md`](ONCALL.md)).

## 6. Uploads are either fully configured or deliberately out of launch scope

Uploads are feature-flagged entirely on `NARVI_OBJECT_STORE_ENDPOINT`
being set (`internal/platform/config.go`) — every upload route either
works end to end or refuses cleanly with nothing half-wired, but only if
this was a deliberate decision. Check: object storage is either (a)
fully configured (`NARVI_OBJECT_STORE_ENDPOINT`/`REGION`/`BUCKET` and
credentials all set, confirmed by a real mint→upload→confirm→fetch round
trip against production object storage) or (b) knowingly left unset for
this launch, with that decision recorded somewhere a later on-call
engineer investigating "uploads don't work" won't mistake for a bug.

## 7. The §32.9 rollback procedure has been drilled once against this deployment, not just read

A rollback procedure nobody has ever actually run is a plan, not a
capability. Check: someone has performed
`docs/TECHNICAL_PLAN.md` §32.9's own per-repository rollback drill
(flip one non-critical enrolled repo's `sessionsEnabled` to `false` via
the seed tool, confirm a fresh `@mention`/REST create against it is
refused per §32.5, confirm `session_rollout_refused_total` incremented,
then re-enroll it) against THIS deployment — not merely against a local
dev environment — before go-live. See [`docs/ONCALL.md`](ONCALL.md) for
the incident-time version of this same drill.

## 8. On-call coverage is real, not aspirational

Check: a named engineer is currently covering this deployment on
whatever paging rotation this organization uses, and that engineer has
actually read [`docs/ONCALL.md`](ONCALL.md) — the entry point this
checklist's own item 7 above, and every alert's own `runbook` field
(`deploy/observability/alerts/*.json`), point at.
