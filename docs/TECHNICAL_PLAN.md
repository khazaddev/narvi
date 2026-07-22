# Narvi — Technical Implementation Plan

> **Audience**: AI coding agent (and human reviewer). This document is self-contained: it carries every architectural decision, invariant, and contract needed to implement the system without access to the original design discussions.

## 0. Context

Narvi runs autonomous coding agents in isolated cloud sandboxes, triggered from the web, Slack, Linear, and GitHub. This document is the self-contained specification for building it in Go. Its design is uncompromising on the properties that make an agent platform trustworthy at scale: one authoritative source of state, a single owner per session, native process supervision, and a resilience test suite (§9.3) that is a first-class exit criterion, not an afterthought.

**What we are building**: two Go services (control plane + in-sandbox agent), packaged as containers and deployable on any Kubernetes cluster or plain Docker/VMs — no cloud lock-in; Postgres as the single source of truth; S3-compatible object storage for media; and a web UI (built in phase 6 from the mockups in §12). The wire contracts in §6 are defined up front, so backend and UI are built against the same generated schemas.

**Non-goals for the initial build** (phases 0-5): the web UI (built in phase 6, §12); a replacement for OpenCode (the agent engine — Narvi wraps it); sandbox providers beyond Modal and RWX (the interface must allow adding them later — a Kubernetes-native sandbox provider is an anticipated adapter); multi-region.

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
- **Two-phase terminalization**: a watchdog never writes `failed` directly. It writes `suspect` and arms `terminal_grace` (default 60s). Any liveness signal during grace returns to previous state. A genuinely late success (e.g. `execution_complete` arriving after terminalization) **reconciles**: turn marked completed, session status re-derived, automation run counters corrected.
- **Circuit breaker**: 3 permanent spawn failures within 5 min blocks spawning for that session. Unknown provider errors default to **transient**, never permanent — a novel transient failure must not trip the breaker.

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

Implement: `modal` (via its API; sandbox env passed as one `SESSION_CONFIG` JSON document — the provider never assembles env fragments) and `rwx`. All Modal traffic goes through the configurable egress proxy.

### 4.2 AgentRuntime (OpenCode anti-corruption layer — see §7)

```go
type AgentRuntime interface {
    StartTurn(ctx, TurnSpec) (ConversationID, <-chan AgentEvent, error)
    ResumeConversation(ctx, ConversationID, TurnSpec) (<-chan AgentEvent, error)
    Stop(ctx) error
}
```

### 4.3 Others

`SourceControl` (GitHub + GitLab: createPR, credential minting, push specs), `Notifier` (Slack/Linear/GitHub comment delivery — consumed via outbox only), `IntentClassifier`, `LLM`, `BlobStore`, `SessionStore`/`TurnStore`/`SandboxStore` (sqlc-backed), `Outbox`, `TimerScheduler`, `Clock` (injectable everywhere — no `time.Now()` in domain).

## 5. Cross-cutting invariants

### 5.1 Persistence
- Postgres is the ONLY store. No cache with authority. Uniqueness by constraints, not convention.
- **Outbox pattern** for every outbound side effect (Slack/Linear/GitHub notifications, webhooks): written in the same tx as the state change; a retry worker delivers with exponential backoff + dead-letter after N attempts. Never 2-attempts-then-drop.
- Dedupe/coalescing (webhook events, concurrent PR @mentions) via `INSERT ... ON CONFLICT` atomic claims. Never eventually-consistent storage for coordination.

### 5.2 Security
- Internal auth: single HMAC helper (`platform/auth`), bearer `timestamp.signature`, 5-min window, fail closed. **Separate secrets per direction** (sandbox→CP, CP→bots, webhook ingress) so one rotation doesn't touch everything.
- Sandbox tokens: hashed at rest, one per gen, rotated on identity rotation with a **previous-gen grace window** during overlapping spawns.
- Git credentials: never long-lived in sandbox. `sandbox-agent` implements a git credential helper that POSTs to CP `/sessions/{id}/scm-credentials` (sandbox bearer), caches to disk with flock, 5-min expiry buffer, scoped https+host only. Never fall back to stale cache.
- Agent policy enforcement is **server-side**: review verdict posting, formal PR review submission, and raw-comment blocking are CP endpoints with policy checks (verdict floor, scoping to review sessions) — never prompt-only invariants.
- PR diffs and external content are untrusted input: wrap them in delimited blocks and treat them as data, never as instructions.

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
- Phasing: adapter-side tagging is Step 17 (OpenCode adapter, alongside the other quirks on this line); UI rendering of sub-task lanes is Step 59 (session timeline, lane nesting) and Step 60 (session rail, cost-breakdown roll-up) — see §12.2 item 1.

