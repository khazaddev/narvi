package gitstate

import "strings"

// NormalizeBranchName implements exactly what §3.4 asks for: "Branch names
// normalized (lowercase) before push." Lowercasing only -- no whitespace
// trimming, no character substitution, no length limit. Those would be
// additional normalization rules the plan does not ask for, and this
// package has zero I/O of any kind to validate against a real git ref-name
// grammar anyway; that enforcement, if ever needed, belongs to whichever
// later Step actually shells out to git (§3.4's header: "enforced by
// sandbox-agent").
func NormalizeBranchName(name string) string {
	return strings.ToLower(name)
}
