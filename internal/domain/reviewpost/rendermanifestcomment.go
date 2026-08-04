package reviewpost

import (
	"fmt"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// RenderManifestComment renders the release manifest check's own typed
// findings (Step 50, "release PR review", §15.2, review.ManifestFinding)
// into the markdown comment body posted on a release PR -- "an audit,
// not a risk verdict" (§15.2's own words), mirroring RenderVerdictComment's
// identical "typed fields -> markdown, nothing parsed back" discipline
// (review/doc.go's own top-level stance). Called unconditionally whenever
// a release PR is detected (§15.2: "always runs") -- even a clean
// manifest (zero findings) still posts, so a maintainer always sees the
// check actually ran, unlike a per-finding sentinel (internal/domain/
// handoff, §14.4) that posts nothing at all when there is nothing to
// report.
//
// aggregateReviewTriggered is §15.3's own ShouldRunAggregateReview
// result, surfaced here as a visible signal: this Step wires the
// decision function itself end-to-end, but does not yet dispatch the
// actual composition-focused LLM review pass §15.3 describes from this
// comment -- see this Step's own PR description for the full reasoning
// and what is deferred.
func RenderManifestComment(findings []review.ManifestFinding, constituentPRCount int, aggregateReviewTriggered bool) string {
	var b strings.Builder

	b.WriteString("### Release manifest check\n\n")
	fmt.Fprintf(&b, "Examined %d constituent pull request(s) merged into this release.\n\n", constituentPRCount)

	if len(findings) == 0 {
		b.WriteString("No compliance issues found: every constituent PR carried an approving review, was green at its merge commit, and no revert went unreviewed.\n\n")
	} else {
		b.WriteString("**Findings:**\n\n")
		for _, f := range findings {
			b.WriteString("- " + renderManifestFinding(f) + "\n")
		}
		b.WriteString("\n")
	}

	if aggregateReviewTriggered {
		b.WriteString("**Composition check**: this release meets the criteria for an aggregate diff review (§15.3) -- multiple constituent PRs converge on the same subsystem, a high-risk PR is present, or a merge required manually resolving a conflict.\n\n")
	} else {
		b.WriteString("**Composition check**: not triggered -- none of §15.3's OR-conditions were met.\n\n")
	}

	b.WriteString("_Posted automatically by Narvi's release manifest check (§15.2) -- a mechanical compliance audit, never a code review._\n")

	return b.String()
}

// renderManifestFinding renders one review.ManifestFinding as a single
// bullet line, mirroring §15.2's own three example findings' exact
// phrasing as closely as the typed data allows.
func renderManifestFinding(f review.ManifestFinding) string {
	base := fmt.Sprintf("PR #%d", f.PRNumber)
	if f.PRTitle != "" {
		base = fmt.Sprintf("%s (%s)", base, f.PRTitle)
	}

	switch f.Kind {
	case review.ManifestFindingUnreviewedMerge:
		if f.Detail != "" {
			return fmt.Sprintf("%s merged without an approving review (%s)", base, f.Detail)
		}
		return fmt.Sprintf("%s merged without an approving review", base)
	case review.ManifestFindingRedAtMerge:
		return fmt.Sprintf("%s was red (CI failing) at its merge commit", base)
	case review.ManifestFindingUnreviewedRevert:
		if f.Detail != "" {
			return fmt.Sprintf("%s was reverted %s after merge; the revert itself was unreviewed", base, f.Detail)
		}
		return fmt.Sprintf("%s was reverted; the revert itself was unreviewed", base)
	default:
		// Defensive: every ManifestFindingKind ComputeReleaseManifestFindings
		// (internal/domain/review/manifestcheck.go) actually produces is
		// handled above; an unrecognized Kind here would mean this file has
		// drifted from that package's own vocabulary, not a real runtime
		// case -- rendered honestly rather than silently dropped.
		detail := f.Detail
		if detail == "" {
			detail = "no further detail"
		}
		return fmt.Sprintf("%s: %s (%s)", base, f.Kind, detail)
	}
}
