package reviewtriage

import "strings"

// changedPathHeaderPrefix is a unified diff's own "+++ b/<path>" file
// header line -- the post-change side, mirroring internal/domain/
// reviewpost's own diffFileHeaderRE precedent (position.go: "^\+\+\+
// b/(.+)$") for what counts as a real file-header line. A deleted file's
// own "+++ /dev/null" header (no "b/" prefix) never matches, which is the
// desired behavior: §26.3's own signals (root dispersion, sensitive-glob
// matching) care about the post-change tree a review actually examines,
// and a fully deleted file has none. Deletions are still reflected in
// Signals.Deletions (the PR resource's own server-reported line count),
// never re-derived here.
const changedPathHeaderPrefix = "+++ b/"

// ExtractChangedPaths deterministically parses diff (a unified,
// possibly-multi-file diff, exactly the shape internal/app/
// reviewcontext.Fetch's own GetCompareDiff call returns and already
// hands to review.RenderTurnPrompt) into the ordered, deduplicated list
// of repo-relative paths it touches -- §26.3's own "changed paths,
// promoted to a first-class structured signal (not just used inline)".
// Pure, no I/O: diff is already-fetched text, never re-fetched here.
//
// Known, accepted limitation (the identical tradeoff internal/domain/
// reviewpost's own diffFileHeaderRE already accepts, position.go): an
// ADDED source line whose own content happens to start with the three
// literal characters "+++ b/" would misidentify as a file header. This
// is vanishingly rare in practice, and the failure direction is safe: it
// can only ever ADD a spurious path to the result, nudging root
// dispersion or a sensitive-glob match toward the MORE conservative
// (deep-routing) outcome, never the less safe one.
func ExtractChangedPaths(diff string) []string {
	if diff == "" {
		return nil
	}

	seen := make(map[string]bool)
	var out []string
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, changedPathHeaderPrefix) {
			continue
		}
		p := strings.TrimPrefix(line, changedPathHeaderPrefix)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
