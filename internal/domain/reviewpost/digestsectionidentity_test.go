package reviewpost_test

import (
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// TestComputeDigestSectionIdentity_SurvivesWhitespaceChurn is §26.5's own
// explicit pin: "a PR update that merely reorders or rewords an unrelated
// section must never make an already-contested ArchDecision read as a new
// one" -- proven here for the specific, common regeneration a re-review
// naturally produces: identical content, differing only in whitespace/case.
func TestComputeDigestSectionIdentity_SurvivesWhitespaceChurn(t *testing.T) {
	a := reviewpost.ComputeDigestSectionIdentity(reviewpost.DigestSectionArchRecap, "Introduced a retry queue table instead of extending the outbox.")
	b := reviewpost.ComputeDigestSectionIdentity(reviewpost.DigestSectionArchRecap, "  introduced   a retry queue table\ninstead of extending the outbox.  ")
	if a != b {
		t.Errorf("ComputeDigestSectionIdentity differs across whitespace/case-only churn: %q != %q", a, b)
	}
}

// TestComputeDigestSectionIdentity_DifferentSectionsNeverCollide proves
// section is genuinely part of the hash input, not merely documentation:
// identical text under two different DigestSection values must hash
// differently, so a maintainer's contest of one section can never be
// silently reconciled against an unrelated section that happens to carry
// the same words.
func TestComputeDigestSectionIdentity_DifferentSectionsNeverCollide(t *testing.T) {
	text := "did not verify against a production-sized table"
	arch := reviewpost.ComputeDigestSectionIdentity(reviewpost.DigestSectionArchRecap, text)
	unverified := reviewpost.ComputeDigestSectionIdentity(reviewpost.DigestSectionUnverifiedLimits, text)
	if arch == unverified {
		t.Errorf("ComputeDigestSectionIdentity(archRecap, %q) == ComputeDigestSectionIdentity(unverifiedLimits, %q) -- section must be part of the hash", text, text)
	}
}

// TestComputeDigestSectionIdentity_DifferentContentNeverCollides is the
// mirror image: genuinely different text under the SAME section must hash
// differently -- a trivial but load-bearing sanity check (a constant hash
// would pass the two tests above vacuously).
func TestComputeDigestSectionIdentity_DifferentContentNeverCollides(t *testing.T) {
	a := reviewpost.ComputeDigestSectionIdentity(reviewpost.DigestSectionArchRecap, "introduced a retry queue table")
	b := reviewpost.ComputeDigestSectionIdentity(reviewpost.DigestSectionArchRecap, "introduced a circuit breaker instead")
	if a == b {
		t.Errorf("ComputeDigestSectionIdentity collided for genuinely different text: %q", a)
	}
}

// TestArchRecapText_EmptyDecisions proves the zero-decisions case renders
// to an empty string (never a panic or a slice-index issue) -- the
// content hash of an empty arch recap is still a well-defined, stable
// value.
func TestArchRecapText_EmptyDecisions(t *testing.T) {
	if got := reviewpost.ArchRecapText(nil); got != "" {
		t.Errorf("ArchRecapText(nil) = %q, want empty", got)
	}
}

// TestArchRecapText_JoinsEachDecisionsThreeFields proves every one of the
// three ArchDecision fields participates in the rendered text (a
// regression that dropped, say, RejectedAlternative would still "look"
// plausible without this check).
func TestArchRecapText_JoinsEachDecisionsThreeFields(t *testing.T) {
	decisions := []reviewpost.ArchDecision{
		{Decision: "Introduced a retry queue", RejectedAlternative: "Extending the outbox", ConventionConformance: "Matches the one-table-per-concern pattern"},
	}
	got := reviewpost.ArchRecapText(decisions)
	for _, want := range []string{"Introduced a retry queue", "Extending the outbox", "Matches the one-table-per-concern pattern"} {
		if !strings.Contains(got, want) {
			t.Errorf("ArchRecapText(...) = %q, want it to contain %q", got, want)
		}
	}
}

// TestArchRecapText_MultipleDecisionsDoNotCollapseIntoOne proves two
// decisions with entirely disjoint content produce a rendering where BOTH
// remain distinguishable (never concatenated in a way that merges their
// own field boundaries).
func TestArchRecapText_MultipleDecisionsDoNotCollapseIntoOne(t *testing.T) {
	decisions := []reviewpost.ArchDecision{
		{Decision: "First decision"},
		{Decision: "Second decision"},
	}
	got := reviewpost.ArchRecapText(decisions)
	if !strings.Contains(got, "First decision") || !strings.Contains(got, "Second decision") {
		t.Errorf("ArchRecapText(...) = %q, want both decisions present", got)
	}
}
