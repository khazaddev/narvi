package ops

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// This file guards the mirror-image mistake sectionref.go's own
// TestNoSectionRefDrift teaches how to fix: a Go comment citing "Step N" at
// all, instead of the technical-plan section that actually governs the
// behavior. A Step number is the implementation plan's own row order -- it
// says WHEN something was built, never WHAT it must do -- and this plan has
// been renumbered before (sectionref.go's own history), so a Step-N
// citation frozen into source is a citation waiting to go stale the next
// time it is. §N.M is the durable one; this repo's own convention is that
// code cites only that, never the Step.
//
// # Telling a plan citation apart from local test narration
//
// A numbered step inside a test's own scenario or procedure comment
// ("// Step 1: the timer fires", "// --- Step 3: kill pod A") is not a
// citation of the plan's Step table at all -- it is narration of what THIS
// test does, at THIS step of ITS OWN sequence, and flagging it would just
// teach every future test author to stop writing readable scenario
// comments. The rule stepRefPattern/narrativeStepLine apply to separate the
// two: a narrative step names an action, always at the very start of a
// comment line (after only whitespace, "//", and an optional "---"
// separator) and always followed immediately by a colon introducing that
// action -- "Step 1: the timer fires", never "Step 1's own timer". A plan
// citation, by contrast, almost always names the Step's own content via a
// possessive ("Step 48's own X") or a parenthetical alongside other
// citation material ("(Step 59, §29.5)") -- shapes a colon-led narrative
// line never takes. stepref_test.go's own mutation tests pin both
// directions of this boundary: a real plan-style citation added to a
// source comment must fail this check, and a narrative "// Step 1:" line
// must not.
//
// # Three citation shapes, one pattern
//
// A full-text audit of this repo found real citations in three shapes:
// the plain "Step 21", the plural "Steps 32-34"/"Steps 32/33/34" (several
// ingress Steps cited together), and the parenthesis form "Step (17)"/
// "Steps (47, 58)". stepRefPattern below matches all three at once. The
// ONE known false-positive risk this widening accepts: a local, in-file
// numbered list that happens to use capitalized "Steps N" for its own
// items (e.g. "Steps 8-10 below" meaning list items 8 through 10, not
// plan Steps) would also match -- found exactly once in this repo
// (scmcredentials.go, fixed by lowercasing to "steps 8-10", which this
// pattern's case sensitivity already treats as ordinary English). That is
// judged rare and cheap enough to fix on sight that it does not clear the
// "fights its users" bar the singular narrative-colon case would have.
//
// The whitespace after "Step(s)" is REQUIRED (\s+, not \s*): an identifier
// like builtInPlanStep1ID has no space between "Step" and the digit, and
// must never be mistaken for a citation.
var stepRefPattern = regexp.MustCompile(`Steps?\s+\(?\d+`)

// narrativeStepLine matches a numbered step at the start of a comment line
// that immediately introduces a local action with a colon -- see this
// file's own top doc comment for why that shape is exempt.
var narrativeStepLine = regexp.MustCompile(`^\s*//\s*(?:-{2,}\s*)?Step\s+\d+[a-z]?:\s`)

// stepRefCheckExemptFiles names the files that document this very
// convention (and the historical incident that motivated it,
// sectionref.go's own doc comment) in prose, rather than citing the plan
// for the behavior of the code around them. They legitimately say "Step N"
// while explaining why nothing else in the tree should.
var stepRefCheckExemptFiles = map[string]bool{
	"internal/ops/sectionref.go":      true,
	"internal/ops/sectionref_test.go": true,
	"internal/ops/stepref.go":         true,
	"internal/ops/stepref_test.go":    true,
}

// StepRef is one disallowed "Step N" citation this check found.
type StepRef struct {
	File string
	Line int
	Text string
}

// CheckStepRefs scans every .go file under the given roots for a "Step N"
// citation that is not local test-scenario narration (narrativeStepLine)
// and not inside one of stepRefCheckExemptFiles, returning one StepRef per
// occurrence, sorted for a stable failure message. A nil/empty result means
// the tree cites only technical-plan sections, never implementation Steps
// -- the CI-passing state.
func CheckStepRefs(root string, scanDirs []string) ([]StepRef, error) {
	var out []StepRef
	for _, dir := range scanDirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			if stepRefCheckExemptFiles[rel] {
				return nil
			}
			raw, readErr := os.ReadFile(path) //nolint:gosec // walking a repo-relative tree
			if readErr != nil {
				return readErr
			}
			for i, line := range strings.Split(string(raw), "\n") {
				if !stepRefPattern.MatchString(line) {
					continue
				}
				if narrativeStepLine.MatchString(line) {
					continue
				}
				out = append(out, StepRef{File: rel, Line: i + 1, Text: strings.TrimSpace(line)})
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", dir, err)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}
