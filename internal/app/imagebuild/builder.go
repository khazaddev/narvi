package imagebuild

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

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

// Builder is the process-wide background image-build loop (see doc.go for
// the full writeup). Constructed once per process (NewBuilder), then run
// via its own Run method -- exactly like app/reconciler.Reconciler and
// app/sessionactor.Registry's RunTimerPump.
type Builder struct {
	store    *postgres.ImageBuildStore
	pool     *pgxpool.Pool
	provider ports.SandboxProvider
	timeouts platform.Timeouts

	failureStreak metric.Int64Counter
}

// NewBuilder builds a Builder backed by store/pool (pool is needed
// directly, alongside store, for the claim step's own transaction --
// mirrors app/sessionactor/timerpump.go's claimDueTimers, which likewise
// acquires its own connection/transaction around a WithTx-scoped store),
// provider (the real ports.SandboxProvider whose BuildImage this builder
// drives), and timeouts (for ImageBuildPumpInterval/backoff config,
// consulted by Run/PumpOnce).
//
// The image_build_failure_streak OTel counter is constructed exactly once,
// here, at construction time -- not per-tick, not per-row -- mirroring
// app/reconciler.NewReconciler's own orphans_reaped precedent exactly.
func NewBuilder(store *postgres.ImageBuildStore, pool *pgxpool.Pool, provider ports.SandboxProvider, timeouts platform.Timeouts) (*Builder, error) {
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
		store:         store,
		pool:          pool,
		provider:      provider,
		timeouts:      timeouts,
		failureStreak: failureStreak,
	}, nil
}

// Run runs the process-wide image-build loop until ctx is done -- mirrors
// app/reconciler.Reconciler.Run/app/sessionactor's own RunTimerPump
// exactly: a ticker on platform.Timeouts.ImageBuildPumpInterval, calling
// PumpOnce each tick, logging (never propagating) any per-tick error so
// one bad tick never kills the whole loop. The caller starts this via its
// own errgroup.Go exactly once per process.
func (b *Builder) Run(ctx context.Context) error {
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
// (decode error, no-SHA-resolution-yet, BuildImage error, a failed/no-op
// outcome-record call) is logged and returns -- it never propagates, so
// one row's problem can never abort the rest of PumpOnce's own batch.
//
// # Step 41/42 boundary (§19.1 vs §19.9) -- documented design decision
//
// row.RepoUrls (§19.1's redefined image_builds column) carries the
// fingerprint's own URL-keyed inputs (repo name -> normalized clone URL),
// NEVER a resolved SHA -- deliberately: the whole point of the
// redefinition is that computing a fingerprint (imageresolve.go's
// resolveAndSetImage, spawn-side) never resolves a SHA at all, for ANY
// spawn, warm-hit or miss alike. §19.1's own prose says the builder
// resolves each repo's default-branch tip SHA "at claim time" -- but
// §19.9's phasing note assigns that exact claim-time SHA resolution, and
// the new platform-level GitHub credential it needs (a session-creator
// token cannot be borrowed here: this pump has no session/creator context
// at all, by construction), to Step 42, not this one. Concretely, that
// means: this Step (41) has NO mechanism ANYWHERE to turn a repo's URL
// into a concrete SHA outside of a live spawn's own resolved-image lookup
// -- there is deliberately no claim-time resolution logic here yet.
//
// A row naming at least one repo therefore cannot be built for real in
// Step 41 -- there is no way to construct a real ports.RepoRef{URL, SHA}
// for it (never pass an empty/zero SHA to BuildImage silently). This is
// handled explicitly and cleanly, the same way a decode failure already
// is: logged, recorded as a failed attempt (so the row cycles through
// EvaluateBackoff's own retry schedule rather than being stuck in
// 'building' forever), and this tick moves on. Once Step 42 lands its own
// claim-time resolution, this exact branch is where that resolution call
// belongs. A row naming NO repos (base+runtime only) has nothing to
// resolve and DOES build for real, even in Step 41 -- there is no reason
// to block that case on Step 42's own work.
func (b *Builder) attempt(ctx context.Context, row sqlcgen.ImageBuild) {
	logger := platform.Logger(ctx).With("fingerprint", row.Fingerprint, "attempt_count", row.AttemptCount)

	var repoURLs map[string]string
	if err := json.Unmarshal(row.RepoUrls, &repoURLs); err != nil {
		logger.Error("imagebuild: decode repo_urls failed; recording as a failed attempt", "error", err)
		b.recordFailure(ctx, logger, row)
		return
	}

	if len(repoURLs) > 0 {
		// See this function's own "Step 41/42 boundary" doc comment above:
		// no claim-time SHA resolution mechanism exists yet (Step 42,
		// §19.2/§19.9) -- a repo-bearing row cannot yet become a real,
		// reproducible BuildImage call.
		logger.Warn("imagebuild: no claim-time SHA resolution available yet (Step 42, §19.2/§19.9); cannot build a repo-bearing fingerprint; recording as a failed attempt",
			"repo_count", len(repoURLs))
		b.recordFailure(ctx, logger, row)
		return
	}

	builtRepoSHAs := map[string]string{}
	ref, buildErr := b.provider.BuildImage(ctx, ports.ImageSpec{
		Base:           row.Base,
		Repos:          map[string]ports.RepoRef{},
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
