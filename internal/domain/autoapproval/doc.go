// Package autoapproval implements §21.2 stage 1's auto-approval
// eligibility engine (§21) -- the REAL replacement for §16's own
// interim stand-in, internal/domain/decisioninbox.ComputeAutoApprovalEligible
// (deleted by this Step; see that function's own former doc comment for
// the full "why this existed, and what replaces it" history this package
// makes good on). Pure per CLAUDE.md/§11: no I/O, no time.Now(), no
// randomness -- every input is already-fetched data, exactly like
// internal/domain/review's own ComputeShippable and
// internal/domain/sentinelfix's own EvaluateMergeGate.
//
// # The criteria, and where each one actually lives
//
// §21.2 states: "A PR becomes auto-approved when Shippable == auto ...
// AND every one of a deterministic, server-checked eligibility list
// holds: CI green at head; no floor raised ...; diff size under a
// configurable-per-repo threshold; no sensitive path touched ...; the
// verdict being relied on was produced against the PR's CURRENT head
// SHA." ComputeEligible below checks exactly these, in this order:
//
//  1. HasNeedsHumanLabel -- checked FIRST, as an unconditional override
//     (§21.2: "review: needs-human ... forces a specific PR out of
//     auto-approval regardless of what the criteria say"), never merely
//     one AND-condition among equals.
//  2. VerdictHeadSHA != CurrentHeadSHA (the stale-verdict guard) -- "a
//     verdict computed against an earlier commit is stale by definition
//     and must never itself satisfy eligibility, no matter how low-risk
//     it once looked" (§21.2). Checked early, deliberately BEFORE the
//     Shippable check below: a stale verdict's own Shippable value is
//     never even a fact worth reasoning about, since it was never
//     computed against the code actually under consideration.
//  3. CIGreen -- must be true.
//  4. Verdict.Shippable == review.ShippableAuto.
//  5. EligibilityInput.ChangedFileCount <= cfg.MaxFilesChanged (diff
//     size) -- GitHub's own authoritative changed-file scalar, never
//     Verdict.FilesChanged, and (Phase 5 audit finding 2, fixed) never
//     a possibly page-truncated len() of the fetched path listing
//     either.
//  6. EligibilityInput.TouchedBlastRadiusKnown is true (Phase 5 audit
//     findings 1+2, fixed) -- the sensitive-path facts check 7 below
//     relies on must have actually been established from GitHub; a
//     failed or page-truncated changed-files fetch refuses here rather
//     than silently reading as "nothing sensitive touched".
//  7. No cfg.SensitiveTags member appears in EligibilityInput.
//     TouchedBlastRadius (no sensitive path touched) -- never
//     Verdict.BlastRadius.
//
// "No floor raised: neither the coverage floor nor the premise floor
// ... is above its baseline" is DELIBERATELY not a separate check of its
// own anywhere in the numbered list above (never conflate this with
// check 6, Phase 5 audit findings 1+2's own "is the fact even knowable"
// gate above, which exists for an entirely different reason: whether
// GitHub's changed-files data could be fetched at all, nothing to do
// with floors). internal/domain/review's own ComputeShippable composes
// RiskLevel's baseline with CoverageFloor/PremiseFloor via max(rank) --
// a RAISE-ONLY composition (review/shippable.go's own doc comment: "this
// function never returns a Shippable ranked BELOW baselineFromRisk(risk)
// alone, nor below CoverageFloor(coverage) alone, nor below
// PremiseFloor(premise) alone"). It follows MATHEMATICALLY that
// Shippable == ShippableAuto (rank 0, the most permissive rank in
// review's own explicit total order) is only ever reachable when EVERY
// one of baseline/coverage-floor/premise-floor independently evaluated
// to ShippableAuto too -- there is no way for a raised floor to
// contribute a HIGHER rank into a max() and still have the max() come
// out at the LOWEST rank. Re-deriving "no floor raised" as a second,
// independent check over the verdict's own raw RiskLevel/TestsCoverage/
// Premise fields would therefore either (a) always agree with check 4
// above, making it dead weight, or (b) disagree with it, which would
// mean domain/review's own raise-only property had a bug -- a bug this
// package has no business re-litigating a second time. Check 4 alone
// already IS "no floor raised", exactly as rigorously as a bespoke
// second check would be. This package's own test suite (eligibility_test.go)
// still exercises "a floor raised" as its own, independently named
// scenario -- via three DISTINCT Verdict fixtures (coverage floor
// raised, premise floor raised, risk baseline alone raised), each
// proving check 4 catches that specific case -- rather than by adding a
// redundant branch that could never independently fail.
//
// # IsDraft / HasChangesRequested are deliberately NOT inputs here
//
// Both of internal/app/decisioninbox's two call sites already filter a
// draft PR before ever reaching this package (aggregate.go's own
// buildPRItems: "if pr.Draft { continue }"), and both already apply
// HasChangesRequested as a SEPARATE, hard block layered on top of this
// package's own eligible/not-eligible verdict (§16.1: "ready_to_merge's
// own 'approval' is auto-approval BY THE DETERMINISTIC ELIGIBILITY
// ENGINE ... never a human GitHub review" -- HasChangesRequested is
// exactly that separate human-review fact, never folded into what
// "eligibility" itself means). §21.2's own criteria list names neither,
// so this package does not manufacture a seventh/eighth check for
// either -- see internal/app/decisioninbox/revalidate.go's own
// revalidateCore for where both are actually enforced.
package autoapproval
