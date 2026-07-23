package outboxworker

import (
	"context"
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
	domainoutbox "github.com/khazaddev/narvi/internal/domain/outbox"
	"github.com/khazaddev/narvi/internal/platform"
)

// meterName is this package's own OTel meter name -- mirrors app/
// imagebuild's/app/reconciler's own "narvi/<package>" convention exactly.
const meterName = "narvi/outboxworker"

// pumpBatchSize bounds how many due outbox rows a single tick claims -- a
// plain count, not a duration, so (mirroring imagebuild.pumpBatchSize's own
// identical precedent) it is a Go constant rather than a platform.Timeouts
// field. Matches imagebuild.pumpBatchSize's own value: a real outbound
// notifier call is a genuine network operation, not free, so a bounded
// batch keeps one tick's own wall-clock time bounded.
const pumpBatchSize = 20

// Builder is the process-wide background outbox-delivery loop (see doc.go
// for the full writeup). Constructed once per process (NewBuilder), then
// run via its own Run method -- exactly like app/imagebuild.Builder and
// app/reconciler.Reconciler.
type Builder struct {
	store     *postgres.OutboxStore
	pool      *pgxpool.Pool
	notifiers map[ports.NotificationKind]ports.Notifier
	timeouts  platform.Timeouts

	outboxLag       metric.Int64Gauge
	deadLetterCount metric.Int64Counter
}

// NewBuilder builds a Builder backed by store/pool (pool is needed
// directly, alongside store, for the claim step's own transaction --
// mirrors app/imagebuild.NewBuilder's own identical reasoning), notifiers
// (the kind->Notifier routing map this Builder's own attempt step
// consults -- see this package's own doc.go), and timeouts (for
// OutboxPumpInterval/OutboxClaimDuration/OutboxDeliveryTimeout/backoff
// config, consulted by Run/PumpOnce).
//
// The outbox_lag gauge and outbox_dead_letter counter are constructed
// exactly once, here, at construction time -- not per-tick, not per-row --
// mirroring app/imagebuild.NewBuilder's own image_build_failure_streak
// precedent exactly.
func NewBuilder(store *postgres.OutboxStore, pool *pgxpool.Pool, notifiers map[ports.NotificationKind]ports.Notifier, timeouts platform.Timeouts) (*Builder, error) {
	meter := otel.Meter(meterName)

	outboxLag, err := meter.Int64Gauge(
		"outbox_lag_seconds",
		metric.WithDescription("Age, in seconds, of the oldest still-due outbox row claimed at the start of the most recent pump tick (§5.3: outbox lag) -- zero when nothing was due."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("outboxworker: construct outbox_lag_seconds gauge: %w", err)
	}

	deadLetterCount, err := meter.Int64Counter(
		"outbox_dead_letter_total",
		metric.WithDescription("Number of outbox entries this Builder has dead-lettered after exhausting domain/outbox.MaxAttempts delivery attempts."),
		metric.WithUnit("{entry}"),
	)
	if err != nil {
		return nil, fmt.Errorf("outboxworker: construct outbox_dead_letter_total counter: %w", err)
	}

	return &Builder{
		store:           store,
		pool:            pool,
		notifiers:       notifiers,
		timeouts:        timeouts,
		outboxLag:       outboxLag,
		deadLetterCount: deadLetterCount,
	}, nil
}

// Run runs the process-wide outbox-delivery loop until ctx is done --
// mirrors app/imagebuild.Builder.Run/app/reconciler.Reconciler.Run
// exactly: a ticker on platform.Timeouts.OutboxPumpInterval, calling
// PumpOnce each tick, logging (never propagating) any per-tick error so
// one bad tick never kills the whole loop. The caller starts this via its
// own errgroup.Go exactly once per process.
func (b *Builder) Run(ctx context.Context) error {
	ticker := time.NewTicker(b.timeouts.OutboxPumpInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := b.PumpOnce(ctx); err != nil {
				platform.Logger(ctx).Error("outboxworker: tick failed", "error", err)
			}
		}
	}
}

// PumpOnce runs exactly one pump tick: claims a batch of due outbox rows,
// records the outbox_lag_seconds gauge for this tick, then attempts
// delivery for each claimed row, OUTSIDE any transaction. Exported (rather
// than only reachable through Run's own loop) so tests can drive exactly
// one tick deterministically, matching imagebuild.Builder.PumpOnce's own
// precedent.
//
// A failure in the batch-level claim step aborts the tick and returns the
// error (Run logs it) -- but once a batch is successfully claimed, one
// row's own delivery failure (or a failure recording its outcome) is
// isolated: logged, and does NOT abort the rest of the batch, exactly like
// imagebuild.Builder.PumpOnce's own per-row isolation.
func (b *Builder) PumpOnce(ctx context.Context) error {
	claimed, oldestCreatedAt, err := b.claimBatch(ctx)
	if err != nil {
		return fmt.Errorf("outboxworker: claim batch: %w", err)
	}

	lag := int64(0)
	if !oldestCreatedAt.IsZero() {
		lag = int64(time.Since(oldestCreatedAt).Seconds())
		if lag < 0 {
			lag = 0
		}
	}
	b.outboxLag.Record(ctx, lag)

	for _, row := range claimed {
		b.attempt(ctx, row)
	}
	return nil
}

