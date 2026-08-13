package reviewtriage

import (
	"strings"

	"github.com/khazaddev/narvi/internal/domain/review"
)

// maxChangedLinesLight and minDistinctRootsForDeep are §26.3's own v1
// thresholds, verbatim ("initial thresholds... >600 changed lines or >=3
// distinct top-level path roots -> deep"). Fixed package constants, not a
// Config field -- see doc.go's own "Per-repo config" section for why.
const (
	maxChangedLinesLight    = 600
	minDistinctRootsForDeep = 3
)

// Reason is Decide's own short, human-readable explanation for why it
// picked the depth it did -- one fixed string per v1 rule, mirroring
// internal/domain/autoapproval.Reason's own identical "a normal domain
// OUTCOME, never a Go error" shape. Suitable for a structured log field
// and for the §18.4-precedent routing-decision record's own reasoning
// column.
type Reason string

const (
	ReasonAlwaysLightConfig Reason = "repo config: mode=always_light"
	ReasonAlwaysDeepConfig  Reason = "repo config: mode=always_deep"
	ReasonSensitiveGlob     Reason = "sensitive glob touched"
	ReasonDeepPathConfig    Reason = "repo-configured deep path touched"
	ReasonChangedLinesOver  Reason = "changed lines exceed threshold"
	ReasonRootDispersion    Reason = "distinct top-level path roots at or above threshold"
	ReasonPriorHighVerdict  Reason = "prior verdict for this PR was high risk"
	ReasonNeedsHumanLabel   Reason = "review:needs-human label present"
	ReasonLightDefault      Reason = "no deep-routing signal"
)

// Decision is Decide's own output -- recorded verbatim on the §18.4-
// precedent routing-decision record (internal/app/reviewtriage) and, via
// its Depth field alone, persisted as review_verdicts.review_path (Step
// 62).
type Decision struct {
	Depth ReviewDepth
	// Reason is the FIRST rule that fired, in the fixed order Decide
	// checks them (this function's own doc comment) -- never a
	// combination of every rule that WOULD have matched, mirroring
	// internal/domain/autoapproval.ComputeEligible's own "first check
	// that fails wins" precedent.
	Reason Reason
	// MatchedSensitiveTags is non-nil only when Reason ==
	// ReasonSensitiveGlob -- the specific review.Tag(s) the sensitive-
	// glob check matched, carried through for the routing-decision
	// record's own audit trail.
	MatchedSensitiveTags []review.Tag
	// ChangedLines is Signals.Additions+Signals.Deletions, carried
	// through verbatim for the routing-decision record.
	ChangedLines int
	// DistinctRoots is the number of distinct top-level path roots
	// Signals.ChangedPaths touches, carried through verbatim for the
	// routing-decision record.
	DistinctRoots int
}

// topLevelRoot returns p's own first path segment -- "internal/domain/
// review/context.go" -> "internal"; a path with no "/" at all (a
// repo-root file, e.g. "README.md") returns itself, its own root.
func topLevelRoot(p string) string {
	if idx := strings.Index(p, "/"); idx >= 0 {
		return p[:idx]
	}
	return p
}

// distinctRoots counts the number of distinct topLevelRoot values across
// paths -- §26.3's own "cross-cutting dispersion: number of distinct
// top-level path roots touched".
func distinctRoots(paths []string) int {
	roots := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		roots[topLevelRoot(p)] = true
	}
	return len(roots)
}

// resolveMode normalizes cfg.Mode -- the zero value or any other
// unrecognized string reads as ModeAuto (Mode's own doc comment: a
// garbled config value must never itself silently force always_deep or
// always_light).
func resolveMode(m Mode) Mode {
	switch m {
	case ModeAlwaysLight, ModeAlwaysDeep:
		return m
	default:
		return ModeAuto
	}
}

// Decide computes what sig's own signals alone route to, under cfg --
// §26.3's own v1 rule cascade, deterministic-first, no LLM tie-break.
// Pure per CLAUDE.md/§11.
//
// Checked in this fixed order, first match wins (mirrors internal/domain/
// autoapproval.ComputeEligible's own "first check that fails wins"
// discipline, and internal/domain/reviewpost.ValidateVerdictInput's own
// "fixed order... deterministic first error" discipline):
//
//  1. cfg.Mode == always_light / always_deep: an explicit admin override,
//     checked before any signal at all.
//  2. Any changed path matches the fixed sensitive-glob set (migrations/
//     auth/infra-as-code+CI-workflows) OR any repo-configured
//     cfg.DeepPaths entry -> deep.
//  3. Signals.Additions+Deletions > 600, OR distinct top-level path roots
//     >= 3 -> deep.
//  4. Signals.PriorVerdictRiskHigh -> deep (§26.3's own explicit fourth
//     rule; see doc.go's own "v1 rules -- five, not three" section).
//  5. Signals.NeedsHumanLabelPresent -> deep (this package's own fifth
//     rule, same section).
//  6. Otherwise: light.
//
// A caller with NO usable signals at all (every Signals field at its own
// zero value -- e.g. a diff fetch that failed entirely) reaches rule 6
// and returns DepthLight, never an error: this function has no error
// return at all, by construction, so "fail open to light" is structurally
// true for THIS function -- see internal/app/reviewtriage.ComputeDepth's
// own doc comment for how the CALLER'S surrounding I/O (config/verdict-
// history reads, which CAN fail) is made to degrade to the same safe
// input before ever reaching here.
func Decide(sig Signals, cfg Config) Decision {
	changedLines := sig.Additions + sig.Deletions
	roots := distinctRoots(sig.ChangedPaths)

	switch resolveMode(cfg.Mode) {
	case ModeAlwaysLight:
		return Decision{Depth: DepthLight, Reason: ReasonAlwaysLightConfig, ChangedLines: changedLines, DistinctRoots: roots}
	case ModeAlwaysDeep:
		return Decision{Depth: DepthDeep, Reason: ReasonAlwaysDeepConfig, ChangedLines: changedLines, DistinctRoots: roots}
	}

	if tags := classifySensitivePaths(sig.ChangedPaths); len(tags) > 0 {
		return Decision{Depth: DepthDeep, Reason: ReasonSensitiveGlob, MatchedSensitiveTags: tags, ChangedLines: changedLines, DistinctRoots: roots}
	}
	if anyDeepPathMatch(sig.ChangedPaths, cfg.DeepPaths) {
		return Decision{Depth: DepthDeep, Reason: ReasonDeepPathConfig, ChangedLines: changedLines, DistinctRoots: roots}
	}

	if changedLines > maxChangedLinesLight {
		return Decision{Depth: DepthDeep, Reason: ReasonChangedLinesOver, ChangedLines: changedLines, DistinctRoots: roots}
	}
	if roots >= minDistinctRootsForDeep {
		return Decision{Depth: DepthDeep, Reason: ReasonRootDispersion, ChangedLines: changedLines, DistinctRoots: roots}
	}

	if sig.PriorVerdictRiskHigh {
		return Decision{Depth: DepthDeep, Reason: ReasonPriorHighVerdict, ChangedLines: changedLines, DistinctRoots: roots}
	}
	if sig.NeedsHumanLabelPresent {
		return Decision{Depth: DepthDeep, Reason: ReasonNeedsHumanLabel, ChangedLines: changedLines, DistinctRoots: roots}
	}

	return Decision{Depth: DepthLight, Reason: ReasonLightDefault, ChangedLines: changedLines, DistinctRoots: roots}
}
