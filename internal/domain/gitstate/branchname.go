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

// sessionBranchPrefix is this package's own invented convention (Step 29,
// "gitstate in-sandbox") for a GENERATED session branch name -- nothing
// else in the codebase names one (internal/app/sessionactor/pushpr.go's own
// doc comment documents the separate, pre-existing gap this fills: a repo
// whose repos[].branch is null has never had anything decide what branch
// actually gets checked out). "narvi/" plus the full session id keeps the
// generated name unique per session, traceable back to the session that
// created it, and vanishingly unlikely to collide with any real branch a
// human or automation might have already created.
const sessionBranchPrefix = "narvi/"

// ResolveSessionBranch decides which branch a repo's boot-sequence checkout
// step (§3.4: "checkout session branch (create from base if absent)")
// should target, given the repo's own explicit configured branch
// (sessionconfig.SessionConfigReposElem.Branch -- this package deliberately
// takes a plain *string rather than importing that generated type, per
// this package's own "zero I/O, self-contained" convention; nil means
// "create the session branch from the repo's default base branch", that
// field's own documented meaning) and the session's own id.
//
// When explicitBranch is non-nil, ITS value is normalized
// (NormalizeBranchName) and returned -- an explicit caller choice always
// wins, verbatim. When it is nil, a session-scoped branch name is invented
// (sessionBranchPrefix + sessionID) and normalized the same way. Every
// return value passes through NormalizeBranchName exactly once, matching
// §3.4's own "branch names normalized (lowercase) before push" rule
// uniformly across both cases -- never applied twice, never skipped.
//
// Pure: no I/O, no randomness -- sessionID is the caller's own already-known
// value (e.g. the session's UUID string), never generated here.
func ResolveSessionBranch(explicitBranch *string, sessionID string) string {
	if explicitBranch != nil {
		return NormalizeBranchName(*explicitBranch)
	}
	return NormalizeBranchName(sessionBranchPrefix + sessionID)
}
