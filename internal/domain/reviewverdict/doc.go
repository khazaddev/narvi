// Package reviewverdict holds §21.1's own persistence-envelope type
// (Record) and the pure analytics-rollup math that reads from
// review_verdicts history -- timeseries, top-risk-driver breakdown, the
// finding-outcome KPI -- plus §21.2 stage 2's contradiction-rate
// calibration math. No I/O, no time.Now(), no randomness (CLAUDE.md/§11):
// every function here is a pure transform of already-fetched data,
// exactly like internal/domain/decisioninbox's own split between "pure
// decision functions here" and "the actual read model, aggregating
// Postgres, one layer up in internal/app/reviewverdict".
//
// # Why Record is not internal/domain/review.Verdict itself
//
// review.Verdict is a CLOSED, seven-field (plus ProposedShippable)
// contract (review/doc.go's own design call #4: "This package cannot
// enforce that at the type-system level... Verdict below carries exactly
// the seven named fields... and nothing else"). Adding HeadSHA/
// RepoFullName/PRNumber/CreatedAt directly onto that struct would breach
// a contract Step 45 deliberately drew a hard line around, for a concern
// (durable persistence) that package explicitly declines to own -- its
// own doc.go states plainly that a Finding/persistence shape is "left for
// whichever Step actually needs it." Record is that shape for THIS Step:
// an envelope wrapping an unmodified review.Verdict value, never a
// second, competing definition of what a verdict IS.
//
// # The "not yet computed" sentinel, applied uniformly
//
// Every rollup function below returns (result, ok bool) -- ok=false means
// "no data existed in the queried window", a legitimate, common outcome
// (a brand-new repo, or an established one simply quiet for a stretch),
// NEVER collapsed into the same return shape as "the data says zero".
// This mirrors internal/domain/decisioninbox.MedianLatency's own
// identical (value, ok) shape exactly -- the SAME discipline applied
// consistently across every analytics-style read model this codebase
// has, rather than reinvented per package.
package reviewverdict
