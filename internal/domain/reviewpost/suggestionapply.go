package reviewpost

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// This file implements the pure validation half of the apply-suggestion
// endpoint (§12.2 item 2, §17.3): "validates SuggestedFix still applies
// against the PR's current head before committing ... hard-stops on a
// stale/conflicting suggestion exactly like §17.4's cherry-pick
// discipline, never auto-resolves." The actual GitHub Contents API
// read/write is real I/O and lives in the httpapi handler that calls this
// function (reviewfindings.go) -- this file is pure per §11: no I/O, no
// time.Now(), no randomness.

// The errors ValidateSuggestionApplies returns -- named distinctly so a
// caller can render a specific 400/409 and a table-driven test can assert
// exactly WHICH check fired, mirroring ValidateVerdictInput/
// ValidateFindingInput's own identical discipline.
var (
	ErrSuggestionEmpty     = errors.New("reviewpost: suggested fix is empty")
	ErrSuggestionNoHunks   = errors.New("reviewpost: suggested fix contains no unified-diff hunks")
	ErrSuggestionWrongFile = errors.New("reviewpost: suggested fix's own diff header names a different file than this finding's own filePath")
	ErrSuggestionStale     = errors.New("reviewpost: suggested fix no longer applies cleanly against the current file content")
	// ErrSuggestionAmbiguous is returned when a hunk's own old block
	// (for a hunk with old lines) matches more than one location in the
	// current content and the hunk's own
	// "@@ -oldStart,oldCount +newStart,newCount @@" header either can't
	// be parsed or names a position that isn't one of the candidate
	// matches -- or, for a pure-insertion hunk (no old lines at all,
	// nothing to search for), when that same header can't be parsed, since
	// the header is then the ONLY source of where the hunk belongs. Per
	// this endpoint's own hard-stop, never-auto-resolve philosophy
	// (§17.4), guessing which occurrence the author meant would risk
	// silently committing an edit to the wrong location on the PR's real
	// head branch, so this fails closed instead of picking one.
	ErrSuggestionAmbiguous = errors.New("reviewpost: suggested fix's location in the file cannot be determined unambiguously")
)

// diffHeaderPrefixes are the two conventional unified-diff old/new-file
// path prefixes ("a/"/"b/", what `git diff`'s own default output always
// uses) -- stripped before comparing a diff header's own path against the
// finding's FilePath, so a patch generated the ordinary way (via `git
// diff`) is never rejected purely for carrying this cosmetic prefix.
var diffHeaderPrefixes = []string{"a/", "b/"}

// ValidateSuggestionApplies checks whether patch (a finding's own
// SuggestedFix, a unified-diff/patch text) still cleanly applies against
// currentContent (filePath's own REAL, CURRENT content at the PR's head,
// already fetched by the caller via GitHub's Contents API) -- a hard
// textual precondition check, never an auto-resolve, mirroring §17.4's
// own cherry-pick discipline ("If the cherry-pick conflicts, that is a
// hard stop, never an auto-resolve") applied here to a HUMAN-triggered
// manual apply instead of the sentinel-auto-fix's own system-initiated
// merge.
//
// Checked, in order:
//  1. patch is non-empty (ErrSuggestionEmpty).
//  2. patch's own "--- "/"+++ " diff header lines, when present, name
//     filePath (after stripping a conventional "a/"/"b/" prefix) --
//     REJECTS a patch whose own diff header targets a DIFFERENT file than
//     the one this finding says it's for: an out-of-scope patch, exactly
//     the case §12.2 item 2's own "rejecting an invalid or out-of-scope
//     patch" requirement names (ErrSuggestionWrongFile). A patch with NO
//     diff header at all (a bare hunk, no "---"/"+++" lines) is not
//     rejected by this check alone -- it has made no file-identity claim
//     to contradict.
//  3. patch contains at least one real unified-diff hunk, a line matching
//     "@@ ... @@" (ErrSuggestionNoHunks) -- a SuggestedFix with no hunks
//     at all could never be meaningfully "applied".
//  4. every hunk with old lines (its context + removed lines, in order,
//     each stripped of its own leading " "/"-" marker) must locate a
//     SINGLE unambiguous position in currentContent: if its old block
//     appears nowhere, that hunk is stale (ErrSuggestionStale); if it
//     appears more than once, the hunk's own "@@ -oldStart,... @@" header
//     line number must name exactly one of those occurrences, or the
//     location is ambiguous (ErrSuggestionAmbiguous) -- existence alone
//     is never enough, because a repeated block (e.g. a common
//     "\treturn nil\n}" in a Go file) can otherwise silently resolve to
//     the WRONG occurrence.
//  5. a hunk with NO old lines at all (a pure insertion, e.g. appending a
//     new function at a specific line) has no anchor text to locate at
//     all -- its own header's old-start line number is the ONLY source of
//     where it belongs, so an unparseable header here is fatal
//     (ErrSuggestionAmbiguous) rather than merely losing a disambiguation
//     tiebreaker.
func ValidateSuggestionApplies(filePath, currentContent, patch string) error {
	if strings.TrimSpace(patch) == "" {
		return ErrSuggestionEmpty
	}

	lines := strings.Split(patch, "\n")

	for _, line := range lines {
		var headerPath string
		switch {
		case strings.HasPrefix(line, "--- "):
			headerPath = strings.TrimSpace(strings.TrimPrefix(line, "--- "))
		case strings.HasPrefix(line, "+++ "):
			headerPath = strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
		default:
			continue
		}
		if headerPath == "" || headerPath == "/dev/null" {
			continue
		}
		for _, prefix := range diffHeaderPrefixes {
			headerPath = strings.TrimPrefix(headerPath, prefix)
		}
		if headerPath != filePath {
			return ErrSuggestionWrongFile
		}
	}

	hunks := parseHunks(lines)
	if len(hunks) == 0 {
		return ErrSuggestionNoHunks
	}

	contentLines := strings.Split(currentContent, "\n")
	for _, h := range hunks {
		if len(h.OldLines) == 0 {
			// A pure-insertion hunk (no context/removed lines at all) --
			// the header is the only thing that says where it goes.
			if !h.HeaderOK || h.OldStart < 0 || h.OldStart > len(contentLines) {
				return ErrSuggestionAmbiguous
			}
			continue
		}
		expectedIdx := -1
		if h.HeaderOK {
			expectedIdx = h.OldStart - 1
		}
		if _, err := locateHunkPosition(contentLines, h.OldLines, expectedIdx); err != nil {
			return err
		}
	}

	return nil
}

