package reviewverdict

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/domain/reviewverdict"
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
