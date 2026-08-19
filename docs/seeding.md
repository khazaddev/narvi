# Config/data seeding

`control-plane seed` (Step 75, "config/data seeding", `docs/IMPLEMENTATION_PLAN.md` row 75, `docs/TECHNICAL_PLAN.md` §10-P6/§13.4) applies an operator-authored **seed manifest** — participants, secrets, automations, repo settings, and RWX preview config — to a live Narvi deployment. It is the tool that opens Phase 6's rollout: everything it writes rides the same tables and the same validation/encryption machinery every other write path in this codebase already uses (`internal/adapters/outbound/postgres`, `internal/domain/sandboxsecret.ValidateName`, `platform.EncryptToken`) — never a second, parallel mechanism.

The manifest itself is **customer data, not system data**: a file you author for your own deployment, kept outside this repository (or in a gitignored, deploy-specific location). `deploy/seed/example.yaml` is a fully-fictional template — copy it, replace every value, never commit real secrets or real people's data.

## Usage

```sh
control-plane seed -manifest ./my-manifest.yaml -dry-run   # preview: computes and prints the plan, writes nothing
control-plane seed -manifest ./my-manifest.yaml            # applies it for real
```

`seed` applies embedded migrations itself (the same mechanism `serve` uses), so it works against a brand-new database with no prior `control-plane serve` run required. It needs the same environment variables `serve` does — in particular `NARVI_DATABASE_URL`, `NARVI_TOKEN_ENCRYPTION_KEY`, and (for participants) `NARVI_INITIAL_ADMIN_EMAILS`.

Exit code is non-zero if any manifest entry fails (setup error, e.g. a malformed manifest) **or** if any individual item in the report errored — the report is always printed first, so you can see exactly what happened before deciding what to fix.

## Manifest sections

See `deploy/seed/example.yaml` for a complete, annotated, fictional example. Summary:

| Section | Table(s) | Idempotency |
|---|---|---|
| `participants` | `users`, `identities` | create-once (see below) |
| `secrets` | `sandbox_secrets` | create-if-absent |
| `automations` | `automations` | create-if-absent, matched by name |
| `repoSettings` | `repo_settings` | reconcile-to-declared, per field |
| `rwxPreview` | `repo_settings.rwx_preview_*` | reconcile-to-declared |

## Participants: how role assignment works

