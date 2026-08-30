// This file holds the synthetic results a suppressed write hands back, and
// the rule §30.6 sets for them: a synthetic result must be impossible to
// mistake for a real one.
//
// That rule is not decoration. These values flow into the same fields real
// ones do -- a PR number rendered on a screen, a commit SHA written into a
// row, a URL someone may click. A plausible-looking fake is the worst
// outcome available: it survives into records and screens where nobody can
// tell it apart, and the evaluation the shadow deployment exists to
// produce becomes unreadable.
//
// So each value here is chosen to be self-evidently synthetic on sight,
// while still being the right SHAPE for the field it fills, so the state
// machines consuming it stay coherent (§30.7).

package shadowscm

import "github.com/khazaddev/narvi/internal/app/ports"

// syntheticPRNumber is deliberately negative. Real GitHub pull request
// numbers are positive and monotonic, so a negative one cannot collide
// with a real PR and cannot be mistaken for one anywhere it is printed,
// compared, or stored -- while still being an int, so nothing downstream
// has to special-case the type.
const syntheticPRNumber = -1

// syntheticCommitSHA is the right length and alphabet for a git object id,
// so anything that validates the shape still works, and spells what it is
// so nobody reads it as a real commit. It is not a valid hex SHA: the
// letters past 'f' make it impossible for it to name an object that could
// ever exist.
const syntheticCommitSHA = "shadowsuppressednotarealcommitsha0000000"

// syntheticPRRef builds the PRRef a suppressed CreatePR returns. The URL
// points nowhere on the real host on purpose -- a link that resolves to a
// customer's actual repository would invite someone to click through and
// conclude the PR is missing, rather than that it was never created.
func syntheticPRRef(owner, repo string) ports.PRRef {
	return ports.PRRef{
		Number: syntheticPRNumber,
		URL:    "shadow-suppressed://" + owner + "/" + repo + "/pull/not-created",
	}
}

// IsSyntheticCommitSHA reports whether sha is exactly the value a
// suppressed UpdateFileContent returns (syntheticCommitSHA, above).
//
// UpdateFileContent's own suppressed branch deliberately returns this
// value with a nil error, never a sentinel error the way MergePR's own
// suppression does (Decorator.MergePR's doc comment) -- keeping the
// result coherent for callers that only need SOMETHING SHA-shaped to keep
// flowing. But httpapi's own apply-suggestion handler (reviewfindings.go)
// is not one of those callers: it decides a review finding's own status
// from the result, and crediting a shadow-suppressed "commit" as a real
// one there is exactly the naive-suppression bug §30.7 names by name
// ("marks the finding fix_applied with a SHA that exists nowhere"). This
// is that caller's one way to tell the two apart without this package
// handing out anything that could itself be mistaken for a credential or
// a real object id.
func IsSyntheticCommitSHA(sha string) bool {
	return sha == syntheticCommitSHA
}

// IsSyntheticPRRef reports whether ref is the placeholder a suppressed
// CreatePR returns rather than a real pull request.
//
// Callers use it to stop at the single hop. §30.9's resolved decision is
// that shadow validates single hops only, and a suppressed CreatePR
// returns a nil error with a usable-looking ref -- so a caller that
// simply carries on runs its downstream lanes against a pull request that
// does not exist: preview enqueues, handoff-readiness reads, and any
// state they write, all about a PR nobody can open. That is the
// second-order exercise the decision rules out, and it does not announce
// itself, because nothing failed.
//
// Matching is on the number alone. syntheticPRNumber is negative and real
// GitHub PR numbers are positive and monotonic, so the test cannot
// collide -- and it stays true for a ref that has been round-tripped
// through a store that kept only the number.
func IsSyntheticPRRef(ref ports.PRRef) bool {
	return ref.Number == syntheticPRNumber
}
