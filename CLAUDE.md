# Narvi — instructions for the implementing agent

You are building the system specified in `docs/TECHNICAL_PLAN.md`, following the Step breakdown in `docs/IMPLEMENTATION_PLAN.md`.

## How to work

- **Read `docs/IMPLEMENTATION_PLAN.md` first**, then implement **one Step at a time, in order**. Each Step row references the technical-plan section (§) that specifies it — read that section before writing code. Each Step's number (e.g. Step 06) is the plan's own row number, not a GitHub PR number — the two only coincide by accident, so never assume they match.
- **Do not start Step N+1 until Step N's exit criterion is green.** The phase-end milestones (technical plan §10) are blocking gates.
- **Conventions are non-negotiable — see technical plan §11:** no I/O, `time.Now()`, or randomness in `/internal/domain` (inject `Clock`/`IDGen`); every state transition goes through the machine's transition table; every timeout/interval lives in `platform/timeouts.go`; table-driven tests; `go test -race` always on; `errgroup` + context for all concurrency; no naked goroutines; keep `main` green.
- **One Step = one PR = one coherent unit + its tests.** Small PRs; feature-flag incomplete paths. The PR title/branch should be named for the work itself (e.g. "dev-loop & internal auth"), not "Step 06" or "PR-06" — reference the Step number only in the PR/commit body, so it never gets confused with GitHub's own PR number.

## Commit, PR, and branch naming

Every commit message and PR title follows [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/):

```
<type>[(scope)]: <short, imperative description>

[optional body]

[optional footer(s)]
```

- **Merge commits are exempt** — GitHub's own auto-generated "Merge pull request #N from `<branch>`" subject is produced by the merge action itself, not authored per this convention.
- **Type** — pick the one that matches what actually changed: `feat` (new capability), `fix` (bug fix), `docs` (documentation only, no code), `chore` (tooling/scaffolding/maintenance with no production behavior change — e.g. repo bootstrap), `refactor` (no behavior change), `test` (test-only), `perf`, `ci`, `build`.
- **Scope** — the primary package or directory touched (`platform`, `postgres`, `contracts`, `sandbox`, `turn`, …), matching §1's repo layout naming; omit it if the change is genuinely repo-wide.
- **Breaking changes** — `!` right after the type/scope (e.g. `feat(contracts)!: …`) or a `BREAKING CHANGE:` footer. Reserve this for changes to an already-merged `/contracts` schema or any other interface other code already depends on — most Steps land behind feature flags before anything consumes them, so this should be rare early on.
- **Reference the plan's Step number in the body or a footer only** (e.g. `Refs: Step 06`) — never in the type, scope, or description. A Step's number is the plan's own row number, not the GitHub PR number the work becomes (see the note in `docs/IMPLEMENTATION_PLAN.md`'s intro) — writing "Step 06" or "PR-06" in a title invites exactly that confusion.
- **Branch names**: `<type>/<short-kebab-description>` — same type vocabulary as commits, no Step number (e.g. `feat/contracts-schemas-codegen`, `docs/sentinel-autofix`, `fix/config-log-level-default`). CI-enforced: the `branch-name` job fails a PR whose source branch doesn't match this pattern (Dependabot's fixed `dependabot/...` branches are exempt).

## Sources of truth

- `docs/TECHNICAL_PLAN.md` — the spec. Ports (§4), invariants (§5), wire contracts to preserve byte-for-byte (§6), the agent-runtime anti-corruption layer (§7), identity/RBAC (§13), product-prototyping workflow (§14), release review (§15), decision inbox (§16).
- `docs/design/mockups.html` — the visual spec for phase 6 (nine views, numbered design decisions). Match it at screenshot level.
- When a feature's exact behavior is underspecified here, resolve it against the mockups and the §6 contracts, implement it cleanly in Go, and keep the domain paths single (repos are always a list, tokens are always hashed, one status taxonomy).

## What NOT to do

- Don't reintroduce a second authority over session/sandbox state — Postgres is the single source of truth (§5.1).
- Don't skip the resilience scenarios (§9.3) — they are the phase-2 exit gate and encode the failure modes this design exists to eliminate.
- Don't couple a port to a single adapter. Interfaces in `/internal/app/ports` must hold for more than one implementation (e.g. the agent runtime and the sandbox provider are each expected to gain a second adapter).
