package reviewpost

import (
	"regexp"
	"strconv"
	"strings"
)

// This file implements §22.1.1's own content-anchored positioning:
// MatchPosition is the pure function "(snippet, diff) -> (StartLine,
// EndLine)" the plan names, run server-side by this package at posting/
// render time (never by shipping the diff back into a model prompt) --
// see doc.go's own package-level doc comment for the fuller picture of how
// this fits alongside §22.1's already-shipped content-based identity
// (Finding.Description/IdentityHash, finding.go).
//
// # One deliberate elaboration on the plan's own two-argument signature
//
// §22.1.1 states the contract as exactly two inputs, snippet and diff.
// This package's own real diff is a WHOLE pull request's unified diff,
// spanning every changed file, while a Finding's own snippet (its
// Description, see the "One field, two consumers" note below) is about
// exactly ONE file (Finding.FilePath). Matching a snippet against every
// hunk of every changed file, blind to which file the finding is even
// about, would risk a false-positive match landing in the WRONG file --
// exactly the "worse than none" failure mode §22.1.1 exists to prevent,
// just relocated from "wrong line" to "wrong file entirely". MatchPosition
// below therefore takes filePath as a third argument and scopes its own
// search to that one file's own hunks before ever computing a score --
// the position is still a pure function of (this finding's own snippet,
// the one diff already in hand), filePath is simply the disambiguating
// key that was always implicit in "this finding's own snippet" (a finding
// without a file is meaningless; ValidateFindingInput already rejects an
// empty FilePath). No new capture, no new field: FilePath is Finding's
// own pre-existing, already-required field (finding.go).
//
// # One field, two consumers
//
// snippet here IS Finding.Description -- the exact same text §22.1
// already hashes into IdentityHash (finding.go's own ComputeFindingIdentity).
// This package deliberately does NOT accept a second, separately-captured
// "code snippet" field: §22.1.1 is explicit ("the snippet §22.1 already
// mandates storing IS the anchor text -- never a second, parallel capture
// of the same content"). Description is ordinarily prose (a finding's own
// explanation), not a literal quoted source excerpt -- the match below is
// therefore a similarity match over words actually shared between that
// prose and the diff's own new-file text, not an expectation of an exact
// source-code quote. This is a real, named tradeoff (see matchThreshold's
// own doc comment for the honest residual it leaves), not an oversight:
// reusing the one already-mandated field, per the plan's own explicit
// instruction, was preferred over inventing a second capture the plan
// just as explicitly rules out.

// diffFileHeaderRE matches a unified diff's own "+++ b/path/to/file" line
// (or "+++ /dev/null" for a deleted file, which this pattern also matches
// but extractFileNewLines below never scopes to, since ValidateFindingInput
// never accepts an empty FilePath and "/dev/null" never equals a real,
// normalized repo-relative path). Captures the path after the standard
// git "b/" prefix.
var diffFileHeaderRE = regexp.MustCompile(`^\+\+\+ b/(.+)$`)

// diffHunkHeaderRE matches a unified diff's own hunk header,
// "@@ -oldStart[,oldCount] +newStart[,newCount] @@ [optional context]" --
// only newStart is needed (the starting line number of this hunk's own
// "new" (post-change) file content, which is what a human reading the
// PR's own "Files changed" tab sees line numbers against).
var diffHunkHeaderRE = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// newFileLine is one line of a diff's own "new" (post-change) file
// content, at the line number it occupies in that new version -- built by
// extractFileNewLines below from exactly the lines a human reading
// GitHub's own rendered diff would see as part of the new file: hunk
// context lines and added ('+') lines, in order, each carrying the real
// new-file line number a removed ('-') line never advances.
type newFileLine struct {
	LineNo int
	Text   string
}

