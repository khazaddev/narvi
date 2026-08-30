// Package shadowledger records what platform shadow mode did not do.
//
// Shadow mode's product is the record (§30.6): the platform runs against
// real customer repositories, every customer-visible write is suppressed,
// and what an operator evaluates afterwards is the ledger of effects that
// would have happened. This package owns that write path -- the token-free
// record types that bound what can reach the table, and the record-or-fail
// insert that refuses to let a suppression go unevidenced.
//
// # Scope: single hops only (§30.9, resolved)
//
// Every row here is a direct consequence of one platform-initiated write
// that this deployment itself attempted and suppressed -- a PR that would
// have been opened, a branch, a merge, a commit, a comment. What a row
// NEVER represents is a downstream, second-order consequence of that
// suppressed write: with no git mirror (§30.9's own resolved decision)
// there is no real ref for a suppressed CreatePR's PR to exist on, so no
// github_pr_session is ever synthesized from it. Nor do the lanes that
// would otherwise hang off a real PR run against a suppressed one:
// review-of-own-work, auto-approval and description-autofix are reached
// only through GitHub's own pull_request webhooks, which never arrive for
// a PR that was never opened; and the two that ARE reached in-process
// from the PR-creation path -- preview dispatch and the
// handoff-readiness sentinel -- stop explicitly on a synthetic ref
// (shadowscm.IsSyntheticPRRef, internal/app/sessionactor's own
// createPRBestEffort).
//
// That last pair is worth naming, because it is what "never execute in
// the first place" would have quietly got wrong: a suppressed CreatePR
// returns a nil error and a usable-looking ref, so carrying on into them
// is the DEFAULT, and it fails silently rather than loudly. They are
// stopped deliberately, not absent by luck. A reader of this package's own rows -- today by querying the
// table directly, eventually through whatever surface reads it -- must
// never infer that a suppressed CreatePR implies any of those lanes were
// exercised, evaluated, or even reached. The honest claim this ledger
// supports is exactly "this platform-initiated write would have happened
// and did not"; it is not evidence about what GitHub, or any other
// system, would have done in response to it.
package shadowledger
