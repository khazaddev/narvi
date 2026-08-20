// Package handoff implements §14.4's own ("handoff-readiness sentinel",
// §14.4/§14.5) pure decision logic: given a scoped-session PR's own
// already-resolved contract-drift signal (reused from §14.3's
// internal/domain/contractdrift, never re-derived here) and a scan of its
// diff for backend-adjacent TODO/FIXME markers (this Step's own new,
// small logic), decide whether there is anything worth telling an
// engineer about and, if so, render it into typed reviewpost.Finding
// values plus a one-way comment body -- never parsed back from posted
// markdown, exactly like §8.2 already established for the ordinary
// review-verdict path (review/reviewpost.go's own doc comments).
//
// Every function here is pure per §11: no I/O, no time.Now(), no
// randomness. The impure halves -- fetching a PR's diff, resolving a
// repo's current contracts fingerprint, reading/writing Postgres,
// claiming idempotency, enqueuing the outbox row -- all live in
// internal/app/sessionactor/handoffsentinel.go, which calls into this
// package exactly the same way internal/app/sessionactor/contractdrift.go
// calls into internal/domain/contractdrift: impure fetch in app, pure
// decision here.
//
// # Why this is its own package, not a new file in reviewpost
//
// reviewpost (§8.2) is specifically about POSTING A REVIEW VERDICT:
// its Finding type is minted by the verdict-posting-tool HTTP endpoint,
// persisted into review_findings, and feeds rebuttal/re-review
// reconciliation -- a lifecycle this Step's own findings never enter (see
// this package's own top-level design-call section below). Reusing
// reviewpost.Finding's SHAPE and reviewpost.ComputeFindingIdentity's HASH
// ALGORITHM (both imported here, per the plan's own "go through §8.2's
// Finding type and its server-computed identity hash -- do not invent a
// parallel finding shape or a second identity scheme") is not the same
// thing as reusing reviewpost's own POSTING pipeline, which this Step
// deliberately never touches -- see handoffsentinel.go's own top comment
// for the full reasoning.
//
// # Design calls made in this Step, flagged rather than papered over
//
//  1. Handoff findings are built via reviewpost.FindingInput/BuildFinding
//     with SentinelKind always nil. reviewpost.SentinelKind is a closed,
//     two-value vocabulary (coverage/docs_drift) that §8.2's own
//     §17.1 "no recursion" rule uses to decide whether a posted finding
//     is eligible to trigger the sentinel-auto-fix child-session flow
//     (httpapi/reviewverdict.go's own hasSentinelFinding). Adding a third
//     SentinelKind value ("handoff") would silently make a handoff
//     finding eligible for that ENTIRELY UNRELATED flow the moment a repo
//     happens to have sentinel_autofix_enabled on -- a real, easy-to-miss
//     coupling bug, and exactly the kind of "child session for handoff"
//     scope creep this Step's own brief explicitly forbids ("no child
//     session for handoff... if you find yourself spawning a session,
//     stop"). Handoff findings never flow through the verdict-posting
//     endpoint at all (see handoffsentinel.go), so SentinelKind's own
//     auto-fix-eligibility meaning never applies to them regardless --
//     nil is simply the correct, honest value: an ordinary,
//     non-sentinel-auto-fix-eligible finding, matching FindingInput's own
//     doc comment ("nil... means an ORDINARY risk-map finding with no
//     sentinel origin at all").
//
//  2. The "uncontracted endpoints" signal is repo-level, not endpoint-
//     level, despite §14.4's own text ("reports which endpoints the
//     prototype calls that have no entry in contracts/api/*"). §14.3's
//     actual, already-merged contract-drift machinery
//     (internal/domain/contractdrift) computes exactly ONE thing: whether
//     a repo's own current (RepoSHA, ContractsFingerprint) pair has
//     drifted from the last recorded Snapshot -- a coarse, whole-directory
//     fingerprint comparison (contractdrift.Fingerprint hashes an entire
//     directory LISTING), with no concept of "which specific endpoint a
//     frontend call site invokes" or "which path a contract entry
//     declares" anywhere in its inputs or outputs. There is no
//     endpoint-level data anywhere in this codebase to reuse. Building an
//     actual per-endpoint scanner (parsing frontend fetch/axios call
//     sites, parsing contracts/api/*'s own OpenAPI paths, diffing the two
//     sets) is precisely "a second endpoint scanner" this Step's own
//     brief says not to write. This package therefore surfaces
//     contractdrift.HasDrifted's own repo-level boolean, worded honestly
//     ("this repo's backend appears to have changed since its contract
//     was last checked" -- see ContractDriftFinding below), never a
//     fabricated list of specific paths nothing in this codebase actually
//     knows. Named here as a genuine, unresolved tension between §14.4's
//     own text and §14.3's actual scope -- not silently narrowed.
//
//  3. Severity: ContractDriftFinding is review.RiskLevelMedium (real
//     engineering follow-up is very likely needed before this ships,
//     but this alone is not itself a defect in the diff under review);
//     TODOFinding is review.RiskLevelLow (an expected, informational
//     marker in a prototype, not a risk). Neither severity is named by
//     the plan -- a deliberate, reviewed choice, not a derived one.
//
//  4. "Backend TODOs the agent left behind" is read as: every TODO/FIXME
//     marker ADDED (never merely left in place) by this diff, with no
//     further keyword filtering (e.g. requiring the marker's own text to
//     mention "backend"/"API"). This is deliberate, not a shortcut: §14.1's
//     own sparse-checkout enforcement means backend files never
//     materialize in a scoped session's sandbox at all ("Excluded paths
//     never materialize on the sandbox filesystem"), so EVERY file this
//     diff can possibly touch is already frontend-only by construction --
//     any TODO an agent adds in that diff is, by definition, a marker left
//     in code that cannot itself do the backend work it names. No
//     additional semantic filter could narrow this further without
//     guessing at the marker's own free-text content, which this package
//     (matching review/doc.go's own "never parse a posted comment"
//     principle applied one step earlier) declines to do.
package handoff