## 8. Feature set (exit criteria, not options)

1. **Plan mode**: persistent plans, HITL approve/reject on web/Slack/Linear/GitHub, server-side implementation dispatch on approval, plan/build model split, cross-channel verdict + archive notifications.
2. **Code review**: review sessions per PR with session reuse; atomic claim coalescing of concurrent @mentions; risk-map verdict with `review:*` labels; test-coverage & doc-drift sentinels; **server-side** verdict floor + formal-review gate + verdict-posting tool (raw issue comments blocked, scoped to review sessions); re-trigger via label/button; inline diff pre-fetched into context (agent must not need to run `gh pr diff` repeatedly); suggestion safety (apply via validated endpoint); label-driven auto-approval (`visual-qa: pass/skip`, `review: low risk`); dedicated review model selection; optional sentinel auto-fix for coverage/doc-drift findings, merge-gated on the origin PR (§17, disabled by default).
3. **Unified intent classifier** (detailed design — see §18): review-vs-request and plan-vs-build across all ingress surfaces; shadow mode (log-only) → active, permanently available, never a one-time launch gate; never-throw contract with an enumerated fallback-reason taxonomy; confidence rubric anchored on textual directness, not model self-reported certainty; DB-backed editable prompt templates with assembled-prompt preview; per-session routing decision records (§18.4).
4. **Automations**: GitHub/Linear/webhook/cron triggers with condition builder; sandbox settings honored on automation sessions; creator/status filters; `last_run` + `artifact_summary` populated; per-automation env vars/secrets.
5. **Enterprise sandbox glue**: cloud credentials via OIDC (provider-agnostic), kubeconfig injection for the target cluster, Docker-in-sandbox, egress proxy, repo/environment/global secrets, OpenCode config storage + injection, toolchain in images (Playwright+Chromium, ripgrep, typescript-language-server).
6. **Files**: uploads to object storage (S3-compatible) + `download_file` tool in sandbox; failed-upload UX signal.
7. **Recovery UX**: relaunch-and-resume (conversation id replay), resume-in-place on live sandbox, Slack/Linear retry buttons, warm-on-type (composer keystrokes pre-warm a sandbox; must not create orphan sessions).
8. **Models**: Anthropic + OpenAI/Codex (ChatGPT OAuth plugin) + reasoning-effort plumbing (per-session and per-message overrides).
9. **RWX previews**: PR preview links dispatched at latest PR commit.
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
4. `execution_complete` arrives AFTER terminalization → state reconciled, automation counters corrected.
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

**Phase 4 — Code review & automations (2 wks)**
Full §8.2 code review; automations engine + sweeps; RWX provider + previews; uploads; secrets scopes; model catalog + Codex OAuth.
*Exit: code review in shadow on live PRs; verdicts reviewed for precision.*

**Phase 5 — Rollout (1-2 wks)**
Config setup (automations, secrets, environments, settings, integrations); cohort-based rollout of sessions; operational dashboards and runbooks; SLO alerts wired.
*Exit: platform serving production traffic under monitoring.*

**Phase 6 — Web UI (~3-4 wks; see §12)**
The SPA on the generated contracts, embedded in the control-plane binary.
*Exit: all nine views in §12.2 built to the mockups (including the decision-inbox home, §16) + the UX items (§12.3); screenshot-level review against the mockups; `make dist` produces the single self-contained binary.*

## 11. Working conventions for the implementing agent

