// Package archrecap implements §26.5's own measurement command:
// `arch recap wrong: <reason>` -- a maintainer+ contests the deep path's
// own architecture-recap digest section (§26.1/§26.4's "Architecture
// choices" section, informed by the architecture-scribe sub-task on the
// deep path). Deliberately mirrors internal/domain/falsepositive's own
// `false positive: <reason>` capture command (§22.2) precisely
// -- IMPLEMENTATION_PLAN.md's own Step 69 row: "a maintainer command
// `arch recap wrong: <reason>` that MIRRORS Step 63's `false positive:
// <reason>` command EXACTLY" -- down to the deterministic, case-
// insensitive whole-prefix match discipline and its own Unicode-byte-
// offset-safe implementation.
//
// No I/O, no time.Now(), no randomness (§11) -- every function here is a
// pure transformation over already-in-hand strings, exactly like
// falsepositive's own identical convention.
//
// Deliberately its own package, not folded into internal/domain/
// falsepositive: a contest is a DIFFERENT concept from a taught false-
// positive pattern -- falsepositive's own pattern is standing, repo-scoped
// advisory content fed INTO every future review (§22.3); an arch-recap
// contest is a per-PR, per-digest-section measurement signal (§26.5's own
// "digest precision (contestation rate)" KPI), never injected back into
// any review's own prompt. Reconciled by content hash of the digest
// section's own persisted text (internal/domain/reviewpost.
// ComputeDigestSectionIdentity, §22.1's identity discipline extended to
// digest sections) rather than repo-scoped teaching, which is why this
// package's own persistence (internal/adapters/outbound/postgres/
// reviewdigestsectionfeedback_store.go) is a structurally different table
// from review_false_positive_patterns, despite the identical capture-
// command shape.
package archrecap
