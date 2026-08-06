# Narvi — Technical Implementation Plan

> **Audience**: AI coding agent (and human reviewer). This document is self-contained: it carries every architectural decision, invariant, and contract needed to implement the system without access to the original design discussions.

## 0. Context

Narvi runs autonomous coding agents in isolated cloud sandboxes, triggered from the web, Slack, Linear, and GitHub. This document is the self-contained specification for building it in Go. Its design is uncompromising on the properties that make an agent platform trustworthy at scale: one authoritative source of state, a single owner per session, native process supervision, and a resilience test suite (§9.3) that is a first-class exit criterion, not an afterthought.

**What we are building**: two Go services (control plane + in-sandbox agent), packaged as containers and deployable on any Kubernetes cluster or plain Docker/VMs — no cloud lock-in; Postgres as the single source of truth; S3-compatible object storage for media; and a web UI (built in phase 7 from the mockups in §12). The wire contracts in §6 are defined up front, so backend and UI are built against the same generated schemas.

**Non-goals for the initial build** (phases 0-6): the web UI (built in phase 7, §12); a replacement for OpenCode (the agent engine — Narvi wraps it); sandbox providers beyond Modal and RWX (the interface must allow adding them later — a Kubernetes-native sandbox provider is an anticipated adapter); multi-region.

## 1. Repository layout

Single Go module, hexagonal architecture. Domain has zero external dependencies.

```
/cmd
  control-plane/          # main: config, wiring, migrations, HTTP+WS server
  sandbox-agent/          # static binary shipped into sandbox images
/internal
  /domain                 # pure business logic; no I/O, no external imports
    session/              # session entity + state machine
    sandbox/              # sandbox state machine, spawn/restore/resume decisions,
                          #   circuit breaker, liveness budgets
    turn/                 # prompt lifecycle: pending→processing→terminal; queue
    gitstate/             # git state machine (stash → checkout → pop)
    automation/           # automation → invocation → runs (CAS failure accounting)
    review/               # code review: verdicts, risk map, sentinels, verdict floor
    plan/                 # plan mode: plan → HITL approval → dispatch
    intent/               # unified intent classifier (rules + LLM, shadow mode)
  /app                    # use cases; defines ports (Go interfaces)
    ports/                # all interfaces (see §4)
    sessionactor/         # one goroutine+mailbox per active session
    reconciler/           # provider reconciliation + orphan GC loop
    scheduler/            # automation cron, recovery sweeps
  /adapters
    /inbound
      httpapi/            # REST (serves the web UI + external clients)
      wshub/              # client WS + sandbox WS (contract in §6)
      github/ slack/ linear/ sentry/  # webhook ingress → normalized events
    /outbound
      postgres/           # stores, outbox, named timers
      modal/ rwx/         # sandbox providers (full interface, §4.1)
      opencode/           # anti-corruption layer around the agent engine (§7)
      githubapi/ gitlabapi/ slackapi/ linearapi/
      llm/                # Anthropic/OpenAI clients for classifier & review
      objstore/           # BlobStore adapter — S3-compatible (AWS S3, MinIO, R2, GCS, …)
  /platform               # config (typed, validated at boot), structured logging,
                          #   OTel, HMAC auth, timeout hierarchy (§5.4)
/contracts                # versioned JSON Schemas for: sandbox WS protocol,
                          #   client WS protocol, SESSION_CONFIG, REST DTOs.
                          #   Go types generated from these; TS types generated
                          #   for the frontend. Round-trip contract tests.
/migrations               # Postgres migrations (goose or golang-migrate)
/test
  resilience/             # replay of known failure scenarios (§9.3)
```

**Stack choices** (use unless a blocker emerges): Go ≥1.23; `net/http` + `chi` for routing; `coder/websocket` for WS; `pgx/v5` + `sqlc` for Postgres; `golang-migrate` for migrations; OpenTelemetry SDK; `log/slog` with a structured envelope carrying `correlation_id`, `session_id`, `sandbox_gen`.

## 2. Core runtime model: the session actor

- One goroutine + mailbox (channel of commands) per **active** session. All mutations of a session's state go through its actor — no other code path writes session/sandbox/turn rows.
- **Hydration on demand**: actor loads state from Postgres on first command, evicts after idle TTL (default 30 min without commands or connected clients).
- **Single-writer across replicas**: Postgres advisory lock keyed by session id, held for the actor's lifetime, plus a **fencing check**: every write includes the actor's `epoch` (bumped on each acquisition); writes with a stale epoch fail. A zombie actor on an old pod can never corrupt state.
- **Transactional writes**: state transition + appended event + outbox entries commit in ONE Postgres transaction. There is no such thing as a fire-and-forget state write.
- **Named persistent timers**: table `session_timers(session_id, name, fires_at)`. Names: `connecting_deadline`, `liveness_check`, `inactivity`, `turn_deadline`, `terminal_grace`. A per-pod timer pump polls due timers (`SELECT ... FOR UPDATE SKIP LOCKED`) and delivers them as actor commands. Timers survive restarts; each is armed/re-armed independently.

## 3. Domain model & state machines

### 3.1 Session

`created → active → completed | failed | cancelled` (+ `archived` flag). Status is **derived** after each turn: pending work → `active`; else terminal per last turn outcome. `cancelled ≠ failed ≠ timeout ≠ never_started` are distinct from day one (separate `status` + `failure_reason` enum).

### 3.2 Sandbox (the critical machine)

```
pending → spawning(gen) → connecting → booting → ready ⇄ snapshotting
                                          │
                       (silence/timeouts) ▼
                              suspect → [grace] → stopped | failed
recovery: stopped|stale + snapshot        → restore (new gen)
          stopped|stale + resume-capable  → resume  (same provider sandbox)
          failed + snapshot               → restore (new gen)
```

Hard rules:
- **Spawn generation (fencing)**: every spawn/restore increments `sandbox.gen` (monotonic, per session). Sandbox WS connections, provider callbacks, and status writes carry `gen`; stale-gen inputs are rejected **and logged** (they must never be able to wedge the session). DB enforces one sandbox row per session (`UNIQUE(session_id)`); history goes to `sandbox_history`.
- **Liveness = max of all signals**. `last_seen_at` is updated by: heartbeat, boot-progress report, any agent event (tool_call, token, step), any WS frame. Every watchdog measures from `last_seen_at` and re-arms on it.
- **Two liveness budgets**: `first_connect_budget` (default **240s**, covers provider cold start + boot) distinct from `steady_heartbeat_budget` (default **90s**; heartbeats every **30s**). Boot-progress reports during long boots re-arm the connecting deadline.
- **Two-phase terminalization**: a watchdog never writes `failed` directly. It writes `suspect` and arms `terminal_grace` (default 60s). Any liveness signal during grace returns to previous state. A genuinely late success (e.g. `execution_complete` arriving after terminalization) **reconciles**: turn marked completed, session status re-derived, automation run counters corrected. This narrows the false-failure window rather than eliminating it — see the note just below the list for the residual case and what closing it would require.
- **Circuit breaker**: 3 permanent spawn failures within 5 min blocks spawning for that session. Unknown provider errors default to **transient**, never permanent — a novel transient failure must not trip the breaker.

Two-phase terminalization's reconciliation above is scoped to a late signal that still finds the turn `Processing` — true whenever it arrives inside `turn_deadline`'s own, separate, much longer budget (60 min vs. `terminal_grace`'s 60s, `platform/timeouts.go`), the ordinary case. What that scoping makes unrepresentable is the contradiction class — a delivered artifact and a failure message both standing for the same turn at once — since a turn still `Processing` can only ever resolve to one clean terminal write, never a conflicting second one. The residual case stays a known limitation: a real `execution_complete` arriving after the turn has already gone terminal via its own `turn_deadline` timeout is logged and discarded, not reconciled — the user is told the turn failed while the work actually completed. Closing this residual would require late-success reconciliation to enqueue a corrective follow-up notification through the same outbox (§5.1) once the original `failed` transition's own notification has already been dispatched; without that, the contradiction class this design otherwise prevents re-enters through that door.

### 3.3 Turn (prompt)

`pending → dispatched → processing → completed | failed | cancelled`. Exactly one `processing` per session. Enqueue → if no live sandbox, trigger spawn and return (dispatch happens when sandbox connects). Dispatch arms `turn_deadline`. On terminal event: complete turn, trigger snapshot, re-derive session status, dispatch next pending. Stop/failure paths emit a **synthetic** `execution_complete` event so clients always see one terminal event per turn. The turn records the OpenCode conversation id **at turn start** (also reported on every heartbeat) so follow-up prompts on a fresh sandbox resume the same conversation — never lazily.

### 3.4 Git state (inside sandbox, enforced by sandbox-agent)

- Image builds must snapshot a **clean tree** (commit or clean `setup.sh` residue before snapshotting).
- Boot: `stash-if-dirty → checkout session branch (create from base if absent) → stash pop`. User working-tree edits are durable data — losing them is a P0.
- Branch names normalized (lowercase) before push. Repo paths: multi-repo under `/workspace/{name}`, position 0 = primary. Repos are **always a list** — no scalar single-repo mirror anywhere.

### 3.5 Automations

`automation → invocation → run(s)` (one run per target, fan-out ≤10). At-most-one failure strike per invocation via CAS (`UPDATE ... WHERE failure_counted_at IS NULL`). Auto-pause after 3 consecutive failed invocations. Recovery sweeps: orphaned `starting` runs >5 min, `running` >90 min. Failed image builds retry with exponential backoff (not fixed 30 min) and alert on streaks.

## 4. Ports (interfaces in `/internal/app/ports`)

### 4.1 SandboxProvider (complete — no out-of-interface operations)

```go
type SandboxProvider interface {
    Capabilities() Capabilities // snapshots, resume, explicitStop, imageBuilds
    CreateSandbox(ctx, CreateSpec) (SandboxRef, error)   // Spec includes gen + full SESSION_CONFIG doc
    StopSandbox(ctx, SandboxRef) error
    ResumeSandbox(ctx, SandboxRef) error                  // optional per capabilities
    TakeSnapshot(ctx, SandboxRef) (SnapshotID, error)
    RestoreFromSnapshot(ctx, SnapshotID, CreateSpec) (SandboxRef, error)
    BuildImage(ctx, ImageSpec) (BuildRef, error)          // image prebuilds are IN the interface
    DeleteImage(ctx, ImageRef) error
    List(ctx) ([]SandboxRef, error)                       // for reconciliation/GC
}
```

Errors are typed `ProviderError{Transient bool}` — classification by provider-specific error codes, **never** by string-matching messages. The provider HTTP client timeout MUST exceed the provider's worst cold-start (Modal cold scheduling alone can take 220s+).

Implement: `modal` (via its API; sandbox env passed as one `SESSION_CONFIG` JSON document — the provider never assembles env fragments) and `rwx` (detailed design: §4.1.1; PR preview links: §4.1.2). All Modal traffic goes through the configurable egress proxy.

### 4.1.1 RWX adapter (Step 57)

RWX (rwx.com) is the second `SandboxProvider` implementation (`adapters/outbound/rwx`), integrating RWX's real, public product exactly as `modal` integrates Modal's API and `githubapi` integrates GitHub's. Everything below is grounded in RWX's own published documentation (www.rwx.com/docs, fetched 2026-08-06); where that documentation is silent, the gap is named in §4.1.3 rather than papered over with an invented API shape — the same verified-against-the-vendor's-own-docs discipline §17.6 applied to GitHub's stacked-PR documentation.

**What RWX actually offers.** Three primitives matter to Narvi. (1) **Sandboxes** — "Run commands in persistent sandboxes" (CLI reference): per-identity VM environments configured by a YAML document (`base` OCI image + `tasks` + `background-processes` with ready checks, Docker support), spun up from RWX's content-addressed cached filesystem layers ("spin up in seconds, not minutes"), auto-stopped "after 30 minutes of inactivity" (configurable: `--first-exec-timeout`, `--inactivity-timeout`), identified "by git branch and the absolute path to the config file", with `rwx sandbox start|exec|stop|reset|list` lifecycle verbs and `rwx sandbox reset` returning to "a fresh state (discarding any changes)". (2) **Dispatched runs** — the one documented HTTP API: `POST https://cloud.rwx.com/mint/api/runs/dispatches` (`Authorization: Bearer $RWX_ACCESS_TOKEN`, JSON body `{key, params, ref, title}`, 201 → `{dispatch_id}`), then `GET …/dispatches/:dispatch_id` polled to `not_ready | error | ready` with `runs[{run_id, run_url}]`. (3) **Preview apps** — §4.1.2's whole subject.

**Transport: the pinned `rwx` CLI, never an invented REST shape.** RWX documents no HTTP API for sandbox lifecycle — its programmatic sandbox surface is the CLI (global `--format json`) plus the Dispatches API above. The adapter therefore drives the pinned `rwx` CLI binary as its transport for sandbox operations: pin the version, record it in the boot fingerprint's environment (§5.3), and run **CI contract tests against the real pinned binary** — the exact discipline §7 already applies to OpenCode, and strictly better than the Modal adapter's own openly-admitted invented wire shapes (modal/doc.go: exercised against a fake, "NOT against real Modal API docs"). The token travels to the subprocess as `RWX_ACCESS_TOKEN` (RWX's documented mechanism: "For programmatic use, set the token as the `RWX_ACCESS_TOKEN` environment variable"), never as `--access-token` argv — argv is visible to process listings (§5.2's leak-class discipline). Credential: an RWX **service account** token, per RWX's own guidance (a personal token "acts as you"; a service account "is owned by the organization, so it survives you leaving it") — wired as static adapter config exactly like `modal.Config.AuthToken`, validated fail-fast; this is a control-plane outbound credential, not a Step 53 sandbox-injected secret. Egress: the subprocess inherits proxy env vars (`HTTPS_PROXY`) so RWX traffic routes like Modal's; whether the CLI honors them is §4.1.3's to verify.

**Capabilities — declared from what RWX verifiably supports, not from hope:**

```go
Capabilities{Snapshots: false, Resume: <verified>, ExplicitStop: true, ImageBuilds: false}
```

- `ExplicitStop: true` — `rwx sandbox stop` is a real, documented operation.
- `Resume` — the one flag Step 57 must settle **empirically, as its first exit criterion**. RWX's docs imply stop→start state preservation ("the sandbox persists between commands"; `reset` exists specifically to discard changes — redundant if a plain stop already discarded them) but never state it outright. If verification confirms preservation, `Resume: true` and RWX becomes the §3.2 "stopped|stale + resume-capable → resume (same provider sandbox)" provider that `ports/capabilities.go`'s own doc comment anticipated. If not, `Resume: false` — and, with `Snapshots` also false, a stopped RWX sandbox's only recovery is recreate-from-scratch: §3.2's snapshot-restore lane simply does not exist for RWX sessions, and §3.4's push discipline is the only durability for working-tree state. That consequence is accepted and named, not hidden — callers already branch on `Capabilities()` (the port's own "optional per capabilities" contract).
- `Snapshots: false` — RWX snapshots task filesystems into its content-addressed cache automatically, but exposes no addressable take-snapshot-now/restore-from-handle API; a cache keyed by content is not a `SnapshotID` a caller can mint and hold. `TakeSnapshot`/`RestoreFromSnapshot` return the permanent `UNSUPPORTED_OPERATION` `ProviderError`, mirroring `modal.ResumeSandbox`'s established pattern.
- `ImageBuilds: false` — `rwx image build|push|pull` exist ("Launch a targeted RWX run and pull its result as an OCI image") but no image delete is documented, and `ImageBuilds` covers `BuildImage` **and** `DeleteImage`. The honest posture: report false; §19's build pump then never engages for RWX-backed sessions and the systematic fallback-to-base (§10 Phase 2) applies — which costs little, because RWX's own content-addressed layer cache already provides the warm-boot effect §19 builds by hand for Modal: unchanged setup work re-hits RWX's cache natively.

**Method mapping.** `CreateSandbox`: generate a per-(session, gen) sandbox config whose `base` image is `spec.Image` (an OCI reference; empty = adapter default) and whose identity — RWX keys sandboxes on branch + config path — embeds session id **and gen**, so two gens can never collide onto one RWX sandbox (§3.2 fencing at the provider's own identity layer); `SESSION_CONFIG` travels as ONE opaque JSON value (a single init param/env entry — §4.1: the provider never assembles env fragments); the sandbox-agent runs as the long-lived exec'd command, its supervised services as `background-processes` with ready checks (§14.2's manifest maps naturally). `StopSandbox`: `rwx sandbox stop`. `ResumeSandbox`: `rwx sandbox start` against the same identity (per the `Resume` verification above; if false, permanent `ProviderError` like Modal's). `List`: `rwx sandbox list --format json` for §4.1's reconciliation/GC (org-wide visibility to verify, §4.1.3). `SandboxRef.ProviderID` holds whatever identity the pinned CLI's JSON output reports — opaque outside the adapter (`ports/refs.go`).

**`ProviderError` classification.** Same shape as Modal's table, same §3.2 default: on the Dispatches API path, HTTP status class (network failure/429/5xx transient; 400/401/403/404/409/422 permanent; anything else transient — "unknown provider errors default to transient, never permanent"); on the CLI path, the classification inputs are the process exit code and the `--format json` error envelope — **never** prose message matching (§4.1's own rule). RWX publishes no error-code taxonomy today; the concrete exit-code/envelope table is pinned by the real-binary contract tests, and until a failure mode is pinned it classifies transient.

