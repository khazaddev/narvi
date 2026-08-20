// Package loopguard is §25.4's own ("domain/workflow + loopguard +
// schema", §25.5) generic, pure circuit breaker for bounded retry loops:
// Evaluate(State{AttemptCount}, Config{MaxAttempts}) Decision
// {ShouldProceed, ShouldEscalate}. No I/O, no time.Now(), no randomness
// (CLAUDE.md, §11) -- and, deliberately, NO TIME WINDOW, unlike
// internal/domain/sandbox.EvaluateCircuitBreaker's own sliding-window
// breaker: §25.5 is explicit that this is a monotonic per-run bound. An
// audit-fix loop that has already burned its attempts must NEVER
// silently reset after a delay -- a sandbox spawn failure is transient
// by nature (the window exists so old failures age out), but a fix
// attempt that did not satisfy the audit is not made retriable again by
// the mere passage of time.
//
// AttemptCount is derived by the CALLER via COUNT(*) on
// workflow_step_runs (each re-execution of a step is its own row,
// migrations/000057_workflows.up.sql) -- never a dedicated counter
// column anywhere (§25.5's own "derive it from the rows that already
// exist" discipline, the same one review_verdicts' DISTINCT ON reduction
// already applies). This package just renders the verdict on the count
// it is handed.
//
// Consulted by the engine (§25.9) only when a needs_fix edge is
// about to RE-fire -- never inside workflow.NextStep itself, and never
// for human-revision loops, which are exempt (§25.9, mirroring §24.6's
// own manual-retrigger exemption). Dark: no caller exists
// yet.
package loopguard
