# Narvi — Implementation plan, broken into Steps

## Context

Narvi's technical specification (autonomous coding agents in sandboxes) is in
[docs/TECHNICAL_PLAN.md](TECHNICAL_PLAN.md) (§0–§16), and the nine-view UI design spec is in
[docs/design/mockups.html](design/mockups.html). This plan breaks the 7 phases (0–6) into **~65 ordered Steps**,
each individually shippable and CI-green, executable by a developer assisted by coding agents (Sonnet 5).
Every Step references the technical-plan section that specifies it. Each Step becomes exactly one PR when
implemented — but a Step's number (e.g. Step 06) is the plan's own row number, not the GitHub PR number it
becomes; the two are unrelated counters that will drift apart (e.g. a docs-only or hotfix PR consumes a GitHub
PR number without consuming a Step), so never write "PR-06" expecting it to mean GitHub PR #6.

**Assumptions** (challenge if needed): mono-module Go, trunk-based (small PRs to `main`, feature flags for
incomplete paths). The mockups ([docs/design/mockups.html](design/mockups.html)) and the §6 contracts are the
spec of record for behavior.

## Cross-cutting conventions (apply to every Step)

- One Step = one PR: one coherent scope + its tests; `go test -race` and lint green; no naked goroutines; no timeout literal outside `platform/` (lint-check from Step 02).
- Every state write = one transaction (transition + event + outbox) once the infra exists (Step 11).
- Steps marked ∥ can run in parallel (independent control-plane / sandbox-agent / UI streams).
- Every phase end = a demonstrable milestone (the exit criterion from technical plan §10).

---

## Phase 0 — Foundations (6 Steps)

| Step | Title | Content | Ref. |
|---|---|---|---|
| 01 | repo bootstrap | Go module, full hexagonal tree (`cmd/`, `internal/domain|app|adapters`, `platform/`, `contracts/`, `migrations/`, `test/resilience/`), Makefile, golangci-lint (no-naked-goroutine, no-time-literals rules), CI lint+test+race, README | §1 |
| 02 | platform: config & timeouts | Typed config validated at boot (fail-fast, named errors); `platform/timeouts.go`: single hierarchy + **invariant test** (provider cap > supervisor > bridge > SSE); lint-check forbidding `time.Second*N` elsewhere | §5.4 |
| 03 | platform: observability | `slog` envelope (correlation_id, session_id, sandbox_gen), correlation-id middleware, baseline OTel traces/metrics | §5.3 |
| 04 | postgres: core schema | Migrations: `users`, `identities`, `sessions`, `turns`, `sandboxes` (UNIQUE(session_id)) + `sandbox_history`, `events`, `session_timers`, `outbox`, `participants`, `artifacts`, `audit_log`; sqlc + store skeletons | §2, §5.1, §13.2 |
| 05 | contracts: schemas + codegen | JSON Schemas: sandbox WS, client WS, SESSION_CONFIG, REST DTOs; Go + TS codegen; round-trip tests (the `tokens` **object**-not-number case — dedicated test) | §6 |
| 06 | dev-loop & internal auth | HMAC helpers (3 distinct secrets per direction), `/health`, `make dev` (Postgres compose), `narvi serve` skeleton | §5.2 |

**Phase 0 milestone**: `make dev` boots, contracts round-trip green.

## Phase 1 — Core (15 Steps)

