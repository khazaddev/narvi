package reviewtriage

// Signals is Decide's own input -- every field already resolved by the
// caller before this pure function ever runs (CLAUDE.md/§11: no I/O in
// domain). internal/app/reviewtriage is the one real assembler: most
// fields come straight off review.PreFetchedContext (Additions/
// Deletions/ChangedPaths/Labels, already fetched alongside the inline
// diff at review-session creation, per §26.3's own framing), and
// PriorVerdictRiskHigh comes from a fresh internal/app/reviewverdict.
// GetLatest read.
type Signals struct {
	// Additions/Deletions are this PR's own server-reported diff-size
	// facts (review.PreFetchedContext.Additions/Deletions) -- summed by
	// changedLines (decide.go) against maxChangedLinesLight.
	Additions int
	Deletions int

	// ChangedPaths is the PR's own changed-file-path listing
	// (review.PreFetchedContext.ChangedPaths, ExtractChangedPaths' own
	// output) -- feeds BOTH the sensitive-glob check (sensitiveglob.go)
	// and the top-level-root-dispersion count (decide.go). A nil/empty
	// value degrades safely: no glob can match, and distinctRoots
	// (decide.go) is simply 0 -- never itself a reason to route deep
	// (Decide's own doc comment covers the "no signal at all" case
	// explicitly).
	ChangedPaths []string

	// NeedsHumanLabelPresent reports whether this PR currently carries
	// reviewpost.LabelNeedsHuman (§8.2's own maintainer escape hatch)
	// -- see doc.go's own "v1 rules -- five, not three" section for why
	// this is one of Decide's five triggers.
	NeedsHumanLabelPresent bool

	// PriorVerdictRiskHigh reports whether the LATEST posted verdict for
	// this exact PR (internal/app/reviewverdict.GetLatest) carried
	// review.RiskLevelHigh -- §26.3's own explicit fourth rule ("a prior
	// high verdict routes deep"). false for a PR with no prior verdict
	// at all, indistinguishable from "the prior verdict was not high
	// risk" -- both are the same safe, non-triggering reading.
	PriorVerdictRiskHigh bool
}