// claimBatch runs the ENTIRE claim step inside one transaction:
// ListDuePending (FOR UPDATE SKIP LOCKED -- so a concurrent tick, this
// pod's or another pod's own Builder, claims a DISJOINT batch rather than
// double-claiming the same row), then Claim for each due row (bumps
// next_attempt_at forward by OutboxClaimDuration, increments attempts),
// then commits -- exactly mirroring imagebuild.Builder.claimBatch's own
// shape. Also returns the oldest CreatedAt among the due rows (the zero
// time.Time if none were due), so PumpOnce can record the outbox_lag_seconds
// gauge for this tick.
func (b *Builder) claimBatch(ctx context.Context) ([]sqlcgen.Outbox, time.Time, error) {
	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txStore := b.store.WithTx(tx)

	due, err := txStore.ListDuePending(ctx, pumpBatchSize)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("list due outbox entries: %w", err)
	}

	var oldestCreatedAt time.Time
	now := time.Now()
	claimed := make([]sqlcgen.Outbox, 0, len(due))
	for _, row := range due {
		if row.CreatedAt.Valid && (oldestCreatedAt.IsZero() || row.CreatedAt.Time.Before(oldestCreatedAt)) {
			oldestCreatedAt = row.CreatedAt.Time
		}

		c, err := txStore.Claim(ctx, row.ID, pgtype.Timestamptz{Time: now.Add(b.timeouts.OutboxClaimDuration), Valid: true})
		if err != nil {
			return nil, time.Time{}, fmt.Errorf("claim outbox entry %s: %w", row.ID.String(), err)
		}
		claimed = append(claimed, c)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, time.Time{}, fmt.Errorf("commit: %w", err)
	}
	return claimed, oldestCreatedAt, nil
}

// attempt performs the real (possibly slow, network-bound) delivery for
// one already-claimed row OUTSIDE any transaction, bounded by
// OutboxDeliveryTimeout, then records the outcome via a single, pool-
// scoped statement. Every failure here (no notifier registered for this
// kind, Deliver error, a failed/no-op outcome-record call) is logged and
// returns -- it never propagates, so one row's problem can never abort the
// rest of PumpOnce's own batch, mirroring imagebuild.Builder.attempt's own
// identical isolation.
func (b *Builder) attempt(ctx context.Context, row sqlcgen.Outbox) {
	logger := platform.Logger(ctx).With("outbox_id", row.ID.String(), "kind", row.Kind, "attempts", row.Attempts)

	notifier, ok := b.notifiers[ports.NotificationKind(row.Kind)]
	if !ok {
		logger.Error("outboxworker: no notifier registered for kind; recording as a failed attempt")
		b.recordFailure(ctx, logger, row, fmt.Sprintf("no notifier registered for kind %q", row.Kind))
		return
	}

	deliverCtx, cancel := context.WithTimeout(ctx, b.timeouts.OutboxDeliveryTimeout)
	defer cancel()

	if err := notifier.Deliver(deliverCtx, ports.Notification{Kind: ports.NotificationKind(row.Kind), Payload: row.Payload}); err != nil {
		logger.Warn("outboxworker: Deliver failed", "error", err)
		b.recordFailure(ctx, logger, row, err.Error())
		return
	}

	if _, err := b.store.MarkDelivered(ctx, row.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The row is no longer 'pending' -- a should-be-rare, benign
			// race (e.g. a bug elsewhere ever double-claims a row); logged,
			// not fatal to this tick.
			logger.Warn("outboxworker: mark delivered no-op: row no longer pending")
			return
		}
		logger.Error("outboxworker: mark delivered failed", "error", err)
	}
}

// recordFailure computes the next retry time (or dead-letter decision) via
// domain/outbox.EvaluateBackoff and records the failure -- shared by
// every attempt failure path above (no notifier registered, Deliver
// error).
func (b *Builder) recordFailure(ctx context.Context, logger *slog.Logger, row sqlcgen.Outbox, lastError string) {
	now := time.Now()
	decision := domainoutbox.EvaluateBackoff(int(row.Attempts), domainoutbox.BackoffConfig{
		BaseDelay: b.timeouts.OutboxBackoffBase,
		MaxDelay:  b.timeouts.OutboxBackoffMax,
	}, now)

	if decision.DeadLetter {
		logger.Warn("outboxworker: outbox entry has exhausted max delivery attempts; dead-lettering",
			"max_attempts", domainoutbox.MaxAttempts,
		)
		if _, err := b.store.MarkDeadLetter(ctx, row.ID, lastError); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				logger.Warn("outboxworker: mark dead letter no-op: row no longer pending")
				return
			}
			logger.Error("outboxworker: mark dead letter failed", "error", err)
			return
		}
		b.deadLetterCount.Add(ctx, 1)
		return
	}

	if _, err := b.store.RecordFailure(ctx, row.ID, pgtype.Timestamptz{Time: decision.NextRetryAt, Valid: true}, lastError); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("outboxworker: record failure no-op: row no longer pending")
			return
		}
		logger.Error("outboxworker: record failure failed", "error", err)
	}
}
