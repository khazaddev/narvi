package reviewverdict

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/domain/reviewverdict"
	"github.com/khazaddev/narvi/internal/platform"
)

// RecordConfirmed idempotently records that (repoFullName, prNumber,
// headSHA)'s own auto-approved verdict was actually MERGED -- the
// engine's own judgment stood (§21.2 stage 2's own "confirmed" outcome).
// Called from BOTH merge-completion paths: httpapi.MergePullRequest (a
// human's 1-click confirm while the repo's auto-merge toggle is off) and
// internal/app/automerge's own worker (once armed). Best-effort: a
// failure here never blocks or unwinds an ALREADY-SUCCEEDED merge --
// mirrors httpapi.MergePullRequest's own existing "the merge already
// happened, a logging failure must never claim otherwise" posture for
// its audit-log write, applied here to this SAME class of post-merge
// bookkeeping.
func RecordConfirmed(ctx context.Context, deps Deps, repoFullName string, prNumber int32, headSHA string) {
	recordOutcome(ctx, deps, repoFullName, prNumber, headSHA, reviewverdict.OutcomeConfirmed)
}

// RecordOverridden idempotently records that (repoFullName, prNumber,
// headSHA)'s own auto-approved verdict was CONTESTED by a human before
// any merge happened (GitHub's own HasChangesRequested became true, or a
// review:needs-human label was applied) -- §21.2 stage 2's own
// "overridden" outcome. Called from internal/app/decisioninbox's own
// buildPROpenItem, which already computes every fact this needs (the
// latest verdict's own Shippable, HasChangesRequested, the needs-human
// label) at zero extra cost -- see that call site for the exact
// condition. Best-effort, mirroring RecordConfirmed above.
func RecordOverridden(ctx context.Context, deps Deps, repoFullName string, prNumber int32, headSHA string) {
	recordOutcome(ctx, deps, repoFullName, prNumber, headSHA, reviewverdict.OutcomeOverridden)
}

func recordOutcome(ctx context.Context, deps Deps, repoFullName string, prNumber int32, headSHA string, outcome reviewverdict.Outcome) {
	if headSHA == "" {
		return
	}
	if err := deps.AutoApprovalOutcomes.Record(ctx, repoFullName, prNumber, headSHA, string(outcome)); err != nil {
		platform.Logger(ctx).Warn("reviewverdict: record auto-approval outcome failed", "error", err, "repo_full_name", repoFullName, "pr_number", prNumber, "outcome", string(outcome))
	}
}

// ContradictionRate computes repoFullName's own §21.2 stage 2
// calibration metric, bounded to deps.Timeouts.ReviewVerdictAnalyticsWindow
// -- see internal/domain/reviewverdict.ContradictionRate's own doc
// comment for the "not yet computed" sentinel (ok=false) this forwards
// verbatim.
func ContradictionRate(ctx context.Context, deps Deps, repoFullName string, now time.Time) (rate float64, contested, total int, ok bool, err error) {
	since := now.Add(-deps.Timeouts.ReviewVerdictAnalyticsWindow)
	totalCount, contestedCount, err := deps.AutoApprovalOutcomes.CountInWindow(ctx, repoFullName, pgtype.Timestamptz{Time: since, Valid: true})
	if err != nil {
		return 0, 0, 0, false, err
	}
	rate, ok = reviewverdict.ContradictionRate(int(totalCount), int(contestedCount))
	return rate, int(contestedCount), int(totalCount), ok, nil
}
