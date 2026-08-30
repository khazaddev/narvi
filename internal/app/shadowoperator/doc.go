// Package shadowoperator implements the shadow-operator surface
// (§30.6, §30.8, §30.9): the repo-scoped read model an admin
// uses to evaluate what platform shadow mode did on their behalf, and
// the "Activate" graduation gesture that promotes a repository out of
// it.
//
// # A read model, not new state (§16.2)
//
// Every type and function in this package reads existing tables --
// shadow_scm_writes (internal/app/shadowledger's own append-only
// ledger), the outbox's own §30.8 epoch stamps, and turns.cost_usd (the
// SAME running total internal/app/sessionactor's own recordStepFinishCost
// already maintains). Nothing here writes a new row of its own except
// Activate's own repo_settings.live_egress_enabled flip and its
// audit_log entry -- both already-established writers (postgres.
// RepoSettingsStore.UpsertLiveEgressEnabled, internal/app/auditlog.Record),
// never a new table.
//
// # Never renders customer content
//
// shadow_scm_writes.spec_json/heavy_content can carry a customer
// repository's own file content in full (migrations/
// 000110_shadow_scm_writes_heavy_content.up.sql). This package's own
// Summary/Entry types never surface either column -- an operator sees
// counts, operations, targets (branch names, file paths -- still
// attacker/customer-influenceable text, rendered through the same
// render-safety path the web layer already uses for every other
// repo-derived string) and links into the sessions that produced them,
// never the payload itself. Reading the raw row (today, only by
// querying shadow_scm_writes directly) is deliberately out of this
// Step's surface.
//
// # RBAC: admin-only, no §13.3 table row
//
// See authz.ActionViewShadowLedger/ActionActivateShadowLedger's own doc
// comments (internal/domain/authz/action.go) for the full "why no
// matrix row" reasoning: this ledger exposes strictly more than even
// the admin-only Settings -> Members audit-log row, and its own
// retention/PII policy (§30.9) is still open -- admin-only is the
// answer until that resolves, not a placeholder pending a matrix
// update.
//
// # Activate and the demotion-sweep analyzer
//
// tools/lint/narvichecks/demotionsweep bans any call to
// RepoSettingsStore.UpsertLiveEgressEnabled outside the postgres package
// and internal/app/seed, because a TRUE->FALSE flip (demotion) that
// skips the sandbox-termination sweep leaves a write-capable credential
// alive past its intended lifetime (§30.4). Activate (activate.go) is
// the analyzer's third permitted caller, added deliberately: it is a
// promotion (FALSE->TRUE), which §30.8 states plainly owes no such
// sweep ("Promotion (shadow→live) is the safe direction on the sandbox
// side -- a shadow repo's sandboxes have never held more than
// read-only"). Activate's own public API takes no boolean -- there is
// no way to call it and demote a repo -- so the promotion-only property
// is structural, not merely a documented intention a future edit could
// silently widen. See demotionsweep.go's own doc comment for the
// allow-list entry itself.
package shadowoperator