| Step | Title | Content | Ref. |
|---|---|---|---|
| 07 | domain/sandbox | Sandbox state machine (explicit transition table), **port the decision-function corpus + its test suite** (spawn/restore/resume, circuit breaker with unknown=transient, first-connect 240s / steady 90s budgets), `gen` fencing | §3.2 |
| 08 | domain/turn + session | Turn machine (single `processing`), queue, session-status derivation, `cancelled≠failed≠timeout≠never_started` taxonomy, synthetic `execution_complete` event | §3.1, §3.3 |
| 09 ∥ | domain/gitstate | Pure git machine: stash-if-dirty → checkout session branch → pop; branch-name normalization | §3.4 |
| 10 | domain: Environment scoping | Extend Environment with `path_scope` (globs) + optional `mock_config`; the clone (§09) runs `git sparse-checkout` per repo when a scope is present; provenance tag on sessions created under a scoped Environment | §14.1 |
| 11 | app/sessionactor | Goroutine+mailbox actor, hydration, advisory lock + epoch fencing, transactional writes, timer pump (`FOR UPDATE SKIP LOCKED`), named timers | §2 |
| 12 | ports + Modal provider | **Complete** `SandboxProvider` interface (§4.1), typed transient/permanent errors by code, HTTP client timeout > cold start, egress proxy, single SESSION_CONFIG document | §4.1 |
| 13 ∥ | sandbox-agent: supervisor | Static binary: boot modes + hook policy, native supervision (process groups, killpg, reaping, drain, bounded shutdown), **boot fingerprint logged first** | §6.4, §5.3 |
| 14 ∥ | sandbox-agent: services.yml | Declarative multi-service manifest (`.narvi/services.yml`): each service supervised by the **same** process-group/reap/drain machinery as Step 13 (no new supervision code); readiness (port/health) → named `boot_progress` phases; transparent fallback to `setup.sh`/`start.sh` when the manifest is absent | §14.2 |
| 15 ∥ | sandbox-agent: git + credentials | Ordered multi-repo clones + AGENTS.md, credential helper (flock, expiry buffer, https+host only, never a stale cache) | §6.4, §5.2 |
| 16 ∥ | sandbox-agent: WS bridge | WS client + **ack protocol** (6 critical types, 1000 buffer, re-send), 30s heartbeats + boot_progress, 401/403/404/410 fatal | §6.1 |
| 17 ∥ | OpenCode adapter | ACL: start server, prompt_async, SSE→AgentEvent translation (quirks §7), **CI contract tests against the pinned binary**, empty-catalog fallback | §7 |
| 18 | wshub: sandbox socket | Control-plane side: gen check, ack receipt, event persistence, liveness `last_seen_at` = max(signals) | §3.2, §6.1 |
| 19 | wshub: clients + session REST | subscribe/replay/fetch_history, close codes 4001/4002, 24h hashed ws-token; REST endpoints the UI needs (create/get/events/artifacts) | §6.2, §6.3 |
| 20 | auth v1 | GitHub OAuth (token = PR attribution), backend-issued host-scoped cookies, allowlist, admin/member role skeleton, route middleware | §13.1, §13.4 |
| 21 | e2e happy path | GitHub SourceControl adapter (createPR, credential minting, push spec), scm-credentials endpoint, end-to-end wiring; resilience test #1 (kill pod mid-turn) | §9.3 |

**Phase 1 milestone (go/no-go)**: end-to-end via the API — session → prompt → stream → push → PR.

## Phase 2 — Resilience (9 Steps)

| Step | Title | Content | Ref. |
|---|---|---|---|
| 22 | snapshots & restore | triggerSnapshot without the status save/restore hack, restore = new gen, post-turn snapshot | §3.2 |
| 23 | resume | Resume-capable path (stop/resume), resume-in-place on a live sandbox | §3.2, §8.7 |
| 24 | two-phase terminalization | `suspect` → 60s grace → terminal; **late-success reconciliation** (turn, session, automation counters) | §3.2 |
| 25 | reconciler + GC | 60s loop against the provider API, orphan reaping, orphans_reaped metric | §5.3 |
| 26 | image builds | Domain + workflow: fingerprint (SHAs+runtime), systematic fallback-to-base, rebuild with exponential backoff + streak alert | §8.5-note, §10-P2 |
| 27 | mocking + contract drift | Mock server declared as a service (Prism from `contracts/api/*`) or MSW switch; extended fingerprint (Step 26) to detect backend↔contract drift — never blocks, feeds the handoff sentinel (Step 44) | §14.3 |
| 28 | turn recovery | Conversation id **at turn start** + heartbeat, re-enqueue of interrupted turns, relaunch-and-resume API | §3.3, §8.7 |
| 29 | gitstate in-sandbox | Full integration in sandbox-agent: clean tree at build, stash/checkout/pop at boot, **sparse-checkout applied end-to-end for scoped Environments (Step 10)**, zero lost edits | §3.4, §14.1 |
| 30 | resilience suite | `test/resilience/` harness + **the 12 scenarios of §9.3** (2 PRs if large: harness then scenarios) | §9.3 |