**Timeouts (§5.4).** RWX's published latency claims ("seconds, not minutes"; "~3s" preview-app start) are directional, not engineering figures — no p50/p99 is published. `ProviderWorstColdStart` remains governed by the fleet's worst provider (Modal's 220s+ cold scheduling dominates every RWX figure), so the §5.4 chain (`providerHTTPClientTimeout > provider worst cold start`, `first_connect_budget > image pull + boot p99`) already holds for RWX without new margins; RWX-specific p99s are measured empirically before any margin is tightened, never assumed from marketing copy. Two RWX knobs join `platform/timeouts.go` (no literal anywhere else): the CLI subprocess exec timeout, and the sandbox `--inactivity-timeout` — set **above** Narvi's own session-idle authority (§2's 30-min actor TTL and the `inactivity` timer) so Narvi's timers, not RWX's, decide idleness. A provider-initiated auto-stop that fires anyway is an ordinary entry into §3.2's `stopped` state feeding the resume/recreate lane — an expected event, never a failure.

### 4.1.2 PR preview links at the latest PR commit (Step 57)

§8.9's exit criterion, built on RWX **preview apps** — RWX's own primitive for exactly this: a run task carrying `app: {endpoint, port, timeout}` serves "traffic on publicly accessible URLs under rwx.run", with a **canonical** URL (`https://{task-cache-key}--{org-slug}.rwx.run`, "pinned to one specific build") and a **friendly** URL (`https://{endpoint}--{org-slug}.rwx.run`, always the latest build — RWX's docs: "Use for shared PR links that automatically pick up new commits", fetched 2026-08-06). Apps start on demand (~3s claimed), spin down "after 10 minutes of inactivity", restart on the next request. Narvi's job is therefore two small side effects per push — (a) trigger a preview build at the PR's newest commit, (b) attach the link to that commit — both delivered through the existing outbox machinery (§5.1), never a new delivery pathway.

1. **Trigger and enqueue point.** `push_complete` (§6.1) already carries `repos[].sha` — the pushed head. The sessionactor's own PR-creation path (`pushpr.go`'s `createPRBestEffort`, which has just ensured the PR exists and holds its `PRRef`) is the ONE enqueue point: for each pushed repo whose per-repo preview setting is present, one small fresh transaction (mirroring `recordPRArtifact`'s own established fresh-transact pattern) writes a `preview`-typed artifact row + both outbox rows below. The `preview` slot in `artifact_type` and the §6.1 `artifact` event have existed since Steps 04/05 with no writer — this is their first real producer, and §12.2 item 1's artifacts rail renders it with zero new UI machinery. Per-repo setting `{dispatchKey, endpointTemplate, orgSlug}`; absent = feature off (off by default, §24.5's posture). No new trigger surface: sessions that never push never preview.
2. **`rwx_preview_dispatch` (new outbox kind).** Delivered by the `rwx` package's own `ports.Notifier` implementation (the outboxworker routes kind→notifier exactly as for every other kind): `POST /mint/api/runs/dispatches` with `ref` = the pushed sha, `key` = the repo's `dispatchKey`, `params` = `{pr-number, head-sha, session-id}` — surfaced to the run as `event.dispatch.params`, from which the repo's own `.rwx` run definition templates its app endpoint. The build runs on RWX's infrastructure from the ref itself — fully decoupled from the session's sandbox — and repeat dispatches are cheap by construction (content-addressed cache). Delivery is the fast dispatch POST only; it never waits for the build.
3. **`github_preview_link` (new outbox kind).** Delivered by a small `githubapi` notifier via a new `CreateCommitStatus` adapter capability: `POST /repos/{owner}/{repo}/statuses/{sha}` with `context: narvi/preview`, `state: success`, `target_url` = the friendly URL rendered from `endpointTemplate` + `orgSlug`, and a description carrying the ephemerality caveat (live while RWX serves it). A commit status **is** "a preview link at a commit": each push posts at the new head, GitHub surfaces exactly the head commit's statuses on the PR, and redelivery of the same (context, sha) converges instead of duplicating — strictly better here than `PostIssueComment`, whose own notifiers document double-posting on retry as an accepted limitation (`releasemanifestnotifier.go`), and zero timeline noise per push. Not GitHub Deployments: a preview that dies with RWX's idle reaper should not masquerade as a deployment environment.
4. **Friendly, not canonical.** The friendly URL is deterministic at enqueue time (no build to await inside a `Deliver`) and never goes stale — it is RWX's own "latest build for this PR" pointer, which is precisely §8.9's promise. The canonical per-build URL requires the finished build's task cache key (pollable via `rwx results <run_id>`); that is the v2 upgrade path if template drift bites (§4.1.3), not v1 machinery.
5. **Human pushes.** v1 covers agent-originated pushes (the `push_complete` trigger). A human push to the same PR updates neither preview nor status until the next agent push; §24's `pull_request/synchronize` ingress lane (Step 65) is the natural carrier for closing that gap — sharing its debounce, arriving with that Step — recorded here, not built in Step 57.

### 4.1.3 Step 57 risks and open questions

- **Sandbox-lifecycle API surface**: CLI-only per current public docs; RWX's token page says tokens work "through the API and the CLI", so a sandbox HTTP API may exist unpublished — worth a vendor question, since it would simplify the transport. Until then, the pinned-CLI contract tests are the drift guard.
- **stop→start state preservation** (the `Resume` flag): inferred from `reset`'s own "discarding any changes" contrast, never stated by the docs. First exit criterion, settled empirically; both outcomes are designed for (§4.1.1).
- **Error taxonomy**: no published error-code table for the CLI or the Dispatches API beyond `{status: "error", error: string}`; exit-code/envelope classification is pinned by contract tests, unknown → transient (§3.2).
- **`rwx sandbox list` scope**: reconciliation/GC (§4.1 `List`) needs org-wide truth; whether the CLI lists beyond the calling device/user is unverified.
- **Egress proxy**: whether the `rwx` CLI honors subprocess proxy env vars is unverified; if not, RWX traffic bypasses the §4.1 egress posture and needs a vendor answer.
- **`endpointTemplate` duplication**: Narvi renders the friendly URL from its own copy of the endpoint template; the repo's `.rwx` definition owns the real one. Drift = a dead link (annoying, never corrupting). v2: read the built app's URL from run results instead.
- **Preview URLs are public** ("publicly accessible URLs under rwx.run"): anyone holding the link sees the app. Per-repo opt-in is the mitigation; whether RWX offers org-restricted visibility is unverified. Secrets must never reach a previewed app's client bundle — §5.2 posture, worth restating in the setting's own UI copy.
- **First-click-during-first-build**: what a friendly URL serves before its first build completes is unverified (likely RWX's building/starting interstitial; empirically confirmed in Step 57).
- **Dispatch cost**: one RWX build per agent push; content-addressed caching bounds it, and the per-repo toggle contains blast radius. If telemetry shows waste, §24.2's trailing-edge debounce idiom is the ready-made coalescer.
- **Stale Step-48 references**: `ports/sandboxprovider.go`, `ports/capabilities.go`, `ports/refs.go`, `ports/providererror.go` and `adapters/outbound/rwx/doc.go` still say RWX lands at "Step 48"/"PR-48" — written before the Phase-5 renumbering that made it Step 57. Step 57's implementation PR corrects them.

**Phasing:** Step 57, Phase 5, ∥ — depends on Step 12 (the port + Modal precedent), Step 21 (`SourceControl`/`pushpr.go`), and Step 35 (outbox delivery workers), all long since landed; independent of Steps 53-56 and of §24/Step 65 (whose synchronize lane only ever extends §4.1.2 point 5). One PR: adapter + contract tests + the two notifiers + the enqueue path + the per-repo setting, per the one-Step-one-PR convention.

### 4.2 AgentRuntime (OpenCode anti-corruption layer — see §7)

```go
type AgentRuntime interface {
    StartTurn(ctx, TurnSpec) (ConversationID, <-chan AgentEvent, error)
    ResumeConversation(ctx, ConversationID, TurnSpec) (<-chan AgentEvent, error)
    Stop(ctx) error
}
```

### 4.3 Others

`SourceControl` (GitHub + GitLab: createPR, credential minting, push specs), `Notifier` (Slack/Linear/GitHub comment delivery — consumed via outbox only), `IntentClassifier`, `LLM`, `BlobStore` (full interface — §28.1), `SessionStore`/`TurnStore`/`SandboxStore` (sqlc-backed), `Outbox`, `TimerScheduler`, `Clock` (injectable everywhere — no `time.Now()` in domain).

## 5. Cross-cutting invariants

### 5.1 Persistence
- Postgres is the ONLY store. No cache with authority. Uniqueness by constraints, not convention.
- **Outbox pattern** for every outbound side effect (Slack/Linear/GitHub notifications, webhooks): written in the same tx as the state change; a retry worker delivers with exponential backoff + dead-letter after N attempts. Never 2-attempts-then-drop.
- Dedupe/coalescing (webhook events, concurrent PR @mentions) via `INSERT ... ON CONFLICT` atomic claims. Never eventually-consistent storage for coordination.
- A human applying a label or clicking a button is a legitimate, deliberate command — the two are equivalent in kind, and neither needs Postgres's own blessing to act. What is never legitimate is treating a label the BOT ITSELF writes back onto an externally-editable surface as durable trigger state: a GitHub/Slack/Linear label is mutable by anyone with triage rights, forgeable, and — the decisive reason — a second copy of a fact Postgres already owns. Durable trigger state lives in Postgres and is only ever read back from there; a bot-written label may still exist as a human-facing status indicator, but the system itself must never treat it as authority (§24 applies this to review re-triggering specifically; the principle is general).

### 5.2 Security
- Internal auth: single HMAC helper (`platform/auth`), bearer `timestamp.signature`, 5-min window, fail closed. **Separate secrets per direction** (sandbox→CP, CP→bots, webhook ingress) so one rotation doesn't touch everything.
- Sandbox tokens: hashed at rest, one per gen, rotated on identity rotation with a **previous-gen grace window** during overlapping spawns.
- Git credentials: never long-lived in sandbox. `sandbox-agent` implements a git credential helper that POSTs to CP `/sessions/{id}/scm-credentials` (sandbox bearer), caches to disk with flock, 5-min expiry buffer, scoped https+host only. Never fall back to stale cache.
- Agent policy enforcement is **server-side**: review verdict posting, formal PR review submission, and raw-comment blocking are CP endpoints with policy checks (verdict floor, scoping to review sessions) — never prompt-only invariants.
- PR diffs and external content are untrusted input: wrap them in delimited blocks and treat them as data, never as instructions.
- Any re-run/re-review phrasing a posted verdict (§8.2/Step 47) recommends to a user is rendered server-side from the verdict's own typed fields (Step 45), never generated or reproduced by a model directly — and that exact phrasing must be recognizable by the intent classifier's deterministic fail-open fallback (§18.1's `FallbackReason`, §18.2's independent-deterministic-check requirement for an irreversible action), not only by its model-based path. A product that recommends a phrase only its LLM-backed classifier can understand becomes unusable at the exact moment that classifier is degraded — the one moment this requirement exists to cover.

### 5.3 Observability (day one, not later)
- `correlation_id` minted at ingress, propagated: webhook → CP → provider → sandbox-agent → OpenCode wrapper → back.
- `sandbox-agent` logs a **boot fingerprint** first: binary version, image digest, repo SHAs, boot mode.
- Every state transition logs `from`, `to`, `trigger`, `gen`. Every routing/classifier decision logs its inputs and verdict.
- OTel traces + metrics: spawn latency, boot phase durations, liveness gaps, watchdog activations (and how many were false alarms — target: ~0), outbox lag, orphan GC count.

### 5.4 Timeout hierarchy (single source: `platform/timeouts.go`)
One struct, validated at boot with the invariant chain asserted in a unit test:
`provider hard cap (2h) > supervisor turn cap > CP turn_deadline > OpenCode SSE inactivity timeout`, each with explicit margin. Also: `providerHTTPClientTimeout > provider worst cold start`; `first_connect_budget > image pull + boot p99`. No timeout literal anywhere else in the codebase.

## 6. Wire contracts (frontend and sandbox protocol)

These are the canonical contracts the web UI and the sandbox agent speak. Formalize each in `/contracts` with round-trip tests — `/contracts` is the single source of wire truth.

### 6.1 Sandbox WS (sandbox-agent ↔ control plane)
- Connect: `wss://…/sessions/{id}/ws?type=sandbox`, `Authorization: Bearer <sandbox_token>`, `X-Sandbox-ID` (+ NEW: `X-Sandbox-Gen`). Server: 410 when session stopped, 403 on id/gen mismatch. Agent treats 401/403/404/410 as fatal (no retry); else exponential-backoff reconnect.
- CP→agent commands: `prompt` (with author scmName/scmEmail for git attribution), `stop`, `push` (per-repo spec; CP awaits `push_complete`, 360s), `snapshot`, `shutdown`, `ack`, `git_sync_complete`.
- Agent→CP events: `ready`, `heartbeat` (30s, carries conversation id + `last_boot_phase`), `boot_progress`, `token` (cumulative text, upsert-by-messageId not append), `tool_call`/`tool_result`, `step_start`/`step_finish` (carries `cost`; NOTE: `tokens` is an **object**, not a number — a number-vs-object mismatch here silently zeroes cost tracking, so pin it in the contract test), `sub_task_start`/`sub_task_finish` (§7.1), `git_sync`, `artifact`, `execution_complete`, `push_complete`/`push_error`, `session_title`, `warning`, `error`, `snapshot_ready`.
- **Ack protocol**: 6 critical types (`execution_complete`, `error`, `snapshot_ready`, `push_complete`, `push_error`, `sub_task_finish`) carry deterministic `ackId = "{type}:{messageId}"`; sender buffers (1000 events, evict oldest non-critical) and re-sends on reconnect until acked; receiver dedupes by upsert-on-messageId. `sub_task_finish` joins the critical set because it closes an "active" state the UI tracks (§12.2 item 1's live sub-lane count) exactly like `execution_complete` does at the turn level — a dropped, never-redelivered `sub_task_finish` would leave a sub-lane stuck active forever, live and in history, with no reconciliation path (the same failure class §3.2's two-phase terminalization and §9.3 #4/#7 exist to prevent at the turn level).
- **Sub-task fan-out** (§7.1): every event type emitted during turn processing additionally accepts an optional `subTaskId` (absent/null = the turn's main lane), for envelope uniformity — session/connection-lifecycle events (`ready`, `heartbeat`, `boot_progress`, `git_sync`, `session_title`, `warning`, `snapshot_ready`) never populate it, only turn/tool/step-scoped events do — so a lane is always unambiguous even when several sub-tasks' events interleave on the wire. `sub_task_start` (`subTaskId`, `label`, `parentMessageId` — the `messageId` of the main-lane `tool_call` event whose invocation spawned this sub-task) and `sub_task_finish` (`subTaskId`, `outcome`: `completed | failed | cancelled`, reusing the turn's own taxonomy, §3.3) bracket a sub-task's lifetime. The model is flat — a sub-task cannot itself spawn a further-nested sub-task.

### 6.2 Client WS (browser ↔ control plane)
Connect → `subscribe{token, clientId}` within 30s → single `subscribed` payload (full state + event replay + artifacts + participants) → broadcast stream. `fetch_history` cursor pagination. Close codes: 4001 = re-auth, 4002 = token expired. WS token: per-participant, hashed at rest, 24h TTL, minted via REST (`/api/sessions/:id/ws-token`).

### 6.3 REST
The BFF-facing routes: sessions CRUD/create, events, artifacts, secrets, environments, automations, uploads, ws-token. Generate TS types for the UI from `/contracts`; `/contracts` is the single source — no hand-written response types.

### 6.4 Sandbox boot contract
Boot modes: `build | fresh | repo_image | snapshot_restore` (env `NARVI_BOOT_MODE`). Hook policy: `setup.sh` runs only in fresh/build (fatal only in build); `start.sh` runs in all non-build modes (primary repo fatal, secondaries warn). Multi-repo ordered clones under `/workspace/{name}` + generated `AGENTS.md` manifest. Tunnel URLs delivered via provider; sandbox env persisted to `/workspace/.env.sandbox`.

**Amendment (§19.4, warm-boot shared images, Step 42):** under `repo_image`, `setup.sh`'s `ShouldRun` is no longer a flat "never" — it reruns, non-fatally, whenever the boot-time workspace has moved from the image's own built SHA. This is a breaking change to the contract as stated above (Conventional-Commits `!` marker required on the landing PR) — see §19.4 for the full redefinition and rationale.

## 7. OpenCode integration (`adapters/outbound/opencode`)

The most delicate area of the system — budget real care here.

- Pin the OpenCode version in the image; record it in the boot fingerprint.
- The adapter runs inside `sandbox-agent`: starts the OpenCode server, POSTs `prompt_async`, consumes the SSE `/event` stream, translates to typed `AgentEvent`s.
- Known quirks to handle: correlate by generated lexicographically-ascending message IDs; filter the global event stream by session id; track sub-tasks (§7.1 — not Narvi's own child sessions, §14.4); handle compaction events; dedupe tool states by `sid:callID:status`; treat "no output" as failure; final-state fetch fallback; SSE inactivity timeout configurable (default 120s).
- Model catalog: injected server-side config; must survive OpenCode upgrades — on empty/failed catalog, fall back to a pinned known-good set (a version bump can silently drop a provider's models — the fallback prevents that).
- **Contract tests in CI against the real OpenCode binary**: start it, run a scripted turn, assert every event shape against `/contracts`. An OpenCode version bump that changes any shape must fail CI, not production.

### 7.1 Sub-task fan-out

Problem this solves: some engine behaviors spawn multiple concurrent sub-agents within a single turn — OpenCode's own task-tool sub-agents today; the same class of behavior exists in other engines under other names (e.g. a coding assistant's own dynamic multi-agent workflow mode), mentioned here only as the general shape of the problem, not something Narvi special-cases per engine. Left untranslated, this reads to the control plane as one flat, interleaved event stream: unattributable, and easy to undercount on cost.

- The adapter assigns each spawned sub-task a stable `subTaskId` (derived from whatever correlator the engine itself exposes — OpenCode's own nested-task id today; not Narvi's own session concepts, per the note below) and tags every event that sub-task produces with it (§6.1), emitting `sub_task_start`/`sub_task_finish` to bracket its lifetime.
- **Not a new domain entity.** A sub-task is a presentation/wire-level grouping of events belonging to one turn — not a new Postgres row, and not Narvi's own "child session" (§14.4: a full session with its own sandbox/turns, spawned by automation/sentinel features — a materially heavier mechanism; the naming here is deliberately distinct so the two are never confused). The turn state machine (§3.3) is unaffected: one turn still has exactly one `processing` state no matter how many sub-tasks ran underneath it.
- **Cost rolls up.** Every `step_finish.cost` (§6.1) is summed into the same turn/session total regardless of which lane — main or any sub-task — produced it; a sub-task's spend is never invisible in the cost breakdown (§12.2 item 1). (Per-model cost attribution when a sub-task runs on a different model than its turn — §12.2 item 6's cost-by-model view — is not designed here; that needs its own `step_finish` model field before it can be claimed, left to whichever future work actually adds it.)
- Phasing: adapter-side tagging is Step 17 (OpenCode adapter, alongside the other quirks on this line); UI rendering of sub-task lanes is Step 80 (session timeline, lane nesting) and Step 81 (session rail, cost-breakdown roll-up) — see §12.2 item 1.

### 7.2 Context-overflow compaction retry (Step 44)

Problem this solves: a long-running turn (many tool calls/steps) can outrun the model's context window mid-turn, or a sandbox restart can reload a session's full history and overflow on the very first call after resume. Today this is invisible as a distinct failure: the adapter's tagged-union decoder already recognizes it by name (`openCodeTaggedError.Name == "ContextOverflowError"`, one of the schema-derived-only union members alongside `ProviderAuthError`/`UnknownError`/`APIError`/etc., `types.go:128-138`) but `deriveOutcome` folds every non-abort error name into the same generic path: `reason := fmt.Sprintf("opencode: %s", err.Name)` → `ExecutionCompleteOutcomeFailed` (`outcome.go:38-45`). A context-overflow turn today just fails, with no attempt at recovery — the only recourse is a human-initiated retry (§8 item 7, Recovery UX).

The adapter already has a *partial* answer for the auto-compaction case: an `Overflow`-flagged `compaction` SSE part is translated into a wire `Warning` when the engine's own auto-compaction fired mid-turn (`sse.go:314-332`, `translate.go`'s `translateCompactionOverflow`) — but that only covers the case where auto-compaction already succeeded on its own. There is no path today that *forces* a compaction after the fact and retries the failed call; `types.go:162-173` records that the adapter's own research pass found a manual-compaction endpoint (`POST /session/{id}/compact`) that returned "not available yet" on the pinned OpenCode version — a different, always-available summarization endpoint is the one this design uses instead (point 2 below).

**Design — entirely inside the OpenCode adapter (`adapters/outbound/opencode`), no port change:**

1. **Classify on the typed discriminator already decoded.** `err.Name == "ContextOverflowError"` (`types.go:136-138`) is a genuine typed signal, not string-matching a provider error body — the same discipline §4.1 requires of `ProviderError` and §18.1 requires of `FallbackReason`.
2. **Force a compaction via the endpoint that actually works.** New `compact.go`: `forceCompaction(ctx, sessionID string) error` issues `POST /session/{id}/summarize` through the adapter's existing `doJSON` helper (`client.go:47`), bounded by a new `OpenCodeSummarizeTimeout` (propose 120s — a single non-streaming summarization call, generous by the same "chosen generously when the concrete cost is unknown" convention `HookTimeout`'s own doc comment already uses, `platform/timeouts.go:250-254`). Every new timeout still lives in `platform/timeouts.go` (§5.4) — no literal anywhere else.
3. **One retry, inside the same `StartTurn` call.** In `turn.go`'s turn lifecycle: on a `ContextOverflowError`, defer surfacing that error to the caller, call `forceCompaction`, and on success re-post the same prompt exactly once. If the retried call also overflows, or `forceCompaction` itself fails, the *original* deferred error is what reaches `deriveOutcome` (with the reason string noting a compaction attempt was made) — never a silent double failure. This keeps `AgentRuntime.StartTurn`'s existing contract intact: exactly one `execution_complete`-shaped terminal event per turn (`agentruntime.go:110-119`), so §3.3's turn machine never observes the transient failure or needs a new state.
4. **No new `FailureReason`.** An unrecovered overflow still resolves to `FailureReasonFailed` (`domain/turn/failurereason.go:14-18` — the four values are migration-pinned to `session_failure_reason`, `migrations/000004_sessions.up.sql`); the differentiator lives in the reason string (`outcome.go`) and structured logs (§5.3: correlation_id-scoped, logging the classify/compact/retry decision at each step), not in a new enum value.
5. **CI contract test asserts the endpoint exists.** Extend §7's real-binary contract tests (Step 17) to assert `POST /session/{id}/summarize` is present and returns 200 on the pinned OpenCode version — mandatory, not optional, given `types.go:162-173` already proves this adapter probed a *sibling* endpoint and found it unavailable once; an OpenCode version bump that drops or renames `/summarize` must fail CI, never surface as a silent production regression.

**Why adapter-local and not a port/domain change:** the recovery action (which endpoint to call, how to detect the condition) is entirely OpenCode-specific — exactly what §7's anti-corruption layer exists to contain, and CLAUDE.md's "don't couple a port to a single adapter" rule argues against threading engine-specific recovery through `AgentRuntime` for a single implementation. A typed `AgentRuntimeError` (mirroring `ports/providererror.go`'s `Transient bool` classification) only becomes worth adding if and when a second engine adapter needs the control plane itself to arbitrate recovery — not before.

**Interaction with turn recovery (§8 item 7, §3.3):** this shrinks, rather than duplicates, the manual-retry class Recovery UX otherwise absorbs — one whole failure mode (mid-turn context overflow) resolves without ever reaching the user as a failed turn. It is unrelated to the sandbox-loss re-enqueue path (`sessionactor/dispatch.go`'s `planReenqueueOrRespawn`/`tryPlanReenqueue`, §9.3 scenario #2): that path recovers from the *sandbox* dying; this recovers from the *agent* reporting a recoverable error on a live sandbox, entirely within one `StartTurn` call, before the turn ever terminalizes.

**Phasing:** Step 44 — one PR: the classifier + `forceCompaction` + retry loop + `OpenCodeSummarizeTimeout` + table-driven unit tests on the retry decision + a fake-server test (mirroring `fake_server_test.go`'s existing precedent) + the one real-binary contract case above. Independent of every warm-boot Step (§19) — it touches only the OpenCode adapter, and can land whenever Step 17 (OpenCode adapter) is already merged, in parallel with anything else.

## 8. Feature set (exit criteria, not options)

1. **Plan mode**: persistent plans, HITL approve/reject on web/Slack/Linear/GitHub, server-side implementation dispatch on approval, plan/build model split, cross-channel verdict + archive notifications.
2. **Code review**: review sessions per PR with session reuse; atomic claim coalescing of concurrent @mentions; risk-map verdict with `review:*` labels — **a structured verdict from day one** (premise state, risk drivers, shippable class — server-computed, never self-reported, never re-parsed from posted text; full design and the automation policy built on it in §21); test-coverage & doc-drift sentinels; **server-side** verdict floor + formal-review gate + verdict-posting tool (raw issue comments blocked, scoped to review sessions); re-trigger via label/button, or automatically on new commits (debounced, off by default per repo, §24); inline diff pre-fetched into context (agent must not need to run `gh pr diff` repeatedly); suggestion safety (apply via validated endpoint); **criteria-driven auto-approval** (`visual-qa: pass/skip` unchanged; `review: low risk` **inverts** into a `review: needs-human` escape hatch — approval itself is deterministic and criteria-driven rather than label-triggered, §21); dedicated review model selection; optional sentinel auto-fix for coverage/doc-drift findings, merge-gated on the origin PR (§17, disabled by default); **review as a merge readout** (§26) — the verdict front-loads a diff-derived summary, the diff's architecture choices, and its risks to the stack, demoting findings to a collapsed appendix; a description-adequacy check with a third raise-only floor and graduated remediation; deterministic light/deep review triage, measurable per path; adversarial counter-review with contested-points surfacing on the deep path.
3. **Unified intent classifier** (detailed design — see §18): review-vs-request and plan-vs-build across all ingress surfaces; shadow mode (log-only) → active, permanently available, never a one-time launch gate; never-throw contract with an enumerated fallback-reason taxonomy; confidence rubric anchored on textual directness, not model self-reported certainty; DB-backed editable prompt templates with assembled-prompt preview; per-session routing decision records (§18.4).
4. **Automations**: GitHub/Linear/webhook/cron triggers with condition builder; sandbox settings honored on automation sessions; creator/status filters; `last_run` + `artifact_summary` populated; per-automation env vars/secrets.
5. **Enterprise sandbox glue** (full design in §27): cloud credentials via OIDC (provider-agnostic), kubeconfig injection for the target cluster, Docker-in-sandbox, egress proxy, repo/environment/global secrets, OpenCode config storage + injection, toolchain in images (Playwright+Chromium, ripgrep, typescript-language-server).
6. **Files** (detailed design — see §28): uploads to object storage (S3-compatible) + `download_file` tool in sandbox; failed-upload UX signal.
7. **Recovery UX**: relaunch-and-resume (conversation id replay), resume-in-place on live sandbox, Slack/Linear retry buttons, warm-on-type (composer keystrokes pre-warm a sandbox; must not create orphan sessions).
8. **Models**: Anthropic + OpenAI/Codex (ChatGPT OAuth plugin) + Gemini (via OpenCode's own already-present `google`/`google-vertex` providers, no new `AgentRuntime` adapter — §25.2) + reasoning-effort plumbing (per-session and per-message overrides).
9. **RWX previews**: PR preview links dispatched at latest PR commit (detailed design — adapter §4.1.1, preview-link mechanism §4.1.2).
10. **Slack/Linear fidelity**: mrkdwn contract both directions; Linear progressive AgentActivity updates; thread↔session mapping.
11. **Multiplayer**: participants, presence, per-user PR attribution (viewer ≠ reviewer), PR created with the *prompting user's* OAuth token (fallback: bot + manual PR URL).
12. **Identity & access** (new — see §13): GitHub sign-in + pluggable OIDC SSO; automatic cross-channel identity linking (Slack/Linear ↔ GitHub by verified email, in-channel link prompt on ambiguity); RBAC with four roles (admin/maintainer/member/viewer) enforced server-side and channel-agnostic; audit log.
13. **Product prototyping workflow** (new — see §14): path-scoped Environments enforced via sparse-checkout (not prompt discipline); a generalized multi-service boot manifest (`services.yml`) supervised natively by `sandbox-agent`; contract-driven mocking with drift detection; a handoff-readiness sentinel that flags backend-touching or uncontracted work for engineering pickup.
14. **Decision inbox** (new — see §16): a home view listing everything waiting on the signed-in user — auto-approved PRs ready to merge (assigned directly or via CODEOWNERS through the identity graph), reviews requested, plans awaiting approval, recoverable failures — with actions inline, server-side re-validation at click time, and decision latency as an analytics KPI.
15. **Sentinel auto-fix** (new — see §17): a coverage/doc-drift finding can spawn a child session that opens its own merge-gated follow-up PR, referenced from the original verdict and auto-merged only once the origin PR lands — a per-repo toggle, off by default.

## 9. Testing strategy

### 9.1 Unit
Domain packages at ~100% branch coverage — they are pure. Seed `domain/sandbox` with an exhaustive decision-function test corpus (spawn/restore/resume decisions, circuit breaker, timeout evaluation).

### 9.2 Contract
Round-trip tests for every `/contracts` schema: Go marshal → validate → unmarshal; TS codegen compiles against frontend usage; OpenCode adapter tested against the real pinned binary (§7).

### 9.3 Resilience (the differentiator — phase 2 exit criterion)
These run as automated scenarios against a real (or provider-faked) stack. Minimum set:
1. Kill the CP pod mid-turn → actor rehydrates, turn resumes or fails-with-reason; no stuck `processing`.
2. Kill the sandbox mid-turn → suspect → grace → respawn+resume with same conversation id.
3. Slow boot (inject 5-min delay in deps install) → boot_progress keeps session alive; no false kill.
4. `execution_complete` arrives AFTER terminalization → state reconciled, automation counters corrected. (Scoped to the turn still being `Processing`, §3.2 — a signal arriving past the turn's own, separate `turn_deadline` timeout is a different, known-residual case, §3.2, not exercised by this scenario.)
5. Two concurrent spawns (double-click / retry race) → single winner by gen fencing; loser sandbox reaped by GC.
6. Stale sandbox from old gen reconnects → rejected 403, logged, session unaffected.
7. WS drop during event stream → ack protocol redelivers the 6 critical events exactly once.
8. Provider API down during spawn → typed transient error, retry with backoff, circuit breaker only on permanent.
9. Outbox: Slack API 500s for 10 min → notification eventually delivered, no loss.
10. Concurrent @mentions on one PR → exactly one review session (atomic claim).
11. Dirty working tree at relaunch → stash → checkout session branch → pop; zero lost user edits.
12. Deploy rollout (rolling restart) → zero sessions marked failed.

### 9.4 Shadow mode (phases 3-4)
Intent classifier and code review run in shadow mode (log-only) on real traffic before activation; divergence report per decision. **Shadow mode is a permanent capability, not a one-time launch gate** (§18.5): activating a classifier or reviewer on a surface must never delete the shadow code path, its config, or its telemetry — the same mechanism is used again for every future model swap, prompt change, or new surface, not just the first activation. Skipping the shadow window on the reasoning that tests alone prove equivalence is not a default; it requires an explicit, documented exception.

## 10. Implementation phases

Each phase ends with a working, demoable increment. Do not start phase N+1 with phase N's exit criterion red.

**Phase 0 — Foundations (1-2 wks)**
Repo skeleton per §1; config loading + validation; `platform/timeouts.go` with invariant test; logging/OTel envelope; Postgres migrations for core tables (`sessions`, `turns`, `sandboxes` + history, `events`, `session_timers`, `outbox`, `participants`, `artifacts`); `/contracts` schemas for §6 + codegen (Go + TS); CI (lint, test, contract tests).
*Exit: `make dev` boots CP against local Postgres; contracts round-trip green.*

**Phase 1 — Core (3 wks)**
Session actor + timer pump + advisory locks + fencing; sandbox & turn state machines (port decisions + tests); Modal provider; minimal `sandbox-agent` (boot contract, git clone, credential helper, WS + ack protocol, heartbeats); OpenCode adapter (happy path); client WS hub with subscribe/replay; REST endpoints the frontend needs for one session.
*Exit: end-to-end via the API (or a minimal client) — create session → prompt → streamed events → agent pushes branch → PR created. Kill-pod test (§9.3 #1) green.*

**Phase 2 — Resilience (2 wks)**
Snapshots/restore/resume; image prebuilds (fingerprint = repo SHAs + runtime version; always fall back to base image on any miss — never block a session); rebuild scheduling with backoff; reconciler + orphan GC; two-phase terminalization + reconciliation; turn recovery + resume-in-place; git state machine complete.
*Exit: full §9.3 suite green.*

**Phase 3 — Ingress & routing (2 wks)**
GitHub/Slack/Linear/webhook ingress with shared toolkit (signature verify, atomic dedupe, one `CreateSessionRequest`); intent classifier (shadow first); plan mode end-to-end; outbox delivery to all notifiers; auth hardening (host-scoped cookies, backend-issued session validation).
*Exit: bot ingress demo; classifier shadow report on real traffic.*

**Phase 4 — Warm boot & agent-turn resilience (Steps 40-44; additive, does not gate Phase 3's exit above or block Phase 5's start)**
Shared, tip-tracking image prebuilds (§19): re-keyed fingerprint, fetch-aware `gitclone.SyncAll`, freshness pump, the `repo_image` setup-rerun contract amendment, hook-output capture, and the telemetry-gated graduated rerun ladder; plus the OpenCode adapter's context-overflow compaction retry (§7.2), fully independent of the warm-boot work.
*Exit: per-Environment warm-boot staleness window observed within the 10–40 min range §19.2 predicts; §9.3-class fetch-fail/stale-image/non-idempotent-setup scenarios green; compaction-retry contract test green against the pinned OpenCode binary. Step 43 specifically does not start until Step 42's rerun-duration telemetry shows a real need.*

**Phase 5 — Code review & automations (2 wks)**
Full §8.2 code review; automations engine + sweeps; RWX provider + previews; uploads; secrets scopes; model catalog + Codex OAuth.
*Exit: code review in shadow on live PRs; verdicts reviewed for precision.*

**Phase 6 — Rollout (1-2 wks)**
Config setup (automations, secrets, environments, settings, integrations); cohort-based rollout of sessions; operational dashboards and runbooks; SLO alerts wired; a per-surface user guide (web/Slack/Linear/GitHub), each surface documenting what it accepts AND its own honest negatives — a CI check ties every documented command to the `/contracts` route or classifier routing record (§18.4) that actually implements it, and the guide documents only shipped behavior, never aspirational text — otherwise a hand-maintained prose guide is just a copy of that same behavior with no mechanism keeping it in sync, which is exactly what the CI check exists to close.
*Exit: platform serving production traffic under monitoring.*

**Phase 7 — Web UI (~3-4 wks; see §12)**
The SPA on the generated contracts, embedded in the control-plane binary.
*Exit: all nine views in §12.2 built to the mockups (including the decision-inbox home, §16) + the UX items (§12.3); screenshot-level review against the mockups; `make dist` produces the single self-contained binary.*

## 11. Working conventions for the implementing agent

- Never put I/O, `time.Now()`, or randomness in `/internal/domain` — inject `Clock`/`IDGen`.
- Every state transition goes through the machine's `Transition(from, trigger) (to, error)` table — no ad-hoc status writes, enforced by making status columns writable only via the store methods that take a transition proof.
- Every new timeout/interval goes in `platform/timeouts.go` — grep-test in CI forbids `time.Second * N` literals outside `platform` and tests.
- Table-driven tests; race detector always on (`go test -race`); `errgroup` + context for all concurrency; no naked goroutines (lint rule).
- When behavior is ambiguous, resolve it against the mockups and the §6 contracts, and keep the domain paths single: repos are always a list, tokens are always hashed, one status taxonomy.
- Commit per coherent unit with tests; keep `main` green.

## 12. Web UI (phase 7)

Design mockups of the nine views exist (decision inbox/home, session workspace, code review, release review, plan mode, automations, settings, analytics, sign-in) and are the visual spec; ask the requester for the artifact if not provided. The mockups do not necessarily cover every individual screen or state phase 7 will need (an empty state, an error state, a secondary modal not explicitly drawn) — any such screen must be derived from the same visual design system the mockups establish (tokens, typography, layout, component patterns), never designed independently of it; §11's own resolve-ambiguity-against-the-mockups rule extends to this.

### 12.1 Architecture
- **SPA, no SSR, no BFF.** Vite + React + TanStack Query/Router. Static build embedded in the control-plane binary via `go:embed`; `narvi serve` serves API + WS + UI on one port. Self-host story: one binary + Postgres.
- **Data layer generated from `/contracts`**: TS types + typed API client + WS event handlers are codegen outputs; no hand-written response types anywhere. Client pattern: WS transport → event log → reducer → query invalidation.
- **Auth pluggable**: generic OIDC (GitHub/Google/SSO as configuration), session tokens issued by the Go backend; no NextAuth. Org-specific enrichments (e.g. company URL context) live behind an extension point, not in core.
- **Theming**: light/dark via CSS custom properties, `prefers-color-scheme` + explicit toggle.

### 12.2 View inventory
1. **Session workspace**: sidebar with status taxonomy chips (running / booting n/m with progress / completed / cancelled / failed·reason), session-source icons (web/Slack/Linear/GitHub), and a creator filter with explicit labels ("My sessions" = created or joined / "All sessions"; no "Team" option — the domain has roles and identities but no team entity), typed-event timeline (collapsed tool calls with durations, per-step cost, streaming text; sub-task fan-out, §7.1, as collapsed labeled sub-lanes nested under the spawning tool call, never interleaved, with a live count while active and a distinct color/icon for a failed or cancelled sub-lane vs. completed), failure cards with persisted reason + one-click Resume (conversation replay), composer (model / effort / plan-mode toggle, warm-on-type indicator), right rail (sandbox panel: status, gen, last-seen, runtime fingerprint, correlation id, timestamped state transitions; boot phases with durations; artifacts: PR / preview / uploads; cost breakdown, inclusive of sub-task spend, §7.1). Multiplayer presence.
2. **Code review**: risk-map verdict table (area × severity × assessment) editable by maintainers, "posted via server-side verdict tool" indicator, per-sentinel status (coverage / doc-drift / visual-QA: pass/fail/skipped), finding cards (severity, file:line, failure scenario, Apply-suggestion via validated endpoint, Dismiss-with-rebuttal feeding re-review reconciliation, auto-fix PR link + merge-gated status when a coverage/doc-drift finding triggered one, §17), review history, coalesced-mention + session-reuse info, re-run action.
3. **Plan mode**: persistent versioned plan document (numbered steps with file refs, scope estimate, v1→v2 history), approval bar (Approve & build / Request changes / Reject), cross-channel awaiting indicator (web/Slack/Linear, first verdict wins), plan-model vs build-model split visible in header.
4. **Automations**: table with health column (success ratio, strike counter "n/3 before auto-pause", auto-paused chip + Resume), expandable invocation → runs rows (per-target status, typed failure reason, artifact_summary one-liner, link to session), triggers/targets/next-run, my-automations/status filters.
5. **Settings**: sectioned nav (general, environments, members & access, secrets, integrations, models, prompt templates, image builds). Environments: ordered repo cards with prebuilt-image status (fingerprint, build duration, drift-check countdown, failed-rebuild state showing backoff + base-image fallback + reason), and a sentinel auto-fix toggle (per repo, off by default, §17). Members & access (§13): role management per member, linked-identity chips (github/slack/linear with pending + resend), audit-log view. Secrets: table with scope chips and per-target resolution display (order: automation → environment → repo → global, "this value wins"). Prompt templates: versioned classifier templates, editable, assembled-prompt preview, shadow-mode divergence metric + Activate.
6. **Analytics**: my-sessions/all + date-range filters; KPI tiles (sessions, success rate, **false failures** = watchdog kills later proven alive with target 0, cost + median per session, boot p95 with sparkline); sessions-per-day stacked by outcome (status colors, legend + hover, 2px spacers); cost-by-model horizontal bars; Review finding outcomes (accepted/rebutted/dismissed — fed by §21's read model; precision computed only over definitively-resolved findings, dismiss-rate reported separately); top typed failure reasons linked to sessions.
7. **Sign-in** (§13.1): "Continue with GitHub" primary + "Continue with SSO (OIDC)" secondary; identity auto-link status panel (github/slack verified, linear pending with in-channel link); allowlist note; mention that the GitHub token also drives PR attribution.
8. **Decision inbox — the home view** (§16): sectioned queue (Ready to merge / Needs your review / Awaiting your approval / Needs attention) with per-row inline actions (Merge, Open review, Approve & build, Assign to engineering, Resume), assignment provenance printed on every row, age with stale highlighting, repo filter only (the inbox is inherently scoped to the signed-in user — a "Mine" filter there would be redundant), median time-to-decision in the toolbar. Sessions list becomes the second tab.
9. **Release review** (§15): manifest table of constituent PRs (review state, CI at merge SHA, flags for admin overrides / red-at-merge / unreviewed reverts), aggregate-diff trigger banner showing why the conditional pass fired, composition findings with Block release / Acknowledge & ship actions.

### 12.3 UX items to land with the UI
Boot progress phases instead of spinner; failure reason + resume everywhere (matching the Slack/Linear retry affordance); distinct cancelled/failed/timeout chips; sandbox "what happened" panel (transitions + fingerprint + correlation id).

**Composer send semantics (item 1's composer, Step 81, decision 5) — acceptance criteria, day one:** Enter sends and Shift+Enter inserts a newline, from the very first ship of this composer, never added later — inverting this after users have built muscle memory around one behavior is a change users route around, not adopt, so it is not a follow-up. An IME composition guard is required: confirming an in-progress IME composition (e.g. selecting a candidate while typing Japanese/Chinese/Korean, which itself uses Enter) must never itself send — the guard checks the browser's own composition state, not a heuristic over the text. Exactly ONE shared can-submit predicate drives both the Send button's disabled state and the keydown handler's own send-or-not decision — never two independently-maintained checks, since a button and a key handler silently drifting apart on when submission is allowed is the classic defect this class of UI produces. Touch/mobile gets an explicit decision rather than an unstated gap: out of scope for this ship — the mockups' existing breakpoints (`docs/design/mockups.html`, three `@media (max-width:980px)` rules collapsing `.app`/`.sidebar`/`.rail`, `.settings`/`.setnav`, and `.charts2` to single-column layouts) reflow for narrower viewports but define no touch-specific interaction anywhere, and the composer itself carries no rule inside any of them; a touch-appropriate composer affordance (mobile virtual keyboards make Shift+Enter awkward to reach) is deferred, named here rather than silently left unspecified.

### 12.4 Sequencing & exit
Built in phase 7. Definition of done: all nine views built to the mockups + §12.3 items; screenshot-level review against the mockups; `make dist` produces the single self-contained binary.

## 13. Identity, authentication & RBAC

### 13.1 Authentication
- **GitHub OAuth is the primary login** and serves double duty: the stored user OAuth token is what attributes PRs to the real author (§8.11). Generic **OIDC** is the secondary provider for SSO (Google/enterprise IdP) — configuration, not code.
- Sessions: **backend-issued, host-scoped cookies** (HttpOnly, SameSite=Lax; never a default cookie name on a shared parent domain — a colliding cookie from a sibling app on the parent domain is a classic random-logout cause). Token/refresh handling lives in the Go control plane; the SPA holds no provider tokens.
- Signup gate: allowlist of email domains / GitHub orgs / explicit users, evaluated at first sign-in; default role assigned from config (e.g. domain match → member).
- Provider tokens encrypted at rest (AES-GCM), per-user.

### 13.2 Identity graph & auto-linking
One person = one `user` across web, GitHub, Slack, Linear.

Schema:
```
users(id, primary_email, display_name, role, disabled, created_at)
identities(user_id, provider ENUM(github,slack,linear,google), external_id,
           email, email_verified, linked_via ENUM(auto_email, prompt, admin),
           created_at, UNIQUE(provider, external_id))
identity_link_prompts(provider, external_id, nonce, expires_at)  -- pending links
```

Auto-link algorithm (runs on first event from an unknown provider identity — Slack mention, Linear webhook):
1. Fetch the actor's profile email from the provider API.
2. Match against `users.primary_email` and verified identity emails.
3. Exactly **one** verified match → auto-link (`linked_via=auto_email`), notify the user in-channel and in-app.
4. Zero or multiple matches → **never guess**: create a link prompt and reply in-channel with a short-lived magic link ("connect your account"). A state-changing action from this not-yet-linked identity is **denied** (audit-fix batch "block unlinked actor state changes" — hardened from an earlier version of this section, which let the action proceed under bot attribution while the prompt was pending; that was a confirmed audit finding, not a design that survived review) — the magic-link prompt is still sent exactly as before, and the actor simply retries the identical action once they've clicked it and linked.
5. Manual link/unlink in Settings → Members; admin can force-link.

Failure rules: a provider email-API failure is a **retryable error, not an empty identity** — retry with backoff and keep the last known value; never null-out an email on transient failure (nulling it breaks both sign-in and identity linking).

Every cross-channel action (prompt from Slack, plan approval from Linear, review re-trigger from a GitHub comment) resolves to a `user_id` before it reaches the domain; a not-yet-linked actor's state-changing action is denied (never bot attribution) while a link prompt is sent in parallel, so the same action succeeds on retry once linked. GitHub is denied the same way (batch fix/deny-unlinked-github-actors — a repo-owner-decided hardening that closed a confirmed, MEDIUM-severity authorization gap: an unlinked GitHub commenter was bypassing role gates a linked-but-restricted user could not), even though a GitHub commenter resolves directly from an existing GitHub-OAuth-login identity with no deferred "auto-link pending" mechanism at all — an unresolved GitHub commenter has simply never logged into Narvi via GitHub OAuth, a permanent case with nothing to retry, unlike Slack/Linear's own transient, self-resolving pending-link state. `AuthorizeLinkedActor`, not `AuthorizeResolvedActor`, now governs GitHub's own create/prompt gates too (`internal/adapters/inbound/github/coalesce.go`); since GitHub has no magic-link prompt to send in parallel, its own ingress instead posts a one-time, honest reply pointing the commenter at the ordinary GitHub OAuth sign-in flow (`internal/adapters/inbound/github/actornotauthorizedreply.go`), deduped per `(repo, PR, commenter)` so a repeat mention doesn't spam the same reply.

### 13.3 RBAC
Roles (global, one per user): **admin > maintainer > member > viewer**.

| Permission | admin | maintainer | member | viewer |
|---|---|---|---|---|
| View sessions / analytics | ✓ | ✓ | ✓ | ✓ (read-only) |
| Create sessions, prompt, approve plans on own/joined sessions | ✓ | ✓ | ✓ | — |
| Stop/resume any session; approve any plan | ✓ | ✓ | — | — |
| Manage automations, environments, repo/env secrets | ✓ | ✓ | — | — |
| Edit review verdicts; re-trigger reviews; auto-approval eligibility config (§21) | ✓ | ✓ | — | — |
| Integrations, global secrets, prompt-template activation, members & roles, per-repo auto-merge toggle (§21), sentinel auto-fix toggle (§17 — stricter than auto-merge since it ends in an unattended merge with no per-repo arming step, not a human Merge click), per-repo automatic re-review opt-in toggle (§24 — off by default, same admin-only row as the other automation-enabling toggles here) | ✓ | — | — | — |

Enforcement — **server-side only, channel-agnostic**:
- `domain/authz`: a table-driven `Authorize(actor, action, resource) error` — the matrix above lives in the domain as data with exhaustive tests. Every state-changing actor command (session actor mailbox, plan approval, verdict edit, automation toggle) calls it, so a Slack approval passes exactly the same check as a web one.
- HTTP middleware handles the coarse route-level gate; WS `subscribe` applies visibility rules. The UI hides what the role can't do, but the server is the authority.
- **Viewer guard**: viewers never gain PR-reviewer attribution or git identity on session artifacts.
- **Audit log**: `audit_log(actor_user_id, action, resource_type, resource_id, detail_json, correlation_id, created_at)` written in the same transaction as the change; surfaced in Settings → Members ("Audit log").

### 13.4 Phasing
- **Phase 1**: GitHub OAuth + cookie sessions + `users` table + role skeleton (admin/member) + route middleware — needed before the first end-to-end session.
- **Phase 3** (with bot ingress): identity auto-linking + link prompts, full four-role matrix, channel-agnostic Authorize on plan/review actions, audit log.
- **Phase 7** (UI): sign-in page, Settings → Members & access (role management, linked-identity chips with pending/resend, audit log view) — mocked in the design artifact.
- First-run seeding: any imported participants map to `users` by GitHub id; everyone defaults to `member`, initial admins set by config.

## 14. Product prototyping workflow (new capability)

Problem this solves: product/PM sessions are almost exclusively frontend work, but an unscoped agent will happily wander into backend files — that work is then thrown away and redone by an engineer, burning tokens and review time for nothing. The fix is **prevention, not correction**: make backend code physically absent from the sandbox rather than relying on the agent (or a prompt) to behave.

### 14.1 Scoped Environments
- Extend the Environment record (referenced from session creation and automation targets, §3.5) with an optional `path_scope: []glob` (absent = full access, unchanged behavior) and an optional `mock_config` (§14.3).
- **Enforcement is at the git layer, not a policy/prompt layer.** `domain/gitstate`'s clone step (§3.4) runs `git sparse-checkout set <globs>` per repo when `path_scope` is present. Excluded paths never materialize on the sandbox filesystem — this cannot be bypassed by prompt injection, agent "helpfulness," or a gap in OpenCode's own permission model, because there is nothing there to edit.
- `path_scope` MUST always include whatever shared contract directories the Environment's declared services reference (§14.2-§14.3), resolved explicitly by the Environment config — never inferred by the agent.
- Sessions created under a scoped Environment carry a provenance tag (alongside the existing `spawn_source`) so the label automation and the handoff sentinel (§14.4) can act on it without re-deriving intent.

### 14.2 Multi-service boot manifest (generalizes §6.4)
Today's boot contract is one `setup.sh` (bake-time) + one `start.sh` (one service, primary-fatal/secondary-warn) per repo. A prototyping Environment typically needs two services at once (frontend dev server + mock API), and pushing that onto a single shell script re-creates exactly the kind of ad-hoc multi-process supervision (backgrounding, signal traps, PID tracking) that Narvi eliminates elsewhere (§7, Step 12) — replicating it into repo-owned bash would just move the bug class, not remove it.

- New optional `.narvi/services.yml` per repo:
  ```yaml
  services:
    - name: web
      cmd: pnpm dev
      cwd: apps/web
      readiness: { port: 3000 }
      criticality: primary
    - name: mock-api
      cmd: prism mock contracts/api/openapi.yaml -p 4000
      readiness: { port: 4000 }
      criticality: primary   # in a prototyping Environment the mock IS the backend
  ```
- `sandbox-agent` supervises every declared service with the **same** process-group/reap/drain machinery already built for OpenCode/code-server/ttyd — no new supervision code path, just more entries in the same table.
- Each service's `readiness` (port poll or HTTP check) becomes a **named `boot_progress` phase** over the existing event (§6.1) — this is what lets the UI show granular boot phases (mockup decision #2) instead of one spinner, for free.
- `criticality: primary | secondary` carries the same fatal/warn semantics `start.sh` already has today — just per-service instead of per-repo.
- **Backward compatible, no forced migration**: if `services.yml` is absent, `sandbox-agent` falls back to the current `setup.sh`/`start.sh` contract unchanged (§6.4).
- `setup.sh` semantics don't change: genuinely expensive steps stay baked into the image at build time (§8.5-note). A mock server regenerated from a static spec file is cheap enough (sub-second) to run live at every boot instead of being baked — staying in sync with `contracts/api/*` at HEAD matters more than the negligible regen cost.

### 14.3 Mocking strategy
The mock is a **versioned repo artifact**, authored once and reviewed like code — never something an agent invents per session (that would just reproduce the waste this feature exists to prevent). Two supported sources, no bespoke platform code needed for either:
- **Contract-driven**: a shared `contracts/api/*.{yaml,json}` spec (the same convention as the platform's own `/contracts`, §6) drives a generated mock server (e.g. Prism), declared as a `services.yml` entry.
- **MSW reuse**: if the frontend already ships Mock Service Worker handlers for its own tests, a prototyping Environment just flips an env var to route through them — zero new infrastructure.

**Drift detection**: extend the image-build fingerprint/staleness mechanism (§8.5-note, same PR scope as image builds) to also fingerprint `contracts/api/*`. If a real backend endpoint changes without the contract being updated, this doesn't block anything — it feeds the handoff sentinel (§14.4).

### 14.4 Handoff to engineering
On PR creation from a scoped session (§14.1), `domain/review` (§8.2) runs a **handoff-readiness sentinel** alongside or instead of a normal risk verdict: it reports which endpoints the prototype calls that have no entry in `contracts/api/*`, and any backend-adjacent TODOs the agent left behind.

- **v1 (ship first, cheap)**: auto-apply a `handoff` label + post the sentinel's summary as a PR comment; optionally open a linked Linear/GitHub issue assigned to an engineering queue.
- **v2 (only if handoff volume justifies it)**: a "Send to engineering" action spawns a **child session** (existing mechanism — `parent_session_id`/`spawn_depth`, max depth 2) in a full-access Environment, pre-loaded with the prototype diff + sentinel summary, started directly in **plan mode** (§8.1) so an engineer approves an implementation plan instead of starting from a blank prompt.

Both reuse existing primitives (review sentinels, labels, child sessions, plan mode) — no new subsystem either way.

### 14.5 Phasing
- `path_scope` + sparse-checkout enforcement: extends `domain/gitstate` and the Environment/session-creation data model — Phase 1-2 (alongside Step 09/26).
- `services.yml` supervision: extends `sandbox-agent` process supervision and `boot_progress` reporting — Phase 1-2 (alongside Step 12).
- Mock contract drift-check: extends the image-build fingerprint work — Phase 2 (alongside Step 24).
- Handoff sentinel v1: extends the review sentinels — Phase 5 (alongside Step 45).
- Handoff v2 (child-session escalation): optional; add later in Phase 5 or beyond if the volume of v1 handoffs justifies the extra complexity.
- UI (Phase 7): Settings → Environments gains a path-scope + services editor; sessions can be filtered/labeled by prototyping provenance; the handoff sentinel surfaces inside the code-review view (§12.2 item 2).

## 15. Release PR review (new capability)

Problem this solves: reviewing a release PR (one that bundles many already-individually-reviewed PRs — a release-branch cut, a `develop→main` promotion) is a different job from reviewing a feature/fix PR. Line-by-line correctness was already checked per constituent PR; what's missing is (a) verifying the safety net actually held for every one of them, and (b) catching composition bugs that only emerge from PRs interacting — which no single PR's review can see. Two concrete failure classes make this non-hypothetical: a migration-numbering collision across sequential PRs (each diff clean; the conflict is only visible in the merged tree) and an endpoint rename silently regressed by an unrelated merge — neither visible in any one PR's diff.

### 15.1 Detecting a release PR
A PR is treated as a release review when it matches a configurable pattern: originates from/targets a `release/*` branch, or carries a `release` label (manually applied, or auto-applied by an automation trigger on branch-name pattern, §8.4). Detection reuses the existing intent-classification seam (§8.3) — release-vs-feature is just one more category alongside review-vs-request and plan-vs-build, not a separate classifier.

### 15.2 Manifest check (always runs)
Extends `domain/review` (§8.2) with a `ReleaseManifestCheck`, distinct from the per-PR risk-map verdict:
- `SourceControl` (§4.3) gains `ListMergedBetween(ctx, baseRef, headRef) ([]MergedPR, error)`. Each `MergedPR` carries: PR number/title, approving reviews, CI conclusion **at the merge SHA** (not the latest SHA — a force-push after approval can hide a run that was red when it actually merged), whether it merged via an admin/policy override, and whether it was later reverted (and whether that revert was itself reviewed).
- Findings are an audit, not a risk verdict — e.g. "PR #142 merged without an approving review (admin override)", "PR #156 was red at its merge SHA", "PR #160 was reverted 2h after merge; the revert itself was unreviewed."
- Fully mechanizable: no code-reasoning required, this is a compliance check, not a code review. Posted through the same server-side verdict-posting tool as any review finding (§5.2) — never a raw comment.

### 15.3 Aggregate diff review (conditional)
A pure decision function (same style as the domain decision functions, §3.2/§9.1) — `ShouldRunAggregateReview(manifest) bool` — fires when ANY of:
- ≥3 constituent PRs touch overlapping path prefixes (same subsystem).
- Any constituent PR was flagged high-risk/critical by the team's own PR-tiering.
- Any constituent PR's merge required manually resolving a conflict.

When triggered, run one review pass over the full diff `baseRef..headRef` — not per constituent PR — with a prompt **distinct from the standard risk-map verdict**: explicitly framed around composition ("do these already-individually-correct changes conflict, duplicate, or invalidate each other's assumptions"), never re-litigating logic already approved per PR. Reuses the same LLM/review pipeline (§4.3, §8.2) with a separate, versioned prompt template (same mechanism as §8.3/§12.2 item 5).

### 15.4 Premise/shippable enrichment: a later extension, not now
Neither the manifest check (§15.2) nor the aggregate diff review (§15.3) computes or consumes the per-PR `Shippable`/`PremiseState` structured verdict (§8.2/Step 45, §21) — both stay exactly the mechanical/compositional passes specified above, with no release-level premise or shippable score. Enriching either pass with that structured type later — e.g., rolling the constituent PRs' `Shippable` states into an aggregate read on the release cut itself — is a possible later extension if experience with the manifest/aggregate-diff passes shows it's needed. It is explicitly not part of this design and not scheduled.

### 15.5 Phasing
Extends the code-review domain and review-session reuse (§8.2, Step 46) plus the intent classifier (§8.3, Step 36) — Phase 5, alongside the rest of the sentinel family (Step 45/46/48/49). No new domain package, no new state machine. UI: a dedicated release-review screen (§12.2 item 9, mocked in the design artifact) — manifest table + trigger banner + composition findings.

## 16. Decision inbox (home view — new capability)

Problem this solves: the session-centric UI answers "what are the agents doing?" — an observation surface. But in an agent-heavy workflow the humans are the serial bottleneck: merges of auto-approved PRs, reviews, plan approvals, recoveries all wait on a person, scattered across GitHub notifications, Slack threads, and the sessions list. The home screen becomes a **queue of pending decisions addressed to the signed-in user**, each row carrying its action inline. Sessions remain one click away for watching execution; watching is no longer the default job.

### 16.1 Item taxonomy
Each row is a pending human decision — with one narrow, admin-toggled exception (sentinel auto-fix follow-up PRs, §17, disabled by default) that merges without appearing here at all, precisely because there is no decision left for a human to make once its own checks pass. Otherwise, every row is one of:
- **ready_to_merge**: open PR authored by a platform session, auto-approved by the deterministic eligibility engine (§21 — CI green, no floor raised, diff size under a configurable threshold, no sensitive path touched; a `review: needs-human` label forces a PR out of auto-approval regardless of criteria; `visual-qa: pass/skip` unaffected), CI green at head, and assigned to the user — directly, as requested reviewer, or via CODEOWNERS. Action: Merge (1-click confirm while a repo's auto-merge toggle, §21, is unarmed; once armed, these merge without ever appearing here).
- **needs_review**: PRs where the user is requested reviewer/code owner and the verdict is ≥ medium or a formal review is gated; includes release cuts with manifest flags (§15).
- **awaiting_approval**: plan-mode plans the user is entitled to approve (per `Authorize`, §13.3) and handoff items (§14.4) sitting in the engineering queue.
- **needs_attention**: sessions failed-with-resume-available, auto-paused automations, dead-lettered outbox deliveries (admin only).

Ranking: by decision cost then age — quick confirmations (ready_to_merge) first; per-row age shown, stale items (>48h, configurable) visually flagged. Every row prints its **assignment provenance** ("yours via CODEOWNERS · internal/app/scheduler/**" vs "assigned directly" vs "requested reviewer") — a queue whose origin the user can't trust becomes a feed they ignore.

### 16.2 Data & enforcement
- **A read model, not new state**: the inbox aggregates existing Postgres state (plans, review sessions, sessions, automations, outbox) plus SCM data. No new state machine, no new writer.
- `SourceControl` (§4.3) gains `ListOpenPRsForUser(ctx, user) ([]OpenPR, error)` (review state, CI at head SHA, labels, assignees/reviewers) and `ResolveCodeOwners(ctx, repo, paths) ([]Owner, error)`. CODEOWNERS teams resolve to persons through the identity graph (§13.2). SCM data is cached with a short TTL and the staleness is displayed ("as of 2 min ago") — never presented as live truth.
- **Actions re-validate server-side at click time**: the Merge endpoint re-checks CI status, approval state, and `Authorize(actor, merge, pr)` before calling the SCM — the rendered queue is never trusted as authority (same policy-on-the-server invariant as verdicts, §5.2). Viewer role sees the queue read-only.
- Metric: **decision latency** (median time from item entering the queue to its action) joins the analytics KPIs (§12.2 item 6) — the human bottleneck, made visible.

### 16.3 Phasing
- **Phase 5**: read model + endpoints (it aggregates code review, plans, automations — they must exist first); `SourceControl` extensions.
- **Phase 7**: the inbox is the **home view** of the new UI (mocked in the design artifact, decisions 32-34); sessions list moves to the second tab.

## 17. Sentinel auto-fix (new capability)

Problem this solves: coverage and doc-drift sentinel findings (§8.2) are almost always mechanical to fix — add the missing test, update the stale doc — yet today a sentinel finding sits as a review comment a human must action manually, adding a full extra round-trip for exactly the kind of low-risk change that shouldn't need one.

### 17.1 Trigger and scope
Fires when a review verdict (§8.2) contains a finding from the test-coverage sentinel and/or the doc-drift sentinel — no other sentinel or finding type triggers this (in particular, the handoff-readiness sentinel, §14.4, is unrelated and unaffected). **Disabled by default**; a per-repo toggle enables it (Settings → Environments, §12.2 item 5), **admin-only** (§13.3) — a stricter gate than the criteria-driven auto-approval config (§21), which still defaults to a human Merge click and only merges unattended once an admin arms that repo's own auto-merge toggle after a calibration period, whereas this one has no such default and no calibration gate at all; the risk delta justifies the stricter row. **No recursion**: a PR opened by a sentinel-auto-fix child session is never itself eligible to trigger another sentinel auto-fix, regardless of what its own review verdict finds — this is an explicit rule, not a depth-counter side effect.

### 17.2 Fix session
On trigger, the review session spawns a child session (existing mechanism, §14.4 v2 — `parent_session_id`/`spawn_depth`) in the origin PR's own environment (full access — sentinel fixes touch test/doc files, never a scoped prototyping environment), pre-loaded with the origin diff and the specific sentinel finding(s), started directly in build mode (no plan-mode gate: this is mechanical remediation, not a design decision, and the safety net is downstream, §17.4). It pushes a branch and opens a PR **against the origin PR's own branch, not `main`** — a stacked PR, since the fix has to apply on top of the code it's fixing, and the origin PR hasn't merged yet when this happens. The child session's write/edit tool capability is additionally restricted server-side to test/doc path patterns — a capability restriction enforced at the `AgentRuntime`/sandbox-session level (§4.2, §7), distinct from §14.1's `path_scope` (which restricts what's physically on disk via git, not what a present file's own tool calls may target) — defense in depth alongside §17.4's post-hoc diff-scope check, never a prompt-only instruction to "only touch tests and docs."

**Registering the pair as a real GitHub stack (amendment — GitHub has since made stacking a first-class server-side object; registering the pair makes the relationship above legible to it).** Once the fix PR exists, register it together with the origin PR as a stack: `POST /repos/{owner}/{repo}/stacks` with `pull_requests: [originPR, fixPR]` (bottom to top; two members, well inside the endpoint's own 100-PR ceiling). This is necessarily a **second** call, made only after both pull requests already exist — the endpoint groups pre-existing pull requests into a stack, it does not create them, and the origin PR was opened by its own independent flow (Step 21, `pushpr.go`) before this feature's trigger (§17.1) ever fires. A `404` (§17.6 discusses what that means for a given repository) or any other failure from this call is logged and otherwise ignored — it never fails the fix-session flow, and both pull requests stay open and correctly based on each other regardless of whether this call succeeds.

Registering the pair is **not**, however, a cosmetic layer on top of an otherwise-unaffected merge path — GitHub's own documentation is explicit that it is not: "Merging a stacked pull request requires the Stacks API. The legacy pull request merge endpoints can't merge a stack" (docs.github.com/en/pull-requests/tutorials/roll-out-stacked-prs, fetched 2026-07-31). Once this call succeeds, the origin and fix PRs are real stack members, and per that same constraint, merging either of them now requires the Stacks API, not the legacy endpoint. §17.4's system-initiated merge (below) and §21.2's auto-merge both reuse the plan's one existing, generic merge call path unchanged, and neither is Stacks-API-aware. **This is a gap this amendment introduces and does not close — an open item for whoever implements Step 48:** either give §17.4/§21.2's merge call Stacks-API awareness for a PR carrying stack context, or hold this registration call until they do; registering the pair here and then merging through the unmodified legacy path would, per GitHub's own stated constraint, simply fail.

**The fix PR's base is never resolved, only assigned (amendment).** `resolvePRBaseBranch` (`internal/app/sessionactor/pushpr.go`) resolves a repository's real current default branch for the ordinary happy-path PR (Step 21) — called unconditionally, for every repo that reaches PR creation, to compute `CreatePRSpec.Base` (`createPRBestEffort`'s per-repo loop calls it for each pushed repo, with no branch-based condition at all). `repos[].branch` is not a base-branch override — it names the session's own head branch to check out and push (`restdtos.CreateSessionRequestReposElem`'s own doc comment: null means "create the session branch from the repo's default base branch"), and a repo whose `repos[].branch` is left null is skipped before PR creation ever runs (`sendPushBestEffort`'s own doc comment) — it never reaches `resolvePRBaseBranch` at all. There is no session-config field that supplies a PR base today. Whichever code opens the fix PR (Step 48) must never route through that same resolution for this one call site: the fix PR's base is fixed by design to the origin PR's own head branch (this section's opening paragraph), because a stacked PR's base is its parent's head branch, not the repository's default — reusing `resolvePRBaseBranch` unmodified here would silently retarget the fix PR at, say, `main`, undoing the entire point of stacking it on the origin. Default-branch resolution and stacked-parent assignment are two disjoint code paths keyed on whether a deliberate parent is in play, never a single path that happens to fall back correctly.

### 17.3 Verdict update
Once the fix PR exists, the original verdict is updated (via the same server-side verdict-posting tool, §5.2 — never a raw comment) to reference it: which finding(s) it addresses, and that it will merge automatically once the origin PR merges. From this point, the finding's manual Apply-suggestion action (§12.2 item 2) is suppressed — the two remediation paths are mutually exclusive per finding, so a human and the automation can never both act on the same finding.

Whichever review session eventually posts a verdict on the fix PR itself (§8.2/Step 46 — re-triggered the same way as any other PR) is bound by §21.1's stacked-PR review-scope decision: that verdict covers only the fix PR's own small diff against the origin PR's branch, never the cumulative origin+fix diff, with the pair's position/size/ultimate base supplied to it only as context.

### 17.4 Merge gating
The fix PR does not auto-merge on its own CI-green — it waits for a merged-PR event on the origin (GitHub ingress webhook). On that event, the fix branch is **not** simply re-targeted at `main`: only the fix session's own commits are cherry-picked onto the current tip of the default branch and force-pushed to the fix branch. A bare base-retarget would only produce a clean, fix-only diff when the origin merged via a merge commit — under squash or rebase-merge the origin's original commits are never reachable in `main` by the same hashes, so retargeting would resurface the *entire* origin diff. Cherry-picking just the fix commits is merge-strategy-agnostic and is the only form of this operation this feature performs. If the cherry-pick conflicts, that is a hard stop, never an auto-resolve — the fix PR falls through to 17.4's failure path below.

**GitHub's own stack machinery does part of this on its own — verified against GitHub's own published documentation for this amendment, not assumed.** GitHub's stacked-PR reference states that when a PR merges, "the pull requests above stay open and automatically re-target the stack's base branch," and its own "Rebasing" section is explicit that this is not merely a pointer update: "When you merge a pull request at the bottom of the stack, the remaining branches are automatically rebased so the next pull request targets the default base branch" (docs.github.com/en/pull-requests/get-started/about-stacked-prs, fetched 2026-07-31, public preview). The origin PR is, by construction, the bottom of this section's two-PR stack, and the fix PR is the one remaining branch above it — so this is not a hypothetical: **the same origin-merged event that triggers this section's own cherry-pick-and-force-push (§17.1) is the exact, documented trigger for GitHub's own automatic rebase of the fix branch.** GitHub's own rewrite and Narvi's own force-push are therefore two independent writers racing to update the same ref off the same triggering event. Narvi's own mechanism does not depend on GitHub's having already run — it force-pushes the fix branch directly by name regardless of the branch's current state, so its own write, computed directly from the fix session's own commits, is a correct cherry-pick result on its own terms whichever writer lands last. **What documentation alone cannot settle, and is a genuine open question to resolve before this ships:** whether GitHub's automatic rebase can itself reject or interfere with a concurrent force-push to the same ref, and whether Narvi should therefore sequence its own push to run only after GitHub's automatic rebase has observably settled, rather than racing it.

This is a **system-initiated action, not a delegated human one** — it does not call `Authorize(actor, ...)` (§13.3), because there is no actor. It instead re-checks, explicitly, the same facts a human clicking Merge would rely on: the cherry-picked diff touches nothing but test and documentation files, CI is green at the new tip, the cherry-pick applied cleanly, and the toggle is still enabled. (The diff-scope check here is independent of, and does not rely on, §17.2's write-capability restriction on the child session — a restriction enforced at spawn time is never trusted as sufficient on its own; this re-verification runs regardless of whether that restriction held.) **Only if all four hold** does it auto-approve under the criteria-driven policy (§21) and merge. Any one failing (scope grown beyond tests/docs, CI red, a conflicting cherry-pick, toggle flipped off mid-flight) leaves the fix PR as an ordinary `needs_review` item (§16.1) instead of forcing it through — the fallback path is always a normal, human-supervised one.

### 17.5 Audit and visibility
The merge is recorded in `audit_log` (§13.3) with `actor_user_id` NULL — using the same allowance already made in the audit_log schema for actions with no human actor — and `action`/`detail_json` capturing the origin PR, the review session, the fix PR, and which of the four checks passed. If the origin PR itself is never merged (closed, abandoned), the fix PR is simply left open as an ordinary review item — never silently discarded.

### 17.6 GitHub-native stacks: registering the existing pair, and why not further

GitHub has, since the rest of this section was first written, made stacked pull requests a first-class server-side object — `POST /repos/{owner}/{repo}/stacks`, `PullRequest.stack`/`stackEntry` in GraphQL (confirmed present via live schema introspection for this amendment; the `Mutation` type carries zero stack mutations, confirming GraphQL is read-only here), and a `stack` object riding on every PR REST resource and on native `pull_request` webhook events. §17.2 already opens the fix PR as a stacked PR in the informal, base-branch-convention sense that predates all of this. This section is about making that **existing** relationship legible to GitHub's own object model and consuming the context GitHub now supplies for free — it is not the introduction of a new capability, and nothing below changes what §17.1-§17.5 already do.

**Scope: the one pair, not an N-deep producer.** Narvi registers exactly the origin+fix pair §17.2 already creates. It does not gain a capability to decompose arbitrary work into a chain of dependent PRs, because nothing else in this plan produces a chain of more than two dependent pull requests today. In particular, two mechanisms that superficially resemble a decomposition-into-multiple-units feature are not stack producers: the sub-task fan-out mechanism (§7.1) operates entirely within one turn — "a presentation/wire-level grouping of events belonging to one turn," not a new Postgres row, and, per its own doc comment, "the turn state machine (§3.3) is unaffected" no matter how many sub-tasks ran — it produces no pull request of any kind; and the product-prototyping handoff's v2 child session (§14.4) is spawned in a fresh, full-access Environment pre-loaded with the prototype diff purely as reading context for its plan-mode approval — §14.4's own text never bases that child session's own eventual work on the prototype PR's branch the way §17.2 explicitly does for the sentinel fix, so it produces an independent PR, not a second stack member. Designing an N-deep stack producer before anything in this plan actually generates that shape of work would be speculative scope with no consumer.

**Ingress: capturing stack context, honestly scoped to what's actually parsed today.** GitHub's own guarantee covers exactly two carriers: every PR's REST resource, and the native `pull_request` webhook event. Narvi's existing GitHub ingress (Step 32, `internal/adapters/inbound/github`) parses neither of those directly — `payload.go`'s two webhook payload structs (`issueCommentPayload`, `pullRequestReviewCommentPayload`) decode the `issue_comment` and `pull_request_review_comment` event types instead, and GitHub's own reference does not state that either of those two event types carries a `stack` object — only the dedicated `pull_request` event type is confirmed to (§24.1 already documents, independently of this amendment, that "nothing in this codebase today parses GitHub's `pull_request` event at all"). The one outbound REST round-trip this ingress already makes — `githubapi.Adapter.GetPullRequest`, called from `headresolve.go` to resolve an `issue_comment` mention's real head branch — decodes the PR resource today via `pullRequestResponse`/`PullRequest`, which model only `head.ref`/`head.repo`; neither type decodes `stack` yet. Capturing stack context therefore needs `pullRequestResponse` extended with a `stack {id, number, size, position, base{ref, sha}}` field (the same nullable-pointer discipline `head.repo` already gets, since a non-stacked PR carries no `stack` object at all) and `PullRequest`/`mention` threaded with the same — an incremental addition to a call this ingress already makes for every `issue_comment` mention, not a new outbound call. (Whether it is also worth adding to the `pull_request`/`synchronize` lane §24 introduces, once that lane exists, is a smaller, later question; the REST path above is sufficient for what §21.1's review-scope decision needs today.)

**Repository enablement — resolved by GitHub's own documentation, not left as a guess.** GitHub's stacked-PR rollout guide states plainly: "If your team is already using pull requests, you're set up to use stacked pull requests" (docs.github.com/en/pull-requests/tutorials/roll-out-stacked-prs, fetched 2026-07-31) — no per-repository toggle, admin action, or opt-in is described; every repository already open to pull requests already has it. Confirmed empirically against this very repository during this amendment: `GET /repos/khazaddev/narvi/stacks` returns `200 OK` with an empty array today, not `404`. §17.2's log-and-ignore handling of a `404` (or any other failure) from the registration call stays in the design regardless — GitHub's own docs still describe the feature as "in public preview and subject to change," and the empirical result above is one data point on one host, not a guarantee for every GitHub Enterprise Server version or organization policy Narvi might run against.

### 17.7 Phasing
(Renumbered from §17.6 on 2026-07-31 to make room for the new §17.6 above; nothing elsewhere in this plan cited §17.6 specifically, so this is a contained rename, not a cascading renumbering.)

Extends the code-review domain and sentinel family (§8.2, Step 45/48) — Phase 5, after the sentinels themselves exist; reuses child sessions (§14.4), the verdict-posting tool, and the criteria-driven auto-approval policy (§21), so no new subsystem. UI: the toggle (Settings → Environments) and the fix-PR link on finding cards (§12.2 items 2 and 5) are mocked/built in Phase 7 alongside the rest of those views. The GitHub-native-stack amendment above (§17.6) lands in this same Step 48 PR — it makes Step 48's own existing behavior legible to GitHub's object model, not a new Step or a new phase dependency.

## 18. Unified intent classifier (detailed design)

§8.3 states the feature as an exit criterion; this section fixes the contract, rubric, and decision-record shape so Step 36 doesn't have to invent them under time pressure.

### 18.1 Never-throw contract
`IntentClassifier.Classify(ctx, input) IntentDecision` never returns a caller-fatal error — every code path resolves to one of two shapes, and callers pattern-match on `Source`, never on error type:

```go
type IntentDecision struct {
    Source         string // "classifier" | "fallback"
    Target         string // decision-specific, e.g. review/request
    Mode           string // e.g. plan/build
    Confidence     string // "high" | "medium" | "low" — classifier source only
    Reasoning      string // classifier source only; see §18.4 for storage/exposure rules
    FallbackReason string // fallback source only; enumerated, see below
}
```

`FallbackReason` is an enumerated, growable set — `no_api_key`, `timeout`, `invalid_output`, `api_error`, `unsupported_provider` — distinguished via typed errors from the underlying `LLM` port (§4.3), classified by error code, **never by string-matching** (the same discipline §4.1 already requires of `ProviderError`). An unsupported or misconfigured classifier-model choice is itself just another fallback reason, not a silent substitution that keeps reporting success.

The `LLM` port must be multi-provider **by construction**, not merely provider-agnostic in name — this is what makes `unsupported_provider` above a real, reachable state rather than dead code that could never fire. Its interface and typed errors carry no vendor-specific shape (no Anthropic/OpenAI-specific error codes, HTTP semantics, or SDK types leaking through the signature); a configured provider name is resolved through a small registry/factory at the wiring layer, and an unrecognized one is a genuinely exercised, tested code path to `unsupported_provider` — not a theoretical possibility. This mirrors `SandboxProvider`/`AgentRuntime`'s own established "one real adapter now, a second stubbed for later" precedent (§4.1/§4.2, and CLAUDE.md's own "don't couple a port to a single adapter" rule): only Anthropic needs a working adapter at Step 36 (OpenAI/Codex is §8.8's own later scope), but adding that second adapter later must require touching only a new adapter package plus one registry entry — never this port's own interface, the classifier's domain logic, or its orchestration.

Timeouts: rely on the LLM client's own request-timeout option/error; never race a manually-armed `context.WithTimeout` against it as a second, redundant layer — the SDK's own internal abort always resolves first, so an outer wrapper timeout would never actually fire. The actual value lives in `platform/timeouts.go` (§5.4), not as a literal here.

### 18.2 Confidence rubric
Anchor confidence on **how directly the input text supports the decision** — never on how certain the model reports feeling (a rubric asking the latter degrades to reporting "high" almost unconditionally):
- **high** — a clear, direct textual signal (even via a well-known synonym) that an attentive reader would not second-guess.
- **medium** — a reasonable inference from context, tone, or indirect phrasing that an attentive reader could plausibly read differently.
- **low** — no strong signal; the input plausibly supports more than one reading.

This rubric is a **single shared constant**, referenced by every ingress surface's classification call — never duplicated per surface (duplication is exactly how it drifts). It lives at the field-description level of the classifier's structured-output schema, next to the field it governs, not floated separately in a system prompt.

`needsClarification` is **derived in application code** from confidence plus how many plausible targets exist — never asked of the model directly, keeping the threshold a versionable, testable piece of code rather than model behavior. For any action that is irreversible once taken (triggering a review, dispatching a build), the classifier's signal must be corroborated by an independent deterministic check (a regex or label match) before acting; on disagreement between the two, ask for clarification rather than guessing. §5.2 states the corollary this rubric's deterministic path must also satisfy: any re-run phrasing a review verdict recommends to a user has to be one this same fallback recognizes, not just the model-based side.

### 18.3 Calibration methodology
Automated shadow-mode divergence reporting (§9.4) catches wrong *routing* decisions, but not a miscalibrated `confidence` field on an otherwise-correct decision (e.g. everything reported "high" regardless of actual ambiguity in the input) — that failure mode only surfaces via periodic **manual spot-review** of a confidence-labeled shadow sample, cross-referenced by `correlation_id` against the deterministic-fallback path's own decision on the same input. Both methods are required for calibration sign-off; an automated divergence rate alone is not sufficient.

### 18.4 Per-session routing decision record
One record per session, `IntentDecisionRecord`: `session_id`, `surface` (web/slack/linear/github), `source` (classifier/explicit/fallback), `target`, `mode`, `confidence` (nullable, classifier source only), `reasoning` (nullable, truncated to a bounded length — never rejected outright for being long, just cut off), `decided_at`, `decided_at_stage` (`create` | `first_prompt` — some surfaces have the full text at session creation, others, e.g. web with warm-on-type, only at the first real prompt), `cost_usd` (nullable — the classifier's own LLM call has a real cost; omit rather than guess when unknown, same discipline as the model catalog's cost data, §8.8).

`reasoning` is **stored for audit** (it lands in the same audit-minded posture as `audit_log`, §13.3) but **never rendered on any Slack/Linear/GitHub-facing surface** by default — same untrusted/sensitive-output handling discipline §5.2 already applies to PR diffs and external content. This resolves the storage-vs-exposure question explicitly rather than leaving it to whatever an implementation happens to do: store it, don't broadcast it.

Persisted **write-once via a guarded update** (`UPDATE sessions SET intent_decision = ... WHERE intent_decision IS NULL`), not read-then-write — first decision wins, no application-level lock needed. A decision record supplied by the calling surface is honored **only** for `spawn_source` values architecturally capable of having classified it themselves; this check is server-side and never trusts a client-supplied claim (§5.2) — anything else is silently re-synthesized server-side.

### 18.5 Shadow mode is permanent, not a launch gate
See §9.4. Activating the classifier on a surface (shadow → acting) must never delete the shadow code path, its config, or its telemetry — the same mechanism gets reused for every future model swap, prompt change, or new ingress surface, not just the first one. Skipping the shadow-mode window for a change because "tests already prove equivalence" is not a default; it requires an explicit, documented exception.

### 18.6 Classification surfaces
The classifier serves multiple independent categories through the same contract, rubric, and record shape (§18.1-§18.4) — never a parallel classifier per category: review-vs-request and plan-vs-build (Step 36), release-vs-feature (§15.1, Step 50), and — new, Step 64 — `plan_followup` (amend-vs-answer, §23), gated on an existing plan being `awaiting_approval`. An internal-only surface like `plan_followup` is never registered on any public HTTP route — private to `app/`, never wired into `httpapi`/`wshub` — a structural exclusion, not a reviewed convention; any future internal-only classification surface should follow the same rule.

### 18.7 Phasing
Detailed design underlying §8.3 (Step 36, phase 3) and the Settings → Prompt templates screen (§12.2 item 5, phase 7). No prior art exists (anywhere referenced in this document's research) for the DB-backed template storage/versioning/assembled-prompt-preview piece — it is designed from scratch when Step 36 is implemented, using this section's contract, rubric, and record schema as the foundation underneath it.

## 19. Warm-boot shared-image prebuilds (new capability)

Problem this solves: `imagebuild.Fingerprint` keys an image on exact `(base, repoSHAs, runtimeVersion)` (fingerprint.go:29-45; §8.5-note/§10-P2's own "fingerprint = repo SHAs + runtime version"). This makes a warm hit structurally rare — any push to any repo in the set invalidates the key, and the very session that registers a pending build spawns on the base image anyway (§10 Phase 2: "always fall back to base image on any miss"); a build only pays off if a *second* session later targets the identical tip SHAs. In an actively-developed repo the hit rate trends toward zero by construction, leaving Step 26's image-build pipeline attached to a near-empty cache.

The fix redefines what "warm" means: an image's job is not to *be* the session's exact workspace, it is to be a warm cache *near* it. Boot-time `gitclone.SyncAll` (§3.4) already exists precisely to close the gap between baked state and desired state (stash → checkout → pop, plus the sparse-checkout tail Step 29 already ships end-to-end for scoped Environments, §14.1). This section re-keys images on SHA/branch/scope-independent inputs, continuously refreshed from each repo's default-branch tip, and extends `SyncAll` just enough — a bounded fetch, a conditional `setup.sh` rerun — to close the now-larger gap.

### 19.1 Fingerprint and the baked/reconciled split

`Fingerprint(base string, repos map[string]string, runtimeVersion string)` is redefined: `repos` maps repo name to its normalized clone URL, not its resolved SHA. The canonical NUL-separated, name-sorted encoding (fingerprint.go:47-55) is unchanged — only the value keyed per name changes. Because `image_builds` carries no data from before this design (the table is a pure cache, never a system of record), the function is redefined outright: no version tag on the digest, no dual-scheme migration period, existing rows are simply dropped as part of the migration that adds the columns below. (Keep this as a reusable technique, not a precedent to repeat casually: the *next* time this fingerprint's inputs change, after real `image_builds` rows exist that must keep resolving during a rollout, a version-tagged digest and a dual-scheme window are the right tool — just not needed here.)

Repo URLs must stay in the key rather than dropping to just `(base, runtimeVersion)`: `SyncAll` cannot reconcile a repo-set or remote-identity mismatch — it never clones (its own doc comment: "SyncAll never runs `git clone`"), never cross-checks the on-disk `origin` against a session's configured URL, and a later `git push` targets whatever origin was baked in. A session must only ever match an image whose repo set and remotes are identical by construction, and keying on `map[name]url` guarantees that directly. In practice this yields one shared image per distinct repo set — roughly one per Environment. `path_scope` and `mock_config` (§14.1) stay excluded from the key, same as today, since scoping is reconciled at boot by the already-shipped sparse-checkout tail, not by image identity. (A repo URL's own case/trailing-slash/`.git`-suffix normalization is not yet a solved problem in Narvi: `reposource.ValidateRepoURL` validates syntax today but does not canonicalize; the fingerprint's own key-derivation step must normalize before hashing, or two differently-spelled URLs for the same remote will silently produce two images.)

Baked into the image at build time:
1. **Full (non-shallow) clone of every repo at its default-branch tip.** Full history is load-bearing for §19.3: the boot-time fetch must be a small delta against a complete object store.
2. **`setup.sh` executed against that tip**, unchanged `BootModeBuild` semantics (fatal on failure, `hook.go:41-47`). Its real payload is the warm dependency caches (`node_modules`, venvs, build caches).
3. **Pinned runtime** (`RuntimeVersion`), unchanged.
4. **A baked self-description manifest**, `/narvi/image-manifest.json`: `{fingerprint, built_at, built_repo_shas: {name: sha}}`. This lets sandbox-agent decide the setup-rerun question (§19.4) locally, with no extra control-plane round trip — the image describes itself.
5. **No sparse-checkout at build time, ever** — shared images are always built unscoped (§19.7).

`ports.ImageSpec` (`createspec.go:86-100`, currently `{Base, RepoSHAs map[string]string, RuntimeVersion}`) becomes `{Base, Repos map[string]RepoRef{URL, SHA}, RuntimeVersion}`. The builder resolves each default-branch tip SHA **at claim time** and passes concrete SHAs to `BuildImage` — builds stay pinned and reproducible; only the *key* is SHA-free. The resolved SHAs persist as `built_repo_shas` on the row and bake into the manifest. `ports.ImageSpec` stays adapter-neutral (§4.1's own "no out-of-interface operations" discipline) — nothing Modal-specific leaks into the shape, and a second `SandboxProvider` adapter receives the same spec.

### 19.2 Rebuild triggers and the staleness window

Extend `app/imagebuild.Builder` (builder.go) with a second phase of `PumpOnce`: every `ImageRefreshCheckInterval` (propose 10 min, `platform/timeouts.go`, alongside `ImageBuildPumpInterval`'s existing 60s), for each `ready` shared row, resolve each repo's default-branch tip; if any differs from `built_repo_shas`, enqueue a refresh. The existing claim query (which already excludes `'building'` rows, doc.go) gives single-flight per fingerprint for free.

**Refresh never degrades availability**: the row stays `ready`, serving the old `image_ref`, while the new build runs; on success the builder atomically swaps `image_ref` + `built_repo_shas` + `built_at`. This requires relaxing the current "no rebuild-of-an-already-ready-fingerprint, ever" invariant (imagebuild's own doc.go: "no rebuild-of-an-already-ready-fingerprint mechanism exists") — deliberately, and only for this in-place refresh path, never for a serving-path rebuild. Expected staleness window: check interval + build duration, roughly 10–40 minutes — acceptable because staleness is no longer a *correctness* boundary once §19.3/§19.4 exist: boot-time fetch reconciles source content and the conditional setup rerun reconciles dependencies. Staleness only sets the *size* of the boot-time delta — it degrades latency, never correctness.

**Credential requirement**: the freshness pump needs GitHub credentials belonging to no session creator (today's per-spawn SHA resolution borrows the spawning creator's token, `imageresolve.go`; a shared image has no creator). The build service already needs clone credentials to build at all — the recommendation is a platform-level credential (GitHub App installation token) shared by the freshness pump and the build service, configured in `platform.Config`. This is the one genuinely new piece of infrastructure this design needs.

**Spawn-path simplification**: `imageresolve.go`'s `resolveAndSetImage` no longer needs a per-repo `ResolveBranchSHA` call at all — the fingerprint is computable from session config alone. This removes up to `len(repos) × RepoSHAResolutionTimeout` (10s each, `platform/timeouts.go`) of sequential GitHub latency from every spawn attempt, and removes the "creator has no GitHub token → cold boot" fallback class entirely. Warm boot becomes the default outcome for every session whose repo set has a ready shared image — which, after the first build of an Environment, is essentially always.

**Newly urgent, still deferred**: image GC. In-place refresh produces a superseded image ref roughly every 10–40 minutes per Environment, so the already-deferred `DeleteImage`/GC gap (imagebuild's own doc.go: "it never calls DeleteImage... no rebuild-of-an-already-ready-fingerprint mechanism exists") grows from "someday" to "schedule within the same phase this design ships in" — named explicitly here rather than silently left implicit.

### 19.3 Boot-time fetch and the degrade policy

`SyncAll` today is local-only by design: `runGit` never wires the credential helper (`sync.go:333-358`), and `checkoutBranch` falls back to `git checkout -b <branch> HEAD` when the branch isn't local (`sync.go:476-491`, the fallback itself at line 486). Under warm-from-tip images that fallback becomes a trap:
- **A session with an explicit branch that exists on the remote but not in the image**: today's code would silently create a *new, same-named* branch at the image's stale tip HEAD — work proceeds on the wrong base, and a later push is non-fast-forward against the real branch. Silent divergence — exactly the failure mode `domain/gitstate`'s whole machinery exists to prevent.
- **Invented `narvi/<sessionID>` branches**: created at image HEAD, i.e. up to the staleness window behind tip. Tolerable, but avoidably conflict-prone.

**Required changes, `internal/sandboxagent/gitclone`:**
1. New step in `syncOne`, before the dirty-check/checkout: `git fetch origin <resolved-branch> <default-branch>`, bounded by a new `GitFetchStepTimeout` (network-bound; propose 90s, distinct from the existing local-only 30s `GitSyncStepTimeout`), with the credential helper wired exactly as the clone path already does for its own remote operations.
2. `checkoutBranch` prefers `origin/<branch>` when the branch isn't local: `git checkout -b <branch> origin/<branch> --`; only when the ref exists on neither side does it fall back to `origin/<default>` (fetched) or `HEAD` (fetch itself failed).
3. **Degrade policy** — the resilience-critical detail:
   - Fetch failure with the target branch resolvable locally, or acceptable-from-HEAD (an invented branch): **warn and proceed on stale image state**, recorded in the boot log/`AGENTS.md`. Warm boot must never become network-dependent for liveness.
   - Fetch failure when the session **explicitly named** a branch that is neither local nor fetchable: **fail that repo** (primary → fatal boot, secondary → warn/exclude — the existing severity split §3.4/§6.4 already uses). Silently forking a same-named branch at a stale base is the one outcome that must never happen; this rule is non-negotiable in review.
4. New `gitstate` states/triggers for fetch outcomes, through the existing `Transition` table (state.go) and `TriggerFor*` helper convention (sequence.go), with the same table-driven test discipline the existing ten states already have.

Full clones at build time (§19.1) keep these boot-time fetches small deltas.

### 19.4 The setup-rerun contract: `workspaceMoved` (§6.4 amendment)

Today's policy — `repo_image` skips `setup.sh` entirely (`EvaluateHook`, `hook.go:41-47`: `ShouldRun: mode==Build||Fresh`) — is sound only because the exact-SHA fingerprint guarantees image content equals session content. Under warm-from-tip images, the post-`SyncAll` tree can differ arbitrarily from what `setup.sh` ran against at build time (a branch adding a dependency, a lockfile bump on the default branch since the image was built). Leaving `repo_image`'s setup hook at `ShouldRun: false` unconditionally would produce sessions with silently missing dependencies — surfacing later as confusing agent/tool errors, not as a boot error, which is the worst failure class available here.

**This is a behavioral contract change to §6.4 and must carry the Conventional-Commits breaking-change marker** (`feat(sandbox)!`) when it ships — it changes what `repo_image` has meant since Step 13.

**Redefined contract**: *`repo_image` means "`setup.sh` ran at build time against a near-tip tree; if the boot-time workspace has **moved** from the built SHA, `setup.sh` runs again, **non-fatally** (warn, continue) — and is expected to be fast, because its outputs are already warm."*

- `EvaluateHook` gains a `workspaceMoved bool` input alongside `(mode, hook, primary)`. Policy: for `HookSetup` in `BootModeRepoImage` with `workspaceMoved`, `ShouldRun: true, FatalOnFailure: false`; `HookStart`'s existing policy is unaffected.
- `workspaceMoved` per repo = (post-`SyncAll` checked-out SHA ≠ the manifest's `built_repo_shas[name]`, §19.1) — computed locally by sandbox-agent from `/narvi/image-manifest.json`. SHA equality is the *entire* cheap dependency-diff check; nothing finer-grained is built. Narvi cannot know which files are setup-relevant for arbitrary repos, and guessing (lockfile globs, ecosystem conventions) would be exactly the kind of second, magical decision path this system avoids everywhere else. SHA-equal skips (the old exact-match case falls out as a pure optimization — zero regression for a session that does land on an unmoved image); SHA-moved reruns.
- This imposes an explicit contract on user repos: **`setup.sh` must be idempotent and incremental** — the same property package managers already provide (`npm ci`/`pip install`/`cargo build` against a warm cache is seconds, not minutes). Document this as a named requirement in the environments docs, next to the delta-script contract §19.6 adds.
- Non-fatal is the correct severity: a failed rerun on an otherwise-warm workspace still leaves a mostly-working tree and a running agent that can diagnose the gap — strictly better than failing the boot, and consistent with the existing "never block a spawn" invariant (§10 Phase 2).

**This "fast because warm" expectation is not free — it needs monitoring, not just trust.** A real setup script does more than package installs (it may provision local service stacks, run codegen, seed local state), and those parts are not necessarily warm-cache no-ops even when the package-manager portion is. Combined with the fact that Narvi's `workspaceMoved` predicate (SHA inequality) fires on essentially *every* warm boot — the §19.2 staleness window plus any branch delta makes an exact SHA match the exception, not the rule — a slow rerun would silently erode the exact latency win this design exists to deliver, non-fatally and therefore invisibly unless it is measured. §19.5's rerun-duration telemetry exists specifically to catch this before it becomes an incident; §19.6's graduated ladder is the designed, ready-to-schedule response once that telemetry shows it firing.

### 19.5 Hook output capture and rerun telemetry

Two small, concrete gaps this design surfaces and closes, both landing no later than the hook-policy change (§19.4) itself:

**(a) Hook output capture.** Non-fatal reruns are now the most frequent nontrivial boot-time work under this design, and today `runHook` spawns hooks with no `Stdout`/`Stderr` writers at all (`hooks.go:134-142`) — a spawned hook's output goes nowhere, and a failure surfaces only as `"%s: exited %d"` (`hooks.go:159-160`) with zero diagnostic content. A *non-fatal* failed rerun with no captured output is undiagnosable by construction: the warning §19.4 promises would carry no information a person or the agent itself could act on. `runHook` must pass a caller-held, bounded, ANSI-stripped output tail (on the order of 120 lines) through the supervisor's existing `Spec.Stdout`/`Stderr` seam (`supervisor.go:38-39`, already caller-owned `io.Writer`s, added for exactly this kind of need) — held by the caller, never allocated inside the awaited `proc.Wait` call, so a timeout-triggered `proc.Stop` can never lose a buffer that was never inside the cancelled operation to begin with. Surfaced in the boot log alongside any non-fatal hook failure. Shares the existing `HookTimeout` (`platform/timeouts.go:250-254`) — a rerun replaces the original `setup.sh` invocation within the same phase, so it needs no new timeout constant of its own.

**(b) Rerun-duration telemetry.** Per-hook wall-clock, emitted from the existing hook-run bracketing in `runRepoHooks`/`runHook`, joins the OTel metrics §5.3 already lists (boot phase durations). This is the concrete measurement §19.4's "expected to be fast" claim needs, and it is §19.6's own adoption trigger — shipping §19.4 without this leaves that trigger unmeasurable.

### 19.6 Graduated setup-rerun ladder (Step 43, telemetry-gated)

Designed now, scheduled later: once §19.5(b)'s telemetry shows full `setup.sh` reruns materially eroding the warm-boot latency win (rather than assumed up front), this adds a middle tier between "skip" and "full rerun."

- An optional, repo-authored delta script (e.g. `sync.sh`, alongside `setup.sh`/`start.sh` in the closed hook vocabulary, `hook.go:9-14`) runs *instead of* full `setup.sh` when `workspaceMoved` is true but `setup.sh` **itself** is unchanged between the built SHA and the checked-out HEAD.
- "Unchanged" is answered exactly, with no new hashing scheme: `git diff --quiet <built_sha> HEAD -- setup.sh`, computed by sandbox-agent from the already-baked manifest (§19.1 bakes `built_repo_shas` for exactly this). This one predicate also uniformly covers a branch *adding or removing* `setup.sh` entirely — no separate empty-case handling needed. Any git error on this check is conservative: ineligible, fall through to full `setup.sh`.
- Failure ladder, matching §19.4's own non-fatal severity throughout (never escalate to fatal — a moved workspace proves nothing about dependencies, so the "moved" predicate can never justify failing the boot): delta script fails → warn → run full `setup.sh` → if that also fails → warn and continue, same as today.
- Every ladder decision (skip / delta / full / ineligible-fallback) logs a structured reason (§5.3), so the decision itself is auditable, not just its outcome.
- One new `EvaluateHook` policy row: in `BootModeRepoImage` with `workspaceMoved`, prefer the delta script over full `setup.sh` when eligible per the predicate above.
- Shares `HookTimeout` — the delta script replaces `setup.sh` within the same phase, no new timeout constant.
- The delta-script contract is documented in the environments docs beside the `setup.sh` idempotency contract §19.4 already requires.

### 19.7 Interaction with scoped Environments

Complementary, not competing: under exact-SHA images, a `repo_image` boot with a *changed* scope was rare (it needed an exact SHA re-hit across a scope change). Under shared, tip-tracking images, the baked workspace is scope-matched *never* — shared images are always built unscoped (§19.1), so every scoped session's `repo_image` boot depends on the sparse-checkout tail Step 29 already ships end-to-end (`applySparseCheckout`, `sync.go:222-232`, run after the pop, per repo, keyed on the session's own `Environment.PathScope`). This design adds no new work to that path — it makes it load-bearing for every scoped session rather than an edge case.

One gap this surfaces, not yet closed: `snapshot_restore` can restore a *scoped* session's snapshot into an *unscoped* config, and there is no `git sparse-checkout disable` branch anywhere in `gitclone` today for the reverse direction (an unscoped session syncing against sparse-checked-out on-disk state). Add the trivial `sparse-checkout disable` branch for the unscoped case as cheap hardening in the same Step that ships §19.3's fetch step — it removes a rule-discipline dependency ("always build shared images unscoped") that would otherwise need to hold forever without ever being checked.

### 19.8 A recorded invariant, not a Step: user-configurable environment variables

Narvi has no per-scope, user-configurable environment variable surface today — `SessionConfig` carries no user env map, hooks run with sandbox-agent's own inherited environment minus `NARVI_SESSION_CONFIG` (`supervisor.EnvWithout`, `hooks.go:141`, `env.go:21-37`), and `ImageSpec` carries nothing but `{Base, Repos, RuntimeVersion}`. Nothing here proposes building that surface — it belongs to a separate feature, if and when one is scoped. This is recorded now, before any such surface exists, purely because it interacts directly with this design's own fingerprint invariant:

**Decision rule for whenever such a surface is designed**: user-configurable environment variables must be either (a) session-boot-time injection only, never passed to `BuildImage`, or (b) if build-time parity is genuinely wanted, a canonical digest of the build-time environment must join the fingerprint inputs (§19.1) — never injected into a build silently. *(2026-08-06: that separate feature is now scoped — §27.1 builds the surface and adopts exactly rule (a), boot-time injection only, with the consequence for secret-requiring `setup.sh` builds named honestly in §27.8; §27.1's own name validation enforces the `NARVI_*` reservation below.)* The reason is structural, not stylistic: §19.1 keys images on content alone and shares one image across every Environment with the same repo set; a scope-bound env value baked into a shared image without joining the key would leak one scope's configured values into every other same-repo-set Environment's sandbox. A corollary for §19.4: `workspaceMoved` (SHA inequality) is a *complete* rerun predicate only while build inputs stay content-only — a build-affecting input invisible to any SHA (an env var change with no accompanying commit) would create a baked-versus-boot divergence that SHA equality alone could never detect. If build-time env vars are ever added, the §19.1 manifest is the natural carrier: bake an env digest beside `built_repo_shas`, and extend `workspaceMoved` to "SHA moved OR env-digest moved." Separately: the `NARVI_*` env-var namespace (already established — `NARVI_BOOT_MODE`, `NARVI_SESSION_CONFIG` carrying the sandbox's own plaintext bearer token, `boot/config.go:33-40`) must be reserved and excluded before any user-settable env surface ships, the same way `hooks.go:141` already excludes `NARVI_SESSION_CONFIG` from every hook's own environment today.

### 19.9 Phasing

- **Step 40** — sandbox-agent fetch-aware sync (§19.3): `gitclone/sync.go` fetch step + credential wiring, `checkoutBranch` remote-tracking preference, degrade policy; `domain/gitstate` fetch states/triggers; new `GitFetchStepTimeout`. Independently shippable and independently valuable — it also hardens today's exact-SHA `repo_image` boots against the local-branch-missing edge, with no other behavior change.
- **Step 41** — shared fingerprint + spawn-path simplification (§19.1): `imagebuild.Fingerprint` redefinition; `imageresolve.go`'s `resolveAndSetImage` drops the per-repo `ResolveBranchSHA` loop; `image_builds` migration adds `built_repo_shas`/`built_at` (existing rows dropped, not migrated in place — pure cache); `ports.ImageSpec` → `{Base, Repos{URL,SHA}, RuntimeVersion}`; build service bakes `/narvi/image-manifest.json` and full clones.
- **Step 42** — refresh pump + hook-policy change + hook diagnostics (§19.2, §19.4, §19.5): `app/imagebuild.Builder` freshness pump, claim-time SHA resolution, in-place `image_ref` swap, the new platform GitHub credential; `EvaluateHook`'s `workspaceMoved` policy and the §6.4 amendment (breaking-change marker); bounded hook-output-tail capture through the supervisor's existing `Stdout`/`Stderr` seam; per-hook rerun-duration telemetry; the `sparse-checkout disable` hardening (§19.7). New §9.3-class resilience scenarios: fetch-fail boot, stale-image boot, refresh-in-flight spawn, non-idempotent-setup boot.
- **Step 43** — graduated setup-rerun ladder (§19.6): scheduled once Step 42's rerun-duration telemetry shows full reruns materially eroding warm-boot latency, not shipped speculatively alongside it.

Stays as-is, unaffected by this design: stash/pop machinery, `CloneAll`'s fresh-clone path, the boot-dispatch shape (`runBootSequence`), the builder's claim/backoff/streak machinery, the separate `contract_drift` mechanism (§14.3; unification remains its own future work).

## 20. Builder epistemic pre-action check (new capability)

Problem this solves: code review (§8.2) catches mistakes after a PR exists, but nothing today checks whether a substantial build-turn action itself rests on a shaky premise *before* the agent takes it — the review side's own after-the-fact scrutiny has no analogue on the builder side, pre-hoc. Left unaddressed, a builder turn that quietly assumes something false about the codebase, the task, or its own prior steps can burn a full session before anyone finds out.

### 20.1 Devil's-advocate preamble
Before a non-trivial build-turn action (never a planning turn, §20.3), the turn prompt is preceded by a short devil's-advocate preamble: it asks the agent to consider, in order, whether anything about the action rests on an unverified assumption, contradicts something already observed in the session, or is otherwise worth a second look — using an explicit two-tier taxonomy:
- **MINOR** — worth a heads-up in the reply, not worth stopping for; the agent proceeds and says what it noticed.
- **STRONG** — worth stopping for; the agent surfaces the concern instead of acting, and waits for the user.

The taxonomy is deliberately biased toward proceeding: default to STRONG only when genuinely warranted, MINOR for everything else worth mentioning, and silence for anything that doesn't rise to either — an epistemic check that flags routine work as a concern trains users to ignore it, defeating the entire mechanism. This bias is stated explicitly in the preamble text itself, not left to the model's own judgment of "how cautious to be."

### 20.2 The structured signal is not optional
Unlike a prompt-only self-check (which produces nothing a query can ever answer — "did this actually fire, how often, was it right"), the agent's response to the preamble additionally emits a **structured, typed field** — `EpistemicOutcome`: `none/minor/strong` — persisted on the turn row, in the **same Step** that ships the preamble itself, never as later follow-up work. This is a non-negotiable part of Step 61, not a nice-to-have: without it, the false-alarm-rate question this feature exists to eventually answer (does STRONG fire too often to be useful, too rarely to matter) is simply unmeasurable — exactly the kind of zero-observability gap §5.3's "day one, not later" principle exists to prevent.

### 20.3 Plan-mode turns are excluded
A turn running under `plan_mode=true` never gets the devil's-advocate preamble — plan mode's own HITL approval step (§8.1) already is the human review of the proposed action before anything executes; injecting a second, independent caution mechanism into a turn a human is about to approve anyway would just be noise duplicating a gate that already exists. The preamble applies only to non-planning build turns.

### 20.4 Threading and defaults
The enable/disable flag follows exactly the same threading `plan_mode` already uses: a global `platform.Config` default plus an optional override field on `SessionConfig`/`TurnSpec`, resolved with the same precedence (session override wins when set, global default otherwise) — no new config-resolution mechanism. **Off by default.** The signal is collected purely for analytics while the feature is calibrated (§21's analytics rollups are the natural home for an eventual false-alarm-rate view); there is no UI prominence beyond a subtle indicator surfaced in the review view (§12.2 item 2, Step 82) once shipped.

### 20.5 Hard-gate is explicitly out of scope
A hard gate — blocking the turn outright on a STRONG outcome rather than just surfacing it — is not designed here and not scheduled. It becomes a candidate only if and when the structured signal's own telemetry (§20.2) shows STRONG firing on genuine misses at a rate that justifies the cost of interrupting a session outright; until that evidence exists, gating on an unvalidated signal would risk blocking correct work on a false alarm, which is a worse failure than the one this feature exists to catch.

### 20.6 Phasing
Step 61, Phase 5. Independent of every other new Phase 5 Step — it extends only the turn prompt-assembly path and the turn row schema.

## 21. Review verdict persistence, analytics, digest & automated approval (new capability)

Problem this solves: posted review verdicts (§8.2) leave no durable, queryable history — there is no data source an analytics view or a digest could read from, only whatever is currently visible on each PR. Worse, the auto-approval mechanism §8.2/§16.1 originally specified is label-driven (`review: low risk`, posted by a human before anything automated can happen) — still a per-PR human bottleneck, exactly the serial chokepoint the decision inbox (§16) exists to relieve everywhere else. This section fixes both: an append-only verdict history feeding analytics and a deterministic digest (§21.1, §21.3), and a fully automated, criteria-driven replacement for the label gate (§21.2) that supersedes part of §8.2/§16.1's original design.

### 21.1 Verdict persistence & analytics read model
Every posted verdict (§8.2/Step 47's structured `domain/review` type, §8.2/Step 45) appends one row to `review_verdicts` — **append-only, one row per post**, never an update-in-place; the structured type means this is pure storage, never re-parsing anything out of posted comment text (the domain object already exists before it is posted). The latest verdict per PR is a `DISTINCT ON (repo, pr_number) ... ORDER BY created_at DESC` reduction (the same idiom already used for `sandbox_history`, §5.1) — no correlated subquery, no second "current verdict" table to keep in sync by convention.

Every row also carries **`head_sha`**: the commit the verdict was actually produced against — the same SHA the review session's own pre-fetched diff (§8.2/Step 46) was already anchored to, so this is forwarding a value already in hand, not deriving a new one. This table is not yet built (Step 62), so the column costs nothing to add now; deferring it would mean a later migration plus a backfill for which no truth exists — nobody can recover, after the fact, which SHA a historical verdict actually examined. With it, verdict staleness becomes a **queryable fact** rather than an inference: the decision inbox (§16) can show how many commits have landed on a PR since its latest verdict was issued, by comparing this column against the PR's current head. §21.2 below states the auto-approval hazard this independently closes.

**Stacked PRs: review scope is the PR's own increment, never the cumulative stack diff.** When a PR carries GitHub's own stack context (§17.6) — today, only ever the origin+sentinel-fix pair §17 registers — a review verdict still covers exactly the diff against **that PR's own** base (its immediate parent in the stack, not the stack's ultimate base), with position, size, and the stack's ultimate base supplied to the review only as context, never as additional diff to verdict over. This falls directly out of `head_sha`'s own design above: a verdict is pinned to one commit of one PR, and a verdict computed over a cumulative multi-PR diff could not be honestly attributed to any single `head_sha`, nor tracked for staleness the same way — a change to a PR below it in the stack would invalidate a cumulative verdict with no mechanism here to detect that and re-trigger it. The accepted residual: a defect that only manifests in composition with the not-yet-merged PR(s) below a given PR in the stack may go unreported at this review level — the same class of gap §15's aggregate-diff review exists to catch for merged, released work, never for an open, in-progress stack.

Every query against this history is **bounded from day one** — an explicit active/recent window (configurable, default matching the decision inbox's own staleness window, §16.1), never an unbounded scan; an unbounded "all PRs ever needing review" query is a design mistake to avoid at the schema stage, not a scaling problem to fix later.

Analytics rollups (timeseries, top-risk-driver breakdown, the "Review finding outcomes" KPI, §12.2 item 6) read from this same history. Any rollup not yet computed for a given window returns an explicit **"not yet computed" sentinel**, distinct from a real zero — a repo with a real 0% dismiss rate and a repo with no data yet must never render identically.

### 21.2 Automated approval: eligibility engine + calibrated auto-merge
This section **supersedes** the label-driven auto-approval mechanism §8.2 and §16.1 originally specified (`review: low risk` as the trigger a human posts to approve a PR for auto-merge). That design was a per-PR human bottleneck — it still required a person to read the PR and apply the label before anything automated could happen, exactly the serial-human chokepoint the decision inbox (§16) exists to relieve elsewhere. The replacement is fully automated from day one, in two decoupled stages:

**Stage 1 — auto-approval (always on).** A PR becomes auto-approved when `Shippable == auto` (Step 45's server-computed field — never the model's self-report) **and** every one of a deterministic, server-checked eligibility list holds:
- CI green at head.
- No floor raised: neither the coverage floor nor the premise floor (§8.2/Step 45's `max(rank)` composition) is above its baseline.
- Diff size under a configurable-per-repo threshold.
- No sensitive path touched — a configurable-per-repo list (migrations, auth code, `/contracts` by default, extensible per repo).
- The verdict being relied on was produced against the PR's CURRENT head SHA (`review_verdicts.head_sha`, §21.1) — a verdict computed against an earlier commit is stale by definition and must never itself satisfy eligibility, no matter how low-risk it once looked. This closes an auto-approval hazard independent of everything else in this list: approving on the strength of a verdict that examined DIFFERENT code. §24's automatic re-review, when a repo opts in, keeps this current proactively — but this check holds on its own regardless of whether that automation is enabled for the repo at all.

No per-PR human label is required or consulted for this decision — the LLM's verdict only ever *proposes* `Shippable`; the server recomputes it and checks every criterion above independently, the same "never trust the model's own verdict" discipline §18.1's `FallbackReason` and `domain/sandbox`'s decision functions already apply elsewhere. The existing `review: low risk` label **inverts into an escape hatch**: replaced by `review: needs-human`, which forces a specific PR out of auto-approval regardless of what the criteria say — a maintainer who knows something the criteria can't see still has a lever, just an opt-out one instead of an opt-in gate. (`visual-qa: pass/skip` is unrelated to this change and continues exactly as before.)

**Stage 2 — auto-merge (per-repo toggle, off by default).** Auto-approval alone does not merge anything. While a repo's auto-merge toggle is off (the default, and the state every repo starts in during a calibration period), an auto-approved PR surfaces in the decision inbox (§16.1's `ready_to_merge` row) as "ready to merge (auto)" with a 1-click human confirm — the human step moves from "decide if this is low-risk" to "confirm the machine's decision," a materially cheaper ask, but not a removed one. Every auto-approval outcome — confirmed as-is, or contested (a human overrides/requests changes on a PR the engine approved) — accumulates into a **contradiction-rate read model**: the fraction of auto-approved PRs a human later disagreed with, per repo. An admin arms the auto-merge toggle for a repo only once this data justifies it; the toggle's own settings row displays the reliability stats (§12.2 item 5, Step 84) next to the control, so arming it is an informed decision, not a leap of faith.

Once armed, auto-merge reuses the decision inbox's **existing** server-side re-validation-at-click contract unchanged (§16.2, Step 60: re-check CI, approval state, `Authorize` before calling the SCM) — merging is simply machine-initiated instead of human-clicked, the same checks either way. This is a deliberate reuse, not a parallel merge path: the inbox's Merge endpoint was already built to never trust its own rendered queue as authority, exactly the property an unattended merge needs.

### 21.3 Deterministic digest
A daily digest is **entirely deterministic, never LLM-narrated** — it renders from the same `review_verdicts`/analytics read model above via a template, not a model call; a digest is a compliance/status artifact, and a fixed rendering is easier to trust and to test than a fresh narration every day. Scope is **per-repo/per-channel from day one**, reusing the decision inbox's own assignment logic (§16.2's identity-graph-backed provenance) rather than inventing a second, separate repo↔channel association mechanism — a person's digest shows what their own inbox would show, not a global fan-out. Sending is **claim-before-act per (date, channel)**: a `digest_send_state(date, channel)` row plus `SELECT ... FOR UPDATE SKIP LOCKED` (the same idiom §5.1 already uses for PR-mention coalescing) guarantees at-most-one send per channel per day even with concurrent ticks — no separate storage-layer serialization mechanism needed, Postgres already does this.

### 21.4 Phasing
Step 62, Phase 5, after Step 45 (verdict shape) and Step 47 (posting path) — designing the verdict schema once, before any of persistence/analytics/digest/auto-approval builds on it, avoids the parallel-reinvention trap a shared schema exists to prevent. UI: Settings → Analytics gains the review-risk section and the per-repo auto-merge toggle with calibration stats (§12.2 items 5-6, Step 84); the decision inbox's `ready_to_merge` row (§16.1, Step 85) gains the "(auto)" 1-click-confirm variant.

## 22. Learned false-positive patterns & rebuttal identity (new capability)

Problem this solves: two independent frictions compound in code review (§8.2) today. A finding's rebuttal is tracked by file:line, so an unrelated edit that merely shifts a line number makes an already-rebutted finding look brand new on the very next review pass, forcing a maintainer to re-argue the same dismissal indefinitely. And a maintainer who teaches a reviewer "that's not actually a problem in this repo" has no way to make it stick — the same false positive fires again on the next PR, because nothing captures what was learned. This section fixes both: content-based rebuttal identity, which applies to every review finding and not just the sentinel-auto-fix surface (§17) it's adjacent to (§22.1), and a repo-scoped table of maintainer-taught false-positive patterns with its own capture/injection/retirement lifecycle (§22.2-§22.4).

### 22.1 Rebuttal identity by content, not position
A finding's rebuttal (§12.2 item 2's Dismiss-with-rebuttal) is reconciled against the finding's own **persisted content** — a hash/text of the finding stored at the moment the verdict that raised it was posted (§8.2/Step 45's structured type already carries this data; storing it is not new capture, just retention) — **never by file:line alone**. A file:line-only identity breaks the moment a line shifts (an unrelated edit above it, a reformat) — the same finding then silently reads as a *new* one, and a human's already-given rebuttal is lost on the very next review pass. Content-based identity survives exactly the churn that makes file:line fragile.

### 22.2 Repo-scoped learned patterns
A repo-scoped table holds maintainer-taught false-positive descriptions: free-text patterns a maintainer teaches once, so the same class of non-issue doesn't need re-litigating on every subsequent review. Capture is via an explicit `false positive: <reason>` command on a PR thread, **dispatch-before-router** — it reuses the existing `Authorize` write-permission gate (§13.3, Step 39) directly, the same check any other state-changing actor command goes through, rather than inventing a parallel permission model for this one command.

**RBAC**: `maintainer+`, direct, immediate effect — no propose/validate intermediate flow for a `member` role. This maps the command onto §13.3's existing role matrix as-is (review verdict edits and re-triggers are already a maintainer+ action) rather than adding a new permission tier.

Upsert is **idempotent, keyed on the triggering comment id** — the same `ON CONFLICT`-with-lifecycle-preservation idiom already used for webhook dedupe (§5.1) and PR-mention coalescing, applied here so a redelivered webhook or a retried command can never double-insert the same pattern.

### 22.3 Advisory injection, never a filter
Learned patterns are injected into every review pass (first pass and re-review alike) as an **explicitly-untrusted, advisory content block** — "weigh this, verify independently, do not skip a legitimate finding on this basis alone" — following the same untrusted-external-content discipline §5.2 already requires of PR diffs. This is deliberately never a pre-filter that silently drops findings matching a pattern: a maintainer-taught pattern is a hint the reviewing agent must reason about, not a rule the pipeline blindly obeys — a wrong pattern (taught once, stale since) should be *outweighed* by a clearly legitimate finding, not used to suppress it outright.

### 22.4 Lifecycle ships in the same Step
Retire, hit-count, and an audit view for this table **ship in the same Step** as the capture mechanism — never a deferred follow-up. A learned-pattern table with no retirement path only ever grows, accumulating stale or wrong patterns with no mechanism to review or remove them; shipping capture without a lifecycle would create exactly that unreviewable, ever-growing state from day one.

### 22.5 Phasing
Step 63, Phase 5, after Step 47 (needs the verdict-posting path new patterns get weighed against) and Step 39 (`Authorize`, RBAC). UI: Settings → Environments gains false-positive pattern view/retire per repo (maintainer+, §12.2 item 5, Step 84); finding cards gain rebuttal history with the content-based finding-identity linkage (§12.2 item 2, Step 82).

## 23. Plan follow-up classification (amend vs answer) (new capability)

Problem this solves: the shipped plan-mode dispatch path (Steps 37-38) originally dispatched any reply that matched no approve/reject keyword as an ordinary build turn (`planMode: false`) while the plan was still `awaiting_approval` — silently starting a build against the *unapproved* plan's assumptions. An interim server-side gate closes the dangerous half of that gap deterministically: such a reply is held (nothing dispatched, honest clarification reply), and a `revise:`-prefixed reply creates a plan-revision turn. What the interim gate cannot do is understand an *unprefixed* natural reply — a clarifying question still gets the hold-and-clarify treatment even when its intent was obvious, and a revision request must remember the prefix. This section replaces the prefix requirement with a real amend-vs-answer classification, extending §8.1 (plan mode) and §18 (unified intent classifier); the deterministic `revise:` prefix stays as an override that bypasses classification entirely.

### 23.1 A new classifier surface
`plan_followup` is a new surface on the existing unified intent classifier (§18) — amend-vs-answer, alongside the classifier's other categories (review-vs-request, plan-vs-build, release-vs-feature, §18.6). Same never-throw contract, same confidence rubric (§18.1/§18.2) — no parallel classification mechanism invented for this one case. There is a **single call site**, gated on "a plan exists and is `awaiting_approval`" — the classifier is never invoked for this purpose outside that state.

### 23.2 Enforcement at the persisted-state layer
The classification result is persisted as an `answer_only` flag on the turn/message row and **consulted at the turn-creation chokepoint** before any dispatch decision is made — never by trusting the sandbox/runtime to self-enforce which mode a turn runs in. Concretely, this is `httpapi.createTurnLocked` (the single shared core every ingress path — REST, Slack, Linear, GitHub bot — already calls through), the same chokepoint the interim awaiting-plan gate (§23 intro) already occupies; a plan-save path runs too late to gate dispatch, since a turn is already created and dispatched by the time any plan-save logic runs. This is the same "Postgres single source of truth" discipline (§5.1, CLAUDE.md) applied to a new case: the state that governs dispatch lives in the database that chokepoint already checks, not in a runtime flag a client or sandbox could misreport.

### 23.3 Fail-open direction: wait for clarification, never silent dispatch
When the classifier fails or returns low confidence while a plan is `awaiting_approval`, the fallback is **wait-for-clarification** — nothing is dispatched, the plan stays `awaiting_approval`, and the reply is an honest prompt asking the user to approve, reject, or clarify — i.e. exactly the interim gate's own hold-and-clarify behavior, which remains the floor this feature can never fall below. A classifier failure therefore degrades to the pre-classifier experience, never past it: no build turn can fire against an unapproved plan under any failure mode, at the cost of occasionally asking a user to repeat themselves when the classifier was merely unconfident, not wrong.

### 23.4 Never a public surface
Like every internal classification surface, `plan_followup` is excluded from public routes **by construction** — private to `app/`, never registered on `httpapi`/`wshub` (§18.6, mirrored here as the second surface to follow this rule; it is not merely a convention to remember but a structural property of where the code lives).

### 23.5 Phasing
Step 64, Phase 5, after Step 36 (classifier) and Steps 37-38 (plan mode, the dispatch point this amends). No UI change — the effect is entirely in the dispatch/reply path.

## 24. Automatic re-review on new commits (new capability)

Problem this solves: review sessions (§8.2/Step 46) re-trigger only when a human deliberately applies the label or clicks the button — a maintainer has to notice a PR moved and act. Left as the only path, commits pushed after a verdict was posted (an addressed finding, an unrelated follow-up commit, a force-push) can sit unreviewed indefinitely; nobody is notified there is anything to notice. This section adds a second, automatic trigger alongside the existing manual one — never replacing it — driven by the PR's own commit history rather than a human remembering to look.

### 24.1 A new ingress lane, not a small extension
This is a genuinely new GitHub ingress lane, not a small addition to the existing one, and it is costed out as such. The existing GitHub ingress (Step 32, `internal/adapters/inbound/github`) handles exactly two webhook event types — `issue_comment` and `pull_request_review_comment` — and both exist to detect a PR @mention; neither carries, and neither is ever asked to carry, a new-commits ("push more code to this PR") signal. Nothing in this codebase today parses GitHub's `pull_request` event at all. Closing this gap costs three concrete things:
- **A new webhook event type**: `X-GitHub-Event: pull_request` with `action: "synchronize"` (GitHub's own name for "new commits landed on this PR's head"), parsed into a new payload shape carrying `repository.full_name`, `pull_request.number`, and `pull_request.head.sha` — none of which the existing `issueCommentPayload`/`pullRequestReviewCommentPayload` structs (payload.go) need, since neither carries a head SHA today. Every other `action` value this event type carries (`opened`, `closed`, `labeled`, …) is acknowledged and ignored, mirroring today's `action != "created"` gate for comments.
- **The same claim/release delivery handling the existing webhook toolkit already provides** (Step 31): claimed via `postgres.WebhookDeliveryStore.Claim(provider="github", deliveryID=X-GitHub-Delivery)` on the same route, the same atomic first-writer-wins dedupe every other GitHub event already gets; a delivery this handler claims but fails to process releases that claim via the same `Release` method handler.go already calls today on a parse failure, so a human-triggered GitHub redelivery can reprocess it — no new dedupe primitive invented.
- **Per-PR routing onto the existing coalescing identity, not a new mapping table.** `github_pr_sessions` (Step 32, keyed on `(repo_full_name, pr_number)`) already IS "the one review session for this PR." A `synchronize` event looks itself up there; no row, or a row with `session_id` still NULL (nobody has ever mentioned the bot on this PR), means there is no review session to re-trigger — acknowledged, untouched, exactly like today's "no mention → acknowledged 200, no session created" case for comments. A row with a session id is the PR this event debounces a re-review for.
- **A direct, actor-bypassing write of both the debounce timer and `pending_head_sha`, in one transaction.** Every existing named timer is armed exclusively via `armTimer`, an unexported `*Actor` method (`timerfired.go`) that requires an already-open, actor-owned `pgx.Tx`, and `command.go`'s `Command` sum type has exactly three members (`TimerFired`, `SandboxEvent`, `EnsureDispatched`) — none of which represents an inbound "new commits pushed" signal an HTTP-layer webhook handler could hand into the actor's mailbox. The `synchronize` handler therefore arms the timer the same way `coalesce.go` already writes `github_pr_sessions` directly today, bypassing the actor entirely: via the exported `postgres.TimerStore.Upsert`, in the SAME transaction as the `pending_head_sha` upsert (§24.2) into `github_pr_sessions`. Both commit atomically as one unit or neither does, so a crash between them cannot leave a pushed commit with no armed timer.

This event carries no comment body and no commenting actor at all, so the existing mention-detection step (doc.go's step 5: detecting whether the comment body actually mentions `Config.BotHandle`) does not apply here — routing is by `(repo, PR)` identity alone, never by text.

### 24.2 Trailing-edge debounce, via the existing timer primitive
A burst of pushes (a rebase, a sequence of small fixup commits) must review once, at the burst's own final head, after a quiet period — not once per push, and not the first push in the burst. This means debouncing on the **trailing** edge, not the leading one: leading-edge throttling marks the moment of the FIRST push and then silently drops every later push inside the window, which recreates exactly the problem this feature exists to solve (unreviewed commits) at the window's own edge — the very last, most current push in a burst would be the one never reviewed.

This is built entirely on the session timer mechanism §2 already establishes (`session_timers(session_id, name, fires_at)`), never a new timer subsystem: each `synchronize` event that resolves to a real review session re-arms one named timer on that session (`review_retrigger_debounce`, a new entry in the existing `name` list — itself documentation of examples, not a closed set) to `now() + ReviewRetriggerDebounce` (a new `platform/timeouts.go` entry, alongside every other named interval this codebase defines). Re-arming a named timer is already an upsert (`UNIQUE(session_id, name)`) — the SAME idiom every one of the 5 existing named timers already uses to re-arm on new activity — so a second push before the first debounce fires simply pushes `fires_at` further out; only the LAST push in a burst survives to actually fire. No new debounce mechanism is invented, but the ARM and the FIRE happen through two different paths, unlike the other 5 named timers: the ARM is a direct write from the `synchronize` webhook handler (§24.1's fourth cost item), not something delivered through the actor's mailbox, since nothing in `Command` (`command.go`) carries that inbound signal; only the timer's later FIRING travels through the existing timer pump (§2) into the actor as a `TimerFired` command exactly like every other named timer already works (§24.3).

`github_pr_sessions` (already the per-PR identity this feature routes through, §24.1) gains one new nullable column, `pending_head_sha`: the most recent `synchronize` event's own reported head SHA, upserted (overwritten, not appended) on every event for that PR. This is what the actor reads back when the debounce timer finally fires — the burst's own last-known head, not a value that has to be independently re-fetched from GitHub.

### 24.3 Trigger state is `review_verdicts.head_sha`, never a label
When `review_retrigger_debounce` fires and reaches the review session's actor (hydrating it on demand if idle, §2, exactly like any other timer-delivered command), the actor:
1. Re-reads the per-repo opt-in setting (§24.5) — if it cannot be read, or is off, the timer is simply dropped: no re-review, logged, fail closed.
2. Reads `github_pr_sessions.pending_head_sha` for this PR and the latest posted verdict for the same PR — the `DISTINCT ON (repo, pr_number) ... ORDER BY created_at DESC` reduction §21.1 already defines — and compares `pending_head_sha` against that verdict's own `head_sha` (§21.1's new column).
3. If they already match (a race where a manual re-trigger, or an earlier automatic one, already reviewed this exact SHA), there is nothing to do: clear `pending_head_sha`, delete the timer, done.
4. Otherwise (§24.6's budget check, below), enqueues a new turn on the review session, then clears `pending_head_sha` and deletes the `review_retrigger_debounce` timer — the same re-arm-or-delete contract every named-timer handler in this codebase already follows (`timerfired.go`'s own hard rule: a handler that leaves the claimed timer row untouched lets the claim window expire and silently redelivers the same `TimerFired` command forever). This step cannot literally be the actor calling `httpapi.CreateTurnForBot`, Step 46's own manual re-trigger path: `internal/app/sessionactor` cannot import `internal/adapters/inbound/httpapi` (httpapi already imports sessionactor throughout its bot/create/turn/plan files; the reverse would be a compile-time import cycle), and `createTurnLocked`, the function `CreateTurnForBot` itself wraps, is unexported besides. The actor instead inserts the turn directly via `a.stores.turn.Create` — the same store-level primitive `createTurnLocked` itself calls — inside the transaction this handler already has open, mirroring Step 46's manual path at the storage layer rather than calling through it. Whether the small amount of logic `createTurnLocked` wraps around that insert (the audit-log write, the awaiting-plan gate) needs duplicating here, or is inapplicable to a review session's automatic turn, is Step 65's own implementation decision, not resolved by this plan.

Trigger state is therefore a comparison of two Postgres columns (`github_pr_sessions.pending_head_sha` vs. `review_verdicts.head_sha`) — never a label read back off the pull request. §5.1 states why this matters as a general principle: a bot-written label is mutable by anyone with triage rights, forgeable, and a second copy of a fact Postgres already owns; this feature's own trigger state must live where every other piece of durable session state already lives.

### 24.4 Resolving the apparent contradiction with Step 46
Step 46 already specifies re-trigger "via label/button" — a human applying that label, or clicking that button, is a **deliberate, in-the-moment command**, identical in kind to clicking any other action button in this UI, and this section changes nothing about it: it stays exactly as specified, dispatches immediately, still `maintainer+` direct (§13.3). What §5.1's principle rejects is a DIFFERENT thing this section could have been tempted to build instead — the BOT itself writing a label back onto the PR (e.g. a hypothetical "stale, needs re-review" label) and then reading that same label back later as the durable signal that a re-review is owed. That would be state the bot wrote for itself to read, on a surface anyone with triage rights can edit or remove, duplicating a fact (§21.1's `head_sha`) Postgres already owns. The distinction is general, not specific to this feature: a label is a legitimate channel for a HUMAN's command, never a legitimate store for the SYSTEM's own memory.

### 24.5 Off by default, per-repo opt-in, fails closed
This entire feature is **off by default**, enabled per repository (Settings, admin-only — the same row as the auto-merge and sentinel-auto-fix toggles, §13.3), for the same reason those two are admin-gated: it changes what runs unattended on a repo's own PRs. If the setting cannot be read (a transient Postgres error, a missing row), the safe direction is treated as OFF — no re-review is triggered, logged, nothing retried; a repo that hasn't explicitly opted in never gets surprised by this running anyway. **This automation never auto-approves anything on its own** — it only ever enqueues an ordinary review turn through Step 46's existing dispatch; whether the resulting verdict lets a PR through auto-approval is entirely §21.2's own eligibility engine's decision, made independently, later, from the fresh verdict this produces.

### 24.6 Per-PR re-review budget
An automated fix session (§17, sentinel auto-fix, or any future automation that pushes commits) can itself trigger the very re-review this feature exists to provide, which can in turn flag something that triggers another automated fix — a loop with no natural end. A per-PR counter (`github_pr_sessions.auto_retrigger_count`, alongside `pending_head_sha`) bounds this: each time §24.3 step 4 actually enqueues an automatic re-review turn, the counter increments; a default budget (not given an explicit figure elsewhere in this plan — propose 10 per PR, configurable, mirroring how other new intervals in this plan are proposed) caps it. This budget governs ONLY the automatic path — a human's manual re-trigger (label/button, Step 46) is never subject to it and always works, regardless of how many automatic re-reviews already fired; the two tracks are independent by design, not merely by accident.

Once the counter reaches the budget, §24.3 step 4's "otherwise" branch stops enqueueing a turn: it still clears `pending_head_sha` (so a later manual re-trigger starts clean) and deletes the `review_retrigger_debounce` timer (the same re-arm-or-delete contract every named-timer handler follows, `timerfired.go`) but does not dispatch. The FIRST time this happens for a given PR, the review session additionally posts one server-side verdict-tool notice (§5.2 — never a raw comment) that automatic re-review has reached its budget and further pushes need the existing manual re-trigger — a one-time event, not repeated on every subsequent debounce firing, so hitting the ceiling is observable without becoming noise. Later `synchronize` events on that same PR keep re-arming the debounce timer exactly as before (a cheap upsert either way); each firing simply finds the budget still exhausted and no-ops without posting a second notice.

### 24.7 Phasing
Step 65, Phase 5, after Step 46 (the claim/coalescing primitives this extends with a second, automatic ingress lane) and Step 62 (`review_verdicts.head_sha`, this feature's own trigger-state source) — designing this after both means it reuses primitives that already exist rather than growing them in parallel. Gates nothing else in Phase 6/7. UI: the per-repo opt-in toggle ships in Settings → Analytics alongside the other per-repo automation toggles (§12.2 items 5-6, Step 84).

## 25. Configurable workflow engine per lane + visual canvas editor (new capability)

Problem this solves: today each of Narvi's three lanes (review, request/build, plan) is one fixed
prompt → one model → one turn. There is no way to express, per lane, a configurable sequence of
steps where each step names its own agent/model, carries its own prompt, and may gate on a human
approval (HITL) — the concrete motivating case is the request lane: Gemini Pro drafts the spec →
Claude Opus scaffolds → Claude Sonnet builds → GPT (via Codex) audits, with a bounded auto-fix
loop after the audit. This section specifies a backend engine exercised by 100% of production
traffic from day one — the three existing lanes become three non-deletable, `is_built_in` system
workflows, never a parallel opt-in path — plus a canvas-style visual editor (Phase 7) on top of it.

Decisions already made, not reopened here: full drag-and-drop canvas UI (§25.12); review/request/
plan become three system workflows an admin may duplicate and customize — globally or per repo,
never in place — never delete;
Gemini ships alongside Anthropic/OpenAI in v1 (§8.8/Step 59, amended); the backend engine lands in
Phase 5 right after the automations work (Steps 51-52), the canvas editor in Phase 7 right after
Settings (Step 84); a HITL "revise" verdict is always a re-execution of the same step with the
human's text folded in as an additional instruction — exactly plan mode's own `revise:` handling
today (§8.1) — never a direct substitution of a structured artifact; and the OpenCode
credential-injection gap (§25.3) is a blocking prerequisite for this entire chantier, built first,
not Gemini-specific scope.

### 25.1 Two findings independently verified in the existing code

Per-turn model selection already exists end-to-end, no new plumbing needed: `turns.model_id`
flows through `dispatch.go`'s `buildPromptPayload` (`Model: target.ModelID`, `dispatch.go:1493`)
into `sandboxws.Prompt.model`, and the OpenCode adapter's own `resolveModel`
(`internal/adapters/outbound/opencode/session.go`) does nothing more than `strings.Cut(raw, "/")`
into provider/model — there is no Narvi-side allowlist of providers or models anywhere on this
path. It is already a generic passthrough to any `provider/model` string OpenCode itself
recognizes.

But no credential is injected into the OpenCode process for ANY provider today.
`internal/sandboxagent/opencodeproc/spawn.go` starts `opencode serve` inheriting sandbox-agent's
own OS environment verbatim (`Env: supervisor.EnvWithout(boot.SessionConfigEnvVar)` — exactly one
variable excluded); no `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, or Google equivalent is wired
anywhere in this codebase. This is the actual, provider-agnostic blocking gap — not a
Gemini-specific cost — and closing it (§25.3) is a prerequisite for per-step model override to do
anything beyond what the zero-config path (§25.6) already does today.

### 25.2 Provider catalog: RESOLVED for Gemini, verified against the live binary

Amends §8.8/Step 59 ("models"). Verified 2026-08-03 directly against the pinned OpenCode 1.17.15
binary's own `GET /provider` catalog — a 4.5MB response, deliberately fetched by bypassing `rtk`
(whose own output-truncation would otherwise silently drop the payload this finding depends on).
The catalog already lists a `google` provider (`env: GOOGLE_API_KEY, GOOGLE_GENERATIVE_AI_API_KEY,
GEMINI_API_KEY`) alongside `google-vertex`/`google-vertex-anthropic` (both keyed on
`GOOGLE_VERTEX_PROJECT`/`GOOGLE_VERTEX_LOCATION`/`GOOGLE_APPLICATION_CREDENTIALS`), with 41 real
Gemini models each — `gemini-3.5-flash` checked directly: `capabilities.toolcall: true`, a
1,048,576-token context window, and real cost data (`input: 1.5, output: 9, cache.read: 0.15`). No
new `AgentRuntime` adapter is needed for Gemini — the small-scenario applies exactly as §7's
anti-corruption layer already generalizes. The one remaining gap is credential injection (§25.3),
which blocks every provider today, not Gemini specifically. A new CI contract test is still
required — mirroring §7's existing pinned-binary contract-test discipline — verifying Gemini's
actual tool-calling/streaming quality through OpenCode, since every existing contract test is
Claude-backed and proves nothing transitively about a different provider's behavior.

### 25.3 Provider credential injection (Step 53) — the blocking prerequisite

Generic secret injection into `spawn.go`'s `cmd.Env` for the OpenCode process closes the gap named
in §25.1 for every provider at once. `ActionManageRepoSecrets`/`ActionManageEnvSecrets`/
`ActionManageGlobalSecrets` (`internal/domain/authz/authorize.go`) already reserve the RBAC row for
exactly this class of data — but grepped directly, no secret-storage table exists anywhere in this
codebase yet (no `secret` column, table, or store outside one unrelated magic-link bearer token,
`migrations/000036`). This Step is the first one that actually has to build it — the same
"reserved in the RBAC/design vocabulary, not actually built" gap migration 000045's own comment
already found and named, for a different mechanism (`parent_session_id`/`spawn_depth`). Scope here
is deliberately narrow: provider API keys only, mapped provider→env-var name
(`GOOGLE_API_KEY`/`GOOGLE_GENERATIVE_AI_API_KEY`/`GEMINI_API_KEY` for `google`, `ANTHROPIC_API_KEY`,
`OPENAI_API_KEY`), sourced per-repo/per-environment/global exactly like the RBAC matrix already
anticipates. No SESSION_CONFIG change and no `AgentRuntime`/port change — this is entirely inside
`sandbox-agent`'s own process-spawn path.

### 25.4 Domain model — `internal/domain/workflow` (Step 54)

Pure, no I/O, same discipline as every other `/internal/domain` package (CLAUDE.md, §11):
- `Lane` — a closed enum (`review`/`request`/`plan`) and `LaneFor(target, mode)`, a pure mapping
  over the classifier's own existing vocabulary (`intent.TargetReview`/`TargetRequest`,
  `intent.ModePlan`/`ModeBuild`, `internal/domain/intent/rubric.go`) — not a new vocabulary
  invented alongside it.
- `WorkflowDefinition{ID, Lane, Name, IsBuiltIn, Version, Steps}`, `StepDefinition{ID, Order, Kind,
  ModelID *string, PromptTemplate, ExecutionScope (same_session|child_session),
  ConversationContinuity (continue|fresh), HITLBefore/HITLAfter, Edges}`.
- `StepOutcomeStatus` — a closed 3-value enum (`ok`/`needs_fix`/`blocked`), the same discipline
  `review.Shippable` already establishes for its own 3-value enum. This is a **distinct axis from
  `Shippable`** — an edge never conditions on anything but this fixed vocabulary, and `Shippable`
  (§21.2) is never routed through it (§25.8 states why).
- `Edge{FromStepID, OnStatus, ToStepID}`: with no explicit edge, `ok` advances to the next step in
  `Order`; `needs_fix`/`blocked` escalate by default (fail-conservative) — a retry loop must be
  wired explicitly, never implied.
- `NextStep(...)` — one pure decision function, the same shape as `turn.Transition`/
  `plan.Transition`/`sandbox.Transition` (`internal/domain/turn/state.go:167`,
  `internal/domain/plan/plan.go:118`, `internal/domain/sandbox/state.go:375`).

Why the three built-in workflows are rows, not Go constants: the "duplicate and customize"
requirement and the canvas editor both need the default to exist in exactly the same shape as a
custom workflow. All three are seeded `is_built_in = true` directly in the migration. `PUT`/
`DELETE` on an `is_built_in = true` row is refused unconditionally — a structural invariant, not an
RBAC rule.

**System template, global binding, and repo override are three distinct concepts, not three rungs
of one fallback ladder:**

- **System template** — a `WorkflowDefinition` row with `IsBuiltIn = true`. Read-only starting
  content; never itself a live setting; the only thing "system" means.
- **Global binding** — a `workflow_bindings` row keyed `(lane, repo_full_name = NULL)`. Exactly one
  per lane, seeded by the migration to point at that lane's system template — but from that point on
  it is an ordinary, independently-repointable setting, not a fallback anyone "reaches." Because it
  is seeded for every lane, this row is **never absent** — there is no "no binding configured" state
  to fail open or closed on.
- **Repo override** — a `workflow_bindings` row keyed `(lane, repo_full_name = '<owner>/<repo>')`.
  Optional, and shadows the global binding for that one repo only.

Resolution: look up `workflow_bindings` for `(lane, repo_full_name)`; if a repo-specific row exists,
use it; otherwise use the `(lane, NULL)` global row. The global row is guaranteed to exist by the
seed migration, so this is a two-step lookup with a guaranteed second step, never an "absent row →
default" fail-open branch — `workflow_bindings` has no row that resolves to nothing.

### 25.5 Circuit breaker — `internal/domain/loopguard` (Step 54)

A second new pure package: `Evaluate(State{AttemptCount}, Config{MaxAttempts}) Decision
{ShouldProceed, ShouldEscalate}` — no time window. Iteration count is read back via `COUNT(*)` on
`workflow_step_runs`, never a dedicated counter column — the same "derive it from the rows that
already exist" discipline `review_verdicts`' own `DISTINCT ON` reduction (§21.1) already applies.

### 25.6 Execution model: turns, sessions, and conversation continuity (Step 55)

By default, every step is an ordinary sequential turn on the SAME `sessions` row, dispatched
through the existing `createTurnLocked`/`CreateTurnCore` (`internal/adapters/inbound/httpapi/
turn.go:309,373`) — no new wire command, no `AgentRuntime` change. The "what happens next"
decision hooks into the sandbox-event handler this codebase already has
(`internal/app/sessionactor/sandboxevent.go:223`, `handleSandboxEvent`).

A child session is used only when real isolation is needed. `parent_session_id`/`spawn_depth`
(`migrations/000045_sessions_child_sessions.up.sql`) already exist — but note precisely what that
migration's own comment says: `spawn_depth` is recorded as observability data, not gated on
numerically; the actual "no recursion" invariant is enforced via `provenance_tag` (a
sentinel-auto-fix child session is never itself eligible to trigger another), not a depth-counter
check. The workflow engine's fix-step child session follows the SAME restriction discipline Step
48 already established (`SpawnSentinelFixChildSession`), never a numeric-depth mechanism this
codebase doesn't actually have — and is reserved for the audit-fix loop's fix step alone, never the
audit step itself.

"Fresh context" is not "a new session": `AgentRuntime.StartTurn` already branches on whether
`cmd.ConversationId` is nil to start a new OpenCode conversation inside the same sandbox
(`internal/app/ports/agentruntime.go:79`). A step that must not inherit the full chat history of
earlier steps uses `ConversationContinuity: fresh` on the SAME session, not a child session.

Typed handoff between steps, never re-parsed free text — the same discipline
`review.RenderTurnPrompt` already applies (`internal/domain/review/context.go:234`). A new,
generic step-outcome-posting tool, structurally identical to the existing verdict-posting tool
(`internal/domain/reviewpost`): `{status: ok|needs_fix|blocked, summary (advisory, never
re-parsed), structuredPayload: json.RawMessage}`. For the audit step specifically, this schema
reuses `review.Verdict` + `reviewpost.Finding[]` in full rather than reinventing them.

Concurrency: the common case (same session) hangs off the transaction the session actor already
has open. A step running in a child session is observed by a DIFFERENT actor than the one owning
the `WorkflowRun` — exactly the situation `sentinel_fixes`/`review_findings` already solve today,
via guarded `UPDATE ... WHERE status = X` writes, never the epoch-fencing mechanism.

### 25.7 Per-step model/provider binding

Confirmed by direct code reading: this is already a parameter within one existing call, not a
second port. `StepDefinition.ModelID` reuses the `provider/model` convention already in place
(§25.1). No new port, no new Narvi-side registry, for any provider OpenCode itself already
recognizes. The `LLM` port (`internal/app/ports/llm.go`) is unrelated — it serves only the
classifier and the review's single-completion calls, never an agentic turn with tool use.

### 25.8 The three built-in workflows, and the override example

- **review**: one step, `ModelID: nil`, prompt = today's unchanged text, no HITL. `Shippable`
  (§21.2) stays a separate axis, consumed after the step completes by the existing auto-approval
  machinery — never routed through `StepOutcomeStatus`.
- **plan**: two steps (plan → build), HITL after step 1 reusing `ApproveKeywords`/`RejectKeywords`/
  `RevisePrefix` unchanged (`internal/domain/plan/verdict.go`), a `needs_fix → same step` loop
  explicitly exempted from the circuit breaker.
- **request**: one step, passthrough, no behavior change.

The Gemini→Opus→Sonnet→Codex example is a non-built-in workflow bound as the **global** Request-lane
binding (`workflow_bindings` row keyed `(request, NULL)`) — it demonstrates that a global binding is
a real, repointable setting, not a fourth built-in default. A repo may still shadow it locally: a
scoped-Environment repo with no backend to audit against can bind a lighter, repo-specific override
(e.g. a two-step prototype flow with no audit step) via its own `(request, '<repo>')` row, which
shadows the global one for that repo alone.

### 25.9 HITL gate (Step 56)

Reuses plan mode's own cross-channel delivery mechanism (Slack/Linear/GitHub/web), not its domain
package: new `NotificationKind` values extending `planslacknotifier.go`/`linearnotifier.go` exactly
as this codebase's own precedent already does twice (`cmd/control-plane/main.go`'s notifier
routing map). Three verdicts: approve (continue), reject (end the run), revise (human text →
always a re-execution of the same step with the text as an extra instruction, never a direct
substitution of a structured artifact). GitHub: a new deterministic `EditPrefix` keyword, the same
strict, never-substring matching discipline as `plan.MatchVerdict`/`MatchRevise`
(`internal/domain/plan/verdict.go:49,121`). Web endpoint: `POST /api/workflow-runs/:runId/steps/
:stepRunId/decide`, the same shape as `decideplan.go`. Human-revision loops are exempt from the
circuit breaker, mirroring §24.6's own exemption of manual re-triggers.

The auto-fix loop itself needs no separate loop mechanism: `Edge{audit, needs_fix, fix}`,
`Edge{fix, ok, audit}` — two ordinary edges `NextStep` already evaluates. `loopguard.Evaluate` is
consulted only when the `audit → fix` edge is about to re-fire; on escalate,
`WorkflowRun.Status = needs_review`, one notice (never repeated, like §24.6), stop. This never
touches or reuses `sentinel_fixes`/`SpawnSentinelFixChildSession` (Step 48) — the fix step here is
structurally parallel to it, never a caller of it.

### 25.10 Wire contracts

New schema-first entities in `contracts/rest/v1/dtos.schema.json`: `WorkflowDefinition`/
`WorkflowStepDefinition`/`Edges`, `WorkflowBinding`, `WorkflowRun`/`WorkflowStepRun` (read-only),
`WorkflowStepDecideRequest`/`Response`. An optional `canvasPosition {x, y}`, opaque server-side. No
SESSION_CONFIG change, no `AgentRuntime` change.

### 25.11 RBAC

Three new actions, each mirroring an existing row in `internal/domain/authz/authorize.go`:
- `ActionManageWorkflowDefinitions` — maintainer+ (same row as `ActionManageAutomations`):
  create/edit an unbound draft.
- `ActionActivateWorkflowBinding` — admin-only (same row as `ActionActivatePromptTemplate`): bind
  `(repo, lane)` to a specific definition — `repo` may be a specific repository or the global
  (org-wide, `repo_full_name = NULL`) scope; the same action gates both.
- `ActionDecideWorkflowStep` — own/joined-aware (same row as `ActionApprovePlan`).

`is_built_in` immutability is a structural invariant, not an RBAC row (§25.4).

### 25.12 Visual canvas editor (Step 86, Phase 7)

A React Flow-style node/edge canvas for authoring a lane/repo workflow's steps and edges. It must
validate/constrain what a user can draw against the engine's closed model — ordered steps plus
3-status edges, no expression language (§25.4) — rejecting an undrawable-by-the-engine graph at
save time, not silently accepting it. Inline progress display of a running workflow in the session
view is a SMALL extension of the already-planned sub-task-lane rendering (§7.1, Steps 80/81) — not
a separate Step.

### 25.13 Risks and open questions

- **Canvas-vs-engine expressivity mismatch**: the editor is a general node/edge canvas; the
  runtime supports only ordered steps + a closed 3-status-edge model, no expression language
  (§25.12).
- **Per-step cost attribution**: does not exist yet. §7.1 already names the adjacent gap as debt:
  per-model cost attribution when a sub-task runs on a different model than its turn "is not
  designed here ... left to whichever future work actually adds it." This chantier inherits, and
  must close, the equivalent gap at the workflow-step level.
- **Decision inbox** (§16, Step 60/78) is not extended by this chantier.
- **`LaneFor` must inherit the classifier's fail-open discipline**: `IsActive` defaults every
  surface to shadow when unconfigured (§18.5) — `LaneFor` must default the same way rather than
  block dispatch on an unresolved lane.
- **Multi-provider streaming/cost/error-taxonomy parity** if Gemini runs through OpenCode is an
  untested hypothesis, not yet validated end-to-end.

### 25.14 Phasing

Steps 53-56, Phase 5, immediately after Step 52 (automations: triggers & extras) — see the Phase 5
renumbering note (IMPLEMENTATION_PLAN.md). 53 is a blocking prerequisite for 54-56; 55 is exercised
by 100% of production traffic from day one. Step 86, Phase 7, immediately before ui finalize (Step
87) — see the Phase 7 renumbering note.

## 26. Review as a merge readout (new capability)

**Context.** When agents author most of the code under review, line-by-line human reading stops
being the bottleneck — the merge **decision** is: merge or not, on what basis. This section
restructures the review verdict from "a findings list with a badge" into a **merge readout** —
named for an instrument's readout: not the raw telemetry, but the synthesized display an operator
reads before a go/no-go call. Here the instrument is the review pipeline, the operator is whoever
decides the merge, and the readout states: what the PR does, which architecture choices it makes,
what it risks in the surrounding stack, and whether its own description tells the truth. Code
findings remain — they are the raw telemetry — demoted to supporting evidence in a collapsed
appendix. Two anchors already exist in the shipped design: `PremiseState` (Step 45)
is the embryo of exactly this posture — "should this PR exist?" — and this section grows it into
the full readout; and every review trigger path (mention, label/button, automatic re-review §24,
release detection §15) already converges on one funnel — review-session creation and dispatch
(Step 46) — where model, effort and prompt are chosen and the inline pre-fetched diff's stats are
already known. That funnel, not the intent classifier, is review's real router (§18 decides
review-vs-request at ingress; everything after that converges here), and it is where the two-path
triage below inserts.

Four Steps (66-69, end of Phase 5). Steps 66-67 deliver the paradigm's core without waiting for
any multi-agent machinery; Step 68 only pays off once the verdict's content has changed; Step 69
rides the deep path 68 creates. All four build on the merged verdict foundation (Steps 45-47) and
on the persistence/analytics instrument (§21/Step 62) — the instrument that will measure whether
the paradigm shift actually operates.

### 26.1 The digest: architecture & risk readout (Step 66)

The rendered verdict is restructured to front-load the decision:

1. **Header** (unchanged): risk badge + why-line + shippable class (Steps 45/47).
2. **"What this PR does"** — 2-4 sentences written **from the diff**, never copied from the PR
   description. The readout's keystone: simultaneously the human's summary, the reference text for
   the adequacy check (§26.2), and the per-PR headline the decision inbox (§16) and deterministic
   digest (§21.3) surface.
3. **"Architecture choices"** — each structural decision the diff makes: what was decided, the
   alternative implicitly rejected, and conformance to the repo's own conventions (its agent
   instructions file, its established patterns).
4. **"Risks to the stack"** — blast radius in the existing fixed vocabulary (`BlastRadius []Tag`,
   Step 45); coupling and deployment risks (migrations, multi-phase deploys, image rebuilds);
   reversibility; and — explicitly — what was **not** verified (honest limits).
5. **Collapsed appendix**: findings, coverage, docs-drift, worth-a-look — retained intact, demoted
   to supporting evidence.

**Typed fields, never markers.** All of this rides the verdict-posting tool's structured payload
(Step 47): `Digest{Summary, ArchDecisions []ArchDecision{Decision, RejectedAlternative,
ConventionConformance}, StackRisks, UnverifiedLimits}`. Nothing is ever parsed back out of posted
markdown (Step 45's invariant); rendering is server-side from the typed fields (`reviewpost`),
like every other verdict element. The digest columns ride the append-only `review_verdicts`
history (Step 62), so digest quality is measurable from day one like everything else.

**Enforcement.** `Digest.Summary` is required on every review from Step 66 on (the adequacy check
and the inbox headline depend on it). The full digest (architecture choices + stack risks) becomes
schema-**required** on the deep path once §26.3 defines it: the posting endpoint rejects a
deep-path verdict without it with a structured reason and the agent re-submits — the same
reject-don't-repair posture the endpoint already applies to invalid payloads — and a deep-path
verdict whose digest is semantically empty raises the `Shippable` floor (§26.2's composition). The
light path requests the full digest but does not hard-require it.

### 26.2 Description adequacy: does the PR tell the truth? (Step 67)

Confirmed gap: a PR's title and body enter review context only as untrusted input blocks (§5.2) —
nothing checks them against the diff. Closing it:

- The agent compares its own diff-derived `Digest.Summary` (§26.1) against title + body and emits
  a typed tri-state on the verdict: `DescriptionAdequacy: ok|drift|misleading`, plus a one-line
  explanation. The description remains untrusted *input* — the comparison consumes it, never obeys
  it.
- **A third raise-only floor.** `misleading` floors `Shippable` at `needs_human`, composing with
  the coverage and premise floors via the existing `max(rank)` (Step 45's
  exactly-one-pure-function-per-floor pattern — this adds the third). Deliberate divergence from
  also inflating `RiskLevel`: the server computes `Shippable`; it never fabricates risk the model
  did not report (Step 45's server-computed-only rule cuts both ways).
- **Graduated remediation.** On PRs authored by Narvi's own sessions (the session→PR linkage
  already records authorship), the agent may rewrite the PR **body** behind a per-repo
  `descriptionAutofix` flag — **default off** — preserving the original in a collapsed block. The
  write is delivered via a `SourceControl` port extension + the outbox (§5.1: every outbound side
  effect), with the Narvi-authorship and flag checks enforced server-side at delivery time (§5.2:
  never prompt-only) — never an in-sandbox `gh pr edit`. On human-authored PRs: a proposed body
  rendered in the digest, never a write. The **title is never rewritten automatically**, in either
  case.

### 26.3 Two-path triage: light and deep (Step 68)

The depth decision is made in the single funnel (Step 46's creation/dispatch path),
**deterministic-first** — the same posture as everywhere else in this system (the server does not
trust agent judgment for routing; deterministic fallbacks throughout, §18):

- **Signals**: additions+deletions and changed-file count (already fetched with the inline diff,
  Step 46); the changed **paths**, promoted to a first-class structured signal; sensitive globs
  (migrations, auth surfaces, infra-as-code, CI workflows) mapped deterministically onto the same
  `BlastRadius` tags the verdict uses; cross-cutting dispersion (number of distinct top-level path
  roots); provenance (Narvi-authored vs human, and the authoring model); the PR's own verdict
  history (Step 62 — a prior `high` verdict routes deep); existing risk labels.
- **v1 rules** (initial thresholds, per-repo-tunable): any sensitive-glob hit → always deep; >600
  changed lines or ≥3 distinct top-level path roots → deep; otherwise light. **No LLM tie-break in
  v1** — a `review_depth` surface on the unified classifier (§18) remains a v2 option only if
  per-path analytics show a real grey zone (the classifier consumes free text today, so this would
  be new surface area, not a config flip).
- **Output**: `reviewDepth: light|deep`, threaded into review-session creation; recorded on the
  routing decision record (§18.4's precedent); persisted as `review_path` on the verdict row (Step
  62) so **cost and precision become measurable per path**. Depth drives model/effort through the
  existing dedicated review-model selection (§8 item 2): light = balanced tier, deep = frontier
  tier + high effort. Depth composes with cross-family counter-review (§26.4): the family comes
  from provenance, the tier from depth.
- **Per-repo config**: `reviewDepth: {mode: auto|always_light|always_deep, deepPaths: [...]}`
  alongside the other per-repo review settings. **Any triage error fails open to light** — a
  review must never be blocked by its own router.
- **Re-review on push** (§24): depth re-evaluated on the delta, but floored at the PR's previous
  depth — once deep, a PR stays deep.

### 26.4 The deep path: adversarial counter-review (Step 69)

**One sandbox.** The primary reviewer orchestrates context-isolated sub-agents via the engine's
own sub-task fan-out (§7.1 — already shipped in Step 17: engine-native sub-agents, `subTaskId`
tagging, flat, cost rolls up). Explicitly **not** N parallel sandboxes (coalescing complexity and
N× boot cost with no real independence gain — each sub-agent already has a clean context) and
**not** Narvi child sessions (§14.4's materially heavier mechanism, the wrong tool here).

- **`architecture-scribe`** (read-only): produces the architecture-decision recap from the diff +
  repo conventions in a virgin context, uncontaminated by the primary's finding hunt.
- **`counter-reviewer`** (read-only, adversarial): receives the primary's findings + digest and
  attempts to **refute** each finding and to surface what was missed. With provider credentials
  injectable (Step 53) and the model-catalog work (Step 59), it can be pinned to an opposing model
  family via the engine's own per-sub-agent model selection — family opposed to the PR's authoring
  model, tier from depth (§26.3).
- **Synthesis**: only findings surviving counter-review are published. Inter-agent disagreements
  surface in the digest as a **"Contested points"** section — agent disagreement is precisely the
  signal that a human must decide.
- **Structural enforcement.** The control plane cannot observe the sandbox's internals, so the
  verdict payload carries a typed `CounterReview: done|skipped` field, schema-required on the deep
  path (rejected if absent — §26.1's reject-don't-repair posture); `skipped` raises the
  `Shippable` floor to `needs_human`. A typed field, never a marker parsed from markdown (Step
  45's invariant, once more).

### 26.5 Measuring the readout (Step 69, on Step 62's instrument)

- **Per-section digest feedback** extends the finding-outcome read model (§21.1): contest/confirm
  per digest section, plus a maintainer command `arch recap wrong: <reason>` mirroring Step 63's
  `false positive:` command exactly — maintainer+ via the existing `Authorize` gate,
  deterministically routable (§5.2), idempotent on the triggering comment id. The recap itself
  becomes measurable and correctable.
- **KPIs** (Step 62 analytics + §12.2): digest precision (contestation rate); decision latency
  (verdict → approve — already a §16 KPI, now attributable per review path); cost per path; and
  the paradigm's proxy metric: **% of PRs approved with zero human inline comments** — the number
  that says whether the shift is actually operating.
- The §21.3 deterministic digest and the §16 decision inbox surface the readout's `Summary` line
  per PR — reusing their existing aggregation, no new mechanism.
- **Evals**: known-PR digest-quality cases (expected architecture decisions on reference diffs,
  seeded description-drift cases) join the shadow-precision discipline the Phase 5 milestone
  already requires.

### 26.6 Interplay with the workflow engine (§25)

The readout lives **inside** the review lane's single workflow step (§25.8's built-in review
workflow is one step, and stays one step); the deep path's counter-review is sub-task
orchestration *within* that step (§7.1), not workflow-engine edges. Decomposing review into
engine-visible steps (scribe → find → counter-review → synthesize) is a possible §25 v2 once both
systems are stable — explicitly not v1 scope, mirroring §25's own decision not to retrofit the
sentinel auto-fix onto the engine.

### 26.7 Decided defaults and v1 non-goals

Defaults (decided; thresholds tunable on per-path analytics): description autofix =
apply-behind-flag, per-repo, default off, Narvi-authored PRs only, body only; triage v1 = pure
deterministic, thresholds as in §26.3. Non-goals for v1: no N-sandbox parallel review; no
comment-parsing of any verdict element; no LLM triage tie-break; no workflow-engine decomposition
of review; and the light path's behavior remains exactly today's review — the router may only ever
*add* depth, never subtract rigor from the default.

### 26.8 Phasing

Steps 66-69, end of Phase 5 — see the Phase 5 renumbering note (IMPLEMENTATION_PLAN.md). 66
(digest) → 67 (adequacy — needs 66's diff-derived summary as its reference text); 68 (triage) is
independent of 66-67 and valuable alone (model/effort tiering per path) but sequenced before 69
because the deep path must exist to route to; 69 (counter-review + measurement) needs 66's digest
structure and 68's deep path, and rides Step 62's instrument. 66 extends Step 45's domain type,
Step 47's posting tool, and Step 62's persistence — hence the whole chantier sits after Steps
62-65. UI: the review screen's readout layout (digest first, collapsed appendix, contested-points
block) lands with the existing review view Step (Step 82, Phase 7); no new screen.

## 27. Enterprise sandbox glue (detailed design)

§8 item 5 names seven capabilities in one line and, until this section, nothing in this plan
designed any of them — the audit that produced this section found every term in that bullet
(`kubeconfig`, `Docker-in-sandbox`, `OpenCode config storage`) appearing exactly once in the whole
document, with no Step citing it. What unifies the seven is that each is a point where a
**customer's own infrastructure meets the sandbox**: their cloud accounts, their clusters, their
container tooling, their network policy, their secrets, their agent-engine configuration, their
browser/toolchain needs. Two already-shipped anchors are reused throughout rather than reinvented:

- **The sandbox-bearer delivery channel.** Secret material never travels through the provider API
  inside SESSION_CONFIG (the one deliberate exception is the sandbox's own bootstrap bearer token,
  §5.2) — it is *pulled* by sandbox-agent at boot over the authenticated sandbox→CP channel, with
  the exact handshake `scmcredentials.go` established and `providercredentialsdelivery.go` (Step
  53) already mirrors once: bearer-token verification (constant-time hash compare), dead-sandbox
  410, `X-Sandbox-Gen` fencing 403. Every new delivery endpoint below mirrors it again, in the
  same order, for the same audit-established reasons.
- **Step 53's scope/resolution/crypto vocabulary.** `internal/domain/providercredential`'s
  `Scope`/`Resolve` (already generic over `Candidate[T]`), the partial-unique-index pattern
  (migration 000056), `platform.EncryptToken`'s AES-256-GCM at rest, and the write-only management
  API posture are the mechanisms; this section extends their use, never forks a parallel set.

Ordering below is by dependency, not the bullet's own comma order: general secrets first (§27.1),
because OpenCode config (§27.2), cloud identity (§27.3), and kubeconfig (§27.4) all lean on its
storage or delivery machinery; the substrate pieces (Docker §27.5, egress §27.6, toolchain §27.7)
close. §19.8's recorded invariant is honored throughout: nothing in this section ever passes a
user-configurable value into `BuildImage` — rule (a), boot-time injection only.

### 27.1 Repo/environment/global secrets

**A second table, deliberately — not an extension of `provider_credentials`.** Step 53's table is
narrow by design: its identity column is a closed Postgres ENUM of three provider names, each
mapped to fixed env-var name(s) by domain code (`providercredential.EnvVarNames`), consumed by
exactly one process (the `opencode serve` env, `spawn.go`). A general secret inverts both
properties: its identity IS a user-chosen env-var name, and its consumers are the whole supervised
process tree (hooks, `services.yml` services, the agent's own shell). Widening the ENUM-typed
column into free text would destroy the closed-vocabulary property Step 53's own docs treat as
load-bearing; so: new table, same idioms.

```
sandbox_secrets(id, scope sandbox_secret_scope ENUM('automation','environment','repo','global'),
                scope_target_id TEXT, name TEXT, value_encrypted BYTEA, created_at, updated_at)
```

- Same shape CHECK and partial-unique-index pair as migration 000056 (`(scope, scope_target_id,
  name)` where target NOT NULL; `(name)` where NULL); `scope_target_id` meanings identical
  (repo = `repo_full_name`, environment = `environments.id`), plus `automation` = `automations.id`.
- **Resolution order: automation → environment → repo → global, most specific wins** — the order
  §12.2 item 5's Settings mockup already displays and `providercredential`'s own doc.go already
  verified for its three scopes; `automation` slots in as the most-specific level.
  `providercredential.Resolve` is reused as-is (it is already generic); only the `scopePriority`
  table gains the fourth row. The `automation` scope ships in the schema now so the deferred
  per-automation secrets follow-up (§8.4/Step 52's explicit deferral, `automation/doc.go`) needs
  no second migration — but its CRUD and consumption wiring remain that follow-up's scope, not
  this Step's.
- **Name validation, fail-closed at save time**: POSIX env-var shape; the `NARVI_*` namespace
  rejected outright (§19.8's reservation — the live namespace is the eight `NARVI_*` vars
  `boot/config.go:33-40` already defines); the exact names `providercredential.EnvVarNames` covers
  (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, the three Google names) rejected too, so every env-var
  name has exactly one owning mechanism and a shadowing conflict between the two tables is
  unrepresentable.
- **Encryption, RBAC, management API**: `platform.EncryptToken` at rest; the same three
  already-reserved actions (`ActionManageRepoSecrets`/`ActionManageEnvSecrets`/
  `ActionManageGlobalSecrets`) govern this table exactly as they govern `provider_credentials` —
  one management surface, two tables behind it; write-only from the management API
  (`providercredentials.go`'s posture), values never returned, never logged.
- **Delivery**: sibling sandbox-facing endpoint `POST /sessions/{id}/sandbox-secrets` mirroring
  `providercredentialsdelivery.go`'s handshake verbatim; response is a plain name→plaintext map of
  RESOLVED winners only, losers never decrypted (Step 53's decrypt-only-the-winner discipline).
- **Injection**: sandbox-agent fetches once, before the first hook runs, with bounded retry;
  threads the map into every process it spawns — hooks (through `runRepoHooks`' existing
  `EnvWithout` seam, `hooks.go:141`), `services.yml` services, and `opencode serve` (appended
  *before* `providerCredentialEnv`, so the ordering question is moot anyway given the disjoint-name
  rule above). Degrade policy on fetch failure: **warn and continue** — recorded in the boot log
  and `AGENTS.md`, never a boot failure — the same reasoning §19.4 already settled for a failed
  setup rerun: a running agent that can diagnose a missing env var beats a dead sandbox, and
  "never block a spawn" (§10-P2) holds.
- **What this mechanism does NOT claim**: in-sandbox secrecy from the agent is a non-goal — the
  agent is the intended consumer. The real boundaries are: encrypted at rest CP-side; never
  through the provider API; never written inside any repo working tree (so never committable);
  never logged. The residual risk that an agent *writes* a secret value into code it then pushes
  is shared with every secrets mechanism in every CI system; output redaction is possible future
  work, not claimed here.

### 27.2 OpenCode config storage + injection

What OpenCode config actually is (verified — partly live in this codebase, partly against
OpenCode's own docs): JSON config (`opencode.json`(c)) merged across an ordered source list,
later-wins — remote → **global** (`~/.config/opencode/opencode.json`) → **custom**
(`OPENCODE_CONFIG` env var) → **project** (workspace `opencode.json` — the slot Step 48's
sentinel-fix agent write already targets and verified loading, `sentinelfixagent.go`) — carrying
providers/models, MCP servers, agents, permissions, LSP config, and `{env:VAR}`/`{file:path}`
substitution. That merge order is the entire injection design — Narvi occupies OpenCode's own
documented slots rather than inventing a merge:

- **Storage**: `opencode_configs(scope ENUM('environment','global'), scope_target_id, document
  JSONB, timestamps)` — same CHECK/partial-unique-index idioms as §27.1; at most one global row,
  one per environment. **Plaintext JSONB, deliberately** — this is configuration users read and
  edit in Settings, not secret material; anything secret belongs in §27.1's table and is
  referenced from the document via OpenCode's own `{env:VAR}` substitution, which resolves at
  OpenCode's load time from the process env sandbox-agent already built. Validation at save:
  parses as a JSON object, bounded size — nothing deeper, because OpenCode's own schema drifts
  with its version and a Narvi-side copy of it would be a second, staler validator (§7's
  pinned-binary contract tests are where engine-shape drift is caught).
- **Injection**: delivered at boot over a sibling sandbox-facing endpoint (same handshake), both
  scopes at once: sandbox-agent writes the global document to
  `~/.config/opencode/opencode.json` (OpenCode's global slot; base images must not bake one) and
  the environment document to a file outside the workspace, setting `OPENCODE_CONFIG` to its path
  on the `opencode serve` process. OpenCode's own precedence then composes everything correctly
  with zero Narvi-side merge code: org-global < environment < repo-committed project config < the
  sentinel-fix capability-restriction write (§17.2/Step 48, which targets the project slot) —
  i.e. **a customer-authored config can never override the security-relevant agent restriction**,
  by the engine's own documented ordering, not by a Narvi convention.
- **RBAC**: global scope admin-only (the §13.3 row that owns integrations/global secrets);
  environment scope maintainer+ (the row that owns environments/env secrets).
- **Trust note, stated plainly**: an org-authored OpenCode config can name MCP servers and
  commands — code execution inside the sandbox. This grants no privilege a repo's own committed
  `opencode.json` or `setup.sh` does not already have (the sandbox runs untrusted repo code by
  design); the management surface above is the gate, and it is role-checked server-side like
  every other state change (§13.3).

### 27.3 Cloud credentials via OIDC (provider-agnostic)

The pattern is the one GitHub Actions standardized for CI↔cloud federation, applied to sandboxes:
**Narvi's control plane becomes an OIDC identity provider; the customer's cloud IAM is configured
to trust it; the sandbox exchanges a short-lived Narvi-signed identity token for short-lived cloud
credentials via the cloud's own STS.** Narvi stores no cloud credential of any kind, ever — the
customer-side trust policy IS the grant, and revoking it is the customer's own kill switch.

- **Issuer**: CP serves `GET /.well-known/openid-configuration` + a JWKS endpoint on a new
  `platform.Config` public issuer base URL, validated at boot; the whole capability is off (and
  binding CRUD refuses, fail-closed) when unset. Signing keys: RS256; private keys generated
  CP-side and encrypted at rest with `platform.EncryptToken` (`oidc_signing_keys(kid,
  private_key_encrypted, public_jwk, created_at, retired_at)`); rotation publishes old + new in
  the JWKS for an overlap window ≥ max token lifetime — the same overlapping-validity discipline
  §5.2 already applies to sandbox-token rotation.
- **Claims**: `iss` = the issuer URL; **`sub` = a stable, deterministic, per-Environment value**
  (`narvi:environment:<environment_id>`), because `sub` is the one claim every cloud can condition
  on and Azure's federated-credential matching requires an exact, predictable subject string —
  never anything session-varying in `sub`. Session-varying context (`session_id`, `gen`, repo
  full names, provenance tag) rides as additional custom claims for clouds whose condition
  languages can use them (GCP's attribute mapping; AWS condition keys). `aud` = per-binding,
  customer-set — each cloud documents what it expects (`sts.amazonaws.com` for AWS; the workload
  identity provider resource name for GCP; `api://AzureADTokenExchange` for Azure). `exp` ≈ 10
  minutes.
- **Bindings** — what connects an Environment to a cloud role: `cloud_identity_bindings(scope
  ENUM('environment','global'), scope_target_id, kind ENUM('aws','gcp','azure','generic'),
  audience, params JSONB, timestamps)`, at most one per (scope target, kind) in v1. `params` are
  identifiers, not secrets (AWS: role ARN; GCP: workload-identity-provider resource name +
  optional service-account email; Azure: client id + tenant id; generic: the env-var name to
  publish the token path under) — stored plaintext, readable, maintainer+ managed (the §13.3
  environments row). **Deliberately no repo scope**: a deployment target is an Environment
  property (§14.1's own model — confirmed, not just assumed), not a repo property.
- **Minting**: sandbox-agent calls `POST /sessions/{id}/cloud-identity-token {audience}` over the
  sandbox-bearer channel (same handshake as every delivery endpoint here). CP refuses any audience
  no binding for this session's Environment (or global fallback) declares — it never mints
  arbitrary-audience tokens. Minting is logged with `correlation_id` (§5.3) and counted as a
  metric; `audit_log` records binding CRUD, not each 5-minute refresh (proportionate, or the audit
  log becomes noise).
- **In-sandbox consumption — file-based, zero custom tooling**: all three clouds' SDK families
  natively consume a *file-sourced* OIDC token via standard env vars, so sandbox-agent maintains
  one token file per binding under a non-workspace path (`/narvi/identity/`, 0700/0600 — never
  inside any repo tree, so never committable), refreshes each at token half-life (background,
  same supervisor discipline as everything else it runs), and sets the standard env vars on every
  spawned process: `AWS_WEB_IDENTITY_TOKEN_FILE` + `AWS_ROLE_ARN` (+ session name) for AWS's
  `AssumeRoleWithWebIdentity` flow; `GOOGLE_APPLICATION_CREDENTIALS` pointing at a generated
  external-account credential-config JSON whose `credential_source.file` is the token file, for
  GCP's STS exchange; `AZURE_FEDERATED_TOKEN_FILE` + `AZURE_CLIENT_ID` + `AZURE_TENANT_ID` for
  Azure workload identity. The clouds' own client libraries perform the exchange in-sandbox;
  **Narvi implements no per-cloud exchange code at all** — that is what "provider-agnostic"
  concretely means here, and why a fourth target (Vault, or any JWT-federating system) is just a
  `generic` binding, not a CP change.
- **Boundary**: what a compromised or over-eager sandbox can do cloud-side is exactly what the
  customer's own trust policy + role grant to that Environment's `sub` — least privilege is the
  customer's lever; Narvi's job is making `sub` fine-grained, stable, and honest. Lifetime bounds
  the tail (≤10-min tokens; minting stops at dead-sandbox/410, like every other delivery
  endpoint); a leaked token is useless against any role whose trust policy names a different
  `sub`/`aud`.

### 27.4 Kubeconfig injection for the target cluster

"The target cluster" is selected the way §14 already models deployment targets: **per-Environment**
— `cluster_bindings(environment_id UNIQUE, name, server_url, ca_bundle, auth_kind
ENUM('cloud','oidc','static'), params JSONB)`, one cluster per Environment in v1 (the bullet's own
singular). sandbox-agent renders a kubeconfig from the binding at boot, writes it under
`/narvi/identity/` (never a repo tree), and sets `KUBECONFIG` on every spawned process. Three auth
rungs, preferring federation over static material:

1. **`cloud`** — EKS/GKE/AKS ride §27.3's already-established identity with zero additional
   secret: the rendered kubeconfig uses the standard exec credential plugin for that cloud
   (`aws eks get-token` / `gke-gcloud-auth-plugin` / `kubelogin` in workload-identity mode), each
   of which consumes exactly the env vars §27.3 already set. Kubernetes' client-go
   exec-credential mechanism does the rest; the toolchain image (§27.7) carries the three plugins.
2. **`oidc`** — a self-managed cluster whose kube-apiserver is configured to trust Narvi's own
   issuer directly (`--oidc-issuer-url` + client-id + claim mappings): the kubeconfig's exec
   plugin is **sandbox-agent's own subcommand** (`kube-credential`), which fetches a CP-minted
   token (§27.3's endpoint, `aud` = the cluster's configured client id) and prints a standard
   `ExecCredential` JSON — the exact same shape as the git-credential-helper subcommand precedent
   (`runCredentialHelper`, `cmd/sandbox-agent/main.go`): git's helper protocol there, client-go's
   here. Authorization inside the cluster is the customer's own RBAC binding on the token's
   claims (recommend a namespace-scoped Role, never cluster-admin — documented, not enforced,
   since the cluster is the customer's).
3. **`static`** — an uploaded kubeconfig for clusters with no OIDC path at all, stored as a §27.1
   secret (the value is the file content; delivered and written to disk by sandbox-agent, never
   env-var-expanded). Supported honestly as the lowest rung and named as such: long-lived
   credential material at rest, exactly what the two rungs above exist to avoid.

### 27.5 Docker-in-sandbox

The well-known hard problem, named rather than hand-waved: nested containers need either privilege
(classic privileged-mode DinD — **rejected outright here**: it is root-equivalent on the host and
incompatible with §5.2's fail-closed/least-privilege posture), a syscall-emulating or user-ns
runtime (gVisor, rootless, sysbox — each with real compatibility limits), or a real kernel per
sandbox (microVM). The decisive architectural fact: **Narvi's sandboxes run on a provider's
substrate, so the isolation technology is the provider's, and the decision surfaces through the
existing port, not a new mechanism**:

- **Per-Environment `docker: required` flag** → carried in SESSION_CONFIG and, like `Gen`, also as
  a top-level `CreateSpec` field (the same deliberate-duplicate-with-`Validate` discipline
  `createspec.go` already documents — the provider must act on it without parsing the opaque doc).
  `ports.Capabilities` gains `DockerInSandbox bool`.
- **Fail-closed, twice**: session creation against an Environment requiring Docker is refused
  up-front when the configured provider reports no support (clearest possible UX), and the spawn
  path re-checks at dispatch — a Docker-requiring session is never silently run somewhere the
  requirement is unenforceable.
- **Modal concretely** (researched for this section, current as of writing): default Modal
  sandboxes run on gVisor, where dockerd's overlay2/bridge-networking stack does not run cleanly;
  Modal's VM runtime option gives the sandbox a real kernel, where Docker/compose/build behave
  normally. The Modal adapter maps the flag onto that runtime option. The costs are real and
  named: VM-runtime boot latency vs §19's warm-boot expectations, snapshot-capability parity
  under a different runtime (see §27.8 — `Capabilities()` is flat today and cannot express
  per-spawn capability variance), the option's experimental status, and per-sandbox cost.
- **The anticipated Kubernetes-native provider** (§0): sysbox-class user-ns runtimes are the
  recommended enablement path, Kata-class microVMs the stronger-isolation alternative —
  **privileged pods never**, under any configuration this plan ships.
- **In-sandbox**: when the flag is set, sandbox-agent supervises `dockerd` as one more entry in
  the same process-supervision table as everything else (§14.2's own "no new supervision code
  path" rule), with a named `boot_progress` phase; the CLI/engine binaries come from the toolchain
  image (§27.7) and the daemon simply never starts when the flag is off.

### 27.6 Egress: what §4.1 already covers, and the sandbox-side gap

The bullet's word "egress proxy" is **half-covered**. Fully covered already, needing no new
design: the control plane's own outbound traffic to the provider API routes through the
configurable proxy (§4.1; `ModalEgressProxyURL`, `modal/provider.go`'s Transport wiring) — that
shipped with Step 12. Not covered anywhere until now: the **sandbox's own egress**. Two halves,
because they are genuinely different guarantees:

- **Cooperative routing** — `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` across the sandbox process
  tree: this is *pure §27.1* — proxy URLs are secret-shaped (they conventionally carry basic-auth
  credentials; `modal/errors.go` already redacts them for exactly that reason), so a customer
  configures them as environment-scoped secrets and every spawned process inherits them. Zero new
  machinery; named here so nobody builds a parallel mechanism. Explicitly NOT enforcement — any
  process can ignore env vars.
- **Enforced policy** — per-Environment `egress_policy {mode: open|allowlist, allowlist}`,
  carried like the Docker flag (SESSION_CONFIG + top-level `CreateSpec`), **enforced at the
  provider substrate** (Modal's own sandbox network controls; NetworkPolicy for the anticipated
  Kubernetes provider), surfaced as `Capabilities.EgressPolicy` and **fail-closed** exactly like
  §27.5: a policy the configured provider cannot enforce refuses the spawn, never runs
  unenforced. A non-negotiable allowlist floor is auto-appended server-side — the CP's own
  WS/API host, the session's git hosts, and nothing less — because a sandbox that cannot reach
  the control plane or clone its repos is not a security posture, it is a boot failure.

### 27.7 Toolchain in images

Confirmed genuinely mechanical — a content addition to the **base sandbox image**, no new
architecture. The base image is the `Base` every build and cold boot already starts from
(`defaultBaseImage`, `dispatch.go:153` — today a placeholder tag; its real build definition is
where these land), NOT the per-repo-set prebuild pipeline (§19 bakes repos and dependency caches
*on top of* base; tools belong below). The additions: Playwright + Chromium (installed with its
OS deps, pinned), `ripgrep`, `typescript-language-server` (+ `typescript`), the Docker CLI/engine
binaries (daemon dormant unless §27.5's flag is set), and §27.4's three cloud exec-credential
plugins. Versions pinned and visible via the boot fingerprint's existing image digest (§5.3). The
one real consideration: Chromium adds on the order of a gigabyte, which prices into image pull
time — absorbed in practice by §19's shared warm images and already inside what
`first_connect_budget` is sized for (§5.4). That is the entire design; stating more would be
manufacturing complexity.

### 27.8 Risks and open questions

- **Public-issuer reachability (§27.3)**: AWS and Azure fetch issuer discovery/JWKS over public
  HTTPS — a firewalled self-hosted CP cannot federate with them directly. GCP accepts an uploaded
  JWKS (no public issuer needed). For fully air-gapped deployments the honest answer today is
  §27.4's `static` rung and §27.1 secrets; a token-exchange relay is out of scope until someone
  actually needs it.
- **Key-rotation cadence (§27.3)**: manual, admin-triggered rotation with the overlap window is
  v1; automatic scheduled rotation is deferred until operational experience says what cadence is
  right. Clock skew between CP and cloud STS endpoints bounds how short `exp` can safely go.
- **`sub` granularity (§27.3)**: per-Environment is the designed grain. If a real customer needs
  per-repo cloud scoping inside one Environment, that collides with Azure's exact-match subject
  requirement and needs its own design pass — named now, not solved speculatively.
- **Per-spawn capability variance (§27.5)**: `Capabilities()` is a flat, provider-level report
  (§4.1); a provider whose snapshot support differs by runtime (Modal gVisor vs VM runtime) cannot
  express that today. If VM-runtime sandboxes turn out snapshot-incapable, either `Capabilities`
  grows a per-spec dimension or Docker-requiring sessions document degraded recovery (resume-only,
  §3.2) — decided at Step 72 implementation time against the provider's real behavior, not
  guessed here.
- **Build-time secrets (§27.1, §19.8)**: rule (a) means shared-image builds run `setup.sh` with
  no user secrets. A `setup.sh` that hard-requires one (private package registry) fails its
  builds (fatal in `BootModeBuild`) and that Environment degrades to base-image cold boots — where
  the boot-time rerun DOES have the secrets and succeeds. Correct, but slow. §19.8's rule (b)
  (env-digest joins the fingerprint) is the designed escape if this bites a real Environment; not
  built until then.
- **Enforced-egress granularity (§27.6)**: whether the provider substrate enforces by domain or
  by CIDR (and how DNS is handled inside the allowlist) is provider-specific and must be verified
  against the provider's real controls at implementation time — the fail-closed rule above is what
  keeps this honest either way.
- **Snapshotting a running dockerd (§27.5)**: daemon/image-store state inside snapshots
  (§3.2/§8.5's snapshot-restore path) is untested territory; Step 72 must add a §9.3-class
  scenario for restore-with-docker before claiming it works.

### 27.9 Phasing

Steps 70-72, opening Phase 6 — see the renumbering note (IMPLEMENTATION_PLAN.md). Placed there,
not in Phase 5, because this is rollout-enabling platform glue, not review/automation scope:
§10-P6's own first line ("Config setup (automations, secrets, environments, settings,
integrations)") already presumes these surfaces exist, and the config/data-seeding Step that opens
the rest of Phase 6 seeds exactly what these Steps build. Step 70 (§27.1 + §27.2) extends Step
53's mechanisms and needs Step 39's `Authorize`; Step 71 (§27.3 + §27.4) builds on 70's delivery
family and the `static` rung stores through 70's table; Step 72 (§27.5 + §27.6's enforced half +
§27.7) is the ports/substrate piece (Steps 12-16's seams, §19's image pipeline adjacency) and can
run in parallel with 71. UI: no new screens — the Settings view (§12.2 item 5, Step 84) gains the
secrets table it already mocks plus cloud/cluster bindings, per-Environment Docker/egress
settings, and the OpenCode config editor.

## 28. Uploads, blob storage & the in-sandbox `download_file` tool (detailed design)

§8.6 states the feature as an exit criterion — "uploads to object storage (S3-compatible) +
`download_file` tool in sandbox; failed-upload UX signal" — and `BlobStore` appears only in §4.3's
port list, with no signature and no design anywhere else in this plan; §12.2 item 1's artifacts
panel and §6.3's `uploads` route name the *surfaces*, not the mechanism. This section fixes the
port, the transport decision, the key/limit/credential model, and the tool's actual wire protocol,
so Step 58 doesn't have to invent them under time pressure (§18's own precedent for closing exactly
this kind of gap).

Two flows, one mechanism. Files move in both directions: a user attaches a file to a prompt (a
screenshot, a spec, a CSV) for the agent to consume, and the agent produces media the session rail
must surface — §12.2 item 1's rail already plans "artifacts: PR / preview / uploads", and the
`artifacts` table has carried an `upload` enum value since Step 04
(`migrations/000012_artifacts.up.sql`) with, per that store's own doc comment, no producer yet.
Both directions are the same design: the control plane mints a short-lived presigned URL against
S3-compatible storage, the bytes move directly between the client (browser or sandbox) and storage,
and the CP verifies the result after the fact and records it in Postgres.

### 28.1 The `BlobStore` port (complete — no out-of-interface operations)

```go
type BlobStore interface {
    PresignPut(ctx, PresignPutSpec) (PresignedURL, error) // Spec: Key, ContentType, ContentLength, TTL
    PresignGet(ctx, PresignGetSpec) (PresignedURL, error) // Spec: Key, TTL, ResponseFilename
    Stat(ctx, BlobKey) (BlobInfo, error)                  // BlobInfo: SizeBytes, ETag — confirm-time verification
    Delete(ctx, BlobKey) error                            // idempotent: deleting an absent key succeeds
}
```

Errors are typed `BlobStoreError{Transient bool}` — classification by storage error code / HTTP
status class, **never** by string-matching messages — the same discipline §4.1 requires of
`ProviderError`, mirroring `ports/providererror.go`'s shape exactly ({Transient, Code, Op, Err},
one `Op` constant per method, and an `IsTransient` helper defaulting unclassified errors to
transient). `Stat` on an absent key returns a typed not-found sentinel (`ErrBlobNotFound`),
distinct from any transient failure — confirm-time verification (§28.4) branches on it, never on a
string.

- `BlobKey` is opaque to the adapter: only the CP's own key builder (§28.3) produces one; the
  adapter never parses or constructs keys.
- TTLs are passed in by the caller from `platform/timeouts.go` (§5.4) — the adapter holds no
  timeout literal of its own (§11's grep-test applies).
- Under SigV4, `PresignPut`/`PresignGet` are local signing operations — pure HMAC over the request
  descriptor, no network round-trip — so a presign cannot meaningfully fail transiently;
  `Stat`/`Delete` are real network calls and carry the full transient/permanent classification.
- `PresignedURL{URL, ExpiresAt, Headers}`: `Headers` are the exact headers the uploader must send
  (e.g. `Content-Type`) for the signature to verify; mint responses forward them verbatim.
- Deliberately absent, with reasons: no `Put`/`Get` streaming methods — nothing in this design
  requires the CP to touch bytes (§28.2), confirm-time verification is metadata-only (`Stat`), and
  §4.1's "complete" discipline cuts both ways (no speculative surface for a consumer that doesn't
  exist; a future feature that genuinely needs CP-side reads adds the method with that feature). No
  multipart-upload surface — the per-file cap (§28.4) sits far below every supported backend's
  single-PUT limit. No `List` — every object's key embeds the artifact row id that minted it
  (§28.3), so there is no orphan-blob class a bucket scan would find that the row-driven sweep
  (§28.4) doesn't already cover.

**Not sqlc-backed, deliberately.** §4.3 groups `SessionStore`/`TurnStore`/`SandboxStore` as
"(sqlc-backed)"; `BlobStore` is not in that trio and stays out: it is an outbound adapter over the
S3 HTTP API (`internal/adapters/outbound/objstore` — the package stub already names AWS S3, MinIO,
R2, GCS), exactly as `githubapi` is an adapter over GitHub's. The split preserves §5.1: object
storage holds **bytes only**, addressed by keys Postgres owns; every fact *about* an upload
(status, size, who, when, why it failed) lives on the `artifacts` row, sqlc-backed like every other
store. Object storage is never a second authority over state — a blob with no row is an orphan to
reap, never a record.

### 28.2 Transport: presigned URLs — bytes never transit the control plane

The decision the rest of this section hangs on: **the CP mints presigned URLs and the bytes move
directly between client and storage, in both directions. The CP never proxies a payload.**

The alternative — client POSTs bytes to the CP, the CP streams them to storage — was rejected on
the codebase's own already-demonstrated posture:
- §6.1's `artifact` event is the existing precedent for how media reaches clients: **by URL
  reference, never by value**. No binary payload rides the sandbox WS (its ack buffer is sized for
  control events — 1000 entries with eviction), and nothing in §6.2/§6.3 contemplates the CP as a
  byte funnel.
- Proxying couples the CP's connection/memory footprint and its timeout hierarchy (§5.4) to upload
  sizes and client link speeds — a max-size upload on a slow link would hold a CP connection open
  for minutes, competing with the WS/actor hot paths the CP exists to serve, and would need its own
  size-scaled timeout tier for no compensating benefit.
- A presigned URL is itself the credential shape §5.2 already prefers (§28.7): scoped to one object
  and one method, expiring in minutes — minting one is strictly cheaper and strictly safer than
  terminating the transfer.

What the decision costs, named honestly: (a) the storage endpoint must be **directly reachable from
browsers and from sandboxes** — a deployment requirement, carried by config (§28.7's
`PublicEndpoint`), with sandbox-side transfers subject to the same egress-proxy path the sandbox's
other outbound traffic uses (§8 item 5) — and once §27.6's per-Environment egress allowlist ships,
the object-storage host joins its server-appended floor (CP host + git hosts + storage host),
or uploads break exactly and only on allowlisted Environments; (b) the bucket needs a **CORS policy** allowing PUT/GET
from the web origin — a deployment-doc item alongside the bucket itself; (c) the CP no longer
observes the transfer — which is exactly why the lifecycle is two-phase (§28.4): the CP verifies
after the fact instead of watching bytes go by.

### 28.3 One bucket, per-session keys, isolation enforced at mint time

- **One configured bucket per deployment** (`platform.Config`, §28.7). Narvi deployments are
  single-tenant by construction — the domain has roles and identities but no org/team entity
  (§12.2 item 1's own note) — so the tenancy boundary IS the deployment: separate deployments,
  separate buckets and credentials, enforced by each deployment's own IAM policy scoping its
  credential to its one bucket.
- **Key convention**: `sessions/{session_id}/uploads/{upload_id}`, where `upload_id` is the
  artifact row's own UUID. The key carries **zero client-controlled bytes** — no filename, no user
  text (the filename lives on the row and is applied at download time via
  `response-content-disposition`, §28.5) — so path traversal, encoding surprises, and collision
  games are unrepresentable rather than validated away.
- **Within a deployment, session-level isolation is enforced at mint time by the CP, not by IAM.**
  Every presigned URL is minted only after the CP has already authorized the caller against that
  specific session (browser: cookie auth + `Authorize`; sandbox: bearer + gen handshake, §28.5),
  and only ever for a key under that session's own prefix. S3-compatible stores diverge too much in
  policy granularity (AWS IAM conditions vs. MinIO policies vs. R2 tokens) for per-prefix IAM to be
  the load-bearing mechanism across all of them: the deployment credential's bucket scope is the
  IAM layer's job; the CP — sole holder of that credential and sole minter — is the session
  boundary. The only storage credential a client ever holds is a single-object, single-method,
  minutes-lived URL.

### 28.4 Upload lifecycle: mint → transfer → confirm, verified server-side

The `artifacts` table gains upload lifecycle columns (one migration; existing `pr`/`preview` rows
take the `ready` default — they were only ever recorded after the fact, so `ready` is what they
always were):

```
artifact_status ENUM('pending','ready','failed');  status NOT NULL DEFAULT 'ready'
failure_reason  ENUM('size_exceeded','quota_exceeded','verification_failed','abandoned') NULL
blob_key TEXT NULL · size_bytes BIGINT NULL · content_type TEXT NULL · filename TEXT NULL
created_by UUID NULL REFERENCES users(id)  -- NULL = agent-produced (§17.5's no-human-actor allowance)
```

**Mint** (`POST` — the two auth variants in §28.5): the request declares `{filename, contentType,
sizeBytes}`. The CP checks the declared size against `MaxUploadBytes` (propose 100 MiB,
per-deployment config) and the session's running total against `MaxSessionUploadBytes` (propose
1 GiB — `SUM(size_bytes)` over the session's `pending`+`ready` uploads, derived from rows that
already exist, never a dedicated counter column, §25.5's own discipline), inserts the `pending`
artifact row (its `url` already the stable content path, §28.5), and returns `{uploadId, putUrl,
headers, expiresAt}`. An over-limit request is refused at mint: no row, no URL, a structured 4xx
naming the limit.

**Transfer**: the client PUTs the bytes to `putUrl` with the returned headers, within
`UploadPresignPutTTL` (propose 15 min — generous for the size cap on a slow link, the same "chosen
generously when the concrete cost is unknown" convention `HookTimeout` documents).

**Confirm** (`POST …/uploads/{uploadID}/complete`): the CP `Stat`s the object and verifies the
object exists, its actual size equals the declared size (the quota math above is only honest if the
declaration was), and both limits hold **re-checked now** — two mints racing past the session cap
is closed here, at the authoritative moment; mint-time checks are a fast-fail courtesy,
confirm-time checks are the enforcement of record, the same never-trust-the-earlier-render posture
as §16.2's re-validation-at-click. Passing: `pending → ready`, committed in the same transaction as
an appended session event (§28.6), broadcast only after commit per the `EventBroadcaster`
contract. Failing: `pending → failed` with the typed `failure_reason`, the same event append, **and
a `blob_delete` outbox entry** — an external delete is an outbound side effect (§5.1), and
fire-and-forget would leak the object forever on a crash between the status write and the delete;
the outbox's retry/dead-letter is exactly the guarantee a cleanup needs. Confirm is idempotent via
a guarded transition (`UPDATE … WHERE status = 'pending'`, the §25.6 idiom): a retried confirm of
an already-resolved row returns the recorded outcome, never re-verifies, never double-appends.
Presigned PUTs pin `Content-Length`/`Content-Type` in the signed headers where the backend honors
them, but the design never *relies* on that honoring — backend divergence again — which is why
`Stat`-at-confirm is the check of record.

**Abandonment sweep**: a `pending` row older than `UploadPendingSweepAfter` (propose 24 h) is
marked `failed(abandoned)` with a `blob_delete` outboxed (the object may half-exist), by the same
`app/scheduler` recovery-sweep machinery §3.5 already runs, on its own named interval in
`platform/timeouts.go` (propose 15 min). A browser that minted and walked away costs one row and
one sweep pass, nothing more.

**Retention is a named non-goal**: `ready` blobs live as long as their session rows do (sessions
are archived, never hard-deleted, §3.1); a retention/GC policy for archived sessions' media is
future work, recorded here the way §19.2 records image GC — named now so it gets scheduled
deliberately instead of discovered as a storage bill.

Ownership of these writes follows the existing split: artifact rows are not actor-owned state
(§2's single-writer rule covers session/sandbox/turn rows; `pushpr.go` already records PR
artifacts, and `coalesce.go`'s direct `github_pr_sessions` writes are the accepted precedent for
non-actor-owned rows, §24.1), so the upload handlers write them directly in their own
transactions. Whether the event append routes through a small actor command or the same
direct-write transaction is Step 58's own implementation decision (the §24.3 style of deferral) —
with the invariant fixed either way: row transition + event append commit atomically; broadcast
only after commit.

### 28.5 The `download_file` tool: a bearer-authenticated redirect, not a new wire type

**No new WS message types, in either direction.** The sandbox WS contract (§6.1) is untouched: no
new agent→CP event, no new CP→agent command. The established mechanism for "the agent needs
something from the CP mid-turn" is a sandbox-bearer REST endpoint — `scm-credentials` (§5.2/Step
21), `snapshot`, `review/verdict` (Step 47), `provider-credentials` (Step 53) — and in this
codebase "tool" already *means* exactly that: a CP HTTP endpoint the rendered turn prompt instructs
the agent to call, with the live URL/bearer/gen substituted into placeholder tokens by
`sandbox-agent` immediately before the prompt reaches the engine
(`cmd/sandbox-agent/reviewverdicttoolprompt.go`'s mechanism, reused — never a second substitution
scheme; no engine plugin/tool registration, no `AgentRuntime` change, §25.6's own "no new wire
command" posture). RPC-over-the-WS was rejected for the same reasons it wasn't used for verdicts:
the WS has no request/response correlation (`snapshot_ready` needed `commandMessageId` retrofitted
for even one-way correlation), and its buffer/ack machinery is sized for control events, not
transfers.

The endpoint — mounted like its siblings: outside `/api`, outside `auth.Middleware`, sandbox
bearer + `X-Sandbox-Gen`, with `scm-credentials`' own dead-sandbox/gen handshake:

```
GET /sessions/{sessionID}/uploads/{uploadID}/content
  → 302  Location: presigned GET (UploadPresignGetTTL, propose 5 min;
         response-content-disposition: attachment; filename="<row.filename>")
  → 404  uploadID unknown, not this session's, or not status='ready'
  → 403  bad bearer / gen mismatch        → 410  dead sandbox
```

One redirect makes the whole tool a single command: `curl -fL -H "Authorization: Bearer <token>"
-o <dest> <url>`. curl does not forward `Authorization` across a cross-host redirect by default, so
the storage endpoint never sees the sandbox bearer — and the presigned URL never appears in the
prompt, the transcript, or any persisted event; it exists only inside that one process's redirect
follow. Forcing `attachment` disposition means user-supplied content is never rendered inline off
the storage origin (an HTML upload must not become a page someone can be linked to — §5.2's
untrusted-content posture applied to serving).

**How the agent learns what to fetch**: the prompt-carrying REST DTOs gain optional
`attachmentIds: []uuid`, validated at the turn-creation chokepoint (`createTurnLocked` — the same
single shared core §23.2 already gates): every id must be a `status='ready'` upload artifact **of
this session**, else a structured 4xx — a failed or foreign upload can never silently ride a
prompt. The turn prompt then carries a deterministic, server-rendered attachment block (the
`review.RenderTurnPrompt` pattern): per attachment — filename, size, content type, and the exact
`download_file` command above with its placeholder tokens; `sandbox-agent` substitutes the live
values. A prompt with no attachments renders no block, and substitution on it is a byte-for-byte
no-op (`reviewverdicttoolprompt.go`'s own unconditional-substitution reasoning).

**The agent-produced direction** uses the same mint/confirm endpoints in their sandbox-bearer
variants (`POST /sessions/{sessionID}/uploads` → presigned PUT → `POST …/complete`, same 403/410
handshake), surfaced to the agent as a compact, deterministic tool note in build-turn prompts (same
server-side render + substitution; exact phrasing is Step 58's). The browser twins live inside the
auth-gated `/api` group (`POST /api/sessions/{id}/uploads`, `…/complete`), gated by a new
`Authorize` action mapped to the same §13.3 row as prompting (member+, own/joined sessions; viewers
never upload — the viewer guard holds). Browser downloads reuse the content route inside `/api`
(`GET /api/sessions/{id}/uploads/{uploadID}/content` → 302), gated by session visibility — a
download is a read, so read-only viewers may. The artifact row's `url` column stores this stable
`/api/…` content path, **never a presigned URL**: a presigned URL is an ephemeral credential, and
this codebase persists no live credential anywhere (§11's "tokens are always hashed" posture,
applied to a different credential shape) — every download mints a fresh one at click time.

Whether the web composer sequences create-session → upload → first prompt, or restricts attachments
to follow-up prompts, is a Phase 7 UI decision resolved against the mockups (§11); minting requires
an existing session id, and that constraint is this section's only contribution to the question.

### 28.6 The failed-upload UX signal

§8.6's "failed-upload UX signal" is **persisted status, not a toast**: the artifact row's
`status`/`failure_reason` (§28.4) is the durable fact, and the signal reaching live clients rides
the same channel every other session fact already rides — an appended event, broadcast and
replayable. The wire `artifact` event (§6.1) gains two additive, optional fields: `status:
ready|failed` (absent = `ready`, so every existing shape stays valid — the same
zero-producers-today additive reasoning `snapshot_ready.commandMessageId` used) and a nullable
`failureReason`; `SubscribedPayload.artifacts` elements and the REST artifact DTO carry the same
two fields, additively there too.

Upload `artifact` events are **CP-synthesized only** (the §3.3 synthetic-`execution_complete`
precedent; the schema documents the fields as never-populated-by-the-sandbox, §6.1's own
`subTaskId`-note convention). The sandbox never emits `artifact` for uploads: the CP already owns
the row before any bytes exist, and a sandbox-reported "I uploaded it" would be a second writer
over a fact Postgres already owns (§5.1's second-copy principle) — and an agent self-report this
system never trusts anyway (§21.2's discipline). The agent's confirm call is a *request to verify*:
the CP's `Stat` is what flips the status, and the confirm response tells the agent honestly whether
verification passed — so the model can retry once or tell the user — while the row and the event
already carry the truth regardless of what the model then says.

Rendering invents nothing: the session rail's artifacts panel (§12.2 item 1) shows a `failed`
upload with a status chip + reason where a `ready` row would show its download link, and the
timeline shows the same event the broadcast/replay stream already delivered. One signal, two
already-planned surfaces.

### 28.7 Credential scoping

The same shape §5.2 already fixed for git credentials, applied to storage:
- **The root storage credential exists in exactly one place**: `platform.Config` (typed,
  boot-validated, §1) — endpoint, region, bucket, access key/secret (or ambient IAM where the
  deployment provides one), optional `PublicEndpoint`, path-style toggle for MinIO-style backends.
  It never appears in `SESSION_CONFIG`, sandbox env, any prompt, or any wire shape — the
  sandbox-side env-hygiene concern §19.8 tracks has nothing to exclude here because nothing is ever
  injected.
- **What a client holds is never that credential** — only a presigned URL: one object, one method,
  minutes-lived (`UploadPresignPutTTL`/`UploadPresignGetTTL`, resident in `platform/timeouts.go`
  like every other interval, §5.4). This is the git-credential-helper pattern (§5.2: never
  long-lived in sandbox, short expiry, tightly scoped) with the mint moved server-side and the
  scope narrowed from host to single object.
- **Presigning binds the host**: URLs are signed against `PublicEndpoint` when set — a signature
  minted against an internal hostname breaks the moment a browser or sandbox resolves the public
  one (the classic S3-behind-a-proxy failure, named here so the deployment docs name the config).
- **Dev/CI story**: `docker-compose.dev.yml` gains a MinIO service for `make dev`; the adapter's
  integration tests run against a MinIO testcontainer (the `postgres:17-alpine` testcontainers
  precedent), asserting presign/PUT/`Stat`/`Delete` round-trips and the not-found/oversize
  classifications — §9.2's real-backend contract-test discipline, applied to storage.
- **Feature-flagged by configuration**: with no object-storage config present, the mint endpoints
  return a structured "uploads not configured" error and nothing else degrades — the standard
  incomplete-path flag posture (§10/CLAUDE.md), so Step 58 can land ahead of any deployment
  actually provisioning a bucket.

### 28.8 Phasing

Step 58, Phase 5, ∥ — independent of every other Phase 5 Step: nothing else consumes uploads, and
it consumes nothing beyond Step 04's `artifacts` table, Step 21's sandbox-bearer endpoint pattern,
and Step 47's prompt-placeholder mechanism. One PR: the `BlobStore` port + `objstore` adapter (+
MinIO integration tests); the artifacts migration; the mint/confirm/content endpoints in both auth
variants; `attachmentIds` + the rendered attachment block + placeholder substitution; the additive
`artifact`-event/DTO fields; the abandonment sweep; the `blob_delete` outbox kind; the config +
timeouts entries. Exit criteria: contract round-trips green including the additive fields; one
end-to-end integration test per direction (browser-shaped mint→PUT→confirm→download;
sandbox-shaped mint→PUT→confirm→`artifact` event observed); a failed-verification case proving
`failed(reason)` + the outboxed delete + the rail-visible event; and the oversized-mint refusal.
UI consumption is Phase 7 (Step 81's artifacts rail — status chip + reason on upload rows; no new
view).