// extractFileNewLines walks diff (a unified, possibly-multi-file diff,
// exactly the shape internal/app/reviewcontext.Fetch's own GetCompareDiff
// call returns) and returns, in order, every new-file line belonging to
// filePath (normalized via normalizeFindingFilePath, mirroring
// ComputeFindingIdentity's own identical path-normalization precedent so
// "./foo.go" and "foo.go" scope to the same file). Returns nil when
// filePath never appears as a "+++ b/..." target in diff at all (the
// file wasn't touched by this diff, or diff itself is empty -- both
// degrade identically, to "nothing to match against").
//
// Line-number bookkeeping mirrors the unified diff format itself exactly:
// a hunk header ("@@ ... @@") resets the running new-file line counter to
// its own declared new-start; a context line (leading space) or an added
// line (leading '+', excluding the "+++ " file-header line itself) both
// occupy a real position in the new file and advance the counter; a
// removed line (leading '-', excluding "--- ") occupies NO position in
// the new file at all and is skipped without advancing anything -- this
// is precisely why line position cannot be read off a diff by counting
// raw lines: the new file's own line numbering only ever advances on
// context/added lines.
func extractFileNewLines(diff, filePath string) []newFileLine {
	if diff == "" {
		return nil
	}
	want := normalizeFindingFilePath(filePath)

	var out []newFileLine
	inTargetFile := false
	newLineNo := 0

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			// Entering a new file's own diff section -- whether it is the
			// target is decided by this section's own "+++ b/..." line,
			// checked next.
			inTargetFile = false
			newLineNo = 0
			continue
		case strings.HasPrefix(line, "+++ "):
			if m := diffFileHeaderRE.FindStringSubmatch(line); m != nil {
				inTargetFile = normalizeFindingFilePath(m[1]) == want
			} else {
				inTargetFile = false
			}
			newLineNo = 0
			continue
		case strings.HasPrefix(line, "--- "):
			continue
		case strings.HasPrefix(line, "@@ "):
			if !inTargetFile {
				continue
			}
			if m := diffHunkHeaderRE.FindStringSubmatch(line); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					newLineNo = n
				}
			}
			continue
		}

		if !inTargetFile || newLineNo == 0 {
			continue
		}

		switch {
		case strings.HasPrefix(line, "+"):
			out = append(out, newFileLine{LineNo: newLineNo, Text: line[1:]})
			newLineNo++
		case strings.HasPrefix(line, " "):
			out = append(out, newFileLine{LineNo: newLineNo, Text: line[1:]})
			newLineNo++
		default:
			// A removed line (leading '-') exists only in the OLD file
			// and occupies no position in the new one; "\ No newline at
			// end of file" and similar diff metadata is neither a real
			// new-file line nor a counter advance either. Both cases are
			// deliberately handled identically: not appended, newLineNo
			// not advanced.
		}
	}

	return out
}

// matchThreshold is the minimum Dice similarity (lineScore below) a
// window's own average score must clear to count as a real match -- not
// a duration, so CLAUDE.md's "every duration literal lives in
// platform/timeouts.go" rule does not apply; named and documented in
// place instead.
//
// Honest residual (named, not hidden, mirroring ComputeFindingIdentity's
// own "honest residual" candor, finding.go): Description is ordinarily
// prose, not a literal quoted source line, so this match is a
// shared-vocabulary similarity, not an exact-text search. 0.4 is a
// deliberately CONSERVATIVE floor -- chosen to keep the failure direction
// §22.1.1 mandates: a snippet that shares little real vocabulary with
// anything in the diff should report UNANCHORED (0, triggering the
// relocation fallback), never a low-confidence guess dressed up as a
// real position. A future telemetry-driven tuning pass is the right way
// to adjust this value, not a first-principles guess baked in harder
// than it needs to be.
const matchThreshold = 0.4

// minSignificantWordLen is the minimum rune length a word must have to
// count toward a similarity score -- excludes short, low-information
// tokens ("a", "is", "to", ...) that would otherwise inflate every
// line's score regardless of real topical overlap.
const minSignificantWordLen = 3

// nonWordRunRE splits text on any run of characters that are not letters
// or digits -- the tokenizer boundary significantWords below uses.
var nonWordRunRE = regexp.MustCompile(`[^\p{L}\p{N}]+`)

// significantWords tokenizes s into a lower-cased, de-duplicated set of
// its own "significant" words (length >= minSignificantWordLen) -- the
// comparison unit lineScore below computes overlap over.
func significantWords(s string) map[string]struct{} {
	words := make(map[string]struct{})
	for _, tok := range nonWordRunRE.Split(strings.ToLower(s), -1) {
		if len([]rune(tok)) >= minSignificantWordLen {
			words[tok] = struct{}{}
		}
	}
	return words
}

