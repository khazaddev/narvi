// Package imagebuild is the process-wide background image-build loop
// (Step 26, "image builds", §8.5-note/§10-P2/§3.5) -- a sibling of
// app/reconciler and app/sessionactor, not folded into either
// (TECHNICAL_PLAN.md §1's own repo-layout convention: one package per
// major loop/subsystem under internal/app/).
//
// Builder.Run (mirroring app/reconciler.Reconciler.Run and app/
// sessionactor's own RunTimerPump/PumpOnce shape exactly) ticks every
// platform.Timeouts.ImageBuildPumpInterval, calling PumpOnce -- exported
// separately, exactly like ReconcileOnce/PumpOnce, so tests can drive
// exactly one tick deterministically. PumpOnce:
//
//  1. Claims a batch of image_builds rows eligible to (re)attempt now
//     (pending, or failed with an elapsed next_retry_at) -- ONE
//     transaction: SELECT ... FOR UPDATE SKIP LOCKED, then per-row UPDATE
//     to status='building' (bumping attempt_count/last_attempt_at),
//     committed BEFORE any real provider call -- mirrors app/sessionactor/
//     timerpump.go's own claimDueTimers precedent exactly (a real,
//     network-bound BuildImage call must never hold a Postgres
//     transaction open, the same discipline app/sessionactor/dispatch.go's
//     own top comment establishes for CreateSandbox).
//  2. For each claimed row, OUTSIDE any transaction: calls
//     ports.SandboxProvider.BuildImage with the row's own persisted
//     (base, repo_shas, runtime_version) -- see migrations/
//     000024_image_builds.up.sql's own doc comment for why those raw
//     inputs are persisted alongside the fingerprint hash, not just the
//     hash itself. On success, records status='ready' + image_ref. On
//     failure, computes the next retry time via domain/imagebuild.
//     EvaluateBackoff (§3.5: "not fixed 30 min") and records
//     status='failed' + next_retry_at, logging a warning (and
//     incrementing the image_build_failure_streak OTel counter,
//     otel.Meter("narvi/imagebuild"), constructed once in NewBuilder --
//     mirroring app/reconciler's own orphans_reaped precedent) once
//     EvaluateBackoff reports the failure streak has crossed
//     domain/imagebuild.ImageBuildStreakThreshold consecutive failures.
//
// One row's BuildImage failure (or any error recording its outcome) is
// logged and does NOT abort the rest of the batch -- exactly like
// app/reconciler.ReconcileOnce's own per-orphan StopSandbox failure
// isolation, and app/sessionactor/timerpump.go's own deliver() per-item
// isolation.
//
// Real alert DELIVERY (Slack/email/notification) for the streak alert is
// explicitly OUT of scope -- no outbox/notification infrastructure exists
// yet (Phase 3, IMPLEMENTATION_PLAN.md row 35). This package's own log
// line + OTel counter is the full extent of "alert" this Step delivers,
// named honestly rather than half-built, matching this project's own
// established discipline (e.g. Step 22/25's own precedent of naming
// deferred items explicitly).
//
// Deliberately, permanently out of scope for this package (do not revisit
// without a new Step): it never calls DeleteImage. Step 42's own in-place
// refresh (below) DOES now relax part of the earlier "no rebuild-of-an-
// already-ready-fingerprint, ever" invariant -- deliberately, and ONLY for
// that in-place refresh path -- but image GC itself remains unbuilt: a
// superseded image_ref (the OLD ref, overwritten in place by a successful
// refresh) is never deleted from the provider, and §19.2 itself names this
// as "newly urgent" (a refresh now produces a superseded ref roughly every
// ImageRefreshCheckInterval-to-build-duration window per Environment) but
// still explicitly out of THIS Step's own scope.
//
// Also out of scope, named explicitly: a process crash between claiming a
// row (status='building') and recording its outcome leaves that
// fingerprint permanently stuck in 'building', never retried by this
// package's own mechanism (ListDueImageBuilds deliberately excludes
// 'building' rows -- see that query's own doc comment). A future Step
// could add a staleness sweep (a 'building' row whose own last_attempt_at
// is older than some bound gets reset to 'failed' with a fresh backoff),
// mirroring Step 24's own two-phase terminalization precedent -- not built
// here. The analogous crash window on the REFRESH path (between
// ClaimForRefresh and RecordRefreshSuccess/RecordRefreshFailure) is
// self-healing by construction instead: refresh_in_progress has no
// separate timeout/sweep of its own, but a stuck-true row simply never
// gets refreshed again until an operator clears it -- named honestly
// as a residual gap, not silently solved, since building one is not this
// Step's own scope either.
//
// # Step 42 addition: the freshness pump (§19.2)
//
// Builder.Run now fans out a SECOND, independent ticker loop
// (runRefreshPump, on platform.Timeouts.ImageRefreshCheckInterval)
// alongside the pre-existing build pump above, calling RefreshOnce each
// tick: for up to refreshBatchSize SHARED (repo-bearing) 'ready' rows,
// resolve each named repo's CURRENT default-branch tip SHA (via the SAME
// claim-time SHA resolution machinery attempt itself now uses,
// resolveRepoSHAs -- a new platform-level GitHub credential,
// platform.Config.GitHubImageBuildToken, shared by both since neither has
// a session/creator context to borrow a token from) and compare it
// against that row's own built_repo_shas (domain/imagebuild.NeedsRefresh).
// Any row whose current tips diverge gets a real in-place refresh build.
//
// refreshBatchSize (builder.go, mirroring pumpBatchSize's own exact
// precedent and value) bounds each tick's own ListReady query/batch --
// RefreshOnce processes its own batch just as strictly sequentially as
// PumpOnce does, so without a cap, one slow/blocked refresh build could
// delay even STARTING every other Environment's own tip-SHA check for the
// rest of that tick, degrading the fleet's effective refresh cadence well
// past this section's own documented 10-40 minute staleness window under
// load. A batch beyond the cap is simply picked up on a later tick
// (ListReady's own ORDER BY updated_at gives across-tick fairness).
//
// Refresh NEVER degrades availability: the row's own `status` column
// never leaves 'ready' for the whole duration a refresh build runs --
// single-flight protection is an entirely SEPARATE, independent
// refresh_in_progress column/CAS (migrations/
// 000040_image_builds_refresh_pump.up.sql), never the status='building'
// transition the pending/failed lifecycle uses. On success, a SINGLE
// atomic UPDATE swaps image_ref + built_repo_shas + built_at (never a
// delete-then-insert) -- a spawn's own concurrent GetImageBuild lookup
// therefore always sees either the complete OLD triple or the complete
// NEW one, never a gap with no usable ready image_ref at all. Any failure
// anywhere in this credential-dependent path (missing/invalid platform
// credential, a GitHub API failure resolving a tip SHA, a lost claim race,
// a BuildImage failure) is logged and simply retried at the next
// ImageRefreshCheckInterval tick -- never a crash, never any effect on
// the still-perfectly-good existing ready row.
package imagebuild
