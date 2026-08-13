package reviewverdict

import (
	"time"

	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
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
	// Digest is Step 66's own merge-readout content (§26.1), persisted
	// alongside Verdict above on the SAME review_verdicts row (migrations/
	// 000077_review_verdicts_digest.up.sql) -- carried here, on Record,
	// rather than on Verdict itself, for the exact same reason HeadSHA/
	// CreatedAt already are: Digest is a persistence-layer fact review.
	// Verdict's own closed seven-field contract never grows to hold
	// (review/doc.go's own design call #4; reviewpost.Digest's own doc
	// comment). The zero value (Digest{}) is what a pre-Step-66 row reads
	// back as -- every field empty/nil, indistinguishable from "requested
	// but the agent reported nothing", by construction (this migration's
	// own doc comment).
	Digest reviewpost.Digest
}