// lineScore is the Dice coefficient between snippetLine's and
// candidateLine's own significant-word sets: 2*|intersection| /
// (|snippetLine's words| + |candidateLine's words|), 0.0 when either side
// carries no significant words at all. Dice (rather than a plain
// intersection-over-snippet-length fraction) is deliberate: a real code
// line is typically SHORT (few significant identifiers once short
// tokens/variable names are filtered), while a finding's own Description
// is typically a full sentence carrying ordinary English words alongside
// whatever code vocabulary it quotes -- dividing by the snippet's own
// (large) word count alone would punish a genuine match for being
// surrounded by ordinary prose. Dice's own denominator (the SUM of both
// sides) rewards a candidate line whose entire own vocabulary is
// contained in the snippet without requiring the snippet to be nothing
// BUT code, while still requiring genuine, non-trivial overlap on both
// sides -- a candidate line sharing only one accidental short word with
// an otherwise-unrelated snippet still scores too low to pass
// matchThreshold, exactly like a snippet sharing nothing with the whole
// diff does.
func lineScore(snippetLine, candidateLine string) float64 {
	snippetWords := significantWords(snippetLine)
	candidateWords := significantWords(candidateLine)
	if len(snippetWords) == 0 || len(candidateWords) == 0 {
		return 0
	}
	overlap := 0
	for w := range snippetWords {
		if _, ok := candidateWords[w]; ok {
			overlap++
		}
	}
	return 2 * float64(overlap) / float64(len(snippetWords)+len(candidateWords))
}

// significantLines splits snippet into its own non-empty (post-trim)
// lines -- the ordinary case (Finding.Description is a single paragraph,
// no embedded newline) yields exactly one line, degrading the "sliding
// window" below to sliding a single-line window across every candidate
// line; a multi-line snippet slides a matching-sized window instead. A
// blank line (whitespace-only, or a run of consecutive newlines) is
// dropped, never treated as a real "line" to match against -- it carries
// no vocabulary of its own and would only ever depress a window's score
// for no informational reason.
func significantLines(snippet string) []string {
	var lines []string
	for _, l := range strings.Split(snippet, "\n") {
		if trimmed := strings.TrimSpace(l); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// MatchPosition is §22.1.1's own pure positioning function: given a
// finding's own file path and snippet (Finding.FilePath/Description) and
// the ONE diff already in hand (§22.1.1: "the one diff already in hand"),
// it reports the finding's CURRENT position in that diff's new-file
// content, or an explicit, un-guessed (0, 0) when no confident match is
// found -- NEVER a nearby-but-wrong line (§22.1.1: "0 is a UI-branchable
// fact... not a plausible-looking wrong answer").
//
// # What "sliding window" concretely means here
//
// snippet is split into its own significant (non-blank) lines
// (significantLines) -- almost always exactly one, since Finding.
// Description is ordinarily a single paragraph. diff is first scoped down
// to filePath's own new-file lines, in new-file line-number order
// (extractFileNewLines) -- so the window only ever slides across ONE
// file's own changed region, never across the whole multi-file PR diff
// (see this file's own top doc comment for why filePath is a necessary
// third input). A window exactly as many lines long as the snippet then
// slides across those candidate lines one position at a time; each
// position is scored by averaging lineScore across its own snippet-
// line/candidate-line pairs (in order); the highest-scoring position
// wins, ties broken toward the EARLIEST (lowest line number) position for
// a fully deterministic result (§11: no randomness). A window whose own
// best score falls below matchThreshold is not a match at all: this
// function returns (0, 0) rather than the best-scoring-but-still-weak
// position, honoring §22.1.1's own "0, never a guess" mandate.
//
// Pure per §11: no I/O, no time.Now(), no randomness -- a deterministic
// function of its three string inputs alone, safe to call directly from
// reviewpost's own render/posting path with no network dependency.
func MatchPosition(filePath, snippet, diff string) (startLine, endLine int) {
	candidates := extractFileNewLines(diff, filePath)
	if len(candidates) == 0 {
		return 0, 0
	}

	snippetLines := significantLines(snippet)
	if len(snippetLines) == 0 {
		return 0, 0
	}

	windowSize := len(snippetLines)
	if windowSize > len(candidates) {
		return 0, 0
	}

	bestScore := -1.0
	bestStart := -1
	for start := 0; start+windowSize <= len(candidates); start++ {
		var sum float64
		for i := 0; i < windowSize; i++ {
			sum += lineScore(snippetLines[i], candidates[start+i].Text)
		}
		score := sum / float64(windowSize)
		if score > bestScore {
			bestScore = score
			bestStart = start
		}
	}

	if bestStart < 0 || bestScore < matchThreshold {
		return 0, 0
	}

	return candidates[bestStart].LineNo, candidates[bestStart+windowSize-1].LineNo
}