- Never put I/O, `time.Now()`, or randomness in `/internal/domain` — inject `Clock`/`IDGen`.
- Every state transition goes through the machine's `Transition(from, trigger) (to, error)` table — no ad-hoc status writes, enforced by making status columns writable only via the store methods that take a transition proof.
- Every new timeout/interval goes in `platform/timeouts.go` — grep-test in CI forbids `time.Second * N` literals outside `platform` and tests.
- Table-driven tests; race detector always on (`go test -race`); `errgroup` + context for all concurrency; no naked goroutines (lint rule).
- When behavior is ambiguous, resolve it against the mockups and the §6 contracts, and keep the domain paths single: repos are always a list, tokens are always hashed, one status taxonomy.
- Commit per coherent unit with tests; keep `main` green.

## 12. Web UI (phase 6)

Design mockups of the nine views exist (decision inbox/home, session workspace, code review, release review, plan mode, automations, settings, analytics, sign-in) and are the visual spec; ask the requester for the artifact if not provided. The mockups do not necessarily cover every individual screen or state phase 6 will need (an empty state, an error state, a secondary modal not explicitly drawn) — any such screen must be derived from the same visual design system the mockups establish (tokens, typography, layout, component patterns), never designed independently of it; §11's own resolve-ambiguity-against-the-mockups rule extends to this.

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
6. **Analytics**: my-sessions/all + date-range filters; KPI tiles (sessions, success rate, **false failures** = watchdog kills later proven alive with target 0, cost + median per session, boot p95 with sparkline); sessions-per-day stacked by outcome (status colors, legend + hover, 2px spacers); cost-by-model horizontal bars; Review finding outcomes (accepted/rebutted/dismissed); top typed failure reasons linked to sessions.
7. **Sign-in** (§13.1): "Continue with GitHub" primary + "Continue with SSO (OIDC)" secondary; identity auto-link status panel (github/slack verified, linear pending with in-channel link); allowlist note; mention that the GitHub token also drives PR attribution.
8. **Decision inbox — the home view** (§16): sectioned queue (Ready to merge / Needs your review / Awaiting your approval / Needs attention) with per-row inline actions (Merge, Open review, Approve & build, Assign to engineering, Resume), assignment provenance printed on every row, age with stale highlighting, repo filter only (the inbox is inherently scoped to the signed-in user — a "Mine" filter there would be redundant), median time-to-decision in the toolbar. Sessions list becomes the second tab.
9. **Release review** (§15): manifest table of constituent PRs (review state, CI at merge SHA, flags for admin overrides / red-at-merge / unreviewed reverts), aggregate-diff trigger banner showing why the conditional pass fired, composition findings with Block release / Acknowledge & ship actions.

### 12.3 UX items to land with the UI
Boot progress phases instead of spinner; failure reason + resume everywhere (matching the Slack/Linear retry affordance); distinct cancelled/failed/timeout chips; sandbox "what happened" panel (transitions + fingerprint + correlation id).

### 12.4 Sequencing & exit
Built in phase 6. Definition of done: all nine views built to the mockups + §12.3 items; screenshot-level review against the mockups; `make dist` produces the single self-contained binary.

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
4. Zero or multiple matches → **never guess**: create a link prompt and reply in-channel with a short-lived magic link ("connect your account"); the action proceeds with bot attribution until linked.
5. Manual link/unlink in Settings → Members; admin can force-link.

Failure rules: a provider email-API failure is a **retryable error, not an empty identity** — retry with backoff and keep the last known value; never null-out an email on transient failure (nulling it breaks both sign-in and identity linking).

Every cross-channel action (prompt from Slack, plan approval from Linear, review re-trigger from a GitHub comment) resolves to a `user_id` before it reaches the domain; unlinked actors get bot attribution + a link prompt.

### 13.3 RBAC
Roles (global, one per user): **admin > maintainer > member > viewer**.

