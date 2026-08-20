package imagebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	domainimagebuild "github.com/khazaddev/narvi/internal/domain/imagebuild"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// meterName is this package's own OTel meter name (mirrors app/reconciler's
// own "narvi/<package>" convention exactly) -- the image_build_failure_streak
// counter (§3.5: "alert on streaks") lives here.
const meterName = "narvi/imagebuild"

// pumpBatchSize bounds how many due image_builds rows a single tick claims
// -- a plain count, not a duration, so (matching timerPumpBatchSize's own
// precedent, app/sessionactor/timerpump.go) it is a Go constant rather
// than a platform.Timeouts field. Chosen smaller than timerPumpBatchSize's
// 50: an image build is a slow, expensive provider operation, not a cheap
// timer delivery, so a smaller batch keeps one tick's own wall-clock time
// bounded.
const pumpBatchSize = 20

// refreshBatchSize bounds how many 'ready' image_builds rows a single
// RefreshOnce tick's own ListReady query returns/processes -- the freshness
// pump's own sibling to pumpBatchSize above (a correctness/scalability
// review finding on this Step: RefreshOnce had NO batch cap at all, unlike
// PumpOnce). RefreshOnce processes its own batch exactly as sequentially
// and synchronously as PumpOnce does (attemptRefresh's own BuildImage call
// is the SAME slow, network-bound provider operation attempt's is, just
// aimed at an already-'ready' fingerprint instead of a pending/failed
// one) -- so the identical "keep one tick's own worst-case wall-clock
// bounded" reasoning applies with equal force here, and there is no basis
// for a DIFFERENT number: a stale-but-still-'ready' row is lower-urgency
// than a pending/failed one only in the sense that its OLD image_ref stays
// servable the whole time (§19.2's own "never degrades availability"), but
// that does not make its own refresh build any cheaper or faster, so a
// larger batch would only let more Environments' worth of one-slow-build
// delay stack up per tick -- working directly against §19.2's own already
// explicit 10-40 minute staleness-window contract this finding cites.
// Reusing pumpBatchSize's own exact value keeps that worst case identical
// to PumpOnce's, rather than introducing a second, differently-justified
// magic number for what is, per-item, the same expensive operation.
const refreshBatchSize = 20

// Builder is the process-wide background image-build loop (see doc.go for
// the full writeup). Constructed once per process (NewBuilder), then run
// via its own Run method -- exactly like app/reconciler.Reconciler and
// app/sessionactor.Registry's RunTimerPump.
type Builder struct {
	store    *postgres.ImageBuildStore
	pool     *pgxpool.Pool
	provider ports.SandboxProvider
	timeouts platform.Timeouts

	// sourceControl and gitHubImageBuildToken back BOTH claim-time SHA
	// resolution for a brand-new repo-bearing build (attempt, §19.1/§19.9's
	// own Step 41/42 boundary) AND the freshness pump's own per-repo
	// current-tip resolution (RefreshOnce, §19.2) -- the SAME platform-level
	// credential, since neither call site has a session/creator context to
	// borrow a token from (a shared image has no creator). sourceControl may
	// be nil (mirrors every other optional-provider precedent in this
	// codebase, e.g. app/sessionactor's own nil-sourceControl tests) and
	// gitHubImageBuildToken may be empty (§19.2: deliberately optional,
	// platform/config.go's own GitHubImageBuildToken doc comment) -- either
	// missing piece degrades resolveRepoSHAs to a clean, logged error,
	// never a crash, recorded as a failed attempt via the same
	// retry/backoff path any other resolution failure uses, which this
	// package's own background loop must honor just as strictly as the
	// claim-time build path itself.
	sourceControl         ports.SourceControl
	gitHubImageBuildToken string

	// cacheVersionStore backs Step 43(c)'s own build-time dependency cache
	// (§19.1's closing paragraph), third iteration: immutable versioned
	// cache snapshots. Mints a fresh PublishVersion and resolves the most
	// recent CONFIRMED MountVersion for a given cache key (cacheMount,
	// below) before every real BuildImage attempt that requests
	// acceleration, and records a confirmed publication (plus retention
	// pruning, domain/imagebuild.PruneCacheVersions) after a success that
	// BuildOutcome.PublishedCacheVersion confirms actually used the mount.
	// May be nil (mirrors sourceControl's own optional-provider
	// precedent immediately above) -- cacheMount degrades to "no cache
	// mount requested this attempt" whenever it is, exactly like every
	// other cache-computation failure this package treats as advisory,
	// never fatal to a build.
	//
	// No rotation epoch anymore -- see domain/imagebuild.CacheVolumeKey's
	// own doc comment for why an immutable-version model makes that
	// escape hatch (platform.Config.CacheVolumeEpoch, attempt 2's own
	// NARVI_CACHE_VOLUME_EPOCH) redundant rather than merely optional: a
	// bad published version is escaped by pointing a later build's own
	// MountVersion resolution at an earlier, known-good one (an operator
	// deleting the bad row via ImageCacheVersionStore.DeleteVersions),
	// never by a second, parallel rotation config surface.
	cacheVersionStore *postgres.ImageCacheVersionStore

	failureStreak metric.Int64Counter

	// permanentlyFailed counts every time a fingerprint is marked
	// permanently_failed (audit-remediation batch B3 round 2, finding #3) --
	// a distinct, one-shot signal from failureStreak above: failureStreak
	// fires repeatedly, forever, for a condition that will never clear on
	// its own, which is exactly the alert-fatigue problem this counter's
	// own introduction fixes. permanentlyFailed fires exactly ONCE per
	// fingerprint (the row is never reclaimed again after this), so an
	// operator dashboard can alert on it without the "why does this keep
	// paging me" confusion a repeating failureStreak trip for the same,
	// un-clearable fingerprint would cause.
	permanentlyFailed metric.Int64Counter

	// refreshClaimReclaimed counts every time ListReady/ClaimForRefresh
	// observe a refresh_in_progress row whose claim has gone stale
	// (audit-remediation batch B2: closes the crash-window gap doc.go used
	// to call "self-healing by construction") -- i.e. every time a stuck
	// claim, left by a crash/SIGTERM/pod-eviction between a previous
	// ClaimForRefresh and its own RecordRefreshSuccess/RecordRefreshFailure,
	// is DETECTED, whether or not this tick's own attempt to reclaim it
	// wins the race. Constructed exactly once, here, at construction time
	// -- mirroring failureStreak's own precedent immediately above (and
	// app/reconciler.NewReconciler's own orphans_reaped precedent it in
	// turn mirrors).
	refreshClaimReclaimed metric.Int64Counter

	// buildTelemetry bundles Step 43(c)'s own build-duration/failure-rate
	// instrumentation (§19.9's closing paragraph; telemetry.go) -- recorded
	// around every real BuildImage call in both attempt and attemptRefresh.
	buildTelemetry buildTelemetry
}

