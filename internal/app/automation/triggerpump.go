package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	domainautomation "github.com/khazaddev/narvi/internal/domain/automation"
	"github.com/khazaddev/narvi/internal/platform"
)

// cronTriggerConfigJSON is the on-wire shape of a TriggerTypeCron
// automation's own trigger_config column -- {"schedule": "<5-field cron
// expr>"}. Deliberately this package's OWN small, private, decode-only
// copy of the shape internal/adapters/inbound/httpapi's own automations.go
// marshals at automation-creation time, rather than a shared exported
// helper: app/automation already imports httpapi (fanout.go, for
// CreateSessionOnTx), so httpapi importing back would be a cycle. This
// package only ever needs to DECODE a cron schedule (the trigger pump
// below); it has no reason to also carry the github/linear trigger_config
// shapes httpapi's own Get/List responses decode, since those two trigger
// types are validated+stored but not yet live-evaluated by this Step (see
// internal/domain/automation/doc.go's own writeup).
type cronTriggerConfigJSON struct {
	Schedule string `json:"schedule"`
}

func unmarshalCronTriggerConfig(raw []byte) (domainautomation.CronTriggerConfig, error) {
	var wire cronTriggerConfigJSON
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &wire); err != nil {
			return domainautomation.CronTriggerConfig{}, fmt.Errorf("automation: unmarshal cron trigger config: %w", err)
		}
	}
	return domainautomation.CronTriggerConfig{Schedule: wire.Schedule}, nil
}

// EvaluateCronTriggersOnce runs exactly one cron-trigger evaluation tick
// (§8.4's own "cron path"): lists every active, TriggerTypeCron automation
// (ListActiveCronAutomations, small -- expected to stay a handful of
// rows), and for each whose own schedule matches the current UTC minute
// (domainautomation.CronMatches), attempts to claim this minute's own fire
// (ClaimCronFire's CAS) and, on winning it, calls CreateInvocation with a
// fresh snapshot of the automation's own current repos as targets --
// mirrors invocationenqueue.go's own doc comment: "Step 52's own future
// trigger evaluator is expected to check this same status before ever
// calling CreateInvocation" -- ListActiveCronAutomations' own "AND status =
// 'active'" filter is exactly that check, evaluated fresh every tick (a
// paused automation simply stops appearing in this list, with no separate
// gate needed).
//
// Exported (rather than only reachable through Run's own loop) so tests
// can drive exactly one tick deterministically, matching PumpOnce/
// ReconcileOnce/SweepOnce's own precedent. now is read ONCE per tick
// (never per-row), mirroring SweepOnce's own identical "one cutoff/instant
// computed once" precedent.
//
// A failure in the batch-level list step aborts the tick and returns the
// error (Run logs it) -- but once listed, one automation's own evaluation
// failure (a malformed trigger_config, a claim error) is isolated: logged,
// and does NOT abort the rest of the batch, exactly like every other pump
// in this package.
func (e *Engine) EvaluateCronTriggersOnce(ctx context.Context) error {
	now := time.Now()
	minuteBucket := pgtype.Timestamptz{Time: now.Truncate(e.timeouts.AutomationCronGranularity), Valid: true}

	rows, err := e.automations.ListActiveCronAutomations(ctx)
	if err != nil {
		return fmt.Errorf("automation: list active cron automations: %w", err)
	}

	logger := platform.Logger(ctx)
	for _, row := range rows {
		e.evaluateCronAutomation(ctx, logger, row, now, minuteBucket)
	}
	return nil
}

func (e *Engine) evaluateCronAutomation(ctx context.Context, logger *slog.Logger, row sqlcgen.Automation, now time.Time, minuteBucket pgtype.Timestamptz) {
	logger = logger.With("automation_id", row.ID.String())

	cfg, err := unmarshalCronTriggerConfig(row.TriggerConfig)
	if err != nil {
		logger.Error("automation: decode cron trigger config failed", "error", err)
		return
	}

	matched, err := domainautomation.CronMatches(cfg.Schedule, now)
	if err != nil {
		logger.Error("automation: evaluate cron schedule failed", "error", err, "schedule", cfg.Schedule)
		return
	}
	if !matched {
		return
	}

	claimed, err := e.automations.ClaimCronFire(ctx, row.ID, minuteBucket)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already fired for this exact minute bucket -- a concurrent
			// tick (this pod's or another pod's own Engine) won the race
			// first. Harmless no-op, the SAME "lost the race" outcome every
			// other CAS-guarded write in this package treats identically.
			return
		}
		logger.Error("automation: claim cron fire failed", "error", err)
		return
	}

	targets, err := UnmarshalTargets(claimed.Repos)
	if err != nil {
		logger.Error("automation: decode automation repos for cron fire failed", "error", err)
		return
	}

	if _, err := CreateInvocation(ctx, e.invocations, claimed.ID, targets); err != nil {
		logger.Error("automation: create invocation for cron fire failed", "error", err)
	}
}