| Permission | admin | maintainer | member | viewer |
|---|---|---|---|---|
| View sessions / analytics | ✓ | ✓ | ✓ | ✓ (read-only) |
| Create sessions, prompt, approve plans on own/joined sessions | ✓ | ✓ | ✓ | — |
| Stop/resume any session; approve any plan | ✓ | ✓ | — | — |
| Manage automations, environments, repo/env secrets | ✓ | ✓ | — | — |
| Edit review verdicts; re-trigger reviews; label-driven auto-approve config | ✓ | ✓ | — | — |
| Integrations, global secrets, prompt-template activation, members & roles, sentinel auto-fix toggle (§17 — stricter than label-driven auto-approve since it ends in an unattended merge, not a human Merge click) | ✓ | — | — | — |

Enforcement — **server-side only, channel-agnostic**:
- `domain/authz`: a table-driven `Authorize(actor, action, resource) error` — the matrix above lives in the domain as data with exhaustive tests. Every state-changing actor command (session actor mailbox, plan approval, verdict edit, automation toggle) calls it, so a Slack approval passes exactly the same check as a web one.
- HTTP middleware handles the coarse route-level gate; WS `subscribe` applies visibility rules. The UI hides what the role can't do, but the server is the authority.
- **Viewer guard**: viewers never gain PR-reviewer attribution or git identity on session artifacts.
- **Audit log**: `audit_log(actor_user_id, action, resource_type, resource_id, detail_json, correlation_id, created_at)` written in the same transaction as the change; surfaced in Settings → Members ("Audit log").

### 13.4 Phasing
- **Phase 1**: GitHub OAuth + cookie sessions + `users` table + role skeleton (admin/member) + route middleware — needed before the first end-to-end session.
- **Phase 3** (with bot ingress): identity auto-linking + link prompts, full four-role matrix, channel-agnostic Authorize on plan/review actions, audit log.
- **Phase 6** (UI): sign-in page, Settings → Members & access (role management, linked-identity chips with pending/resend, audit log view) — mocked in the design artifact.
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
- Handoff sentinel v1: extends the review sentinels — Phase 4 (alongside Step 40).
- Handoff v2 (child-session escalation): optional; add later in Phase 4 or beyond if the volume of v1 handoffs justifies the extra complexity.
- UI (Phase 6): Settings → Environments gains a path-scope + services editor; sessions can be filtered/labeled by prototyping provenance; the handoff sentinel surfaces inside the code-review view (§12.2 item 2).

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

### 15.4 Phasing
Extends the code-review domain and review-session reuse (§8.2, Step 41) plus the intent classifier (§8.3, Step 36) — Phase 4, alongside the rest of the sentinel family (Step 40/41/43/44). No new domain package, no new state machine. UI: a dedicated release-review screen (§12.2 item 9, mocked in the design artifact) — manifest table + trigger banner + composition findings.

## 16. Decision inbox (home view — new capability)

Problem this solves: the session-centric UI answers "what are the agents doing?" — an observation surface. But in an agent-heavy workflow the humans are the serial bottleneck: merges of auto-approved PRs, reviews, plan approvals, recoveries all wait on a person, scattered across GitHub notifications, Slack threads, and the sessions list. The home screen becomes a **queue of pending decisions addressed to the signed-in user**, each row carrying its action inline. Sessions remain one click away for watching execution; watching is no longer the default job.

### 16.1 Item taxonomy
Each row is a pending human decision — with one narrow, admin-toggled exception (sentinel auto-fix follow-up PRs, §17, disabled by default) that merges without appearing here at all, precisely because there is no decision left for a human to make once its own checks pass. Otherwise, every row is one of:
- **ready_to_merge**: open PR authored by a platform session, auto-approved under the label policy (§8.2: `review: low risk`, `visual-qa: pass/skip`), CI green at head, and assigned to the user — directly, as requested reviewer, or via CODEOWNERS. Action: Merge.
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
- **Phase 4**: read model + endpoints (it aggregates code review, plans, automations — they must exist first); `SourceControl` extensions.
- **Phase 6**: the inbox is the **home view** of the new UI (mocked in the design artifact, decisions 32-34); sessions list moves to the second tab.

## 17. Sentinel auto-fix (new capability)

Problem this solves: coverage and doc-drift sentinel findings (§8.2) are almost always mechanical to fix — add the missing test, update the stale doc — yet today a sentinel finding sits as a review comment a human must action manually, adding a full extra round-trip for exactly the kind of low-risk change that shouldn't need one.

