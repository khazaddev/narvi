// Package falsepositive implements §22's own §22.2-§22.4 pure domain
// logic (docs/TECHNICAL_PLAN.md §22): the deterministic `false positive:
// <reason>` PR-thread command a maintainer+ uses to teach a repo-scoped
// false-positive pattern (§22.2), and the advisory content block that
// pattern is later injected back into every review pass as (§22.3).
//
// No I/O, no time.Now(), no randomness (§11) -- every function here is a
// pure transformation over already-in-hand strings, exactly like internal/
// domain/plan's MatchRevise/RerunGuidance-shaped siblings this package
// deliberately mirrors:
//
//   - command.go: Prefix, Match -- the deterministic, case-insensitive
//     PREFIX detector the capture command is dispatched on, mirroring
//     plan.RevisePrefix/MatchRevise's own identical "whole-prefix, never a
//     substring" discipline (and its own Unicode-byte-offset bug fix,
//     applied here from the start rather than discovered later).
//   - advisory.go: Pattern, RenderAdvisoryBlock -- the §22.3 advisory
//     content block a review turn's prompt carries, delimited and
//     explicitly marked untrusted (§5.2), mirroring internal/domain/
//     reviewpost.RenderAlreadyAnsweredFacts' own established delimiter
//     discipline for a DIFFERENT reason: a taught pattern's own reason
//     text is maintainer-authored, first-party content, but it is still
//     never rendered as an instruction -- see RenderAdvisoryBlock's own
//     doc comment for why this is structurally, not just textually,
//     incapable of acting as a filter.
//
// Deliberately its own package, not a further extension of internal/
// domain/review or internal/domain/reviewpost: this is a third, distinct
// concept from either -- review.Verdict is the structured verdict a
// review session produces (§8.2), reviewpost.Finding is one verdict's
// own per-finding content-anchored identity/position (§22.1/§22.1.1,
// Step 48/63) -- a maintainer-taught PATTERN is neither; it is standing,
// repo-scoped guidance that exists independently of any single verdict or
// finding, fed INTO every future review rather than produced BY one.
package falsepositive
