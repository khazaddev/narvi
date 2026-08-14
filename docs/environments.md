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

## Two more ways a `setup.sh` rerun is avoided or shortened (§19.6)

Section §19.6 of `docs/TECHNICAL_PLAN.md` (Step 43) adds two additional, automatic-or-optional tiers between "skip `setup.sh` entirely" and "rerun all of `setup.sh`" — both are **shipped**, ungated. Neither changes the `setup.sh` idempotency requirement above: it still governs any boot where neither tier below applies, and the delta script below has an idempotency requirement of its own.

### Tier 1: the dependency-manifest-digest skip (automatic, no repo-side contract)

Every shared image's own `/narvi/image-manifest.json` now also carries a `dependency_manifest_digests` map, alongside `built_repo_shas` — one digest per repo, computed over the content of whichever of its known dependency-lockfiles (`package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `requirements.txt`, `Pipfile.lock`, `poetry.lock`, `go.sum`, `Cargo.lock`, `Gemfile.lock`, `composer.lock`) are present at the repo's root. At boot, sandbox-agent recomputes the same digest against the checked-out tree; if it matches the baked value, `setup.sh` is skipped entirely — even though `workspaceMoved` is true — because the match proves the dependency surface itself never changed, only ordinary code did.

This tier needs nothing from a repo author: it is fully automatic, and an unreadable, missing, or unrecognized digest (an older image built before this tier existed, for example) simply falls through to the next tier, never a silent skip.

### Tier 2: the delta script (`sync.sh`, optional, repo-authored)

A repo may add an optional `sync.sh` at its root, alongside `setup.sh`/`start.sh`, in the same closed hook vocabulary (`internal/domain/sandboxboot.Hook`) — there is still no way to register an arbitrary fourth hook name. When present, `sync.sh` runs **instead of** a full `setup.sh` rerun whenever the workspace has moved (`workspaceMoved == true`) but `setup.sh` *itself* is byte-identical between the image's own built SHA and the current checkout (`git diff --quiet <built_sha> HEAD -- setup.sh` is clean) — the reasoning being that if your own install/provisioning *logic* never changed, whatever it would have done on a full rerun is exactly what `sync.sh` is meant to do more cheaply, using whatever repo-specific knowledge you have about what actually needs re-syncing (e.g. `npm install` only when the lockfile itself moved, skipping steps you know are unaffected by an ordinary code-only commit).

**Requirements on `sync.sh`, mirroring `setup.sh`'s own contract above:**

- It must be safe to run on every eligible warm boot — idempotent and incremental, the same property required of `setup.sh` itself.
- A `sync.sh` failure is **never fatal** and does not block the boot: sandbox-agent falls back to running the full `setup.sh` instead, and if *that* also fails, the boot still continues (a warning is logged either way, with the failing script's own output captured). Do not treat a `sync.sh` failure as something your script needs to guard against escalating — the platform already guarantees it can't.
- `sync.sh` is optional. A repo with no `sync.sh` keeps today's exact behavior — full `setup.sh` reruns on every `workspaceMoved` boot the digest-skip tier (above) doesn't already cover — byte for byte, whether or not this tier exists.
- Eligibility is judged purely on whether `setup.sh` changed, not on whether `sync.sh` itself did — editing `sync.sh` alone (with `setup.sh` untouched) does not disable this tier.

Every one of these decisions (skip / delta / full / falling through because a check was itself inconclusive) is logged, so a boot's own dependency-work path is auditable from the boot log alone, not just its final outcome.

## See also

- `docs/TECHNICAL_PLAN.md` §6.4 — the sandbox boot contract (boot modes, hook policy) this page's hook requirements amend.
- `docs/TECHNICAL_PLAN.md` §19 — the full warm-boot design (shared images, the freshness pump, this rerun contract, and the graduated rerun ladder, §19.6).
- `internal/domain/sandboxboot/hook.go` — the actual `EvaluateHook` policy table implementing the contract described above.
