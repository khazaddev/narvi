package imagebuild

import "time"

// ImageBuildStreakThreshold is the number of CONSECUTIVE failed build
// attempts for the SAME fingerprint that crosses the "streak" alert (log
// warning + the image_build_failure_streak OTel counter, app/imagebuild).
// Mirrors sandbox.CircuitBreakerThreshold's own "3" (§3.2: "3 permanent
// spawn failures") and §3.5's own "Auto-pause after 3 consecutive failed
// [automation] invocations" -- this codebase already treats 3 consecutive
// failures as its own established "this is not a blip" signal in two other
// places, so image builds reuse the SAME number rather than inventing a
// fourth threshold value with no particular justification of its own.
const ImageBuildStreakThreshold = 3

// BackoffConfig configures EvaluateBackoff's exponential schedule. Both
// fields are populated by the caller from platform.Timeouts (this package
// imports no duration literals of its own -- see doc.go).
type BackoffConfig struct {
	// BaseDelay is the delay scheduled after the FIRST failed attempt
	// (attemptCount == 1). Populated from platform.Timeouts.
	// ImageBuildBackoffBase.
	BaseDelay time.Duration
	// MaxDelay caps the exponential growth -- populated from platform.
	// Timeouts.ImageBuildBackoffMax. Once reached, every subsequent
	// consecutive failure schedules the SAME MaxDelay again (a plateau,
	// not a further increase) -- this is what makes a persistently-broken
	// build's own eventual retry cadence bounded and predictable, rather
	// than growing without limit.
	MaxDelay time.Duration
}

// BackoffDecision is EvaluateBackoff's verdict for one failed attempt.
type BackoffDecision struct {
	// NextRetryAt is when this fingerprint becomes eligible to (re)attempt
	// again (app/imagebuild's own ListDueImageBuilds poll condition).
	NextRetryAt time.Time
	// StreakAlert reports whether attemptCount has reached
	// ImageBuildStreakThreshold -- true on every attempt from the
	// threshold onward (not just the first crossing), since each is a
	// real, independent occurrence of "this fingerprint is still failing"
	// worth its own counter increment (app/imagebuild increments
	// image_build_failure_streak once per StreakAlert-true outcome, never
	// per tick regardless of outcome).
	StreakAlert bool
}

// EvaluateBackoff computes the next retry time for a build attempt that
// just failed, and whether this failure crossed the streak-alert
// threshold. attemptCount is the TOTAL number of attempts made against this
// fingerprint so far, INCLUDING the one that just failed (the caller
// increments it at claim time, before the real BuildImage call -- see
// app/imagebuild's own ClaimImageBuild). attemptCount < 1 is treated as 1
// (defensive: there is no such thing as a "0th" failed attempt).
//
// §3.5's one hard, explicit requirement: "retry with exponential backoff
// (not fixed 30 min)" -- the delay MUST grow with repeated failures, never
// stay constant. Doubling per additional failure (BaseDelay, 2×BaseDelay,
// 4×BaseDelay, ...), capped at MaxDelay, is a standard, easily-verified
// schedule satisfying that: with the shipped defaults (BaseDelay=1min,
// MaxDelay=30min, platform/timeouts.go), a fingerprint that keeps failing
// is retried at 1m, 2m, 4m, 8m, 16m, then every 30m thereafter -- far more
// responsive than a fixed 30min from the very first failure, while still
// converging on that SAME eventual cadence once a build is confirmed
// persistently broken, rather than hammering the provider indefinitely
// faster and faster.
func EvaluateBackoff(attemptCount int, cfg BackoffConfig, now time.Time) BackoffDecision {
	if attemptCount < 1 {
		attemptCount = 1
	}

	delay := cfg.BaseDelay
	for i := 1; i < attemptCount; i++ {
		if delay >= cfg.MaxDelay {
			delay = cfg.MaxDelay
			break
		}
		delay *= 2
	}
	if delay > cfg.MaxDelay {
		delay = cfg.MaxDelay
	}

	return BackoffDecision{
		NextRetryAt: now.Add(delay),
		StreakAlert: attemptCount >= ImageBuildStreakThreshold,
	}
}