**Phase 2 milestone**: the 12 scenarios green.

## Phase 3 — Ingress & routing (9 Steps)

| Step | Title | Content | Ref. |
|---|---|---|---|
| 31 | webhook toolkit | Signature verify, dedupe/claims `INSERT ON CONFLICT`, single `CreateSessionRequest` | §5.1 |
| 32 ∥ | GitHub ingress | PR mentions, events → automations, per-PR coalescing | §8.2 |
| 33 ∥ | Slack ingress | Thread↔session in Postgres, bidirectional mrkdwn contract, in-thread acks | §8.10 |
| 34 ∥ | Linear ingress | AgentSession webhooks, progressive AgentActivity, OAuth | §8.10 |
| 35 | outbox delivery | Slack/Linear/GitHub delivery workers, retry backoff, dead-letter, outbox-lag metric | §5.1 |
| 36 | intent classifier | Domain + DB-editable templates + assembled-prompt preview, **shadow mode** + decision logs, deterministic fail-open | §8.3 |
| 37 | plan mode (web) | Persistent versioned plan, approval, **server-side implementation dispatch**, plan/build model split | §8.1 |
| 38 | plan mode (cross-channel) | Slack/Linear verdicts via the same `Authorize`, first-wins + notify the other channels | §8.1, §13.3 |
| 39 | identities + full RBAC | Auto-linking (verified-email match, magic link if ambiguous, email-API retry), table-driven 4-role matrix + exhaustive tests, viewer guard, transactional audit log, members API | §13.2, §13.3 |

**Phase 3 milestone**: GitHub/Slack/Linear ingress live; classifier shadow report on real traffic.

## Phase 4 — Code review & automations (12 Steps)

| Step | Title | Content | Ref. |
|---|---|---|---|
| 40 | domain/review | Risk-map verdict, severities, **verdict floor in the domain** | §8.2 |
| 41 | review sessions | Per-PR reuse, atomic claim on concurrent mentions, re-trigger via label/button, inline pre-fetched diff | §8.2 |
| 42 | server-side verdict | Verdict-posting tool + raw-comment blocking (scoped to review sessions), `review:*` label sync, formal-review gate | §8.2, §5.2 |
| 43 | sentinels + suggestions | Coverage & doc-drift sentinels, apply-suggestion via validated endpoint, rebuttals → re-review reconciliation; sentinel auto-fix — child session opens a stacked follow-up PR, cherry-picked onto main and merge-gated once the origin PR merges, admin-only toggle off by default | §8.2, §17 |
| 44 | handoff-readiness sentinel | Dedicated review sentinel on PRs from scoped sessions (Step 10): uncontracted endpoints (Step 27), backend TODOs left by the agent; **v1** = `handoff` label + summary posted as a comment (+ optional linked issue); **v2 deferred** (child session + plan mode) only if handoff volume justifies it | §14.4, §14.5 |
| 45 | release PR review | Release detection (`release/*` branch/label, via classifier §8.3); **manifest** (always): list PRs merged since the last release point, check review + CI-at-merge-SHA + reverts for each, posted via the server-side verdict tool; **conditional aggregate diff** (pure rule: ≥3 PRs in the same subsystem, a critical-tier PR present, or a manually-resolved conflict), dedicated composition/interaction prompt, never a PR-by-PR re-review | §15 |
| 46 | automations: engine | Automation→invocation→runs, CAS failure strikes, auto-pause at 3, recovery sweeps | §3.5 |
| 47 | automations: triggers & extras | GitHub/Linear/webhook/cron conditions, sandboxSettings, `artifact_summary`, per-automation secrets | §8.4 |
| 48 ∥ | RWX provider + previews | RWX adapter, preview links at the latest PR commit | §8.9 |
| 49 ∥ | uploads | Object storage (S3-compatible) + `download_file` tool, failed-upload signal | §8.6 |
| 50 | models | Catalog, Codex/ChatGPT OAuth plugin, per-session/per-message reasoning effort; shadow-comparison tooling for review | §8.8 |
| 51 | decision inbox: read model + API | Read-only aggregation (plans, review sessions, sessions, automations, outbox) + `ListOpenPRsForUser` / `ResolveCodeOwners` on the SourceControl port (CODEOWNERS→persons via the identity graph §13.2, short-TTL cache with displayed staleness); ready_to_merge / needs_review / awaiting_approval / needs_attention taxonomy; per-item assignment provenance; Merge endpoint with **server-side re-validation at click time** (CI + approval + Authorize); decision-latency metric | §16 |

