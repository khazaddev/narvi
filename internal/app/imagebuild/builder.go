package imagebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
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
	// missing piece degrades resolveRepoSHAs to a clean, logged error, never
	// a crash, per §19.2's own "never blocks a spawn" invariant, which this
	// package's own background loop must honor just as strictly as the
	// spawn path itself.
	sourceControl         ports.SourceControl
	gitHubImageBuildToken string

	failureStreak metric.Int64Counter
}

// NewBuilder builds a Builder backed by store/pool (pool is needed
// directly, alongside store, for the claim step's own transaction --
// mirrors app/sessionactor/timerpump.go's claimDueTimers, which likewise
// acquires its own connection/transaction around a WithTx-scoped store),
// provider (the real ports.SandboxProvider whose BuildImage this builder
// drives), timeouts (for ImageBuildPumpInterval/ImageRefreshCheckInterval/
// backoff config, consulted by Run/PumpOnce/RefreshOnce), sourceControl
// (Step 42's own claim-time/freshness-pump SHA resolution, §19.2 -- may be
// nil), and gitHubImageBuildToken (the new platform-level credential,
// platform.Config.GitHubImageBuildToken -- may be empty).
//
// The image_build_failure_streak OTel counter is constructed exactly once,
// here, at construction time -- not per-tick, not per-row -- mirroring
// app/reconciler.NewReconciler's own orphans_reaped precedent exactly.
func NewBuilder(store *postgres.ImageBuildStore, pool *pgxpool.Pool, provider ports.SandboxProvider, timeouts platform.Timeouts, sourceControl ports.SourceControl, gitHubImageBuildToken string) (*Builder, error) {
	meter := otel.Meter(meterName)

	failureStreak, err := meter.Int64Counter(
		"image_build_failure_streak",
		metric.WithDescription("Number of image-build attempts that landed at or beyond the consecutive-failure streak threshold for their own fingerprint (§3.5: alert on streaks)."),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, fmt.Errorf("imagebuild: construct image_build_failure_streak counter: %w", err)
	}

	return &Builder{
		store:                 store,
		pool:                  pool,
		provider:              provider,
		timeouts:              timeouts,
		sourceControl:         sourceControl,
		gitHubImageBuildToken: gitHubImageBuildToken,
		failureStreak:         failureStreak,
	}, nil
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
// # Step 42's own claim-time SHA resolution (§19.1/§19.2/§19.9)
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
// -- §19.2's own explicit invariant: "Any failure anywhere in this
// credential-dependent path... is logged and degrades to today's existing
// retry/backoff behavior -- never a crash, never blocks a spawn." A row
// naming NO repos (base+runtime only) has nothing to resolve and builds
// immediately, exactly as before this Step.
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
			logger.Warn("imagebuild: claim-time SHA resolution failed; recording as a failed attempt", "error", err, "repo_count", len(repoURLs))
			b.recordFailure(ctx, logger, row)
			return
		}
		for name, repoURL := range repoURLs {
			repos[name] = ports.RepoRef{URL: repoURL, SHA: resolved[name]}
		}
		builtRepoSHAs = resolved
	}

	ref, buildErr := b.provider.BuildImage(ctx, ports.ImageSpec{
		Base:           row.Base,
		Repos:          repos,
		RuntimeVersion: row.RuntimeVersion,
	})
	if buildErr != nil {
		logger.Warn("imagebuild: BuildImage failed", "error", buildErr)
		b.recordFailure(ctx, logger, row)
		return
	}

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

	imageRef := string(ref)
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

// errGitHubImageBuildTokenNotConfigured and errSourceControlNotConfigured
// are resolveRepoSHAs' own typed degrade-cleanly reasons (§19.2: "missing/
// invalid platform credential... is logged and degrades to today's
// existing retry/backoff behavior") -- named sentinels rather than a bare
// fmt.Errorf string so a caller/log line names EXACTLY which piece is
// missing, mirroring this codebase's own "named error, never a bare
// generic one" convention (internal/platform/config.go's own
// InvalidHMACSecretError/MissingRequiredEnvError family).
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
		owner, repoName, err := parseOwnerRepo(repoURL)
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

