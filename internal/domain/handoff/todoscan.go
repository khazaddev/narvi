package handoff

import (
	"regexp"
	"strconv"
	"strings"
)

// TODOFinding is one backend-adjacent TODO/FIXME marker ScanTODOs found
// among a unified diff's own ADDED lines -- never a marker the diff
// merely leaves in place (a removed or unchanged/context line), and never
// one a caller merely feeds in from unrelated, pre-existing file content.
type TODOFinding struct {
	// FilePath is the marker's own repo-relative file path, taken from the
	// diff's "+++ b/<path>" header -- the SAME "b/" NEW-file-side
	// convention reviewpost.ValidateSuggestionApplies already strips
	// (suggestionapply.go's own diffHeaderPrefixes).
	FilePath string
	// Line is the marker's own 1-indexed line number in the file's NEW
	// (post-diff) content -- derived from the enclosing hunk's own "@@
	// -old +new @@" header, never from the diff's old/pre-image side.
	Line int
	// Text is the marker's own added line, trimmed of leading/trailing
	// whitespace but otherwise verbatim (including its own leading "//",
	// "#", "<!--", etc. comment syntax, whatever the source file happens
	// to use) -- this package renders it as-is; it is never re-parsed by
	// anything downstream.
	Text string
}

// todoMarkerRE matches a TODO or FIXME marker, case-insensitively, as a
// whole word -- so "TODO", "todo", "Todo:", "// FIXME(alice):" all match,
// while an identifier that merely CONTAINS one of these words as a
// substring (e.g. "TODOList", a real Go identifier) does not.
var todoMarkerRE = regexp.MustCompile(`(?i)\b(TODO|FIXME)\b`)

// hunkHeaderRE parses a unified-diff hunk header's own NEW-file start line
// number: "@@ -oldStart[,oldCount] +newStart[,newCount] @@ ...". Only the
// new-side start is needed -- ScanTODOs only ever reports line numbers in
// the diff's own new (post-diff) content.
var hunkHeaderRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// diffNewFilePrefixes are the two conventional unified-diff new-file path
// prefixes ("b/", what `git diff`'s own default output always uses for
// the post-image side; GitHub's own .diff media type follows the same
// convention) -- stripped so a reported FilePath is a plain,
// repo-relative path, mirroring reviewpost.ValidateSuggestionApplies' own
// diffHeaderPrefixes precedent (suggestionapply.go), narrowed to just the
// new-file side since ScanTODOs never reports an old-file path.
const diffNewFilePrefix = "b/"

// ScanTODOs is a pure, deterministic scan of diff (a unified-diff/patch
// text -- e.g. GitHub's own .diff media type, internal/adapters/outbound/
// githubapi.Adapter.GetPullRequestDiff's own result) for every
// TODO/FIXME marker an ADDED line introduces. A file deleted by this diff
// ("+++ /dev/null") contributes nothing (there is no new-file content to
// report a line number against). Malformed/truncated diff text degrades
// gracefully to fewer (or zero) findings rather than panicking -- this
// function never returns an error, mirroring
// internal/app/reviewcontext.Fetch's own "never returns an error of its
// own, degrade instead" precedent for a comparably best-effort input.
//
// A nil or all-whitespace diff returns nil.
func ScanTODOs(diff string) []TODOFinding {
	if strings.TrimSpace(diff) == "" {
		return nil
	}

	var findings []TODOFinding
	var currentFile string
	var inFile bool
	var newLine int
	// inHeaderBlock is true exactly while a "--- "/"+++ " line means what
	// it conventionally means (this file's own old/new diff header):
	// true at the very start (a bare, git-less unified diff/patch may
	// open directly with "--- "/"+++ ", no "diff --git" line at all) and
	// reset true on every "diff --git " line -- git's own, and GitHub's
	// .diff media type's own, unambiguous per-file boundary marker. It
	// goes false the moment this file's first "@@" hunk header is seen,
	// since from that point on a line starting with "+++ " (or "--- ")
	// is a HUNK BODY line whose own added (or removed) source text
	// happens to start with "++ " (or "-- ") -- a real added line of
	// code (a C-style increment, a comment, a markdown list item), not a
	// new file's header. Treating "+++ " as a header unconditionally,
	// anywhere, misattributes or drops exactly that line.
	inHeaderBlock := true

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHeaderBlock = true
			inFile = false
		case inHeaderBlock && strings.HasPrefix(line, "+++ "):
			currentFile = normalizeNewFilePath(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")))
			inFile = currentFile != ""
		case inHeaderBlock && strings.HasPrefix(line, "--- "):
			// Old-file header -- ScanTODOs never reports against the
			// old/pre-image side, so there is nothing to record here;
			// "+++ " (above) is the sole authority on the current file.
		case strings.HasPrefix(line, "@@"):
			inHeaderBlock = false
			if m := hunkHeaderRE.FindStringSubmatch(line); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					newLine = n
				}
			}
		case !inFile:
			// Between "diff --git"/"index ..." lines and this file's own
			// first hunk, or after a "+++ /dev/null" (deleted file) --
			// nothing to scan.
		case strings.HasPrefix(line, "+"):
			text := line[1:]
			if todoMarkerRE.MatchString(text) {
				findings = append(findings, TODOFinding{
					FilePath: currentFile,
					Line:     newLine,
					Text:     strings.TrimSpace(text),
				})
			}
			newLine++
		case strings.HasPrefix(line, "-"):
			// Removed line -- does not exist in the new file; the new-side
			// line counter does not advance.
		case strings.HasPrefix(line, " "):
			// Unchanged context line -- exists in both old and new content.
			newLine++
		default:
			// e.g. "index abc..def 100644", or "\ No newline at end of
			// file" -- not part of any hunk's own
			// line-numbered content.
		}
	}

	return findings
}

// normalizeNewFilePath strips diff's own "b/" new-file prefix and reports
// "" for a deleted file's "/dev/null" marker (so callers can treat that
// as "nothing to scan against", mirroring the caller loop's own !inFile
// gate above).
func normalizeNewFilePath(p string) string {
	if p == "" || p == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(p, diffNewFilePrefix)
}
