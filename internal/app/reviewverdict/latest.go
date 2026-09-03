package reviewverdict

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/internal/domain/reviewverdict"
)

// GetLatest fetches (repoFullName, prNumber)'s own most-recently-posted
// verdict. ok=false (never an error) means no verdict has ever been
// posted for this PR -- a legitimate, common outcome (e.g. a brand-new
// PR no review has run on yet) every real caller (the auto-approval
// eligibility engine's own callers, the decision inbox's classification)
// must treat as "not eligible" rather than propagating a store error.
func GetLatest(ctx context.Context, deps Deps, repoFullName string, prNumber int32) (record reviewverdict.Record, ok bool, err error) {
	row, err := deps.ReviewVerdicts.GetLatest(ctx, repoFullName, prNumber)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reviewverdict.Record{}, false, nil
		}
		return reviewverdict.Record{}, false, err
	}
	return recordFromRow(row), true, nil
}

// GetLatestNonShadow is GetLatest's own §30.8 customer-consequential
// sibling -- excludes any verdict whose own suppressed_in_shadow stamp
// is true, or that predates repoFullName's own live_egress_promoted_at
// fence (GetLatestNonShadowReviewVerdict's own generated doc comment).
// ok=false means no NON-SHADOW verdict has ever been posted for this PR
// -- callers must treat this identically to GetLatest's own "no verdict
// at all" outcome (a shadow-era verdict is, from a customer-consequential
// caller's own point of view, indistinguishable from one that was never
// posted): internal/app/sessionactor/reviewretrigger.go's own
// auto-retrigger decision is this function's one caller, so a shadow-era
// "already reviewed" fact can never suppress a real re-review once a
// repo goes live, and a shadow-era risk level can never be quoted in a
// real, customer-visible budget-exhausted notice.
func GetLatestNonShadow(ctx context.Context, deps Deps, repoFullName string, prNumber int32) (record reviewverdict.Record, ok bool, err error) {
	row, err := deps.ReviewVerdicts.GetLatestNonShadow(ctx, repoFullName, prNumber)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reviewverdict.Record{}, false, nil
		}
		return reviewverdict.Record{}, false, err
	}
	return recordFromRow(row), true, nil
}
