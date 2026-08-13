package reviewtriage

import "strings"

// The unified-diff-header line prefixes/markers ExtractChangedPaths below
// recognizes -- mirrors internal/domain/reviewpost's own diffFileHeaderRE
// precedent (position.go: "^\+\+\+ b/(.+)$") for what counts as a real
// file-header line, extended (adversarial-review fix, D3: "sensitive-glob
// rule is blind to deleted and renamed files") to also recognize a
// deletion's own "--- a/<path>" line (paired with a "+++ /dev/null"
// post-change side) and a rename's own "rename from <path>"/"rename to
// <path>" lines.
const (
	// changedPathAddedPrefix is a file's post-change path, present for
	// every ADDED or MODIFIED file (never present at all for a pure
	// rename with no content change -- see renameFrom/ToPrefix below for
	// that case).
	changedPathAddedPrefix = "+++ b/"
	// changedPathRemovedPrefix is a file's pre-change path -- present for
	// every MODIFIED or DELETED file. For a modified file this is
	// redundant with changedPathAddedPrefix's own identical path
	// (deduplicated below); for a DELETED file (paired with
	// changedPathDeletedMarker, immediately following) it is this file's
	// ONE AND ONLY path -- there is no "+++ b/" line to fall back on at
	// all, which is exactly the gap this fix closes.
	changedPathRemovedPrefix = "--- a/"
	// changedPathDeletedMarker is a deleted file's own post-change side
	// -- "+++ /dev/null", the unified-diff convention for "this file no
	// longer exists". When seen, the immediately-preceding
	// changedPathRemovedPrefix line (this SAME file section's own
	// "--- a/<path>") is what gets harvested instead.
	changedPathDeletedMarker = "+++ /dev/null"
	// changedPathAddedMarker is an added file's own pre-change side --
	// "--- /dev/null" -- recognized ONLY to reset pendingRemoved (below)
	// so a stale "--- a/<path>" value can never leak forward from an
	// earlier file section into a LATER, unrelated one.
	changedPathAddedMarker = "--- /dev/null"
	// diffGitHeaderPrefix marks the start of each new file's own diff
	// section -- recognized ONLY to reset pendingRemoved (below), the
	// same defensive reason changedPathAddedMarker is.
	diffGitHeaderPrefix = "diff --git "
	// renameFromPrefix/renameToPrefix are a DETECTED RENAME's own two
	// lines (git's unified-diff extension, present whenever git's own
	// rename-detection fires, WHETHER OR NOT the rename also carries a
	// content change) -- each is its own single, unambiguous line (a
	// fixed literal prefix, the path is everything after it to end of
	// line), unlike the "diff --git a/<old> b/<new>" header line itself,
	// which concatenates BOTH paths onto one line with no unambiguous
	// separator when either path could itself contain the literal
	// substring " b/" -- deliberately NOT parsed here for that reason.
	// For a PURE rename (100% similarity, no content change at all),
	// these two lines are the ONLY place either path appears anywhere in
	// the diff -- no "--- a/"/"+++ b/" pair is emitted for such a file at
	// all.
	renameFromPrefix = "rename from "
	renameToPrefix   = "rename to "
)

// ExtractChangedPaths deterministically parses diff (a unified,
// possibly-multi-file diff, exactly the shape internal/app/
// reviewcontext.Fetch's own GetCompareDiff call returns and already
// hands to review.RenderTurnPrompt) into the ordered, deduplicated list
// of repo-relative paths it touches -- §26.3's own "changed paths,
// promoted to a first-class structured signal (not just used inline)".
// Pure, no I/O: diff is already-fetched text, never re-fetched here.
//
// Every file this diff touches contributes AT LEAST one path (a
// modification or addition: its post-change path via
// changedPathAddedPrefix; a deletion: its pre-change path via
// changedPathRemovedPrefix, paired against changedPathDeletedMarker) and,
// for a detected rename, BOTH its pre- and post-change paths (via
// renameFromPrefix/renameToPrefix) -- a rename vacates one location and
// populates another, and both are real changes to the tree this
// package's own downstream signals (sensitive-glob matching, distinct-
// root counting) care about.
//
// # Adversarial-review fix (D3): deleted and renamed files used to contribute NO path at all
//
// Before this fix, this function recognized ONLY changedPathAddedPrefix
// ("+++ b/<path>") -- a deleted file's own "+++ /dev/null" header (no
// "b/" prefix) never matched, contributing NO path entry at all, and a
// 100%-similarity rename (no "---"/"+++" pair emitted at all) likewise
// contributed nothing. Both path-derived signals downstream (the
// sensitive-glob rule, the distinct-root-count rule) went BLIND on these
// two file shapes: a PR that deletes a sensitive path (a migration file,
// a CI workflow) routed light, identically to a PR that never touched it
// at all -- the identical blast radius as EDITING that same path (which
// correctly routed deep), just because the change happened to be a
// deletion rather than a modification. This was NOT a purely safe,
// only-ever-conservative gap the way the "an added line's own content
// happens to start with a header-shaped string" false-positive risk
// (still real, still accepted, see below) is -- this one was a genuine
// false NEGATIVE, the unsafe direction, for a real and unremarkable class
// of PR.
//
// Known, accepted limitation (unchanged by this fix, the ONE remaining
// case where this function's own parse can only ever ADD a spurious path,
// never miss a real one -- internal/domain/reviewpost's own
// diffFileHeaderRE already accepts the identical tradeoff, position.go):
// an ADDED source line whose own content happens to start with one of
// this function's own recognized prefixes (e.g. the three literal
// characters "+++ b/") would misidentify as a file header. This is
// vanishingly rare in practice, and the failure direction here IS safe:
// it can only ever ADD a spurious path to the result, nudging root
// dispersion or a sensitive-glob match toward the MORE conservative
// (deep-routing) outcome, never the less safe one -- unlike the deletion/
// rename gap this fix closes, which failed in the UNSAFE direction.
func ExtractChangedPaths(diff string) []string {
	if diff == "" {
		return nil
	}

	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	// pendingRemoved is the most recently seen changedPathRemovedPrefix
	// line's own path -- consumed (and cleared) by the VERY NEXT
	// changedPathDeletedMarker or changedPathAddedPrefix line, which is
	// always this SAME file section's own paired post-change side in a
	// well-formed unified diff. Explicitly cleared on changedPathAddedMarker
	// and diffGitHeaderPrefix too, so a malformed/truncated diff can never
	// leak a stale value forward onto an unrelated LATER file's deletion.
	var pendingRemoved string
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, diffGitHeaderPrefix):
			pendingRemoved = ""
		case line == changedPathAddedMarker:
			pendingRemoved = ""
		case strings.HasPrefix(line, changedPathRemovedPrefix):
			pendingRemoved = strings.TrimPrefix(line, changedPathRemovedPrefix)
		case line == changedPathDeletedMarker:
			add(pendingRemoved)
			pendingRemoved = ""
		case strings.HasPrefix(line, changedPathAddedPrefix):
			add(strings.TrimPrefix(line, changedPathAddedPrefix))
			pendingRemoved = ""
		case strings.HasPrefix(line, renameFromPrefix):
			add(strings.TrimPrefix(line, renameFromPrefix))
		case strings.HasPrefix(line, renameToPrefix):
			add(strings.TrimPrefix(line, renameToPrefix))
		}
	}
	return out
}
