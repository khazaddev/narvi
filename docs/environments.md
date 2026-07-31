# Environments

An **Environment** in Narvi is a named repo set (one or more Git repositories, cloned together under `/workspace/{name}`) plus the boot hooks that prepare and start it. This page documents the requirements an Environment's own `setup.sh`/`start.sh` scripts must satisfy — requirements that exist because of how Narvi builds and reuses sandbox images, not because of any per-repo convention. It is the "environments docs" referenced by `docs/TECHNICAL_PLAN.md` §19.4 and §19.6.

## The two hooks

Every repo may define, at its root:

- **`setup.sh`** — installs dependencies (`npm ci`, `pip install`, `cargo build`, etc.) and does any other one-time-per-checkout provisioning.
- **`start.sh`** — starts whatever long-running service(s) the session needs.

Both are optional, closed-vocabulary hook names (`internal/domain/sandboxboot.Hook`) — there is no third hook, and no way to register an arbitrary script name.

## Requirement: `setup.sh` must be idempotent and incremental

**This is a named, first-class requirement, not an implementation detail buried in a Go doc comment.**

`setup.sh` **must** be safe to run more than once against the same checkout, and **must** be fast (a no-op or near-no-op) when nothing dependency-relevant has changed since its last run. Concretely:

- Re-running `setup.sh` against an unchanged tree must not fail, must not duplicate state (no repeated `git clone`/append-only config writes without a guard), and must not error out just because a dependency is already installed.
- Re-running `setup.sh` against a tree that has moved forward (new commits, but no dependency-manifest changes) should be dominated by your package manager's own warm-cache path (`npm ci`/`pip install`/`cargo build` against an already-populated cache is seconds, not minutes) — not a from-scratch install.

This is exactly the property mainstream package managers already give you for free when a lockfile/cache directory persists between runs; the requirement is that your `setup.sh` doesn't defeat it (e.g. by unconditionally `rm -rf`-ing a cache directory, or by exiting non-zero when it detects a prior run).

### Why this is required now

Narvi builds one shared, prebuilt sandbox image per Environment (per distinct repo set), keyed on each repo's clone URL rather than an exact commit SHA (`docs/TECHNICAL_PLAN.md` §19.1). That image is kept warm by a background refresh pump that periodically rebuilds it from each repo's current default-branch tip (§19.2), and a session's own boot-time checkout is synced forward from whatever SHA the image happened to be built at (`gitclone.SyncAll`) rather than always matching it exactly.

Because of this, **`setup.sh` now reruns on essentially every warm boot** — not just on a cold/fresh boot the way it used to. Concretely (`internal/domain/sandboxboot.EvaluateHook`, §19.4):

- On a `repo_image` boot, `setup.sh` is skipped only when the checked-out SHA still exactly matches the image's own baked `built_repo_shas` entry for that repo (`/narvi/image-manifest.json`).
- Otherwise (`workspaceMoved == true` — the common case, since the freshness-pump staleness window plus any ordinary branch activity makes an exact SHA match the exception, not the rule) `setup.sh` reruns, **non-fatally**: a failed rerun is logged as a warning and the boot continues, exactly per the "never block a spawn" invariant (§10 Phase 2) — a moved workspace proves nothing about whether dependencies actually changed, so it can never justify failing the boot outright.

A `setup.sh` written under the old assumption ("I run exactly once, at image build, never again") can break in exactly this new situation: appending to a config file on every run, cloning a vendor directory it assumes doesn't already exist, seeding a database without an existence check, or simply exiting non-zero when it detects one of its own prior side effects. Under the old contract this was invisible because `setup.sh` genuinely never ran twice against the same image. Under the current contract it reruns on nearly every warm boot, non-fatally — so a non-idempotent `setup.sh` degrades every warm boot to a silently-incomplete one (the workspace is left exactly as warm-cache-broken as the failed rerun left it, with only a log warning, never a boot failure) rather than failing loudly. This is the exact "non-idempotent-setup boot" scenario the resilience suite (§9.3) exists to bound — write your `setup.sh` to be re-runnable, and this failure mode never triggers.

## Planned: the delta-script contract (`sync.sh`, not yet shipped)

`docs/TECHNICAL_PLAN.md` §19.6 designs — but has **not yet shipped** — an optional middle tier between "skip `setup.sh` entirely" and "rerun all of `setup.sh`": a repo-authored delta script (working name `sync.sh`, alongside `setup.sh`/`start.sh` in the same closed hook vocabulary) that runs instead of full `setup.sh` when the workspace has moved but `setup.sh` itself is byte-identical between the image's built SHA and the current checkout.

This is Step 43 in `docs/IMPLEMENTATION_PLAN.md`, gated on real telemetry (§19.5(b)'s per-hook rerun-duration metric) showing full `setup.sh` reruns are actually eroding the warm-boot latency win in practice, rather than being built speculatively ahead of evidence. Until Step 43 ships, there is no `sync.sh` hook — every `workspaceMoved` rerun runs the whole of `setup.sh`, per the section above — and repo authors should not rely on the delta-script tier existing yet. This section is a placeholder for that contract, so Step 43 has a documented home to land its own requirements in when it ships, rather than needing to invent a new environments doc at that point.

## See also

- `docs/TECHNICAL_PLAN.md` §6.4 — the sandbox boot contract (boot modes, hook policy) this page's hook requirements amend.
- `docs/TECHNICAL_PLAN.md` §19 — the full warm-boot design (shared images, the freshness pump, this rerun contract, and the planned delta-script ladder).
- `internal/domain/sandboxboot/hook.go` — the actual `EvaluateHook` policy table implementing the contract described above.