### 17.1 Trigger and scope
Fires when a review verdict (§8.2) contains a finding from the test-coverage sentinel and/or the doc-drift sentinel — no other sentinel or finding type triggers this (in particular, the handoff-readiness sentinel, §14.4, is unrelated and unaffected). **Disabled by default**; a per-repo toggle enables it (Settings → Environments, §12.2 item 5), **admin-only** (§13.3) — a stricter gate than label-driven auto-approve config, because that mechanism still ends in a human Merge click (§16.1) while this one does not; the risk delta justifies the stricter row. **No recursion**: a PR opened by a sentinel-auto-fix child session is never itself eligible to trigger another sentinel auto-fix, regardless of what its own review verdict finds — this is an explicit rule, not a depth-counter side effect.

### 17.2 Fix session
On trigger, the review session spawns a child session (existing mechanism, §14.4 v2 — `parent_session_id`/`spawn_depth`) in the origin PR's own environment (full access — sentinel fixes touch test/doc files, never a scoped prototyping environment), pre-loaded with the origin diff and the specific sentinel finding(s), started directly in build mode (no plan-mode gate: this is mechanical remediation, not a design decision, and the safety net is downstream, §17.4). It pushes a branch and opens a PR **against the origin PR's own branch, not `main`** — a stacked PR, since the fix has to apply on top of the code it's fixing, and the origin PR hasn't merged yet when this happens.

### 17.3 Verdict update
Once the fix PR exists, the original verdict is updated (via the same server-side verdict-posting tool, §5.2 — never a raw comment) to reference it: which finding(s) it addresses, and that it will merge automatically once the origin PR merges. From this point, the finding's manual Apply-suggestion action (§12.2 item 2) is suppressed — the two remediation paths are mutually exclusive per finding, so a human and the automation can never both act on the same finding.

### 17.4 Merge gating
The fix PR does not auto-merge on its own CI-green — it waits for a merged-PR event on the origin (GitHub ingress webhook). On that event, the fix branch is **not** simply re-targeted at `main`: only the fix session's own commits are cherry-picked onto the current tip of the default branch and force-pushed to the fix branch. A bare base-retarget would only produce a clean, fix-only diff when the origin merged via a merge commit — under squash or rebase-merge the origin's original commits are never reachable in `main` by the same hashes, so retargeting would resurface the *entire* origin diff. Cherry-picking just the fix commits is merge-strategy-agnostic and is the only form of this operation this feature performs. If the cherry-pick conflicts, that is a hard stop, never an auto-resolve — the fix PR falls through to 17.4's failure path below.

This is a **system-initiated action, not a delegated human one** — it does not call `Authorize(actor, ...)` (§13.3), because there is no actor. It instead re-checks, explicitly, the same facts a human clicking Merge would rely on: the cherry-picked diff touches nothing but test and documentation files, CI is green at the new tip, the cherry-pick applied cleanly, and the toggle is still enabled. **Only if all four hold** does it auto-approve under the existing label policy (§8.2) and merge. Any one failing (scope grown beyond tests/docs, CI red, a conflicting cherry-pick, toggle flipped off mid-flight) leaves the fix PR as an ordinary `needs_review` item (§16.1) instead of forcing it through — the fallback path is always a normal, human-supervised one.

### 17.5 Audit and visibility
The merge is recorded in `audit_log` (§13.3) with `actor_user_id` NULL — using the same allowance already made in the audit_log schema for actions with no human actor — and `action`/`detail_json` capturing the origin PR, the review session, the fix PR, and which of the four checks passed. If the origin PR itself is never merged (closed, abandoned), the fix PR is simply left open as an ordinary review item — never silently discarded.

### 17.6 Phasing
Extends the code-review domain and sentinel family (§8.2, Step 40/43) — Phase 4, after the sentinels themselves exist; reuses child sessions (§14.4), the verdict-posting tool, and the label-auto-approval policy, so no new subsystem. UI: the toggle (Settings → Environments) and the fix-PR link on finding cards (§12.2 items 2 and 5) are mocked/built in Phase 6 alongside the rest of those views.

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

