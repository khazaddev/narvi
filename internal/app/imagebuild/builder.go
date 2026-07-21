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
// (decode error, BuildImage error, a failed/no-op outcome-record call) is
// logged and returns -- it never propagates, so one row's problem can
// never abort the rest of PumpOnce's own batch.
func (b *Builder) attempt(ctx context.Context, row sqlcgen.ImageBuild) {
	logger := platform.Logger(ctx).With("fingerprint", row.Fingerprint, "attempt_count", row.AttemptCount)

	var repoSHAs map[string]string
	if err := json.Unmarshal(row.RepoShas, &repoSHAs); err != nil {
		logger.Error("imagebuild: decode repo_shas failed; recording as a failed attempt", "error", err)
		b.recordFailure(ctx, logger, row)
		return
	}

	ref, buildErr := b.provider.BuildImage(ctx, ports.ImageSpec{
		Base:           row.Base,
		RepoSHAs:       repoSHAs,
		RuntimeVersion: row.RuntimeVersion,
	})
	if buildErr != nil {
		logger.Warn("imagebuild: BuildImage failed", "error", buildErr)
		b.recordFailure(ctx, logger, row)
		return
	}

	imageRef := string(ref)
	if _, err := b.store.RecordSuccess(ctx, sqlcgen.RecordImageBuildSuccessParams{
		Fingerprint: row.Fingerprint,
		ImageRef:    &imageRef,
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
