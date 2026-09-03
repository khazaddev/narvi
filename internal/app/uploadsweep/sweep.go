package uploadsweep

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/narvidev/narvi/contracts/gen/go/sandboxws"
	"github.com/narvidev/narvi/internal/adapters/outbound/objstore"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/ports"
	"github.com/narvidev/narvi/internal/platform"
)

// meterName is this package's own OTel meter name (§5.3 "day one, not
// later" observability) -- mirrors internal/app/reconciler's own
// "narvi/<package>" naming convention exactly.
const meterName = "narvi/uploadsweep"

// batchSize bounds one sweep tick's own work, mirroring
// internal/app/automation/sweep.go's own sweepBatchSize precedent -- a
// deployment with an unusually large abandoned-upload backlog is swept
// down over several ticks rather than in one unbounded pass.
const batchSize = 100

// Sweeper is the process-wide upload-abandonment sweep loop (see doc.go
// for the full writeup). Constructed once per process (NewSweeper), then
// run via its own Run method -- exactly like internal/app/reconciler's
// own NewReconciler/Run pair.
type Sweeper struct {
	pool      *pgxpool.Pool
	artifacts *postgres.ArtifactStore
	events    *postgres.EventStore
	outbox    *postgres.OutboxStore
	sandboxes *postgres.SandboxStore

	broadcaster ports.EventBroadcaster
	timeouts    platform.Timeouts

	uploadsAbandoned metric.Int64Counter
}

// NewSweeper builds a Sweeper. The uploads_abandoned OTel counter is
// constructed exactly once, here, mirroring
// internal/app/reconciler.NewReconciler's own orphans_reaped counter
// construction precedent.
func NewSweeper(pool *pgxpool.Pool, artifacts *postgres.ArtifactStore, events *postgres.EventStore, outbox *postgres.OutboxStore, sandboxes *postgres.SandboxStore, broadcaster ports.EventBroadcaster, timeouts platform.Timeouts) (*Sweeper, error) {
	meter := otel.Meter(meterName)

	uploadsAbandoned, err := meter.Int64Counter(
		"uploads_abandoned",
		metric.WithDescription("Number of pending upload artifact rows marked failed(abandoned) by the abandonment sweep."),
		metric.WithUnit("{upload}"),
	)
	if err != nil {
		return nil, fmt.Errorf("uploadsweep: construct uploads_abandoned counter: %w", err)
	}

	return &Sweeper{
		pool:             pool,
		artifacts:        artifacts,
		events:           events,
		outbox:           outbox,
		sandboxes:        sandboxes,
		broadcaster:      broadcaster,
		timeouts:         timeouts,
		uploadsAbandoned: uploadsAbandoned,
	}, nil
}

// Run runs the process-wide sweep loop until ctx is done -- mirrors
// internal/app/reconciler.Reconciler.Run exactly: a ticker on
// platform.Timeouts.UploadAbandonmentSweepInterval, calling SweepOnce each
// tick, logging (never propagating) any per-tick error so one bad tick
// never kills the whole loop. The caller starts this via its own
// errgroup.Go exactly once per process.
func (s *Sweeper) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.timeouts.UploadAbandonmentSweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.SweepOnce(ctx); err != nil {
				platform.Logger(ctx).Error("uploadsweep: tick failed", "error", err)
			}
		}
	}
}

// SweepOnce runs one sweep pass: every `pending` upload artifact row
// older than UploadPendingSweepAfter, oldest first, up to batchSize rows,
// is resolved to failed(abandoned). One row's own failure is logged and
// does not stop the batch -- exported so a test can drive one pass
// directly without waiting on Run's own ticker.
func (s *Sweeper) SweepOnce(ctx context.Context) error {
	cutoff := pgtype.Timestamptz{Time: time.Now().Add(-s.timeouts.UploadPendingSweepAfter), Valid: true}

	rows, err := s.artifacts.ListPendingUploadsOlderThan(ctx, cutoff, batchSize)
	if err != nil {
		return fmt.Errorf("uploadsweep: list pending uploads older than cutoff: %w", err)
	}

	var errs []error
	for _, row := range rows {
		if err := s.resolveAbandoned(ctx, row); err != nil {
			errs = append(errs, fmt.Errorf("uploadsweep: resolve abandoned upload %s: %w", row.ID.String(), err))
			continue
		}
		s.uploadsAbandoned.Add(ctx, 1)
	}
	return errors.Join(errs...)
}