// NewBuilder builds a Builder backed by store/pool (pool is needed
// directly, alongside store, for the claim step's own transaction --
// mirrors app/sessionactor/timerpump.go's claimDueTimers, which likewise
// acquires its own connection/transaction around a WithTx-scoped store),
// provider (the real ports.SandboxProvider whose BuildImage this builder
// drives), timeouts (for ImageBuildPumpInterval/ImageRefreshCheckInterval/
// backoff config, consulted by Run/PumpOnce/RefreshOnce), sourceControl
// (§19.2's own claim-time/freshness-pump SHA resolution, §19.2 -- may be
// nil), gitHubImageBuildToken (the new platform-level credential,
// platform.Config.GitHubImageBuildToken -- may be empty), and
// cacheVersionStore (Step 43(c)'s own immutable-cache-version bookkeeping,
// third iteration -- may be nil; see the Builder field's own doc comment).
//
// The image_build_failure_streak OTel counter is constructed exactly once,
// here, at construction time -- not per-tick, not per-row -- mirroring
// app/reconciler.NewReconciler's own orphans_reaped precedent exactly.
func NewBuilder(store *postgres.ImageBuildStore, pool *pgxpool.Pool, provider ports.SandboxProvider, timeouts platform.Timeouts, sourceControl ports.SourceControl, gitHubImageBuildToken string, cacheVersionStore *postgres.ImageCacheVersionStore) (*Builder, error) {
	meter := otel.Meter(meterName)

	failureStreak, err := meter.Int64Counter(
		"image_build_failure_streak",
		metric.WithDescription("Number of image-build attempts that landed at or beyond the consecutive-failure streak threshold for their own fingerprint (§3.5: alert on streaks)."),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, fmt.Errorf("imagebuild: construct image_build_failure_streak counter: %w", err)
	}

	refreshClaimReclaimed, err := meter.Int64Counter(
		"image_refresh_claim_reclaimed",
		metric.WithDescription("Number of times a stuck refresh_in_progress claim (a crash/SIGTERM/pod-eviction between ClaimForRefresh and its own RecordRefreshSuccess/RecordRefreshFailure) was detected as stale and reclaimed (audit-remediation batch B2)."),
		metric.WithUnit("{claim}"),
	)
	if err != nil {
		return nil, fmt.Errorf("imagebuild: construct image_refresh_claim_reclaimed counter: %w", err)
	}

	permanentlyFailed, err := meter.Int64Counter(
		"image_build_permanently_failed",
		metric.WithDescription("Number of image-build fingerprints marked permanently failed (a repo url naming an unsupported source-control host) and excluded from any further retry (audit-remediation batch B3 round 2)."),
		metric.WithUnit("{fingerprint}"),
	)
	if err != nil {
		return nil, fmt.Errorf("imagebuild: construct image_build_permanently_failed counter: %w", err)
	}

	buildTel, err := newBuildTelemetry(meter)
	if err != nil {
		return nil, err
	}

	return &Builder{
		store:                 store,
		pool:                  pool,
		provider:              provider,
		timeouts:              timeouts,
		sourceControl:         sourceControl,
		gitHubImageBuildToken: gitHubImageBuildToken,
		cacheVersionStore:     cacheVersionStore,
		failureStreak:         failureStreak,
		refreshClaimReclaimed: refreshClaimReclaimed,
		permanentlyFailed:     permanentlyFailed,
		buildTelemetry:        buildTel,
	}, nil
}

// cacheMount resolves the ports.CacheMount a real BuildImage call requests
// to accelerate a build (§19.1's closing paragraph(c), third
// iteration: immutable versioned cache snapshots). Key is
// domain/imagebuild.CacheVolumeKey(base, runtimeVersion) -- deliberately
// NOT a function of repos, so every fingerprint sharing the same
// (base, runtimeVersion) resolves to the identical cache lineage
// regardless of which repo set it is building. MountVersion is the most
// recently CONFIRMED version under Key (ImageCacheVersionStore.
// LatestVersion) -- empty when none has ever been confirmed yet, an
// ordinary "this lineage's first cache-requesting build" outcome, never an
// error. PublishVersion is freshly minted for THIS attempt alone
// (ImageCacheVersionStore.MintVersion) -- see that method's own doc
// comment for why a reservation that never gets confirmed (this build
// fails, or the adapter declines the mount) is simply, harmlessly
// abandoned.
//
// Returns nil -- "no cache mount requested this attempt", exactly like a
// caller that never opted in -- whenever b.cacheVersionStore is nil, or
// EITHER Postgres call above fails for any reason other than "no version
// confirmed yet" (pgx.ErrNoRows on LatestVersion, the one expected,
// non-error outcome). This mirrors every other "cache computation problem
// degrades to an ordinary cold build, never a BuildImage failure" path
// this package and ports.CacheMount's own doc comment both establish --
// resolving a MountVersion/PublishVersion pair is itself just one more
// thing that can go wrong on the way to requesting acceleration, and it
// must never become a reason a build doesn't happen at all.
//
// Whether the requested mount is actually honored is entirely up to the
// provider (ports.CacheMount: "purely advisory... a caller must never be
// able to distinguish 'the cache was used' from 'the cache was declined'
// via any ERROR path") -- this method never inspects or branches on that;
// only BuildOutcome.PublishedCacheVersion, consulted AFTER a successful
// BuildImage call (recordCachePublish, below), does. Paths comes from
// domain/imagebuild.WellKnownCachePaths() -- a function, not a shared
// package var, so every call here gets its own fresh slice this Builder
// can never be accused of mutating out from under a concurrent build.
func (b *Builder) cacheMount(ctx context.Context, logger *slog.Logger, base, runtimeVersion string) *ports.CacheMount {
	if b.cacheVersionStore == nil {
		return nil
	}

	key := domainimagebuild.CacheVolumeKey(base, runtimeVersion)

	mountVersion := ""
	switch latest, err := b.cacheVersionStore.LatestVersion(ctx, key); {
	case err == nil:
		mountVersion = strconv.FormatInt(latest, 10)
	case errors.Is(err, pgx.ErrNoRows):
		// No version confirmed yet for this key -- ordinary and expected
		// for a brand-new cache lineage; mountVersion stays "".
	default:
		logger.Warn("imagebuild: cache: resolve latest confirmed version failed; building without a cache mount this attempt", "cache_key", key, "error", err)
		return nil
	}

	publishVersion, err := b.cacheVersionStore.MintVersion(ctx, key)
	if err != nil {
		logger.Warn("imagebuild: cache: mint publish version failed; building without a cache mount this attempt", "cache_key", key, "error", err)
		return nil
	}

	return &ports.CacheMount{
		Key:            key,
		MountVersion:   mountVersion,
		PublishVersion: strconv.FormatInt(publishVersion, 10),
		Paths:          domainimagebuild.WellKnownCachePaths(),
	}
}

