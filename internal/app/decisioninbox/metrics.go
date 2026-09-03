package decisioninbox

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/domain/decisioninbox"
)

// maxRecentlyDecidedPlans bounds ListRecentlyDecided's own query --
// generous for a 30-day window of plan decisions in any realistic
// deployment, mirroring this codebase's own "bounded from day one"
// discipline (§21.1) rather than an unbounded result set.
const maxRecentlyDecidedPlans = 1000

// Metrics computes §16.2's own decision-latency metric: "median time from
// an item entering the queue to its action". Scoped, for this Step, to
// PLAN decisions only (created_at -> decided_at, both real, already-
// persisted columns, requiring no new writer) -- PR-merge latency is
// DELIBERATELY deferred: unlike a plan (one row, one session, a direct
// created_at/decided_at pair), computing it would require scanning
// "recently merged PRs" across every repo this deployment's users touch,
// and this codebase maintains no canonical registry of "every repo Narvi
// manages" to scan (the SAME gap ListOpenPRsForUser's own doc comment
// already names and works around by querying per-user instead) --
// nothing here silently mislabels that gap; ok=false with sampleSize=0
// distinguishes "no plan decisions in the window yet" from a real
// computed value, and a later Step (§21's own analytics work)
// is the natural place to extend this once review_verdicts gives PR-merge
// latency a durable source too.
func Metrics(ctx context.Context, deps Deps, now time.Time) (median time.Duration, sampleSize int, ok bool, err error) {
	since := now.Add(-deps.Timeouts.DecisionInboxLatencyWindow)
	plans, err := deps.Plans.ListRecentlyDecided(ctx, pgtype.Timestamptz{Time: since, Valid: true}, maxRecentlyDecidedPlans)
	if err != nil {
		return 0, 0, false, err
	}

	durations := make([]time.Duration, 0, len(plans))
	for _, p := range plans {
		if !p.DecidedAt.Valid || !p.CreatedAt.Valid {
			continue
		}
		enteredQueueAt := p.CreatedAt.Time
		actionedAt := p.DecidedAt.Time
		if actionedAt.Before(enteredQueueAt) {
			continue // defensive: never a real, legitimate negative latency
		}
		durations = append(durations, actionedAt.Sub(enteredQueueAt))
	}

	med, medOk := decisioninbox.MedianLatency(durations)
	return med, len(durations), medOk, nil
}
