// Package imagebuild is the process-wide background image-build loop
// ("image builds", §8.5-note/§10-P2/§3.5) -- a sibling of
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
//  2. For each claimed row, OUTSIDE any transaction: resolves each named
//     repo's current default-branch tip SHA from the row's own persisted
//     (base, repo_urls, runtime_version) -- repo_urls, not repo_shas since
//     §19.1 renamed the column and re-keyed it on each repo's
//     normalized clone URL rather than a resolved SHA (migrations/
//     000039_image_builds_shared_fingerprint.up.sql's own doc comment) --
//     then calls ports.SandboxProvider.BuildImage with those concrete,
//     freshly-resolved SHAs (attempt, builder.go). See migrations/
//     000024_image_builds.up.sql's own doc comment for why the raw
//     fingerprint inputs are persisted alongside the fingerprint hash at
//     all, not just the hash itself. On success, records status='ready'
//     + image_ref. On
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
// yet (§5.3's own observability work). This package's own log
// line + OTel counter is the full extent of "alert" this Step delivers,
// named honestly rather than half-built, matching this project's own
// established discipline (naming
// deferred items explicitly elsewhere too).
//
// Deliberately, permanently out of scope for this package (do not revisit
// without a deliberate decision to do so): it never calls DeleteImage. §19.2's own in-place
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
// mirroring §3.2's own two-phase terminalization precedent -- not built
// here.
//
// The analogous crash window on the REFRESH path (between ClaimForRefresh
// and RecordRefreshSuccess/RecordRefreshFailure) USED TO be documented
// right here as "self-healing by construction" -- that claim was false:
// refresh_in_progress had no timeout/lease/sweep of any kind, a stuck-true
// row was never refreshed again, NOTHING ever told an operator clearing
// was needed, and because that row's own updated_at froze at the same
// moment, it also permanently occupied the front of every
// ListReadyImageBuilds LIMIT window -- silently starving the entire
// freshness pump one wedged claim at a time. Audit-remediation batch B2
// closes this for real, with a genuine LEASE rather than removing the
// false claim: ClaimImageBuildForRefresh (queries/image_builds.sql) treats
// a refresh_in_progress claim whose refresh_started_at predates
// platform.Timeouts.ImageRefreshClaimStaleAfter ago as abandoned and
// reclaimable, and ListReadyImageBuilds' own WHERE clause mirrors the
// identical predicate so a stuck row is still surfaced to a tick instead
// of becoming permanently invisible. See migrations/
// 000041_image_builds_refresh_lease.up.sql's own doc comment for why this
// is a lease keyed to the claim's own timestamp rather than a startup
// sweep keyed to a process's own boot time: a boot-time sweep cannot
// safely distinguish an abandoned claim from a DIFFERENT pod's own
// still-live one in this codebase's own explicitly anticipated
// multi-control-plane-pod deployment shape (ListDueImageBuilds' own doc
// comment), and would risk stomping a genuinely in-progress refresh.
// Every stale-claim detection also logs a Warn and increments the
// image_refresh_claim_reclaimed OTel counter (attemptRefresh, builder.go)
// -- the operator-visible signal this package previously promised but
// never delivered.
//
// A lease alone (a staleness bound compared against the claim's own
// timestamp) is NOT, by itself, sufficient: it bounds how long a claim can
// sit unreleased before another tick may reclaim it, but it does nothing
// to stop a DIFFERENT failure mode -- a delayed writer whose own
// outcome-recording call (RecordRefreshSuccess/RecordRefreshFailure)
// outlives that same bound (e.g. blocked on a Postgres row lock for
// longer than ImageRefreshClaimStaleAfter, for reasons entirely unrelated
// to the refresh itself: an unrelated long-running transaction, a stalled
// connection, a replica failover -- attemptRefresh runs on this package's
// own long-lived background context, which carries no per-call deadline).
// Once such a write finally lands, an UPDATE guarded only by "fingerprint
// = $1 AND status = 'ready'" still matches (status never changes across a
// reclaim), so it would unconditionally overwrite whatever a SECOND tick
// has since legitimately claimed and possibly already completed --
// clobbering a fresher build with a stale one, or worse, wiping out a
// still in-flight claim, with no error, no log, and no counter anywhere.
// Audit-remediation batch B2 round 2 closes this with a FENCING TOKEN,
// not merely a lease: RecordImageRefreshSuccess and RecordImageRefreshFailure
// (queries/image_builds.sql) both now additionally require
// "refresh_started_at = <the exact value THIS call's own ClaimForRefresh
// returned to it>", never a freshly computed now(). A write whose claim
// has since been reclaimed therefore matches zero rows -- the exact same
// harmless, expected "lost the race" outcome every OTHER superseded-claim
// path in this package already treats as a no-op -- rather than
// clobbering whichever tick currently, legitimately holds the lease. See
// RecordImageRefreshSuccess/RecordImageRefreshFailure's own generated doc
// comments and attemptRefresh's own top doc comment for the full mechanism.
//
// # §19.2's own addition: the freshness pump
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
// load. A batch beyond the cap is simply picked up on a later tick --
// ListReady's own ORDER BY updated_at gives across-tick fairness only
// because attemptRefresh advances that column on EVERY row it inspects
// this tick (TouchImageBuildChecked), not merely ones that reach a real
// claim; see ListReadyImageBuilds' and attemptRefresh's own doc comments
// for the starvation this rules out (a genuinely-not-stale or
// persistently-SHA-resolution-failing row can no longer permanently
// occupy the front of the queue).
//
// # Build-time dependency cache (§19.1's closing
// # paragraph), third iteration: immutable versioned cache snapshots
//
// Every real BuildImage call this package makes -- both attempt's own
// brand-new claim and attemptRefresh's own in-place refresh -- now also
// requests acceleration via ports.ImageSpec.CacheMount, built by the
// cacheMount helper (builder.go) from domain/imagebuild.CacheVolumeKey
// (base, runtimeVersion -- no rotation epoch anymore, see that function's
// own doc comment), b.cacheVersionStore.LatestVersion (the most recent
// CONFIRMED version to mount read-only), b.cacheVersionStore.MintVersion
// (a brand-new version to publish if this attempt succeeds), and
// domain/imagebuild.WellKnownCachePaths(). Purely advisory on the ERROR
// path (ports.CacheMount's own doc comment): a cache-computation problem
// (b.cacheVersionStore nil, or any Postgres error resolving either
// version) degrades cacheMount to nil -- an ordinary cold build, never a
// BuildImage failure -- and this package's own existing recordFailure/
// backoff path is otherwise entirely unchanged.
//
// What DOES change from the pure "advisory, never inspected" posture:
// after a successful BuildImage call, recordCachePublish (builder.go)
// checks BuildOutcome.PublishedCacheVersion -- the one SUCCESS-path signal
// this iteration needs, never an error -- and, only when it confirms this
// attempt's own request genuinely still carried the cache mount (not
// silently dropped by the adapter's own decline-and-retry-cold fallback),
// records the new version as CONFIRMED (ImageCacheVersionStore.
// PublishVersion) and immediately applies retention
// (domain/imagebuild.PruneCacheVersions -- keep the newest
// RetainedCacheVersions per cache key, prune this control plane's own
// bookkeeping beyond that). telemetry.go adds the build-duration/
// failure-rate instrumentation §19.9's own closing paragraph calls for
// alongside this (ungated, shipped for the same "size the win, catch a
// regression" reason §19.5's telemetry plays for (a)/(b), never a
// precondition to ship (c) itself).
//
// There is no shared mutable cache volume anymore, and therefore no write
// window for anything to observe: a build mounts one specific, already-
// published, immutable version read-only, and any successful publish
// creates a brand-new, distinctly-numbered version rather than mutating
// the one just read. This is the third iteration of this design -- the
// first mounted a shared volume read-write on a false content-addressing
// premise; the second mounted it read-only but still merged one unguarded
// write-back into that SAME shared volume after success, which review
// found was still an unguarded writer into state a concurrent reader
// could observe. This iteration removes the write window instead of
// narrowing it further (ports.CacheMount's own doc comment has the full
// three-iteration history). One direct consequence for this package: the
// rotation escape hatch attempt 2 needed (cacheVolumeEpoch,
// NARVI_CACHE_VOLUME_EPOCH) is gone, not merely unused -- an immutable
// version model makes it redundant (domain/imagebuild.CacheVolumeKey's
// own doc comment explains why "point a later build at an earlier good
// version" replaces "rotate the whole volume"). Real size enforcement
// beyond domain/imagebuild.RetainedCacheVersions' own fixed retention
// policy (reclaiming the underlying provider-side bytes for a pruned
// version) remains a named, deferred build-service obligation -- see
// ports.CacheMount's own doc comment.
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
