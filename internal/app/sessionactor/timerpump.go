package sessionactor

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/platform"
)

// timerPumpBatchSize bounds how many due timers a single pump tick
// claims -- a plain count, not a duration, so (like mailboxBufferSize) it
// is a Go constant rather than a platform.Timeouts field.
const timerPumpBatchSize = 50

// RunTimerPump runs the process-wide timer-pump loop (§2: "A per-pod
// timer pump polls due timers ... and delivers them as actor commands")
// until ctx is done. The caller is expected to start this via its own
// errgroup.Go (§11: no naked `go` statements) exactly once per process,
// independent of any one session's Actor.
func (r *Registry) RunTimerPump(ctx context.Context) error {
	ticker := time.NewTicker(r.timeouts.TimerPumpInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := r.PumpOnce(ctx); err != nil {
				platform.Logger(ctx).Error("sessionactor: timer pump tick failed", "error", err)
			}
		}
	}
}

// PumpOnce runs exactly one pump tick (§2): claims a batch of due
// timers in one transaction (ListDue, then Claim per row -- pushing
// fires_at forward by TimerClaimDuration so a concurrent/later tick, this
// pod or another, never redelivers the same row before the claim window
// elapses), commits, then delivers each claimed timer to its session's
// Actor as a TimerFired command. Exported (rather than only reachable
// through RunTimerPump's loop) so tests can drive exactly one tick
// deterministically.
func (r *Registry) PumpOnce(ctx context.Context) error {
	claimed, err := r.claimDueTimers(ctx)
	if err != nil {
		return err
	}

	for _, t := range claimed {
		r.deliver(ctx, t)
	}
	return nil
}

func (r *Registry) claimDueTimers(ctx context.Context) ([]sqlcgen.SessionTimer, error) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txTimers := r.stores.timer.WithTx(tx)

	due, err := txTimers.ListDue(ctx, timerPumpBatchSize)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	claimed := make([]sqlcgen.SessionTimer, 0, len(due))
	for _, t := range due {
		c, err := txTimers.Claim(ctx, sqlcgen.ClaimDueTimerParams{
			FiresAt:   pgtype.Timestamptz{Time: now.Add(r.timeouts.TimerClaimDuration), Valid: true},
			SessionID: t.SessionID,
			Name:      t.Name,
		})
		if err != nil {
			return nil, err
		}
		claimed = append(claimed, c)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return claimed, nil
}

// deliver hydrates (or reuses) the Actor owning t.SessionID and sends it
// a TimerFired command. Failures are logged, never propagated: one
// session's delivery problem must never abort the rest of the batch.
func (r *Registry) deliver(ctx context.Context, t sqlcgen.SessionTimer) {
	a, err := r.GetOrSpawn(ctx, t.SessionID)
	if err != nil {
		if errors.Is(err, ErrSessionActorElsewhere) {
			// Another pod owns this session; it will pick this same
			// timer up on ITS OWN next poll tick once the claim window
			// elapses. An accepted, documented latency trade-off, not a
			// bug -- no cross-pod command forwarding.
			return
		}
		platform.Logger(ctx).Error("sessionactor: timer pump: GetOrSpawn failed",
			"error", err, "session_id", t.SessionID.String(), "timer_name", t.Name)
		return
	}

	if err := a.Send(ctx, TimerFired{Name: t.Name}); err != nil {
		platform.Logger(ctx).Error("sessionactor: timer pump: delivering TimerFired failed",
			"error", err, "session_id", t.SessionID.String(), "timer_name", t.Name)
	}
}
