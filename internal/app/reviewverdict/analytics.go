package reviewverdict

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
	"github.com/khazaddev/narvi/internal/domain/reviewverdict"
)

// maxAnalyticsRows bounds every analytics rollup's own Postgres read --
// §21.1's own "bounded from day one" discipline, applied here the same
// way maxRecentlyDecidedPlans already bounds internal/app/decisioninbox.
// Metrics' own comparable window scan. Generous for any single repo's
// real verdict/finding volume within platform.Timeouts.
// ReviewVerdictAnalyticsWindow.
const maxAnalyticsRows = 5000

// ListRecordsSince fetches repoFullName's own review_verdicts history
// posted after sinceTime, converted to the pure domain shape -- the ONE
// Postgres read every rollup function below shares, mirroring
// internal/domain/reviewverdict's own doc comment ("caller fetches, pure
// package reduces"). Exported (unlike an unexported windowRecords helper)
// so a DIFFERENT caller with its own window -- internal/app/digest's own
// much narrower one-day rollup, §21.3 -- can reuse the exact same
// fetch-and-convert logic rather than re-deriving it.
func ListRecordsSince(ctx context.Context, deps Deps, repoFullName string, sinceTime time.Time) ([]reviewverdict.Record, error) {
	rows, err := deps.ReviewVerdicts.ListInWindow(ctx, repoFullName, pgtype.Timestamptz{Time: sinceTime, Valid: true}, maxAnalyticsRows)
	if err != nil {
		return nil, err
	}
	records := make([]reviewverdict.Record, len(rows))
	for i, row := range rows {
		records[i] = recordFromRow(row)
	}
	return records, nil
}

// Timeseries computes repoFullName's own §21.1 timeseries rollup, bounded
// to deps.Timeouts.ReviewVerdictAnalyticsWindow -- see
// internal/domain/reviewverdict.Timeseries' own doc comment for the
// "not yet computed" sentinel this forwards verbatim (ok=false).
func Timeseries(ctx context.Context, deps Deps, repoFullName string, now time.Time) ([]reviewverdict.DayBucket, bool, error) {
	records, err := ListRecordsSince(ctx, deps, repoFullName, now.Add(-deps.Timeouts.ReviewVerdictAnalyticsWindow))
	if err != nil {
		return nil, false, err
	}
	buckets, ok := reviewverdict.Timeseries(records)
	return buckets, ok, nil
}

// TopRiskDrivers computes repoFullName's own §21.1 top-risk-driver
// breakdown, bounded the same way Timeseries above is.
func TopRiskDrivers(ctx context.Context, deps Deps, repoFullName string, now time.Time) ([]reviewverdict.TagCount, bool, error) {
	records, err := ListRecordsSince(ctx, deps, repoFullName, now.Add(-deps.Timeouts.ReviewVerdictAnalyticsWindow))
	if err != nil {
		return nil, false, err
	}
	drivers, ok := reviewverdict.TopRiskDrivers(records)
	return drivers, ok, nil
}

// FindingOutcomes computes repoFullName's own §21.1/§12.2 item 6
// "Review finding outcomes" KPI, read from review_findings (Step 48) --
// see internal/domain/reviewverdict.FindingOutcomes' own doc comment for
// why this reads a DIFFERENT table than Timeseries/TopRiskDrivers above.
func FindingOutcomes(ctx context.Context, deps Deps, repoFullName string, now time.Time) ([]reviewverdict.FindingStatusCount, bool, error) {
	since := now.Add(-deps.Timeouts.ReviewVerdictAnalyticsWindow)
	raw, err := deps.ReviewFindings.ListStatusesInWindow(ctx, repoFullName, pgtype.Timestamptz{Time: since, Valid: true}, maxAnalyticsRows)
	if err != nil {
		return nil, false, err
	}
	statuses := make([]reviewpost.FindingStatus, len(raw))
	for i, s := range raw {
		statuses[i] = reviewpost.FindingStatus(s)
	}
	outcomes, ok := reviewverdict.FindingOutcomes(statuses)
	return outcomes, ok, nil
}
