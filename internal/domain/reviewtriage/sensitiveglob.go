package reviewtriage

import (
	"path"
	"strings"

	"github.com/khazaddev/narvi/internal/domain/autoapproval"
	"github.com/khazaddev/narvi/internal/domain/review"
)

// sensitiveTags is the fixed subset of review's eight-value BlastRadius
// vocabulary that triggers Decide's own "any sensitive-glob hit -> deep"
// rule (§26.3: "sensitive globs (migrations, auth surfaces,
// infra-as-code, CI workflows) mapped deterministically onto the same
// BlastRadius tags the verdict uses"). Deliberately a DIFFERENT, smaller
// set than internal/domain/autoapproval.DefaultSensitiveTags (migrations/
// auth/contracts, §21.2's own auto-approval-eligibility defaults): §26.3
// names four categories for TRIAGE specifically -- migrations, auth,
// infra-as-code, and CI workflows -- omitting "contracts" (which
// DefaultSensitiveTags includes) and folding "infra-as-code"/"CI
// workflows" onto the SAME review.TagInfra
// (autoapproval.matchesInfra already covers both: a top-level infra/
// deploy/k8s/terraform-shaped path segment, AND ".github/workflows/").
// This is a real, deliberate divergence from the auto-approval engine's
// own sensitive-tag defaults, not an oversight -- the two features
// answer different questions ("should this merge unattended" vs. "how
// much review rigor does this diff need"), and §26.3's own text supports
// a narrower, TRIAGE-specific set.
var sensitiveTags = map[review.Tag]bool{
	review.TagMigrations: true,
	review.TagAuth:       true,
	review.TagInfra:      true,
}

// classifySensitivePaths reports the subset of paths' own classified
// BlastRadius tags (via autoapproval.ClassifyChangedPaths -- see doc.go's
// own "Do not invent a second BlastRadius vocabulary" section) that fall
// within THIS package's own sensitiveTags set above. Returned in the
// SAME fixed order autoapproval.ClassifyChangedPaths itself produces
// (that function's own doc comment: stable, deterministic ordering), so
// two calls over the same input always agree byte-for-byte.
func classifySensitivePaths(paths []string) []review.Tag {
	all := autoapproval.ClassifyChangedPaths(paths)
	if len(all) == 0 {
		return nil
	}
	var out []review.Tag
	for _, tag := range all {
		if sensitiveTags[tag] {
			out = append(out, tag)
		}
	}
	return out
}

// matchDeepPath reports whether changedPath matches pattern, one entry
// of a repo's own Config.DeepPaths (§26.3's own per-repo "deepPaths:
// [...]" extension) -- two supported, explicitly documented shapes,
// chosen for simplicity over inventing a full glob-expression language
// this section's own text never specifies:
//
//  1. A pattern containing "*" is matched via the standard library's
//     path.Match -- single-segment glob semantics (a "*" never crosses a
//     "/" boundary), e.g. "internal/billing/*.go".
//  2. A pattern with no "*" at all is treated as a directory-or-file
//     PREFIX: it matches changedPath itself, or anything nested under it
//     (changedPath == pattern, or changedPath starts with pattern+"/") --
//     e.g. "internal/billing" matches every file under that directory,
//     not just a file literally named that.
//
// A malformed path.Match pattern (path.ErrBadPattern) is treated as a
// non-match, never an error this pure function has any way to surface --
// mirrors this package's own uniform "an unrecognized/malformed input
// degrades to the SAFE reading for THIS specific check" posture (an
// unmatched deepPaths entry can still be caught by the fixed
// sensitive-glob set or the line/root thresholds; it is not this one
// check's only chance to route deep).
func matchDeepPath(pattern, changedPath string) bool {
	if pattern == "" {
		return false
	}
	if strings.Contains(pattern, "*") {
		ok, err := path.Match(pattern, changedPath)
		return err == nil && ok
	}
	return changedPath == pattern || strings.HasPrefix(changedPath, pattern+"/")
}

// anyDeepPathMatch reports whether any path in changedPaths matches any
// pattern in deepPaths.
func anyDeepPathMatch(changedPaths, deepPaths []string) bool {
	for _, pattern := range deepPaths {
		for _, p := range changedPaths {
			if matchDeepPath(pattern, p) {
				return true
			}
		}
	}
	return false
}