Timeouts: rely on the LLM client's own request-timeout option/error; never race a manually-armed `context.WithTimeout` against it as a second, redundant layer — the SDK's own internal abort always resolves first, so an outer wrapper timeout would never actually fire. The actual value lives in `platform/timeouts.go` (§5.4), not as a literal here.

### 18.2 Confidence rubric
Anchor confidence on **how directly the input text supports the decision** — never on how certain the model reports feeling (a rubric asking the latter degrades to reporting "high" almost unconditionally):
- **high** — a clear, direct textual signal (even via a well-known synonym) that an attentive reader would not second-guess.
- **medium** — a reasonable inference from context, tone, or indirect phrasing that an attentive reader could plausibly read differently.
- **low** — no strong signal; the input plausibly supports more than one reading.

This rubric is a **single shared constant**, referenced by every ingress surface's classification call — never duplicated per surface (duplication is exactly how it drifts). It lives at the field-description level of the classifier's structured-output schema, next to the field it governs, not floated separately in a system prompt.

`needsClarification` is **derived in application code** from confidence plus how many plausible targets exist — never asked of the model directly, keeping the threshold a versionable, testable piece of code rather than model behavior. For any action that is irreversible once taken (triggering a review, dispatching a build), the classifier's signal must be corroborated by an independent deterministic check (a regex or label match) before acting; on disagreement between the two, ask for clarification rather than guessing.

### 18.3 Calibration methodology
Automated shadow-mode divergence reporting (§9.4) catches wrong *routing* decisions, but not a miscalibrated `confidence` field on an otherwise-correct decision (e.g. everything reported "high" regardless of actual ambiguity in the input) — that failure mode only surfaces via periodic **manual spot-review** of a confidence-labeled shadow sample, cross-referenced by `correlation_id` against the deterministic-fallback path's own decision on the same input. Both methods are required for calibration sign-off; an automated divergence rate alone is not sufficient.

### 18.4 Per-session routing decision record
One record per session, `IntentDecisionRecord`: `session_id`, `surface` (web/slack/linear/github), `source` (classifier/explicit/fallback), `target`, `mode`, `confidence` (nullable, classifier source only), `reasoning` (nullable, truncated to a bounded length — never rejected outright for being long, just cut off), `decided_at`, `decided_at_stage` (`create` | `first_prompt` — some surfaces have the full text at session creation, others, e.g. web with warm-on-type, only at the first real prompt), `cost_usd` (nullable — the classifier's own LLM call has a real cost; omit rather than guess when unknown, same discipline as the model catalog's cost data, §8.8).

`reasoning` is **stored for audit** (it lands in the same audit-minded posture as `audit_log`, §13.3) but **never rendered on any Slack/Linear/GitHub-facing surface** by default — same untrusted/sensitive-output handling discipline §5.2 already applies to PR diffs and external content. This resolves the storage-vs-exposure question explicitly rather than leaving it to whatever an implementation happens to do: store it, don't broadcast it.

Persisted **write-once via a guarded update** (`UPDATE sessions SET intent_decision = ... WHERE intent_decision IS NULL`), not read-then-write — first decision wins, no application-level lock needed. A decision record supplied by the calling surface is honored **only** for `spawn_source` values architecturally capable of having classified it themselves; this check is server-side and never trusts a client-supplied claim (§5.2) — anything else is silently re-synthesized server-side.

### 18.5 Shadow mode is permanent, not a launch gate
See §9.4. Activating the classifier on a surface (shadow → acting) must never delete the shadow code path, its config, or its telemetry — the same mechanism gets reused for every future model swap, prompt change, or new ingress surface, not just the first one. Skipping the shadow-mode window for a change because "tests already prove equivalence" is not a default; it requires an explicit, documented exception.

### 18.6 Phasing
Detailed design underlying §8.3 (Step 36, phase 3) and the Settings → Prompt templates screen (§12.2 item 5, phase 6). No prior art exists (anywhere referenced in this document's research) for the DB-backed template storage/versioning/assembled-prompt-preview piece — it is designed from scratch when Step 36 is implemented, using this section's contract, rubric, and record schema as the foundation underneath it.
