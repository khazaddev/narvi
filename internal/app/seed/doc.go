// Package seed implements Step 75's ("config/data seeding", §10-P6,
// §13.4) per-install seeding tool: it reads an operator-authored
// internal/domain/seedmanifest.Manifest and reconciles it against a live
// Postgres database via the SAME stores (internal/adapters/outbound/
// postgres) and the SAME domain validators/crypto (internal/domain/
// sandboxsecret.ValidateName, platform.EncryptToken) every other write
// path in this codebase already uses -- never a second, parallel
// mechanism. cmd/control-plane's own "seed" subcommand (seed.go) is the
// thin CLI wrapper: config load, DB pool, migrations, then Run below.
//
// # Why per-install data, not a migration
//
// This codebase already has a precedent for seeding rows FROM a
// migration: migrations/000057_workflows.up.sql inserts the three
// built-in workflow_definitions/steps/edges/bindings rows directly, with
// fixed UUIDs, because that data is FIXED SYSTEM DATA -- identical on
// every install, part of the product itself, never customer-authored.
// Automations/secrets/environments/settings/integrations are the
// opposite: every value is customer-specific (a customer's own repo
// list, a customer's own third-party token, a customer's own admin
// roster) and differs install to install. Putting THAT in a migration
// would mean editing a migration file -- a piece of schema history every
// environment applies identically and permanently -- every time an
// operator wants to add one automation or rotate one secret. This
// package is the other kind: a script an operator re-runs, pointed at a
// manifest file that lives OUTSIDE this repository (or in a
// deploy-specific, gitignored location) -- see deploy/seed/example.yaml
// for a fully-fictional template, never real data.
//
// # Idempotency semantics, decided per resource, not by one blanket rule
//
// "Safe to re-run" does not mean "every re-run is a no-op" -- see
// docs/seeding.md for the operator-facing version of this. Each
// resource's semantics below were picked to match what that resource's
// OWN existing store methods already support, not an arbitrary uniform
// policy:
//
//   - Participants -> users/identities/role: CREATE-ONCE. If an identity
//     already exists for a participant's GitHub id, this tool touches
//     NOTHING about that user -- not their role, not their display name.
//     internal/adapters/outbound/postgres.UserStore has no bulk/seed-safe
//     "update role" path other than UpdateRole, which is the SAME
//     admin-only, audited, human-in-the-loop action Settings -> Members
//     uses (internal/adapters/inbound/httpapi/members.go) -- this tool
//     never calls it. This is what makes "the role-granting path must
//     never escalate an existing user silently" true by construction: a
//     re-run that finds an existing identity does zero user-table
//     writes, full stop, regardless of what the manifest says that
//     participant's role should be (there is no such field -- see
//     seedmanifest's own doc comment).
//
//   - Secrets (sandbox_secrets): CREATE-IF-ABSENT. A secret's value is
//     stored ciphertext an operator cannot visually diff against the
//     manifest's own plaintext, so silently overwriting an existing row
//     on every re-run risks exactly the same "quiet revert" hazard as a
//     role change, just applied to a credential instead of a permission
//     -- e.g. a value rotated out-of-band after a leak would be silently
//     restored to its old, compromised value by the next seed re-run.
//     Rotating an already-seeded secret's value is a deliberate,
//     separate action -- use the existing PUT /api/.../sandbox-secrets/
//     {id} endpoint (internal/adapters/inbound/httpapi/sandboxsecrets.go),
//     not this tool.
//
//   - Automations: CREATE-IF-ABSENT, matched by name. AutomationStore has
//     no Update method for name/prompt/repos/trigger config AT ALL --
//     only Pause/Resume/RotateWebhookToken/UpdateLastRun exist. There is
//     no reconcile-to-declared option available even if it were wanted;
//     create-if-absent is this resource's only sound choice today.
//
//   - repo_settings / RWX preview: RECONCILE-TO-DECLARED. Both tables
//     have NEVER had any write path other than upsert (RepoSettingsStore.
//     Upsert/UpsertAutoMergeToggle/.../UpsertPreviewSettings) -- there is
//     no create-vs-update distinction to preserve, and admins are
//     already expected to change these repeatedly (the mockups' own
//     Settings -> Environments view). A field this tool's manifest
//     leaves nil/absent is left completely untouched (the column-scoped
//     Upsert* methods this package calls make that possible); a field
//     the manifest DOES declare is written unconditionally on every run,
//     the same as it always has been from any other caller. If an
//     operator wants a field to stop being seed-managed, they remove it
//     from the manifest (or manage it exclusively via the future
//     Settings UI/API from then on) -- documented in docs/design/
//     seeding.md, not silently assumed.
//
// # Scope decisions this Step deliberately made
//
//   - No standalone "environments" seeding. migrations/
//     000021_environments.up.sql's own doc comment, and migrations/
//     000055's own identical note, are both explicit that no
//     create/list/reuse-by-id surface exists ANYWHERE in this codebase
//     for the environments table today -- an environments row is
//     created inline, once, at session-creation time only, and nothing
//     ever looks one up by a caller-supplied id from outside that same
//     creation transaction. Pre-seeding a bare environments row would be
//     a permanently orphaned row with zero consumers -- a tool that
//     reports "success" while doing something structurally inert is
//     worse than one that names the gap. The live equivalent of
//     "environment-shaped" config TODAY is the sandbox_path_scope/
//     sandbox_mock_configured/sandbox_contracts_path columns Step 52 put
//     directly on automations (§8.4) -- this package's own Automations
//     section seeds exactly those. If a later Step gives Environment a
//     real reuse-by-id surface, this package gains an Environments
//     section powered by the same postgres.EnvironmentStore.Create
//     already wired into cmd/control-plane/main.go today.
//
//   - No OAuth-based "integrations" seeding. internal/domain/authz's own
//     ActionManageIntegrations is explicit about what that word means in
//     THIS codebase: "connecting/disconnecting a third-party integration
//     (Slack/Linear workspace, etc.)" -- backed by tables like
//     linear_installations (migrations/000031_linear_installations.
//     up.sql) whose own rows carry a REAL OAuth access+refresh token
//     pair obtained through a live, human-driven consent redirect
//     (internal/adapters/inbound/linear/callback.go). No manifest file
//     can fabricate that exchange -- there is no "value" an operator
//     could paste in. This tool's own answer to Step 75's "integrations"
//     checklist item is therefore RWX preview settings
//     (repo_settings.rwx_preview_*, Step 57, §4.1.2): static,
//     operator-known config (a dispatch key + endpoint template + org
//     slug) with an existing store method
//     (RepoSettingsStore.UpsertPreviewSettings) and, as of today, NO
//     other write path at all ("No admin-facing REST route calls this
//     yet" -- that file's own doc comment) -- making this tool the
//     first real way to populate it.
//
//   - cloud_identity_bindings / cluster_bindings (Step 73) are also out
//     of scope: both are environment-or-global scoped, and the
//     environment half of that inherits the exact same "no standalone
//     Environment" gap above; a global-only subset would be an
//     asymmetric, half-finished section. Left for a follow-up once
//     Environment itself is seedable.
//
// # Audit log
//
// Every actual write this package performs (a new user+identity, a new
// secret, a new automation, a repo_settings/RWX-preview upsert) writes
// one audit_log row in the SAME transaction as the change, reusing
// internal/app/auditlog.Record exactly like every other Authorize-gated
// state change in this codebase (§13.3's own "written in the same
// transaction as the change"). actorUserID is always the explicit
// invalid pgtype.UUID{} -- auditlog.Record's own doc comment names this
// case directly: "an explicit invalid pgtype.UUID{} for a bot/webhook-
// attributed change or a system-driven one". A batch import acting on
// behalf of many people at once has no single human actor to attribute
// to (unlike an OAuth sign-in, which attributes to the signing-in user
// themselves) -- this deliberately differs from internal/adapters/
// inbound/auth's own createUserAndIdentity, which uses the newly created
// user's own id as the actor of their OWN sign-in action. This tool sets
// every audit row's own correlation id to one fresh id per Run
// invocation (platform.WithCorrelationID), so every row a single seed
// run produced can be found together in Settings -> Members -> Audit
// log.
//
// # Dry-run
//
// Run's own dryRun parameter computes the exact same plan a real run
// would (every read, every "does this already exist" check) but performs
// no write of any kind -- decided in favor of building it because this
// tool's two most consequential actions (minting a role and writing a
// secret) are also its least visually reversible: unlike an ordinary
// admin UI action, there is no confirmation dialog and no single target
// to review before a whole manifest applies. A report a human can read
// BEFORE anything is written is the check this tool's own scale (many
// rows, one invocation) makes worth having; see report.go.
package seed