// recordCachePublish confirms cm's own PublishVersion was genuinely
// published -- publishedCacheVersion is BuildOutcome.PublishedCacheVersion
// from the SAME successful BuildImage call cm was passed into, and it must
// equal cm.PublishVersion for anything to be recorded at all (empty means
// the adapter's own decline-and-retry-cold fallback dropped the mount
// before the attempt that ultimately succeeded -- see BuildOutcome's own
// doc comment for why this confirmation, not an optimistic "success means
// it worked" assumption, is what this control plane records: recording a
// version nothing actually published would point every future build's own
// MountVersion at an object that was never created). On a genuine
// confirmation, applies retention immediately afterward
// (domain/imagebuild.PruneCacheVersions -- keep the newest
// RetainedCacheVersions for this Key, prune the rest of this control
// plane's OWN bookkeeping).
//
// Never blocks or fails the caller's own success path: every Postgres
// error here is logged and this function simply returns -- a failed
// confirmation only means a FUTURE build resolves an older MountVersion
// (or none), never a build failure now or later (a stale/missing
// MountVersion degrades via the adapter's own decline-and-retry-cold
// fallback regardless, ports.CacheMount's own pure-accelerator rule). A
// nil cm (no cache mount was ever requested this attempt) is a no-op.
func (b *Builder) recordCachePublish(ctx context.Context, logger *slog.Logger, cm *ports.CacheMount, publishedCacheVersion, fingerprint string) {
	if cm == nil || publishedCacheVersion == "" || publishedCacheVersion != cm.PublishVersion {
		return
	}

	version, err := strconv.ParseInt(publishedCacheVersion, 10, 64)
	if err != nil {
		logger.Error("imagebuild: cache: parse published version failed; not recording", "cache_key", cm.Key, "published_cache_version", publishedCacheVersion, "error", err)
		return
	}

	if err := b.cacheVersionStore.PublishVersion(ctx, cm.Key, version, fingerprint); err != nil {
		logger.Warn("imagebuild: cache: record published version failed", "cache_key", cm.Key, "version", version, "error", err)
		return
	}

	versions, err := b.cacheVersionStore.ListVersions(ctx, cm.Key)
	if err != nil {
		logger.Warn("imagebuild: cache: list versions for retention failed; skipping prune this attempt", "cache_key", cm.Key, "error", err)
		return
	}
	prune := domainimagebuild.PruneCacheVersions(versions)
	if len(prune) == 0 {
		return
	}
	if err := b.cacheVersionStore.DeleteVersions(ctx, cm.Key, prune); err != nil {
		logger.Warn("imagebuild: cache: prune old versions failed", "cache_key", cm.Key, "pruned_count", len(prune), "error", err)
	}
}

// Run runs the process-wide image-build loop until ctx is done: TWO
// independent ticker loops, fanned out via a zero-value errgroup.Group
// (never a bare `go` statement, §11; a zero-value Group, NOT
// errgroup.WithContext, so one loop's own ctx.Err() return can never
// cancel-race the other -- mirrors internal/sandboxagent/supervisor.
// Supervisor's own StopAll fan-out precedent exactly for the identical
// "independent, unrelated failures" reasoning) -- the pre-existing build
// pump (ticks on ImageBuildPumpInterval, calling PumpOnce, UNCHANGED from
// before this Step) and this Step's own NEW freshness pump (ticks on
// ImageRefreshCheckInterval, calling RefreshOnce, §19.2). Each tick's own
// error is logged, never propagated, so one bad tick never kills either
// loop. The caller starts this via its own errgroup.Go exactly once per
// process, exactly as before this Step.
func (b *Builder) Run(ctx context.Context) error {
	var g errgroup.Group
	g.Go(func() error { return b.runBuildPump(ctx) })
	g.Go(func() error { return b.runRefreshPump(ctx) })
	return g.Wait()
}

// runBuildPump is Run's own pre-existing ticker loop, factored out
// unchanged (a pure refactor, no behavior change) so Run can fan it out
// alongside runRefreshPump below.
func (b *Builder) runBuildPump(ctx context.Context) error {
	ticker := time.NewTicker(b.timeouts.ImageBuildPumpInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := b.PumpOnce(ctx); err != nil {
				platform.Logger(ctx).Error("imagebuild: tick failed", "error", err)
			}
		}
	}
}

// runRefreshPump is this Step's own new freshness-pump ticker loop (§19.2),
// mirroring runBuildPump's own exact shape -- a second, independent
// ticker on ImageRefreshCheckInterval calling RefreshOnce each tick.
func (b *Builder) runRefreshPump(ctx context.Context) error {
	ticker := time.NewTicker(b.timeouts.ImageRefreshCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := b.RefreshOnce(ctx); err != nil {
				platform.Logger(ctx).Error("imagebuild: refresh tick failed", "error", err)
			}
		}
	}
}

// PumpOnce runs exactly one pump tick: claims a batch of due image_builds
// rows, then attempts a real BuildImage call for each, OUTSIDE any
// transaction. Exported (rather than only reachable through Run's own
// loop) so tests can drive exactly one tick deterministically, matching
// PumpOnce/ReconcileOnce's own precedent.
//
// A failure in the batch-level claim step aborts the tick and returns the
// error (Run logs it) -- but once a batch is successfully claimed, one
// row's own BuildImage failure (or a failure recording its outcome) is
// isolated: logged, and does NOT abort the rest of the batch, exactly like
// app/reconciler.ReconcileOnce's own per-orphan StopSandbox failure
// isolation.
func (b *Builder) PumpOnce(ctx context.Context) error {
	claimed, err := b.claimBatch(ctx)
	if err != nil {
		return fmt.Errorf("imagebuild: claim batch: %w", err)
	}

	for _, row := range claimed {
		b.attempt(ctx, row)
	}
	return nil
}

