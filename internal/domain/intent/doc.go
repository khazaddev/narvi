// Package intent holds the unified intent classifier's pure decision
// logic (§8.3, §18): the confidence rubric text, the needsClarification
// and target-corroboration derivation functions, the per-session
// IntentDecisionRecord value type + its bounded-length reasoning
// truncation helper, and the DB-backed prompt-template assembly
// primitive. Everything here is pure -- no I/O, no time.Now(), no
// randomness (CLAUDE.md) -- the impure orchestration (calling the LLM
// port, persisting records, gating shadow/active mode) lives in
// internal/app/intentclassifier, which imports this package.
//
// # Confidence rubric (§18.2)
//
// ConfidenceRubric is the single shared constant every ingress surface's
// classification call references, via its field-description placement in
// the classifier's structured-output JSON schema (internal/app/
// intentclassifier builds that schema; this package only owns the text).
// Never duplicated per surface.
//
// # needsClarification and target corroboration (§18.2)
//
// DeriveNeedsClarification turns (confidence, plausible-target-count)
// into a versionable, testable bool -- never asked of the model directly.
// CorroborateTarget implements the mandatory independent-deterministic-
// check requirement for irreversible actions (triggering a review,
// dispatching a build): on disagreement between the classifier's own
// Target and a caller-supplied deterministic signal, it reports the
// disagreement rather than silently picking one, so a caller can lower
// confidence / ask for clarification instead of guessing.
//
// # IntentDecisionRecord (§18.4)
//
// The per-session routing decision record's value type + TruncateReasoning,
// the bounded-length helper the write-once persistence path
// (internal/app/intentclassifier) uses before ever storing Reasoning.
//
// # DB-backed prompt templates (§18.6)
//
// AssembleTemplate/ValidateTemplate implement the simple, non-Turing-
// complete "{{variable_name}}" placeholder syntax this Step's own prompt-
// template design chose (§18.6: "designed from scratch when Step 36 is
// implemented") -- pure string substitution only; the DB-backed template
// storage itself (the Postgres table + store) is necessarily impure and
// lives in internal/adapters/outbound/postgres +
// internal/app/intentclassifier.
package intent