**Phase 4 milestone**: code review in shadow on live PRs, verdicts reviewed for precision.

## Phase 5 — Rollout (4 Steps)

| Step | Title | Content | Ref. |
|---|---|---|---|
| 52 | config/data seeding | Scripts to seed automations, secrets, environments, settings, integrations; participants→users mapping (by GitHub id, default member) | §10-P5, §13.4 |
| 53 | cohort rollout | Feature-flagged cohort rollout of sessions, documented rollback | §10-P5 |
| 54 | ops | Dashboards/alerts (false failures, outbox lag, orphans, boot p95), runbooks from the resilience catalog (§9.3) | §5.3 |
| 55 | launch readiness | Production checklist, SLO alerts wired, on-call runbook | §10-P5 |

## Phase 6 — UI (10 Steps, visual spec = nine-view mockups)

| Step | Title | Content | Ref. |
|---|---|---|---|
| 56 | ui bootstrap | Vite+React+TanStack, `go:embed` + `narvi serve`, light/dark theme tokens from the mockups, front CI | §12.1 |
| 57 | ui data layer | TS client generated from `/contracts`, WS transport → event-log → reducer | §12.1 |
| 58 | ui sign-in | GitHub primary, SSO OIDC, identity auto-link panel, allowlist errors | §12.2-7, §13.1 |
| 59 | ui session: timeline | Sidebar (status chips + session source, My sessions/All filter, boot n/m), typed-event timeline incl. sub-task lane nesting, failure cards + Resume | decisions 1-4, 31, §7.1 |
| 60 | ui session: composer & rail | Composer (model/effort/plan mode, warm-on-type), sandbox rail (transitions, gen, fingerprint, boot phases, artifacts, cost incl. sub-task roll-up) | decisions 5-6, §7.1 |
| 61 | ui code review + release review | Editable risk-map, sentinels, findings (apply/rebut), history, server-side badge; handoff-readiness sentinel (Step 44) on scoped sessions; **dedicated release-review screen: manifest + trigger banner + composition findings (Step 45)** | decisions 7-14, §14.4, §15 |
| 62 | ui plan mode + automations | Versioned plan + multi-channel approval bar; automations health/runs table | decisions 15-20 |
| 63 | ui settings + analytics | Environments/secrets/templates/members&access (roles, identities, audit); **path-scope + services editor for product Environments**; analytics (KPIs incl. false failures and decision latency, charts) | decisions 21-30, §14.1, §14.2 |
| 64 | ui decision inbox (home) | Home view: decision queue by section (merge/review/approval/attention), inline actions wired to the Step 51 API, assignment provenance, staleness, repo-only filter (the inbox is inherently user-scoped), median time-to-decision; the sessions list moves to a second tab | decisions 32-34, §16 |
| 65 | ui finalize | `make dist` single self-contained binary, screenshot review vs mockups, ship | §12.4 |

## Sequencing & parallelism

- **Parallel streams in phase 1**: control-plane (07-08, 09-12, 18-20) ∥ sandbox-agent (13-17) — converge at 21.
- **Phase 3**: 32/33/34 parallel after 31; 36-38 after 35.
- **Phase 6** can start 56-57 during phase 5 (backend and contracts are frozen by then).
- Go/no-go after Step 21 (~1 month).

## Verification

- Each Step: CI (lint, `go test -race`, contract tests) + its own criterion listed on its row.
- Phase milestones = blocking gates: e2e via API/UI (P1), 12 resilience scenarios (P2), classifier shadow report (P3), review-verdict diff reviewed for precision (P4), flag-reversible rollout (P5), 9 views built to mockups + screenshot review (P6).
- Project end: `make dist` produces the standalone `narvi` binary; all phase gates green.