// claimBatch runs the ENTIRE claim step inside one transaction: ListDue
// (FOR UPDATE SKIP LOCKED -- so a concurrent tick, this pod's or another
// pod's own Builder, claims a DISJOINT batch rather than double-claiming
// the same fingerprint), then Claim for each due row (flips it to
// 'building', bumps attempt_count/last_attempt_at), then commits -- exactly
// mirroring app/sessionactor/timerpump.go's own claimDueTimers shape.
func (b *Builder) claimBatch(ctx context.Context) ([]sqlcgen.ImageBuild, error) {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txStore := b.store.WithTx(tx)

	due, err := txStore.ListDue(ctx, pumpBatchSize)
	if err != nil {
		return nil, fmt.Errorf("list due image builds: %w", err)
	}

	claimed := make([]sqlcgen.ImageBuild, 0, len(due))
	for _, row := range due {
		c, err := txStore.Claim(ctx, row.Fingerprint)
		if err != nil {
			return nil, fmt.Errorf("claim image build %q: %w", row.Fingerprint, err)
		}
		claimed = append(claimed, c)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return claimed, nil
}

// attempt performs the real (possibly slow, network-bound) BuildImage call
// for one already-claimed row OUTSIDE any transaction, then records the
// outcome via a single, pool-scoped statement (RecordSuccess/RecordFailure
// are each already atomic as a single UPDATE ... WHERE status='building',
// so no additional transaction wrapper is needed the way claimBatch's own
// multi-statement claim sequence requires one). Every failure here
// (decode error, SHA-resolution error, BuildImage error, a failed/no-op
// outcome-record call) is logged and returns -- it never propagates, so
// one row's problem can never abort the rest of PumpOnce's own batch.
//
// # §19.2's own claim-time SHA resolution (§19.1/§19.2/§19.9)
//
// row.RepoUrls (§19.1's redefined image_builds column) carries the
// fingerprint's own URL-keyed inputs (repo name -> normalized clone URL),
// NEVER a resolved SHA -- the fingerprint itself stays SHA-free by design
// (imageresolve.go's resolveAndSetImage never resolves one, for ANY
// spawn). §19.1's own prose says the builder resolves each repo's
// default-branch tip SHA "at claim time" -- this is that resolution,
// landing here per §19.9's own Step 41/42 boundary note: it needs a
// platform-level GitHub credential no session/creator context can supply
// (a shared image has no creator), which Step 42 is the one that adds
// (platform.Config.GitHubImageBuildToken, threaded through NewBuilder).
//
// A row naming at least one repo resolves EVERY named repo's current
// default-branch tip SHA (resolveRepoSHAs, below) before ever calling
// BuildImage -- if ANY repo's resolution fails (missing/invalid
// credential, a GitHub API failure), the WHOLE attempt is recorded as a
// failed attempt (never a partial/zero-SHA BuildImage call) and cycles
// through the SAME EvaluateBackoff retry schedule any other failure uses
// -- never a crash, and never something that blocks a spawn, since a
// miss here just leaves the fingerprint's row un-ready and every spawn
// keeps falling back to the base image exactly as before this Step. A
// row naming NO repos (base+runtime only) has nothing to resolve and
// builds immediately, exactly as before this Step.
func (b *Builder) attempt(ctx context.Context, row sqlcgen.ImageBuild) {
	logger := platform.Logger(ctx).With("fingerprint", row.Fingerprint, "attempt_count", row.AttemptCount)

	var repoURLs map[string]string
	if err := json.Unmarshal(row.RepoUrls, &repoURLs); err != nil {
		logger.Error("imagebuild: decode repo_urls failed; recording as a failed attempt", "error", err)
		b.recordFailure(ctx, logger, row)
		return
	}

	repos := map[string]ports.RepoRef{}
	builtRepoSHAs := map[string]string{}
	if len(repoURLs) > 0 {
		resolved, err := b.resolveRepoSHAs(ctx, repoURLs)
		if err != nil {
			// Audit-remediation batch B3: an unsupported-host error is a
			// PERMANENT condition (this fingerprint's own repo url will
			// never resolve, no matter how many times it is retried) --
			// escalated to Error, with a message that says so explicitly,
			// rather than folding it into the same Warn-level "might just
			// be a transient blip" message every other resolution failure
			// (missing credential, GitHub API error) gets. Still recorded
			// as a failed attempt via the SAME recordFailure/backoff path
			// as every other resolveRepoSHAs failure -- see
			// resolveRepoSHAs' own doc comment for why this codebase does
			// not (yet) have a distinct terminal "permanently failed, stop
			// retrying" status to route this to instead.
			var hostErr *reposource.UnsupportedRepoHostError
			if errors.As(err, &hostErr) {
				// Audit-remediation batch B3 round 2 (finding #3): this is a
				// PERMANENT condition -- no amount of retrying ever makes an
				// unsupported host become supported -- so it is recorded via
				// recordPermanentFailure (status stays 'failed', but
				// permanently_failed=true, next_retry_at cleared), NOT the
				// same recordFailure/EvaluateBackoff exponential-backoff path
				// a transient failure below uses. This is what stops the
				// fingerprint cycling failed -> next_retry_at -> claimed ->
				// failed forever, and stops re-tripping the
				// image_build_failure_streak alert on every subsequent tick
				// for a condition no retry will ever clear.
				logger.Error("imagebuild: claim-time SHA resolution failed: repo url names an unsupported source-control host -- PERMANENT, will not resolve on any retry until the session's repo url is fixed; recording as a permanently failed attempt, excluded from any further retry", "error", err, "repo_count", len(repoURLs))
				b.recordPermanentFailure(ctx, logger, row)
				return
			}
			logger.Warn("imagebuild: claim-time SHA resolution failed; recording as a failed attempt", "error", err, "repo_count", len(repoURLs))
			b.recordFailure(ctx, logger, row)
			return
		}
		for name, repoURL := range repoURLs {
			repos[name] = ports.RepoRef{URL: repoURL, SHA: resolved[name]}
		}
		builtRepoSHAs = resolved
	}

	cm := b.cacheMount(ctx, logger, row.Base, row.RuntimeVersion)
	buildStart := time.Now()
	outcome, buildErr := b.provider.BuildImage(ctx, ports.ImageSpec{
		Base:           row.Base,
		Repos:          repos,
		RuntimeVersion: row.RuntimeVersion,
		CacheMount:     cm,
	})
	b.buildTelemetry.record(ctx, "claim", time.Since(buildStart).Seconds(), buildErr != nil)
	if buildErr != nil {
		logger.Warn("imagebuild: BuildImage failed", "error", buildErr)
		b.recordFailure(ctx, logger, row)
		return
	}
	b.recordCachePublish(ctx, logger, cm, outcome.PublishedCacheVersion, row.Fingerprint)

	builtAt := time.Now()
	builtRepoSHAsJSON, err := json.Marshal(builtRepoSHAs)
	if err != nil {
		// Cannot happen for a plain map[string]string, but this function
		// never lets a marshal failure propagate -- see this function's
		// own top doc comment.
		logger.Error("imagebuild: marshal built_repo_shas failed; recording as a failed attempt", "error", err)
		b.recordFailure(ctx, logger, row)
		return
	}

	imageRef := string(outcome.Ref)
	if _, err := b.store.RecordSuccess(ctx, sqlcgen.RecordImageBuildSuccessParams{
		Fingerprint:   row.Fingerprint,
		ImageRef:      &imageRef,
		BuiltRepoShas: builtRepoSHAsJSON,
		BuiltAt:       pgtype.Timestamptz{Time: builtAt, Valid: true},
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The row is no longer 'building' -- a should-be-rare, benign
			// race (e.g. a bug elsewhere ever double-claims a row); logged,
			// not fatal to this tick.
			logger.Warn("imagebuild: record success no-op: row no longer building")
			return
		}
		logger.Error("imagebuild: record success failed", "error", err)
	}
}

// recordFailure computes the next retry time (and streak-alert status) via
// domain/imagebuild.EvaluateBackoff and records the failure -- shared by
// both attempt's own decode-error and BuildImage-error paths.
func (b *Builder) recordFailure(ctx context.Context, logger *slog.Logger, row sqlcgen.ImageBuild) {
	now := time.Now()
	decision := domainimagebuild.EvaluateBackoff(int(row.AttemptCount), domainimagebuild.BackoffConfig{
		BaseDelay: b.timeouts.ImageBuildBackoffBase,
		MaxDelay:  b.timeouts.ImageBuildBackoffMax,
	}, now)

	if decision.StreakAlert {
		logger.Warn("imagebuild: fingerprint has reached the consecutive-failure streak threshold",
			"streak_threshold", domainimagebuild.ImageBuildStreakThreshold,
			"next_retry_at", decision.NextRetryAt,
		)
		b.failureStreak.Add(ctx, 1)
	}

	if _, err := b.store.RecordFailure(ctx, sqlcgen.RecordImageBuildFailureParams{
		Fingerprint: row.Fingerprint,
		NextRetryAt: pgtype.Timestamptz{Time: decision.NextRetryAt, Valid: true},
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("imagebuild: record failure no-op: row no longer building")
			return
		}
		platform.Logger(ctx).Error("imagebuild: record failure failed", "error", err, "fingerprint", row.Fingerprint)
	}
}

// recordPermanentFailure implements this file's own terminal "give up"
// path (audit-remediation batch B3 round 2, finding #3): unlike
// recordFailure above, no domain/imagebuild.EvaluateBackoff decision is
// ever computed here -- there is no retry schedule to compute, since this
// fingerprint's own repo url names a source-control host this deployment's
// SourceControl adapter can never resolve against, no matter how long a
// backoff waits. Fires the image_build_permanently_failed counter exactly
// ONCE for this call (never repeatedly -- see that metric's own doc
// comment on Builder), then records permanently_failed=true via
// b.store.RecordPermanentFailure, which ListDueImageBuilds' own
// "AND permanently_failed = false" guard then excludes from every future
// tick until an operator fixes the underlying repo config.
func (b *Builder) recordPermanentFailure(ctx context.Context, logger *slog.Logger, row sqlcgen.ImageBuild) {
	b.permanentlyFailed.Add(ctx, 1)

	if _, err := b.store.RecordPermanentFailure(ctx, row.Fingerprint); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("imagebuild: record permanent failure no-op: row no longer building")
			return
		}
		platform.Logger(ctx).Error("imagebuild: record permanent failure failed", "error", err, "fingerprint", row.Fingerprint)
	}
}

