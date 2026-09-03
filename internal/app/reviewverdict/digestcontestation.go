package reviewverdict

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/domain/reviewpost"
	"github.com/narvidev/narvi/internal/domain/reviewtriage"
	"github.com/narvidev/narvi/internal/domain/reviewverdict"
)

// DigestContestationRate computes §26.5's own "digest precision
// (contestation rate)" KPI for repoFullName's own arch-recap digest
// section, bounded to deps.Timeouts.ReviewVerdictAnalyticsWindow --
// mirrors Timeseries/TopRiskDrivers/FindingOutcomes' own identical
// "fetch, then reduce via a pure internal/domain/reviewverdict function"
// shape (analytics.go). Deliberately reuses reviewverdict.ContradictionRate
// (contradiction.go, §21.2's own calibration-rate function) rather than a
// near-identical new function: both are the SAME "contested/total" ratio
// with the SAME "zero total means not-yet-computed, never a real zero"
// sentinel -- §26.4 explicitly warns
// against duplicating existing aggregation.
//
// total is the count of DEEP-PATH verdicts posted in the window (only a
// deep-path review ever produces an arch recap at all, §26.4/§26.9 --
// counting light-path verdicts in the denominator would understate the
// rate against recaps that could never have been contested in the first
// place); contested is review_digest_section_feedback's own row count for
// section="arch_recap" in the SAME window (deps.DigestSectionFeedback.
// Count). ok=false (deps.DigestSectionFeedback == nil, OR zero deep-path
// verdicts in the window) means "not yet computed" -- a brand-new repo,
// or one with no deep-path reviews yet, must never render as a
// misleadingly reassuring "0% contested".
func DigestContestationRate(ctx context.Context, deps Deps, repoFullName string, now time.Time) (rate float64, ok bool, err error) {
	if deps.DigestSectionFeedback == nil {
		return 0, false, nil
	}

	since := now.Add(-deps.Timeouts.ReviewVerdictAnalyticsWindow)
	records, err := ListRecordsSince(ctx, deps, repoFullName, since)
	if err != nil {
		return 0, false, err
	}

	deepCount := 0
	for _, r := range records {
		if r.ReviewPath == reviewtriage.DepthDeep {
			deepCount++
		}
	}

	contested, err := deps.DigestSectionFeedback.Count(ctx, repoFullName, string(reviewpost.DigestSectionArchRecap), pgtype.Timestamptz{Time: since, Valid: true})
	if err != nil {
		return 0, false, err
	}

	rate, ok = reviewverdict.ContradictionRate(deepCount, int(contested))
	return rate, ok, nil
}
