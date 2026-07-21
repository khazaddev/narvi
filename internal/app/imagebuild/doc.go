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
// without a new Step): it never calls DeleteImage, and it never garbage-
// collects a 'ready' row whose fingerprint is no longer referenced by any
// live session -- a fingerprint names an exact (base, repo SHAs, runtime
// version) triple, so a 'ready' row simply stops being looked up once
// nothing spawns against that exact combination again; no rebuild-of-an-
// already-ready-fingerprint mechanism exists. Named explicitly as a
// deferred gap (mirroring Step 25's own "orphan cloud objects, not DB
// rows" scoping precedent), not silently half-solved.
//
// Also out of scope, named explicitly: a process crash between claiming a
// row (status='building') and recording its outcome leaves that
// fingerprint permanently stuck in 'building', never retried by this
// package's own mechanism (ListDueImageBuilds deliberately excludes
// 'building' rows -- see that query's own doc comment). A future Step
// could add a staleness sweep (a 'building' row whose own last_attempt_at
// is older than some bound gets reset to 'failed' with a fresh backoff),
// mirroring Step 24's own two-phase terminalization precedent -- not built
// here.
package imagebuild
