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

// This file guards the OTHER half of this repo's documentation-reference
// surface. TestNoMetricDrift and TestNoGuideDrift (drift.go, guidedrift.go)
// bind dashboards, alerts and user guides to real instrument names and real
// routes. Nothing bound the ~7,300 "§N" and "§N.M" citations that Go comments
// and the plan documents make into docs/TECHNICAL_PLAN.md -- and a citation
// nobody checks is exactly the "looks maintained but isn't" hazard doc.go
// argues against.
//
// # Why this matters more than an ordinary broken link
//
// "§N" and "Step N" are DIFFERENT namespaces that overlap numerically: §21 is
// a real technical-plan section AND Step 21 is a real implementation Step, and
// they are unrelated -- worse, per this repo's own convention (docs/
// IMPLEMENTATION_PLAN.md's cross-cutting conventions), code comments cite only
// the durable §N; a Step number is a scheduling artifact of the plan's own row
// order, says WHEN something was built rather than WHAT it must do, and does
// not belong in source at all. A Phase 6 audit of every reference found 239
// comment citations using the section sigil where Steps 46, 60 and 62 were
// meant -- harmless only because TECHNICAL_PLAN.md has no section numbered 46,
// 60 or 62 yet. It has 33 top-level sections and gained four in a single
// working session. The moment it reaches section 46, every one of those
// citations silently starts resolving to a real but WRONG section, and no
// tool would flag it, because by then the reference is valid.
//
// Those 239 were first mechanically rewritten to "Step N", which fixed the
// immediate ambiguity but got the underlying convention backwards -- a Step
// number is exactly the renumberable reference this repo's own history (the
// plan has been renumbered before) argues against. They were corrected a
// second time: each cites whichever §N.M actually governs the behavior in
// question, or is rephrased to describe that behavior directly with no
// citation at all when no section does. This check is what stops either
// class of error -- the wrong sigil, or a citation of a section that later
// stops existing -- from coming back undetected.
//
// It cannot catch a citation that resolves to the wrong-but-existing section
// (that is semantics, the boundary docs/guides/README.md draws for the sibling
// checks). It catches every citation that resolves to nothing, which is the
// form the defect actually took.

// sectionRefPattern matches a citation of a plan section, bare or dotted.
//
// Greedy, with NO trailing guard, and that is deliberate. An earlier cut ended
// the pattern with a "next char is not a digit or dot" clause to stop a dotted
// citation also matching as its bare prefix. That silently made every citation
// ENDING A SENTENCE invisible, because a trailing period could not satisfy it
// -- and this is not a corner case: 5,913 of this repo's ~7,300 citations end
// in a period, so the check was inspecting under a fifth of its own surface,
// and its own first mutation test passed when it should have failed. Greedy
// matching handles both forms: "27.1." captures 27.1 and leaves the period,
// "987." captures 987.
var sectionRefPattern = regexp.MustCompile(`§(\d+(?:\.\d+)?)`)

var (
	topSectionPattern = regexp.MustCompile(`(?m)^##\s+(\d+)\.\s`)
	subSectionPattern = regexp.MustCompile(`(?m)^###\s+(\d+\.\d+)\s`)
)

// numberedListSection is the one section whose items are cited as if they were
// subsections. §8 ("Feature set") is a numbered markdown list, not a heading
// hierarchy, so a dotted citation of it means an item index -- a real and
// widely-used citation (343 mentions) that no ### heading backs. Its item count is read from the
// document rather than hardcoded, so adding a feature-set item does not
// require editing this file.
const numberedListSection = "8"

// ScanPlanSections returns every section number docs/TECHNICAL_PLAN.md
// actually defines: "N" for each top-level "## N. Title", "N.M" for each
// "### N.M Title", and "8.1".."8.K" for §8's own numbered list items.
func ScanPlanSections(planPath string) (map[string]bool, error) {
	raw, err := os.ReadFile(planPath) //nolint:gosec // a repo-relative doc path, not user input
	if err != nil {
		return nil, fmt.Errorf("read technical plan: %w", err)
	}
	doc := string(raw)

	out := make(map[string]bool)
	for _, m := range topSectionPattern.FindAllStringSubmatch(doc, -1) {
		out[m[1]] = true
	}
	for _, m := range subSectionPattern.FindAllStringSubmatch(doc, -1) {
		out[m[1]] = true
	}

	for i := 1; i <= numberedListItemCount(doc, numberedListSection); i++ {
		out[fmt.Sprintf("%s.%d", numberedListSection, i)] = true
	}
	return out, nil
}

// numberedListItemCount counts the top-level "N. " items in the body of the
// given top-level section -- the span from its own "## N." heading to the next
// "## " heading.
func numberedListItemCount(doc, section string) int {
	start := regexp.MustCompile(`(?m)^##\s+` + regexp.QuoteMeta(section) + `\.\s`).FindStringIndex(doc)
	if start == nil {
		return 0
	}
	body := doc[start[1]:]
	if next := regexp.MustCompile(`(?m)^##\s`).FindStringIndex(body); next != nil {
		body = body[:next[0]]
	}
	return len(regexp.MustCompile(`(?m)^\d+\.\s`).FindAllString(body, -1))
}

// SectionRef is one unresolved citation, carrying enough to fix it.
type SectionRef struct {
	Section string
	File    string
	Count   int
}

// CheckSectionRefs scans every .go and .md file under the given roots for
// "§N"/"§N.M" citations and returns those naming a section
// docs/TECHNICAL_PLAN.md does not define, sorted for a stable failure message.
func CheckSectionRefs(root string, scanDirs []string, defined map[string]bool) ([]SectionRef, error) {
	perFile := make(map[string]map[string]int)

	for _, dir := range scanDirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || (!strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".md")) {
				return nil
			}
			raw, readErr := os.ReadFile(path) //nolint:gosec // walking a repo-relative tree
			if readErr != nil {
				return readErr
			}
			for _, m := range sectionRefPattern.FindAllStringSubmatch(string(raw), -1) {
				if defined[m[1]] {
					continue
				}
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				if perFile[rel] == nil {
					perFile[rel] = make(map[string]int)
				}
				perFile[rel][m[1]]++
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk %s: %w", dir, err)
		}
	}

	var out []SectionRef
	for file, sections := range perFile {
		for section, count := range sections {
			out = append(out, SectionRef{Section: section, File: file, Count: count})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Section < out[j].Section
	})
	return out, nil
}
