// Package repodemotion implements §30.4's own demotion fix: "a write
// credential minted just before a live->shadow flip stays served until
// ScmCredentialTTL (15min, internal/platform/timeouts.go) plus the
// helper's cache buffer elapse, and the underlying OAuth token itself
// never expires on that clock. Demotion therefore must terminate (or
// respawn) every sandbox of the repo and cancel in-flight push signals."
//
// # Two halves, two owners
//
// Sweep (sweep.go) is the Postgres-only half: given a repository that has
// just been demoted (repo_settings.live_egress_enabled flipped true ->
// false), it finds every currently-live sandbox belonging to a session
// that names that repository and (1) flags it for termination
// (sandboxes.demotion_terminate_requested_at, migrations/
// 000108_sandbox_demotion_termination.up.sql) and (2) cancels any
// push/PR decision currently outstanding on it (sandboxes.
// pending_push_cancelled, migrations/
// 000107_sandbox_pending_push_egress_mode.up.sql -- see
// app/sessionactor/pushpr.go's own createPRBestEffort for how that
// cancellation is honored). Sweep never calls a real provider: it only
// writes durable Postgres state, so it runs safely from ANY process,
// including internal/app/seed's own one-shot CLI invocation, which never
// constructs a ports.SandboxProvider at all.
//
// The SECOND half -- actually terminating the flagged sandbox's real
// cloud resource -- is internal/app/reconciler.Reconciler's own new
// demotion-sweep tick, added to the SAME process-wide loop that already
// calls ports.SandboxProvider.StopSandbox for orphan reaping (§5.3): it
// runs inside "control-plane serve", the one process that actually holds
// a real SandboxProvider, on whatever pod happens to run it, ticking
// every platform.Timeouts.ReconcilerInterval -- so a demotion recorded by
// the seed CLI (a separate process, possibly on a separate host) is
// picked up and acted on the very next tick, with no direct coupling
// between the two.
//
// # Who calls Sweep
//
// internal/app/seed/reposettings.go's own seedRepoSetting is, today, the
// ONLY writer of repo_settings.live_egress_enabled (migrations/
// 000101_repo_settings_live_egress_enabled.up.sql's own doc comment: "no
// REST route calls this yet") -- it calls Sweep exactly once a real
// true->false transition commits. Under shadow-by-default-at-onboarding
// (§30.8), that is the ONLY window this package's job exists for: a
// fresh evaluation's own sandboxes have never held more than read-only,
// so they have nothing this package protects against. A future REST
// "deactivate" handler (no such endpoint exists yet -- only the
// promotion gesture, §30.8's own "Activate", does) inherits the same
// obligation: flip the flag, then call Sweep the same way.
package repodemotion
