package reviewverdict

import (
	"encoding/json"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/domain/review"
	"github.com/khazaddev/narvi/internal/domain/reviewverdict"
)

// marshalTags converts tags into JSONB bytes -- review_verdicts.
// blast_radius/repo_settings.sensitive_blast_radius_tags' own shared
// wire shape (migrations/000067's own doc comment: "a plain JSON array
// of tag strings", mirroring sessions.repos' own JSON-array-column
// precedent over a native Postgres array type). A nil/empty tags always
// marshals to "[]", never a JSON null -- both review_verdicts.blast_radius
// (NOT NULL DEFAULT '[]'::jsonb) and this package's own readers expect a
// present, empty array to mean "no tags", not an absent column.
func marshalTags(tags []review.Tag) ([]byte, error) {
	strs := make([]string, len(tags))
	for i, t := range tags {
		strs[i] = string(t)
	}
	if strs == nil {
		strs = []string{}
	}
	return json.Marshal(strs)
}

// unmarshalTags is marshalTags' own inverse -- a malformed/empty input
// (a NULL column read as a nil []byte, or genuinely invalid JSON, which
// should never happen for a column ONLY ever written by marshalTags
// above, but defended against anyway) degrades to an empty tag list
// rather than propagating a decode error up through every caller of
// recordFromRow -- an empty BlastRadius is always a SAFE, conservative
// reading (autoapproval.ComputeEligible's own sensitive-path check finds
// no overlap against an empty list, which can only ever make a PR LESS
// likely to be flagged sensitive, never more -- so this is the one place
// in this package's own read path where the fail-conservative direction
// legitimately favors "assume nothing tagged" over "fail closed", since
// the alternative -- treating a decode failure as though EVERY tag were
// present -- has no principled meaning at all, unlike domain/review's
// own enum-level fail-conservative convention, which always has a real
// worst-known-value to fall back to).
func unmarshalTags(raw []byte) []review.Tag {
	if len(raw) == 0 {
		return nil
	}
	var strs []string
	if err := json.Unmarshal(raw, &strs); err != nil {
		return nil
	}
	tags := make([]review.Tag, len(strs))
	for i, s := range strs {
		tags[i] = review.Tag(s)
	}
	return tags
}

// recordFromRow converts a freshly-inserted-or-read sqlcgen.ReviewVerdict
// into the pure reviewverdict.Record shape -- the one seam every domain-
// layer analytics/eligibility caller in this package goes through, never
// hand-built ad hoc at each call site.
func recordFromRow(row sqlcgen.ReviewVerdict) reviewverdict.Record {
	return reviewverdict.Record{
		RepoFullName: row.RepoFullName,
		PRNumber:     row.PrNumber,
		HeadSHA:      row.HeadSha,
		CreatedAt:    row.CreatedAt.Time,
		Verdict: review.Verdict{
			RiskLevel:         review.RiskLevel(row.RiskLevel),
			Premise:           review.PremiseState(row.Premise),
			BlastRadius:       unmarshalTags(row.BlastRadius),
			FilesChanged:      int(row.FilesChanged),
			TestsCoverage:     review.TestsCoverageState(row.TestsCoverage),
			DocsDrift:         review.DocsDriftState(row.DocsDrift),
			ProposedShippable: review.ProposedShippable(row.ProposedShippable),
			Shippable:         review.Shippable(row.Shippable),
		},
	}
}
