package reviewpost

import (
	"errors"
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
//  4. every hunk's own "old" text (its context + removed lines, in
//     order, each stripped of its own leading " "/"-" marker) appears,
//     VERBATIM and CONTIGUOUS, somewhere in currentContent -- the same
//     precondition a real `patch`/`git apply` invocation checks before
//     ever touching a file (ErrSuggestionStale on the first hunk that
//     fails this). A hunk with NO old lines at all (a pure insertion,
//     e.g. appending a new test at end-of-file) trivially satisfies this
//     check -- there is nothing to locate.
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

	for _, h := range hunks {
		if h.Old == "" {
			// A pure-insertion hunk (no context/removed lines at all) --
			// nothing to locate, trivially satisfied.
			continue
		}
		if !strings.Contains(currentContent, h.Old) {
			return ErrSuggestionStale
		}
	}

	return nil
}

// ApplySuggestionPatch actually applies patch to currentContent, returning
// the resulting new content -- callers MUST have already confirmed
// ValidateSuggestionApplies(filePath, currentContent, patch) == nil first;
// this function does not re-validate. Each hunk's own old block is
// replaced by its new block, in order, via a single (count=1)
// replacement -- safe specifically BECAUSE ValidateSuggestionApplies
// already confirmed every old block appears verbatim; a hunk with an
// empty old block (a pure insertion) is APPENDED at the end of
// currentContent instead (there is no anchor to replace).
//
// This is a small, honest, non-general patch applier -- not a full `git
// apply`/`patch` reimplementation. It is sufficient for exactly the case
// this endpoint validates against (a single-file, contiguous-hunk
// unified diff whose old text already matches verbatim); a more exotic
// patch (fuzzy context matching, offset hunks) is out of scope, matching
// this feature's own "hard-stop, never auto-resolve" philosophy: if the
// hunks don't match verbatim, ValidateSuggestionApplies already rejected
// the call before this function is ever reached.
func ApplySuggestionPatch(currentContent, patch string) (string, error) {
	lines := strings.Split(patch, "\n")
	hunks := parseHunks(lines)
	if len(hunks) == 0 {
		return "", ErrSuggestionNoHunks
	}

	result := currentContent
	for _, h := range hunks {
		if h.Old == "" {
			if result != "" && !strings.HasSuffix(result, "\n") {
				result += "\n"
			}
			result += h.New
			continue
		}
		if !strings.Contains(result, h.Old) {
			return "", ErrSuggestionStale
		}
		result = strings.Replace(result, h.Old, h.New, 1)
	}

	return result, nil
}

// hunkPair is one unified-diff hunk's own reconstructed old (context +
// removed lines) and new (context + added lines) text, each rejoined with
// "\n".
type hunkPair struct {
	Old string
	New string
}

// parseHunks scans lines (a patch, already split on "\n") and returns one
// hunkPair per unified-diff hunk found ("@@ ... @@" header) -- shared by
// ValidateSuggestionApplies (which only needs Old) and ApplySuggestionPatch
// (which needs both), so the two functions can never silently disagree on
// what a "hunk" is.
func parseHunks(lines []string) []hunkPair {
	var hunks []hunkPair
	var oldLines, newLines []string
	inHunk := false

	flush := func() {
		if inHunk {
			hunks = append(hunks, hunkPair{Old: strings.Join(oldLines, "\n"), New: strings.Join(newLines, "\n")})
		}
		oldLines, newLines = nil, nil
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "@@") {
			flush()
			inHunk = true
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