// parseOwnerRepo extracts (owner, repo) from a repo clone URL's own path
// -- mirrors internal/app/sessionactor/pushpr.go's own identical helper
// exactly (a small, deliberately duplicated parse rather than an
// exported, shared one: this package and sessionactor have no other
// coupling, and contractdrift.go's own doc comment already establishes
// the precedent of re-deriving this kind of thing independently rather
// than threading a shared intermediate value across otherwise-unrelated
// features).
func parseOwnerRepo(rawURL string) (owner, repo string, err error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", "", fmt.Errorf("parse repo clone url %q: %w", rawURL, err)
	}

	trimmed := strings.Trim(parsed.Path, "/")
	trimmed = strings.TrimSuffix(trimmed, ".git")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("repo clone url %q does not have an /owner/repo path", rawURL)
	}
	return parts[0], parts[1], nil
}

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
// any row beyond that cap is simply picked up on a later tick (ListReady's
// own ORDER BY updated_at gives across-tick fairness -- see that query's
// own generated doc comment).
//
// One row's own refresh failure (a resolution error, a lost claim race, a
// BuildImage failure) is isolated -- logged, and does NOT abort the rest
// of this tick's own batch, exactly like PumpOnce's own per-row isolation.
func (b *Builder) RefreshOnce(ctx context.Context) error {
	rows, err := b.store.ListReady(ctx, refreshBatchSize)
	if err != nil {
		return fmt.Errorf("imagebuild: list ready image builds: %w", err)
	}

	for _, row := range rows {
		b.attemptRefresh(ctx, row)
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
func (b *Builder) attemptRefresh(ctx context.Context, row sqlcgen.ImageBuild) {
	logger := platform.Logger(ctx).With("fingerprint", row.Fingerprint)

	var repoURLs map[string]string
	if err := json.Unmarshal(row.RepoUrls, &repoURLs); err != nil {
		logger.Error("imagebuild: refresh: decode repo_urls failed; skipping this tick", "error", err)
		return
	}
	if len(repoURLs) == 0 {
		// A base-only row is never stale in the sense this design cares
		// about -- ListReadyImageBuilds already excludes this case at the
		// SQL level, but this guard stays as defense in depth against a
		// future caller of attemptRefresh that doesn't go through
		// RefreshOnce's own query.
		return
	}

	current, err := b.resolveRepoSHAs(ctx, repoURLs)
	if err != nil {
		// §19.2's own explicit invariant: a credential-dependent failure
		// here degrades to "try again next tick", never a crash, never any
		// effect on this fingerprint's own already-ready row.
		logger.Warn("imagebuild: refresh: resolve current tip SHAs failed; will retry next tick", "error", err)
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
		return // still fresh -- nothing to do this tick.
	}

	claimed, err := b.store.ClaimForRefresh(ctx, row.Fingerprint)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// A normal, expected outcome: a concurrent tick (this pod's or
			// another pod's own Builder) already claimed this fingerprint's
			// refresh, or the row is no longer 'ready' at all (e.g. it was
			// never built in the first place by the time this tick got
			// here) -- never logged as an error.
			return
		}
		logger.Error("imagebuild: refresh: claim for refresh failed", "error", err)
		return
	}

	repos := make(map[string]ports.RepoRef, len(repoURLs))
	for name, repoURL := range repoURLs {
		repos[name] = ports.RepoRef{URL: repoURL, SHA: current[name]}
	}

	ref, buildErr := b.provider.BuildImage(ctx, ports.ImageSpec{
		Base:           claimed.Base,
		Repos:          repos,
		RuntimeVersion: claimed.RuntimeVersion,
	})
	if buildErr != nil {
		logger.Warn("imagebuild: refresh: BuildImage failed; releasing claim, old image_ref stays servable", "error", buildErr)
		b.releaseRefreshClaim(ctx, logger, row.Fingerprint)
		return
	}

	builtRepoSHAsJSON, err := json.Marshal(current)
	if err != nil {
		// Cannot happen for a plain map[string]string (mirrors attempt's
		// own identical reasoning) -- logged, claim released, never
		// propagated.
		logger.Error("imagebuild: refresh: marshal built_repo_shas failed", "error", err)
		b.releaseRefreshClaim(ctx, logger, row.Fingerprint)
		return
	}

	imageRef := string(ref)
	if _, err := b.store.RecordRefreshSuccess(ctx, sqlcgen.RecordImageRefreshSuccessParams{
		Fingerprint:   row.Fingerprint,
		ImageRef:      &imageRef,
		BuiltRepoShas: builtRepoSHAsJSON,
		BuiltAt:       pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The row is no longer 'ready' at all (a should-be-rare,
			// benign race) -- logged, not fatal to this tick.
			logger.Warn("imagebuild: refresh: record success no-op: row no longer ready")
			return
		}
		logger.Error("imagebuild: refresh: record success failed", "error", err)
	}
}

// releaseRefreshClaim releases attemptRefresh's own refresh_in_progress
// claim without touching anything else -- shared by every one of
// attemptRefresh's own post-claim failure paths (BuildImage failure, a
// marshal failure). The row is left exactly as it was: still 'ready',
// still serving its own old image_ref, picked up again at the next
// ImageRefreshCheckInterval tick.
func (b *Builder) releaseRefreshClaim(ctx context.Context, logger *slog.Logger, fingerprint string) {
	if _, err := b.store.RecordRefreshFailure(ctx, fingerprint); err != nil {
		logger.Error("imagebuild: refresh: release refresh claim failed", "error", err, "fingerprint", fingerprint)
	}
}
