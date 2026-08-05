package loopguard

// DefaultMaxAttempts is the default retry bound a caller puts in
// Config.MaxAttempts absent any more specific configuration -- 3, the
// SAME established threshold this codebase already uses three times for
// "how many strikes before giving up" (sandbox.CircuitBreakerThreshold,
// automation.AutoPauseThreshold, imagebuild.ImageBuildStreakThreshold).
// A plain int, kept as a Config field rather than hardcoded into
// Evaluate, so the function stays a pure, fully parameterized decision
// like every other one under /internal/domain.
const DefaultMaxAttempts = 3

// State is the loop's observed progress. AttemptCount is how many
// attempts have ALREADY run -- for the workflow engine, COUNT(*) of the
// guarded step's own workflow_step_runs rows within this run (§25.5;
// see doc.go for why no dedicated counter column exists). Never
// negative in practice (a COUNT(*) cannot be); a defensively-negative
// value simply reads as "no attempts yet".
type State struct {
	AttemptCount int
}

// Config bounds the loop. MaxAttempts is the most attempts the loop may
// consume before escalating (normally DefaultMaxAttempts). A
// MaxAttempts <= 0 -- a misconfiguration, since it permits nothing --
// fails conservative: Evaluate escalates immediately rather than ever
// reading "no limit" into it (an unbounded auto-fix loop is exactly the
// failure mode this package exists to make impossible).
type Config struct {
	MaxAttempts int
}

// Decision is the breaker's verdict. Exactly one of the two fields is
// true -- both are explicit (never "escalate = !proceed" derived by the
// caller) because each names a REAL, distinct consequence the engine
// applies (§25.9): proceed re-fires the edge and creates the next
// attempt's own workflow_step_runs row; escalate flips
// WorkflowRun.Status to needs_review, posts ONE notice (never repeated,
// like §24.6), and stops. Mirrors sandbox.CircuitBreakerDecision's own
// multi-field-verdict shape, minus its window/wait machinery (see
// doc.go for why there is deliberately no time axis here).
type Decision struct {
	ShouldProceed  bool
	ShouldEscalate bool
}

// Evaluate decides whether the guarded loop may consume one more
// attempt: proceed while fewer than cfg.MaxAttempts attempts have
// already run, escalate from then on. Monotonic in state.AttemptCount --
// with no time input at all, there is structurally no way for the
// verdict to relax as time passes (§25.5's "no time window", see
// doc.go).
func Evaluate(state State, cfg Config) Decision {
	if state.AttemptCount < cfg.MaxAttempts {
		return Decision{ShouldProceed: true}
	}
	return Decision{ShouldEscalate: true}
}