Every participant is identified **only** by their immutable numeric GitHub user id (`githubId` — the `id` field GitHub's own `GET /user` API returns, never a login/username: a login can be renamed and the freed name re-registered by someone else). There is no `role` field anywhere in the manifest schema, and the manifest format rejects an unrecognized key outright (an accidental or malicious `role:` entry fails to parse, it is never silently ignored).

Instead:

- Every participant defaults to **member**.
- A participant becomes **admin** at *creation time only* if their email (case-insensitive) appears in `NARVI_INITIAL_ADMIN_EMAILS` — the exact same environment variable, and the exact same matching logic, that a live GitHub OAuth first sign-in already uses. Editing that environment variable is the *only* way to change who gets admin from this tool; nothing in the manifest can.
- **If a participant's GitHub id is already linked to a user** (from an earlier seed run, or a real sign-in), this tool touches **nothing** about that user — not their role, not anything else. It does not matter whether `NARVI_INITIAL_ADMIN_EMAILS` has since changed, whether an admin promoted or demoted them through Settings → Members, or whether the manifest still lists them. Re-running seed is always safe with respect to an existing member's role: it can never silently grant or revoke admin.

If you need to change an existing member's role, use the Members API/UI (`PATCH /api/members/{userID}/role`) — that is the one, audited, human-in-the-loop path for it, and this tool deliberately never calls it.

## Secrets: why re-running never overwrites a value

`secrets` entries are **create-if-absent**: if a `sandbox_secrets` row already exists at the same (scope, name), the seed run skips it and leaves the stored value untouched. This is deliberate — a secret's value is stored as ciphertext an operator can't visually diff against the manifest's own plaintext, so blindly overwriting on every run would risk silently restoring an old value over one that was rotated out-of-band (for example, after a leak). To change an already-seeded secret's value, use the existing secret-management endpoint (`PUT /api/.../sandbox-secrets/{id}`), not this tool.

Every secret name is validated by the same fail-closed rule the rest of the platform uses (`internal/domain/sandboxsecret.ValidateName`): the `NARVI_*` namespace, the whole `OPENCODE_*` namespace, and every provider-credential/cloud-identity/cluster-binding name are all rejected. This tool supports `global` and `repo` scoped secrets only (see "What this tool does not seed" below for why).

## Automations: create-if-absent, matched by name

An automation is created only if no automation with the same `name` already exists. There is currently no way to *update* an existing automation's prompt, repos, or trigger config from any tool in this codebase (only pause/resume/webhook-token-rotate exist) — so re-running seed with a changed automation entry has no effect on an already-created automation; you'd need to rename it (creating a new one) or edit it through a future automations-editing surface once one exists.

`triggerType: webhook` mints a real webhook bearer token exactly once, at creation, and prints it in the run report — it is never shown again (the database only ever stores its hash). Copy it immediately if you need it.

Supported trigger types: `manual`, `cron`, `webhook`. `github` and `linear` triggers are not supported by this tool (see below).

## Repo settings and RWX preview: reconcile-to-declared

Unlike participants and secrets, `repoSettings` and `rwxPreview` entries are **reconciled to the manifest on every run** — a field you declare is written unconditionally, every time, exactly matching how these tables have always worked (there has never been a separate create-vs-update path for them; every existing write path is already an upsert). Concretely:

- A field you **omit** from a `repoSettings` entry is left completely untouched — a maintainer's own change to that specific field, made through the UI/API, survives a later seed run that doesn't mention it.
- A field you **do** declare is applied every run — if you don't want this tool to keep managing a field, remove it from the manifest (or accept that the next run will re-apply whatever the manifest says).
- `rwxPreview` entries are always applied in full (all three fields together) on every run.

## What this tool does not seed, and why

- **Standalone `environments` rows.** As of this Step, nothing in this codebase looks up an `environments` row by a caller-supplied id from outside its own creation transaction — an `environments` row only ever comes into existence inline, at session-creation time. Pre-seeding one today would be a permanently orphaned row with no consumer. The live equivalent of "environment-shaped" config is already on `automations` directly (`pathScope`/`mockConfigured`/`contractsPath` in the `automations` section above). If a later Step gives Environment a real reuse-by-id surface, this tool gains an `environments` section.
- **Slack/Linear workspace connections** ("integrations" in this codebase's other sense — `internal/domain/authz.ActionManageIntegrations`). These require a live, human-driven OAuth consent redirect with the third party; there is no value a manifest file could supply to fabricate that exchange. RWX preview settings are this tool's answer to Step 75's "integrations" checklist item instead: static, operator-known config with no OAuth step.
- **`github`/`linear` automation triggers, `cloud_identity_bindings`, `cluster_bindings`.** Deliberately out of scope for this Step — see `internal/app/seed/doc.go` for the full reasoning on each.

## Audit trail

Every real write this tool performs (a new participant, a new secret, a new automation, a repo-settings/RWX-preview change) writes one `audit_log` row in the same transaction as the change, visible in Settings → Members → Audit log. `actor_user_id` is `NULL` for every row a seed run writes (a system-driven batch action, not attributable to one human) — every row from one `seed` invocation shares a single correlation id, so you can find them all together.

## Dry run

`-dry-run` computes the exact same plan a real run would — every "does this already exist" check — but performs no write. Nothing is minted (a `-dry-run` webhook-trigger automation never gets a token), and nothing is encrypted. Recommended before every real run, especially the first one against a new deployment.