// errGitHubImageBuildTokenNotConfigured and errSourceControlNotConfigured
// are resolveRepoSHAs' own typed degrade-cleanly reasons -- a missing or
// invalid platform credential is logged and degrades to the same
// retry/backoff behavior any other resolution failure gets (§19.2) --
// named sentinels rather than a bare fmt.Errorf string so a caller/log
// line names EXACTLY which piece is missing, mirroring this codebase's
// own "named error, never a bare generic one" convention (internal/
// platform/config.go's own InvalidHMACSecretError/MissingRequiredEnvError
// family).
var (
	errGitHubImageBuildTokenNotConfigured = errors.New("imagebuild: NARVI_GITHUB_IMAGE_BUILD_TOKEN is not configured")
	errSourceControlNotConfigured         = errors.New("imagebuild: no SourceControl configured")
)

// resolveRepoSHAs resolves EVERY repo named in repoURLs' own current
// default-branch tip SHA via b.sourceControl.ResolveBranchSHA, using
// b.gitHubImageBuildToken -- the ONE platform-level credential shared by
// claim-time build resolution (attempt) and the freshness pump's own
// staleness check (attemptRefresh), since neither has a session/creator
// context to borrow a token from (§19.2: "a shared image has no
// creator"). Returns an error (never a partial map) the instant EITHER
// prerequisite is missing (no credential configured, no SourceControl
// wired) or ANY single repo's resolution fails -- callers therefore never
// receive a map with some repos resolved and others silently zero-valued.
//
// Iterates repo names in SORTED order purely for deterministic,
// reproducible logging/test behavior -- resolution outcome does not
// depend on order (each repo's ResolveBranchSHA call is independent), so
// this is not a correctness requirement, only a readability one.
//
// # Host check (audit-remediation batch B3)
//
// b.sourceControl is always internal/adapters/outbound/githubapi's real
// adapter in production (ports.GitHubSourceControlHost's own doc
// comment) -- but nothing about a repo_urls entry's own shape guarantees
// its URL actually names github.com: reposource.ValidateRepoURL (already
// run upstream, at session-config-ingestion time) accepts any https host,
// and this codebase's own fixtures already exercise non-GitHub URLs
// (e.g. httpapi/scmcredentials_integration_test.go's gitlab.example.com).
// Before this batch, a GitLab (or any other) repo URL's owner/repo path
// was extracted and handed straight to ResolveBranchSHA regardless --
// either failing confusingly against GitHub's real API, or worse,
// SUCCEEDING against an unrelated public GitHub repo that happens to
// share that owner/repo path, whose SHA would then be persisted as this
// fingerprint's own built_repo_shas and passed to BuildImage as the
// commit to check the GitLab (or other) URL out AT. reposource.
// CheckRepoHost below closes this per-repo, BEFORE ParseOwnerRepo ever
// derives an owner/repo or a single ResolveBranchSHA call is made for
// it -- returning a *reposource.UnsupportedRepoHostError a caller can
// distinguish from every other failure this function can return (see
// attempt's/attemptRefresh's own call sites: both escalate logging for
// this exact error, since -- unlike a missing credential or a transient
// GitHub API error -- an unsupported host is a PERMANENT condition no
// retry will ever clear).
func (b *Builder) resolveRepoSHAs(ctx context.Context, repoURLs map[string]string) (map[string]string, error) {
	if b.gitHubImageBuildToken == "" {
		return nil, errGitHubImageBuildTokenNotConfigured
	}
	if b.sourceControl == nil {
		return nil, errSourceControlNotConfigured
	}

	names := make([]string, 0, len(repoURLs))
	for name := range repoURLs {
		names = append(names, name)
	}
	sort.Strings(names)

	resolved := make(map[string]string, len(repoURLs))
	for _, name := range names {
		repoURL := repoURLs[name]

		if err := reposource.CheckRepoHost(repoURL, ports.SupportedSourceControlHosts()...); err != nil {
			return nil, fmt.Errorf("repo %q: %w", name, err)
		}

		owner, repoName, err := reposource.ParseOwnerRepo(repoURL)
		if err != nil {
			return nil, fmt.Errorf("parse owner/repo from %q: %w", repoURL, err)
		}

		shaCtx, cancel := context.WithTimeout(ctx, b.timeouts.RepoSHAResolutionTimeout)
		sha, _, err := b.sourceControl.ResolveBranchSHA(shaCtx, ports.ResolveBranchSHASpec{
			Owner: owner,
			Repo:  repoName,
			// Branch empty: §19.1/§19.2's own tip-tracking design always
			// resolves the repo's own DEFAULT branch tip, never a
			// session-specific branch -- a shared image has no single
			// session's branch to track in the first place.
			Branch: "",
			Token:  b.gitHubImageBuildToken,
		})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("resolve branch sha for repo %q: %w", name, err)
		}
		resolved[name] = sha
	}
	return resolved, nil
}

