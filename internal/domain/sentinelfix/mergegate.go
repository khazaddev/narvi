// Package sentinelfix implements the pure decision logic behind Step 48's
// own ("sentinels + suggestions") sentinel-auto-fix merge-gating step
// (§17.4): "the fix branch is re-checked... the same facts a human
// clicking Merge would rely on: the cherry-picked diff touches nothing
// but test and documentation files, CI is green at the new tip, the
// cherry-pick applied cleanly, and the toggle is still enabled. Only if
// all four hold does it auto-approve... and merge."
//
// Every function here is pure per §11: no I/O, no time.Now(), no
// randomness. The real I/O this decision depends on (fetching the fix
// PR's own changed files, CI status, mergeable state, and the repo's own
// current toggle value) lives in internal/adapters/inbound/github's own
// merge-gating webhook lane (pullrequestevent.go) -- this package is the
// one place the FOUR-CHECK POLICY itself is expressed and tested,
// independently of how each input was obtained.
package sentinelfix

import "strings"

// MergeGateDecision is EvaluateMergeGate's own result -- Allowed is the
// final yes/no; Reason is set (non-empty) exactly when Allowed is false,
// naming WHICH of the four checks failed first, for the audit_log entry
// §17.5 requires ("capturing... which of the four checks passed").
type MergeGateDecision struct {
	Allowed bool
	Reason  string
}

// allow is EvaluateMergeGate's own success value -- a small, named
// constructor so every failure branch below reads symmetrically.
func allow() MergeGateDecision { return MergeGateDecision{Allowed: true} }

func deny(reason string) MergeGateDecision { return MergeGateDecision{Allowed: false, Reason: reason} }

// EvaluateMergeGate implements §17.4's own four-check policy, checked in
// the SAME fixed order every time (so a caller presenting more than one
// failing condition always gets the same, deterministic first reason,
// mirroring reviewpost.ValidateVerdictInput's own identical discipline):
//
//  1. toggleEnabled -- the repo's sentinel_autofix_enabled flag, re-read
//     fresh at merge-gating time ("the toggle is still enabled" --
//     §17.4's own explicit fourth check: an admin may have disarmed it
//     mid-flight).
//  2. ciGreen -- CI status at the fix branch's own new tip.
//  3. cherryPickClean -- the cherry-pick (or GitHub's own automatic
//     stack-rebase equivalent, when the pair was successfully registered
//     as a stack) applied without conflict.
//  4. changedFiles -- EVERY file the cherry-picked diff touches must be a
//     test or documentation path (IsTestOrDocPath) -- independent of, and
//     never assuming, §17.2's own separate spawn-time capability
//     restriction (that restriction is never trusted as sufficient on its
//     own; see this package's own doc comment).
//
// Any single failing condition denies the WHOLE merge -- there is no
// partial credit; a denied merge is never itself a hard failure to the
// caller, only a signal that the fix PR falls through to the ordinary,
// human-supervised needs_review path (§17.4's own explicit fallback).
func EvaluateMergeGate(changedFiles []string, ciGreen, cherryPickClean, toggleEnabled bool) MergeGateDecision {
	if !toggleEnabled {
		return deny("sentinel-auto-fix toggle is no longer enabled for this repo")
	}
	if !ciGreen {
		return deny("CI is not green at the fix branch's own new tip")
	}
	if !cherryPickClean {
		return deny("the cherry-pick (or automatic stack rebase) did not apply cleanly")
	}
	if offender, ok := firstNonTestOrDocPath(changedFiles); ok {
		return deny("cherry-picked diff touches a file outside test/doc scope: " + offender)
	}
	return allow()
}

// firstNonTestOrDocPath returns the first path in files that is NOT a
// test/doc path (IsTestOrDocPath), and ok=true -- or ok=false if every
// path in files qualifies (including the degenerate case of an empty
// files list, which trivially satisfies "every file is test/doc" since
// there are none to violate it).
func firstNonTestOrDocPath(files []string) (string, bool) {
	for _, f := range files {
		if !IsTestOrDocPath(f) {
			return f, true
		}
	}
	return "", false
}

// IsTestOrDocPath classifies one repo-relative file path as "test/doc
// scope" -- the SAME scope §17.2's own spawn-time capability restriction
// grants write access to, but a deliberately INDEPENDENT implementation
// (this package imports nothing from internal/adapters/outbound/opencode,
// and vice versa) -- §17.4's own text is explicit that this check "is
// independent of, and does not rely on, §17.2's write-capability
// restriction on the child session... this re-verification runs
// regardless of whether that restriction held." A path qualifies when:
//
//   - it ends in "_test.go" (a Go test file), or
//   - it has a "testdata" path component anywhere (Go's own convention
//     for fixture directories the toolchain never compiles), or
//   - it has a "docs" path component anywhere, or
//   - it ends in ".md" or ".mdx" (documentation, wherever it lives in the
//     tree -- a README.md at the repo root is not under "docs/", but is
//     still unambiguously documentation).
//
// Fails conservative: an empty path is never a test/doc path (there is
// nothing to classify as one).
func IsTestOrDocPath(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	if strings.HasSuffix(path, "_test.go") {
		return true
	}
	if strings.HasSuffix(path, ".md") || strings.HasSuffix(path, ".mdx") {
		return true
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "testdata" || segment == "docs" {
			return true
		}
	}
	return false
}
