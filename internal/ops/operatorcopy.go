// This file implements the check that keeps technical-plan section numbers
// out of the text an operator reads on screen.
//
// The rule is not that §-citations are bad -- this codebase depends on them,
// and a comment that cites the section it implements is doing its job. The
// rule is about AUDIENCE. A comment is read by someone with the repository
// open; a paragraph rendered in the web UI is read by someone operating the
// platform, who has no access to the technical plan and for whom "§27.3"
// names nothing. Shipping one is the same defect as printing a table name or
// a Go symbol at a user: it describes the implementation to an audience that
// asked what the system is doing.
//
// This is worth a check rather than a convention because it is mechanically
// detectable, unlike its neighbour. The Step-citation rule (stepref.go) is a
// citation convention over prose, and its own doc comment records four
// evasion axes and one deliberate blind spot. A section number reaching a
// JSX text node, by contrast, is a syntactic fact: strip the comments, then
// look at what is left. Twelve of them shipped across four screens during
// the UI phase and were found by reading the running app, not by any check.

package ops

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// OperatorCopyRef is one section citation found in rendered text.
type OperatorCopyRef struct {
	File string
	Line int
	Text string
}

var (
	// blockComment and lineComment are stripped BEFORE looking for citations:
	// a citation inside a comment is correct and must not be reported. This
	// is the whole reason the check is worth having -- it can tell the two
	// audiences apart, which a plain grep for "§" cannot.
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment  = regexp.MustCompile(`(?m)^\s*//.*$`)

	// A citation in a JSX text node (>…§…<) or in a string literal that
	// reaches the screen. String literals are included because operator copy
	// is frequently assembled in a helper and returned rather than written
	// inline -- every one of the twelve found in the UI phase that was NOT
	// in a text node was in a returned string.
	operatorCopyPatterns = []*regexp.Regexp{
		regexp.MustCompile(`>[^<>{}]*§[^<>]*<`),
		regexp.MustCompile(`"[^"\n]*§[^"\n]*"`),
		regexp.MustCompile(`'[^'\n]*§[^'\n]*'`),
	}

	operatorCopyExtensions = map[string]bool{".ts": true, ".tsx": true}
)

// CheckOperatorCopy reports every technical-plan section citation that
// reaches operator-facing text under the given directories. Test files are
// skipped: a test asserting on copy legitimately quotes it.
func CheckOperatorCopy(root string, scanDirs []string) ([]OperatorCopyRef, error) {
	var out []OperatorCopyRef
	for _, dir := range scanDirs {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !operatorCopyExtensions[filepath.Ext(path)] {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			rel = filepath.ToSlash(rel)
			if strings.Contains(rel, "__tests__") || strings.HasSuffix(rel, ".test.ts") || strings.HasSuffix(rel, ".test.tsx") {
				return nil
			}
			raw, readErr := os.ReadFile(path) //nolint:gosec // walking a repo-relative tree
			if readErr != nil {
				return readErr
			}
			// Comments out first, so their citations are not reported, then
			// scan what remains -- which is only what can reach a screen.
			body := lineComment.ReplaceAllString(blockComment.ReplaceAllString(string(raw), ""), "")
			for i, line := range strings.Split(body, "\n") {
				for _, pat := range operatorCopyPatterns {
					if m := pat.FindString(line); m != "" {
						out = append(out, OperatorCopyRef{File: rel, Line: i + 1, Text: strings.TrimSpace(m)})
						break
					}
				}
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