// parseOwnerRepo used to live here as a byte-for-byte fork of
// internal/app/sessionactor/pushpr.go's own identical helper -- audit-
// remediation batch B3 moved both into
// internal/domain/reposource.ParseOwnerRepo and deleted both forks. See
// that function's own doc comment for the full rationale; resolveRepoSHAs
// above now calls it directly.

// RefreshOnce runs exactly one freshness-pump tick (§19.2): for up to
// refreshBatchSize SHARED (repo-bearing) 'ready' image_builds rows,
// resolves each repo's CURRENT default-branch tip SHA and compares it
// against that row's own built_repo_shas; any row whose current tips
// diverge enqueues an in-place refresh build. Exported (rather than only
// reachable through Run's own runRefreshPump loop) so tests can drive
// exactly one tick deterministically, mirroring PumpOnce's own precedent
// exactly.
//
// Like PumpOnce, this processes its own batch strictly SEQUENTIALLY, in
// this one goroutine -- refreshBatchSize (mirroring pumpBatchSize's own
// precedent) bounds how many rows a single tick can claim/attempt, so one
// slow/blocked attemptRefresh call can delay starting at most
// refreshBatchSize-1 others in THIS tick, never an unbounded fleet's worth;
// any row beyond that cap is simply picked up on a later tick. ListReady's
// own ORDER BY updated_at gives across-tick fairness under an INVARIANT,
// not merely a fixed set of branches: attemptRefresh (below) advances that
// column on EVERY row it inspects this tick, including every early-return
// path that never reaches a real claim -- see ListReadyImageBuilds' own
// generated doc comment for the full starvation this rules out (a
// correctness review finding on this very batch-cap mechanism: a row
// genuinely not stale, or whose SHA resolution persistently fails, would
// otherwise keep an arbitrarily old updated_at and permanently occupy the
// front of every tick's own LIMIT window).
//
// One row's own refresh failure (a resolution error, a lost claim race, a
// BuildImage failure) is isolated -- logged, and does NOT abort the rest
// of this tick's own batch, exactly like PumpOnce's own per-row isolation.
//
// staleClaimCutoff (audit-remediation batch B2) is computed ONCE per tick
// -- now() minus platform.Timeouts.ImageRefreshClaimStaleAfter -- and
// threaded through to both ListReady and every attemptRefresh call this
// tick makes: ListReady's own WHERE clause and ClaimForRefresh's own CAS
// must agree on EXACTLY the same cutoff instant, or a row ListReady
// decides is reclaimable could lose a since-moved-on ClaimForRefresh
// comparison (or vice versa) purely from tick-internal clock drift between
// two separate now() calls.
func (b *Builder) RefreshOnce(ctx context.Context) error {
	staleClaimCutoff := pgtype.Timestamptz{Time: time.Now().Add(-b.timeouts.ImageRefreshClaimStaleAfter), Valid: true}

	rows, err := b.store.ListReady(ctx, refreshBatchSize, staleClaimCutoff)
	if err != nil {
		return fmt.Errorf("imagebuild: list ready image builds: %w", err)
	}

	for _, row := range rows {
		b.attemptRefresh(ctx, row, staleClaimCutoff)
	}
	return nil
}