// ApplySuggestionPatch actually applies patch to currentContent, returning
// the resulting new content -- callers MUST have already confirmed
// ValidateSuggestionApplies(filePath, currentContent, patch) == nil first;
// this function does not re-validate the file-identity or empty/no-hunks
// checks, though it still performs (and can still fail) the same
// position-resolution as ValidateSuggestionApplies, since this writes
// straight to a user's real PR branch and a stale race between the two
// calls, however unlikely, must never silently apply to the wrong place.
//
// Each hunk is spliced in at a single, unambiguous line position: the
// hunk's own old block (for a hunk with old lines) is searched for in the
// content, and if it matches more than once, the hunk's own
// "@@ -oldStart,... @@" header line number (adjusted for the net line
// delta any earlier hunks in this same patch already introduced) must
// pick out exactly one of those occurrences -- otherwise this returns
// ErrSuggestionAmbiguous rather than guessing, matching this feature's own
// "hard-stop, never auto-resolve" philosophy: a refused suggestion is
// recoverable, a silent wrong-location commit is not. A pure-insertion
// hunk (no old lines) is spliced in at the line position its own header
// names, not appended at end-of-file.
//
// This is a small, honest, non-general patch applier -- not a full `git
// apply`/`patch` reimplementation. It is sufficient for exactly the case
// this endpoint validates against (a single-file unified diff whose old
// text already matches verbatim and whose position is unambiguous); a more
// exotic patch (fuzzy context matching, offset hunks with no header) is
// out of scope.
func ApplySuggestionPatch(currentContent, patch string) (string, error) {
	lines := strings.Split(patch, "\n")
	hunks := parseHunks(lines)
	if len(hunks) == 0 {
		return "", ErrSuggestionNoHunks
	}

	contentLines := strings.Split(currentContent, "\n")
	// lineDelta tracks how many net lines earlier hunks in this same
	// patch have already added/removed, since each hunk's own header
	// line number is relative to the ORIGINAL (pre-patch) file, exactly
	// like a real `patch`/`git apply` has to account for.
	lineDelta := 0

	for _, h := range hunks {
		if len(h.OldLines) == 0 {
			if !h.HeaderOK {
				return "", ErrSuggestionAmbiguous
			}
			idx := h.OldStart + lineDelta
			if idx < 0 || idx > len(contentLines) {
				return "", ErrSuggestionAmbiguous
			}
			out := make([]string, 0, len(contentLines)+len(h.NewLines))
			out = append(out, contentLines[:idx]...)
			out = append(out, h.NewLines...)
			out = append(out, contentLines[idx:]...)
			contentLines = out
			lineDelta += len(h.NewLines)
			continue
		}

		expectedIdx := -1
		if h.HeaderOK {
			expectedIdx = h.OldStart - 1 + lineDelta
		}
		idx, err := locateHunkPosition(contentLines, h.OldLines, expectedIdx)
		if err != nil {
			return "", err
		}

		out := make([]string, 0, len(contentLines)-len(h.OldLines)+len(h.NewLines))
		out = append(out, contentLines[:idx]...)
		out = append(out, h.NewLines...)
		out = append(out, contentLines[idx+len(h.OldLines):]...)
		contentLines = out
		lineDelta += len(h.NewLines) - len(h.OldLines)
	}

	return strings.Join(contentLines, "\n"), nil
}

