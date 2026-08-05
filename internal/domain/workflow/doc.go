// Package workflow holds Step 54's own ("domain/workflow + loopguard +
// schema", §25.4) pure domain model for the configurable per-lane
// workflow engine: the Lane vocabulary and its LaneFor mapping over the
// intent classifier's own existing (target, mode) vocabulary, the
// Definition/StepDefinition/Edge value types (§25.4's
// WorkflowDefinition/StepDefinition/Edge -- the package-qualified Go
// name workflow.Definition drops the spec prose's Workflow prefix
// purely to satisfy revive's stutter rule, exactly what the wire DTO
// keeps as WorkflowDefinition in restdtos' flat namespace) mirroring
// the workflow_* tables (migrations/000057_workflows.up.sql), the
// StepOutcomeStatus closed enum, and NextStep -- the ONE pure decision
// function the eventual execution engine (Step 55, §25.6) consults to
// learn what follows a finished step. No I/O, no time.Now(), no
// randomness (CLAUDE.md, §11) -- this package is data plus decision
// functions, nothing more; the impure engine
// (internal/app/workflowengine, Step 55) and the HITL gate (Step 56,
// §25.9) import it, never the reverse.
//
// Everything in this package is DARK as of Step 54: no dispatch wiring,
// no behavior change, nothing consumes these types at runtime yet (the
// Step's own row: "Dark -- no dispatch wiring yet, no behavior change").
//
// # Lane and LaneFor (§25.4)
//
// Lane is a closed 3-value enum (review/request/plan) matching the
// workflow_lane Postgres enum exactly. LaneFor maps the classifier's own
// existing vocabulary (internal/domain/intent/rubric.go) onto it -- "not
// a new vocabulary invented alongside it" (§25.4) -- including the
// release-vs-feature category Step 50 added, and §25.13's fail-open
// requirement for anything unrecognized. See LaneFor's own doc comment
// for the full mapping and the documented judgment calls.
//
// # StepOutcomeStatus is a distinct axis from review.Shippable (§25.4, §25.8)
//
// StepOutcomeStatus (ok/needs_fix/blocked) is the ONLY vocabulary an
// Edge may condition on. It is deliberately a separate axis from
// internal/domain/review's own 3-value Shippable enum (§21.2), and
// Shippable is never routed through it: the built-in review workflow is
// a single step whose Shippable verdict is consumed AFTER the step
// completes by the existing auto-approval machinery, exactly as today
// (§25.8) -- an edge never branches on it.
//
// # Default edge semantics are fail-conservative (§25.4)
//
// With no explicit edge for (step, outcome): StepOutcomeOK advances to
// the next step in Order (or completes the run at the last step);
// StepOutcomeNeedsFix/StepOutcomeBlocked escalate. A retry loop must be
// wired explicitly (e.g. §25.9's Edge{audit, needs_fix, fix} +
// Edge{fix, ok, audit}), never implied. An explicit edge always wins
// over the default -- that is what "default" means -- so an explicit
// StepOutcomeOK edge may legally point backward (the fix -> audit loop)
// or at the same step.
//
// NextStep never consults the circuit breaker: internal/domain/loopguard
// (§25.5) is a separate pure package the ENGINE consults only when a
// needs_fix edge is about to re-fire (§25.9) -- keeping NextStep the
// same single-authority, table-driven shape as turn.Transition/
// plan.Transition/sandbox.Transition, just with the table carried as
// per-definition DATA (Steps + Edges) instead of a package-level map,
// since every definition is its own machine.
//
// # The is_built_in immutability invariant (§25.4) -- recorded here, enforced by Steps 55-56
//
// The three built-in workflows are ROWS, seeded is_built_in = true
// directly in migration 000057 (§25.4: the "duplicate and customize"
// requirement and the canvas editor both need the default to exist in
// exactly the same shape as a custom workflow). PUT/DELETE on an
// is_built_in = true row is refused UNCONDITIONALLY -- even for an
// admin. This is a STRUCTURAL invariant, not an RBAC rule (§25.11), and
// deliberately NOT a per-role matrix row in internal/domain/authz. No
// REST surface for workflow definitions ships in this Step (dark), so
// the refusal's enforcement point is the store/handler layer Steps 55-56
// add -- recorded here and in migration 000057's own header comment so
// those Steps implement it rather than rediscover it. See the migration
// header for why a DB-level trigger was considered and deliberately NOT
// added.
//
// # What Steps 55-56 consume (context only -- none of it lives here)
//
// Step 55 (engine, §25.6-§25.8): resolves the (lane, repo) binding, loads
// the bound definition, dispatches each step as an ordinary turn on the
// same session (child_session only for real isolation; fresh continuity
// is a new OpenCode conversation on the SAME session, never a child
// session), and calls NextStep on every posted step outcome. Step 56
// (HITL + breaker, §25.9): the approve/reject/revise verdicts on
// HITLBefore/HITLAfter gates, the decide endpoint, and loopguard
// consultation on re-firing needs_fix edges.
package workflow