// attemptRefresh implements RefreshOnce's own per-row body (§19.2).
//
// # Never degrades availability
//
// The row's own `status` column is NEVER touched by this function at
// any point -- it stays 'ready', continuously servable via the OLD
// image_ref, for the entire duration a refresh build runs. Single-flight
// protection is ClaimForRefresh's own INDEPENDENT refresh_in_progress CAS
// (migrations/000040_image_builds_refresh_pump.up.sql), never the
// status='building' transition attempt/claimBatch use for a brand-new
// pending/failed row -- see that migration's own doc comment for exactly
// why reusing status here would silently reopen the availability gap this
// whole design exists to close. On success, RecordRefreshSuccess performs
// a SINGLE atomic UPDATE swapping image_ref + built_repo_shas + built_at
// (never a delete-then-insert) -- a session's own concurrent GetImageBuild
// spawn-time lookup therefore always observes either the complete OLD
// triple or the complete NEW one, never a mix, and never a moment with no
// usable ready image_ref at all.
//
// # INVARIANT: every inspected row advances its own ordering key, and
// # every claim taken is released, on every early-return path
//
// This function has several early-return paths, some of which decide
// "nothing to rebuild this tick" WITHOUT ever reaching ClaimForRefresh (a
// repo_urls decode failure, the base-only defense-in-depth guard, a
// resolveRepoSHAs error, NeedsRefresh reporting still-fresh, or a lost/
// failed ClaimForRefresh race) and some of which reach ClaimForRefresh and
// then fail AFTER taking the claim (a BuildImage failure, a marshal
// failure, or a RecordRefreshSuccess failure). Two invariants hold across
// ALL of them, deliberately stated as invariants rather than an enumerated
// branch count -- the exact set of branches has already grown more than
// once since this function was first written, and prose naming a count
// silently rots the next time it does:
//
//  1. Every branch that never took a claim (or lost the race for one)
//     calls touchChecked before returning, bumping ONLY updated_at
//     (TouchImageBuildChecked -- no other column, never a state
//     transition) -- exactly as if this row had reached a real claim. A
//     row genuinely NOT stale (a repo that simply hasn't pushed lately),
//     or one whose SHA resolution PERSISTENTLY fails (a renamed/deleted
//     repo, a token missing org access), would otherwise keep an
//     arbitrarily OLD updated_at forever and, being oldest, permanently
//     occupy the ENTIRE LIMIT window of every tick's own
//     ListReadyImageBuilds call (ORDER BY updated_at ASC) -- starving any
//     row that went stale LATER (a newer updated_at always sorts behind
//     that static front cohort, so it would never even be RETURNED, let
//     alone refreshed). This is what makes ListReadyImageBuilds' own
//     ORDER BY updated_at a genuine round-robin over the WHOLE 'ready'
//     population reachable via RefreshOnce's own query, not merely the
//     subset that happens to need a rebuild this tick.
//  2. Every branch that DID take a claim (successfully reached
//     ClaimForRefresh) releases it on every path out -- either by
//     RecordRefreshSuccess's own atomic swap-and-release, or by
//     releaseRefreshClaim (RecordRefreshFailure) -- INCLUDING when
//     RecordRefreshSuccess itself fails: the root defect audit-remediation
//     batch B2 closes was exactly this last case leaking the claim
//     (RecordRefreshSuccess failing used to return without ever releasing
//     it, wedging refresh_in_progress=true forever for that fingerprint,
//     which ALSO froze its own updated_at, recreating the same starvation
//     invariant 1 above closes -- via a different door). A claim release
//     that itself fails (e.g. the same crash/context-cancellation that
//     caused the failure being released FOR) is the one case this
//     function cannot locally repair -- that is exactly the crash window
//     staleClaimCutoff/ClaimForRefresh's own lease (platform.Timeouts.
//     ImageRefreshClaimStaleAfter) exists to heal on a LATER tick, see
//     this package's own doc.go.
func (b *Builder) attemptRefresh(ctx context.Context, row sqlcgen.ImageBuild, staleClaimCutoff pgtype.Timestamptz) {
	logger := platform.Logger(ctx).With("fingerprint", row.Fingerprint)

	if row.RefreshInProgress {
		// ListReady's own WHERE clause only ever returns an
		// already-refresh_in_progress row when its claim has gone STALE
		// (older than staleClaimCutoff) -- a healthy, actively-refreshing
		// row is excluded there entirely. Reaching this branch therefore
		// means this fingerprint's PREVIOUS claim was never released --
		// almost certainly a crashed/killed process that never reached its
		// own RecordRefreshSuccess/RecordRefreshFailure. Logged and
		// counted here, at DETECTION time, regardless of whether this
		// tick's own ClaimForRefresh below actually wins the reclaim race
		// (a concurrent tick, this pod's or another pod's, may win it
		// instead) -- the operator-visible signal this package's own
		// doc.go used to promise ("until an operator clears it") but never
		// actually delivered.
		logger.Warn("imagebuild: refresh: stale refresh_in_progress claim detected; attempting to reclaim", "refresh_started_at", row.RefreshStartedAt.Time)
		b.refreshClaimReclaimed.Add(ctx, 1)
	}

	var repoURLs map[string]string
	if err := json.Unmarshal(row.RepoUrls, &repoURLs); err != nil {
		// Same ordering-key reasoning as every other inspected-but-skipped
		// branch below: row.RepoUrls is only ever written by this
		// codebase's own json.Marshal (imageresolve.go), so a decode
		// failure here should be unreachable in practice -- but "should be"
		// is not "is", and touching the ordering key costs nothing, so this
		// row still rotates to the back of ListReadyImageBuilds' own next
		// window rather than risking a permanent front-of-queue occupant if
		// this ever does fire (future schema drift, manual data repair).
		logger.Error("imagebuild: refresh: decode repo_urls failed; skipping this tick", "error", err)
		b.touchChecked(ctx, logger, row.Fingerprint)
		return
	}
	if len(repoURLs) == 0 {
		// A base-only row is never stale in the sense this design cares
		// about -- ListReadyImageBuilds already excludes this case at the
		// SQL level, but this guard stays as defense in depth against a
		// future caller of attemptRefresh that doesn't go through
		// RefreshOnce's own query. Touches the ordering key for the same
		// reason as the decode-error branch above: unreachable via any
		// path that exists today, but free to guard against regardless.
		b.touchChecked(ctx, logger, row.Fingerprint)
		return
	}

	current, err := b.resolveRepoSHAs(ctx, repoURLs)
	if err != nil {
		// Same degrade-cleanly contract as attempt's own credential
		// failure above (§19.2): a credential-dependent failure here just
		// means try again next tick, never a crash, never any
		// effect on this fingerprint's own already-ready row -- EXCEPT its
		// own ordering key: a PERSISTENTLY failing resolution (see this
		// function's own top doc comment) must still rotate this row to
		// the back of ListReadyImageBuilds' own next window, or it would
		// permanently occupy the front of every tick's batch.
		//
		// Audit-remediation batch B3: an unsupported-host error is escalated
		// to Error (not Warn) with an explicit "permanent" callout, exactly
		// mirroring attempt's own equivalent branch -- see that branch's own
		// comment and resolveRepoSHAs' own doc comment for why this still
		// follows the SAME "touch and retry next tick" path regardless
		// (no distinct terminal failure status exists to route it to
		// instead), rather than treating it any differently mechanically.
		var hostErr *reposource.UnsupportedRepoHostError
		if errors.As(err, &hostErr) {
			logger.Error("imagebuild: refresh: resolve current tip SHAs failed: repo url names an unsupported source-control host -- PERMANENT, will not resolve on any retry until the session's repo url is fixed; will keep retrying next tick regardless", "error", err)
		} else {
			logger.Warn("imagebuild: refresh: resolve current tip SHAs failed; will retry next tick", "error", err)
		}
		b.touchChecked(ctx, logger, row.Fingerprint)
		return
	}

	var built map[string]string
	if len(row.BuiltRepoShas) > 0 {
		if err := json.Unmarshal(row.BuiltRepoShas, &built); err != nil {
			logger.Error("imagebuild: refresh: decode built_repo_shas failed; treating as stale (safe default)", "error", err)
			built = nil
		}
	}

	if !domainimagebuild.NeedsRefresh(built, current) {
		// Still fresh -- nothing to REBUILD this tick, but this row WAS
		// genuinely inspected (its current tip was resolved and compared),
		// so its own ordering key still advances -- see this function's
		// own top doc comment for why a genuinely-not-stale row must never
		// be allowed to sit at the front of the queue forever merely
		// because it keeps not needing a rebuild.
		b.touchChecked(ctx, logger, row.Fingerprint)
		return // still fresh -- nothing to do this tick.
	}

	claimed, err := b.store.ClaimForRefresh(ctx, row.Fingerprint, staleClaimCutoff)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// A normal, expected outcome: a concurrent tick (this pod's or
			// another pod's own Builder) already holds a still-fresh claim
			// on this fingerprint's refresh, or the row is no longer
			// 'ready' at all (e.g. it was never built in the first place by
			// the time this tick got here) -- never logged as an error.
			// Still touches the ordering key (invariant 1, this function's
			// own top doc comment): a lost claim race is exactly as much
			// "genuinely inspected this tick" as any other early return,
			// and was previously the ONE branch that didn't -- letting a
			// row stuck losing this race every tick permanently occupy the
			// front of the queue exactly like every other invariant-1
			// branch this function guards against.
			b.touchChecked(ctx, logger, row.Fingerprint)
			return
		}
		logger.Error("imagebuild: refresh: claim for refresh failed", "error", err)
		b.touchChecked(ctx, logger, row.Fingerprint)
		return
	}

	repos := make(map[string]ports.RepoRef, len(repoURLs))
	for name, repoURL := range repoURLs {
		repos[name] = ports.RepoRef{URL: repoURL, SHA: current[name]}
	}

	// claimedRefreshStartedAt (audit-remediation batch B2 round 2) is the
	// FENCING TOKEN for this specific claim instance -- the exact
	// refresh_started_at ClaimForRefresh just stamped and returned to THIS
	// call, never a freshly computed now(). Every write below that
	// releases or supersedes this claim (RecordRefreshSuccess,
	// releaseRefreshClaim) is scoped to it, so a write that outlives this
	// claim's own staleness window (e.g. blocked on a Postgres row lock
	// past ImageRefreshClaimStaleAfter, long enough for a concurrent tick
	// to reclaim this fingerprint) becomes a harmless no-op instead of
	// clobbering whatever the reclaiming tick has since written or is
	// still legitimately holding. See RecordImageRefreshSuccess's own
	// generated doc comment and this function's own top doc comment.
	claimedRefreshStartedAt := claimed.RefreshStartedAt

	cm := b.cacheMount(ctx, logger, claimed.Base, claimed.RuntimeVersion)
	refreshBuildStart := time.Now()
	outcome, buildErr := b.provider.BuildImage(ctx, ports.ImageSpec{
		Base:           claimed.Base,
		Repos:          repos,
		RuntimeVersion: claimed.RuntimeVersion,
		CacheMount:     cm,
	})
	b.buildTelemetry.record(ctx, "refresh", time.Since(refreshBuildStart).Seconds(), buildErr != nil)
	if buildErr != nil {
		logger.Warn("imagebuild: refresh: BuildImage failed; releasing claim, old image_ref stays servable", "error", buildErr)
		b.releaseRefreshClaim(ctx, logger, row.Fingerprint, claimedRefreshStartedAt)
		return
	}
	b.recordCachePublish(ctx, logger, cm, outcome.PublishedCacheVersion, row.Fingerprint)

	builtRepoSHAsJSON, err := json.Marshal(current)
	if err != nil {
		// Cannot happen for a plain map[string]string (mirrors attempt's
		// own identical reasoning) -- logged, claim released, never
		// propagated.
		logger.Error("imagebuild: refresh: marshal built_repo_shas failed", "error", err)
		b.releaseRefreshClaim(ctx, logger, row.Fingerprint, claimedRefreshStartedAt)
		return
	}

	imageRef := string(outcome.Ref)
	if _, err := b.store.RecordRefreshSuccess(ctx, sqlcgen.RecordImageRefreshSuccessParams{
		Fingerprint:             row.Fingerprint,
		ImageRef:                &imageRef,
		BuiltRepoShas:           builtRepoSHAsJSON,
		BuiltAt:                 pgtype.Timestamptz{Time: time.Now(), Valid: true},
		ClaimedRefreshStartedAt: claimedRefreshStartedAt,
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Either the row is no longer 'ready' at all (a should-be-rare,
			// benign race), OR -- the failure mode audit-remediation batch
			// B2 round 2 closes -- this claim has gone stale and been
			// reclaimed by a concurrent tick since THIS call took it (this
			// call was blocked, e.g. on a Postgres row lock, long enough
			// for that to happen): claimedRefreshStartedAt's own fencing
			// check no longer matches the row's CURRENT refresh_started_at.
			// Either way, this tick's own claim is gone: releasing it below
			// is scoped to claimedRefreshStartedAt too, so if a reclaim IS
			// what happened, that release is ALSO a no-op against the new
			// claim (rather than wiping it out) -- see releaseRefreshClaim's
			// own doc comment.
			logger.Warn("imagebuild: refresh: record success no-op: row no longer ready, or this claim was reclaimed; releasing (fenced)")
			b.releaseRefreshClaim(ctx, logger, row.Fingerprint, claimedRefreshStartedAt)
			return
		}
		// A genuine error (not merely a superseded/no-op row) -- e.g. a
		// connection reset between ClaimForRefresh and here. Still release
		// the claim (invariant 2, this function's own top doc comment):
		// returning here without releasing was exactly the root defect
		// audit-remediation batch B2 closes (finding: attemptRefresh leaks
		// the refresh_in_progress claim when RecordRefreshSuccess fails).
		// If THIS release call also fails (e.g. the same failure that
		// broke RecordRefreshSuccess, such as a canceled ctx, breaks it
		// too), releaseRefreshClaim's own logging says so, and
		// staleClaimCutoff's own lease is the backstop that heals it on a
		// later tick regardless.
		logger.Error("imagebuild: refresh: record success failed; releasing claim (fenced)", "error", err)
		b.releaseRefreshClaim(ctx, logger, row.Fingerprint, claimedRefreshStartedAt)
	}
}