// locateHunkPosition finds the single unambiguous 0-based line index in
// lines at which the contiguous run oldBlock begins, using expectedIdx --
// the position the hunk's own "@@" header names (adjusted for any earlier
// hunks already applied), or -1 if unknown/unparseable -- to disambiguate
// when oldBlock matches more than one place. It never guesses: if
// oldBlock's own text pins down more than one candidate location and
// expectedIdx isn't one of them, it returns ErrSuggestionAmbiguous rather
// than picking one; if oldBlock's text appears nowhere, it returns
// ErrSuggestionStale.
func locateHunkPosition(lines, oldBlock []string, expectedIdx int) (int, error) {
	var matches []int
	for i := 0; i+len(oldBlock) <= len(lines); i++ {
		if linesEqual(lines[i:i+len(oldBlock)], oldBlock) {
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 0:
		return -1, ErrSuggestionStale
	case 1:
		return matches[0], nil
	default:
		for _, m := range matches {
			if m == expectedIdx {
				return m, nil
			}
		}
		return -1, ErrSuggestionAmbiguous
	}
}

// linesEqual reports whether a and b hold the same lines in the same
// order.
func linesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// hunkHeaderRE matches a unified-diff hunk header's own
// "@@ -oldStart[,oldCount] +newStart[,newCount] @@" line and captures
// oldStart -- the 1-based line number, in the file the patch was
// generated FROM, that the hunk's own old block (context + removed lines)
// begins at. By convention, a hunk with zero old lines (a pure insertion,
// no context) names, as oldStart, the line number immediately BEFORE the
// insertion point (0 meaning "at the very start of the file") -- the same
// convention `diff`/`patch` themselves use, and the one both of
// parseHunkOldStart's callers rely on.
var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+\d+(?:,\d+)? @@`)

// parseHunkOldStart parses header (a patch line already known to start
// with "@@") and returns its old-start line number and whether parsing
// succeeded. A header that doesn't match the expected shape (missing line
// numbers, malformed) returns (0, false) -- callers must treat that as
// "position unknown", never as "position 0".
func parseHunkOldStart(header string) (int, bool) {
	m := hunkHeaderRE.FindStringSubmatch(header)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// hunkPair is one unified-diff hunk's own reconstructed old (context +
// removed lines) and new (context + added lines) lines, plus the position
// data parsed from its own "@@ ... @@" header -- shared by
// ValidateSuggestionApplies and ApplySuggestionPatch so the two functions
// can never silently disagree on what a "hunk" is or where it belongs.
type hunkPair struct {
	OldLines []string // context+removed lines, in file order; empty (len 0) for a genuine pure-insertion hunk with no context at all.
	NewLines []string // context+added lines, in file order.
	OldStart int      // the hunk's own header-declared old-file starting line number (1-based); meaningless unless HeaderOK.
	HeaderOK bool     // whether the hunk's own "@@ ... @@" header parsed cleanly enough to trust OldStart.
}

// parseHunks scans lines (a patch, already split on "\n") and returns one
// hunkPair per unified-diff hunk found ("@@ ... @@" header).
func parseHunks(lines []string) []hunkPair {
	var hunks []hunkPair
	var oldLines, newLines []string
	var oldStart int
	var headerOK bool
	inHunk := false

	flush := func() {
		if inHunk {
			hunks = append(hunks, hunkPair{
				OldLines: oldLines,
				NewLines: newLines,
				OldStart: oldStart,
				HeaderOK: headerOK,
			})
		}
		oldLines, newLines = nil, nil
		oldStart = 0
		headerOK = false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			flush()
			inHunk = true
			oldStart, headerOK = parseHunkOldStart(line)
			continue
		}
		if !inHunk {
			continue
		}
		switch {
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			// A new file's diff header ends the current hunk (a
			// multi-file patch) -- flush and wait for the next "@@".
			flush()
			inHunk = false
		case strings.HasPrefix(line, " "):
			oldLines = append(oldLines, line[1:])
			newLines = append(newLines, line[1:])
		case strings.HasPrefix(line, "-"):
			oldLines = append(oldLines, line[1:])
		case strings.HasPrefix(line, "+"):
			newLines = append(newLines, line[1:])
		default:
			// e.g. "\ No newline at end of file" -- ignored.
		}
	}
	flush()

	return hunks
}
