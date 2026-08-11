package reviewverdict

import (
	"time"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// Record is one review_verdicts row (§21.1) -- an envelope around an
// unmodified review.Verdict value plus the persistence-layer facts that
// package deliberately never carries (doc.go's own "why Record is not
// review.Verdict itself"). Built by internal/app/reviewverdict from a
// freshly-inserted or freshly-read sqlcgen.ReviewVerdict row; never
// constructed with a hand-computed Verdict.Shippable (review.Verdict's
// own CONTRACT, unaffected by this wrapper).
type Record struct {
	RepoFullName string
	PRNumber     int32
	// HeadSHA is the commit this verdict was produced against -- see
	// migrations/000067_review_verdicts.up.sql's own doc comment for the
	// full "why" and where it comes from.
	HeadSHA string
	Verdict review.Verdict
	// CreatedAt is when this verdict was POSTED (review_verdicts.created_at,
	// Postgres's own now() at INSERT time) -- never re-derived or
	// defaulted by this package (no Clock -- CLAUDE.md/§11); the caller
	// supplies it from the row it already fetched.
	CreatedAt time.Time
}