// releaseRefreshClaim releases attemptRefresh's own refresh_in_progress
// claim without touching anything else -- shared by every one of
// attemptRefresh's own post-claim failure paths (a BuildImage failure, a
// marshal failure, and a RecordRefreshSuccess failure -- an INVARIANT,
// every post-claim failure path calls this, not a fixed enumerated list;
// see attemptRefresh's own top doc comment). The row is left exactly as it
// was: still 'ready', still serving its own old image_ref, picked up
// again at the next ImageRefreshCheckInterval tick.
//
// claimedRefreshStartedAt (audit-remediation batch B2 round 2) is the
// SAME fencing token attemptRefresh threads through RecordRefreshSuccess
// -- the exact refresh_started_at value THIS call's own ClaimForRefresh
// returned, never a freshly computed now(). It scopes this release to the
// SAME claim instance attemptRefresh originally took: if that claim has
// since gone stale and been reclaimed by a concurrent tick, the row's
// CURRENT refresh_started_at no longer matches, RecordRefreshFailure's own
// WHERE clause matches zero rows, and this release becomes a harmless
// no-op -- rather than unconditionally clearing refresh_in_progress out
// from under whichever tick (this pod's or another pod's) currently,
// legitimately holds it. See RecordImageRefreshFailure's own generated doc
// comment for the full failure mode this closes.
func (b *Builder) releaseRefreshClaim(ctx context.Context, logger *slog.Logger, fingerprint string, claimedRefreshStartedAt pgtype.Timestamptz) {
	if _, err := b.store.RecordRefreshFailure(ctx, fingerprint, claimedRefreshStartedAt); err != nil {
		logger.Error("imagebuild: refresh: release refresh claim failed", "error", err, "fingerprint", fingerprint)
	}
}

// touchChecked bumps fingerprint's own ordering key (updated_at, via
// TouchImageBuildChecked) with NO other side effect -- called by every one
// of attemptRefresh's own early-return paths that skip ClaimForRefresh
// entirely or lose/fail its own claim race (an INVARIANT, not a fixed
// enumerated list -- see attemptRefresh's own top doc comment) so
// ListReadyImageBuilds' own ORDER BY updated_at reflects genuine "last
// looked at this tick", not merely "last mutated" -- see
// TouchImageBuildChecked's own generated doc comment for the full
// starvation this rules out. A failure here is logged, never fatal to
// this tick: at worst this one row's own fairness rotation is delayed by
// a tick, which is a far smaller problem than the one this call exists to
// fix.
func (b *Builder) touchChecked(ctx context.Context, logger *slog.Logger, fingerprint string) {
	if err := b.store.TouchChecked(ctx, fingerprint); err != nil {
		logger.Warn("imagebuild: refresh: touch checked failed; this row's own fairness rotation may be delayed a tick", "error", err, "fingerprint", fingerprint)
	}
}