// resolveAbandoned transitions one row from pending to failed(abandoned),
// in one transaction with an appended CP-synthesized artifact event and a
// blob_delete outbox entry, broadcasting only after commit -- the same
// shape internal/adapters/inbound/httpapi's own confirmUploadCore uses for
// its own failure path (see this package's own doc.go for why this is a
// separate, small implementation rather than a shared one).
func (s *Sweeper) resolveAbandoned(ctx context.Context, row sqlcgen.Artifact) error {
	logger := platform.Logger(ctx)

	var gen int32
	if sandboxRow, err := s.sandboxes.Get(ctx, row.SessionID); err == nil {
		gen = sandboxRow.Gen
	} else if !errors.Is(err, pgx.ErrNoRows) {
		logger.Warn("uploadsweep: get sandbox gen for artifact event failed; defaulting to 0", "error", err)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rowsAffected, err := s.artifacts.WithTx(tx).MarkUploadFailedIfPending(ctx, row.ID, row.SessionID, sqlcgen.ArtifactFailureReasonAbandoned)
	if err != nil {
		return fmt.Errorf("guarded transition: %w", err)
	}
	if rowsAffected == 0 {
		// Lost the race to a concurrent confirm between ListPendingUploadsOlderThan
		// above and this UPDATE -- nothing to do, not a failure.
		return nil
	}

	reason := sqlcgen.ArtifactFailureReasonAbandoned
	wireStatus := sandboxws.ArtifactStatus(sqlcgen.ArtifactStatusFailed)
	eventPayload, err := json.Marshal(sandboxws.Artifact{
		Type:          "artifact",
		MessageId:     uuid.NewString(),
		SessionId:     row.SessionID.String(),
		Gen:           int(gen),
		ArtifactType:  sandboxws.ArtifactArtifactTypeUpload,
		Url:           row.Url,
		Metadata:      sandboxws.ArtifactMetadata{},
		Status:        &wireStatus,
		FailureReason: &sandboxws.ArtifactFailureReason{Value: string(reason)},
	})
	if err != nil {
		return fmt.Errorf("marshal artifact event: %w", err)
	}

	createdEvent, err := s.events.WithTx(tx).Create(ctx, sqlcgen.CreateEventParams{
		SessionID: row.SessionID,
		Type:      "artifact",
		MessageID: uuid.NewString(),
		Payload:   eventPayload,
	})
	if err != nil {
		return fmt.Errorf("append artifact event: %w", err)
	}

	// Reads the row's own already-stored blob_key (set once at mint,
	// §28.4) rather than recomputing it -- the row is the source of
	// truth, and this avoids any risk of drifting from
	// internal/domain/upload.BuildBlobKey's own convention.
	var blobKey string
	if row.BlobKey != nil {
		blobKey = *row.BlobKey
	}
	blobDeletePayload, err := json.Marshal(objstore.BlobDeletePayload{Key: blobKey})
	if err != nil {
		return fmt.Errorf("marshal blob_delete outbox payload: %w", err)
	}
	if _, err := s.outbox.WithTx(tx).Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: row.SessionID,
		Kind:      string(ports.NotificationKindBlobDelete),
		Payload:   blobDeletePayload,
	}); err != nil {
		return fmt.Errorf("enqueue blob_delete outbox entry: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	if s.broadcaster != nil && createdEvent.Inserted {
		s.broadcaster.Broadcast(row.SessionID.String(), eventPayload)
	}
	return nil
}
