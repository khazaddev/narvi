// This file (telemetry.go) is the audit-remediation batch B7 fix for
// Finding 3 (HIGH, cmd/sandbox-agent, but the underlying gap lives here):
// before this fix, internal/sandboxagent/gitclone had ZERO timing
// instrumentation -- neither the new §19.3 boot-time fetch step (up to
// GitFetchStepTimeout's own 90s ceiling) nor SyncAll's own checkout phase
// were measured anywhere, even though both eat directly into "warm-boot
// latency" every bit as much as internal/sandboxagent/boot's own
// sandbox_agent_hook_rerun_duration_seconds does. §19.6's gating question
// ("is a hook rerun materially eroding the warm-boot latency win") is a
// claim about a RATIO -- rerun overhead vs. total warm-boot time -- and an
// untimed fetch/checkout step left that total genuinely unmeasurable from
// metrics alone, forcing an operator back to eyeballing timestamp deltas
// across separate boot-fingerprint/git_sync log lines by hand.

package gitclone

import (
	"context"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meterName is this package's own OTel meter name, mirroring
// internal/sandboxagent/boot's own "narvi/sandboxagent-boot" and
// internal/app/imagebuild's "narvi/imagebuild" precedent exactly (§5.3: one
// named meter per major subsystem).
const meterName = "narvi/sandboxagent-gitclone"

// gitFetchDurationBuckets follows internal/sandboxagent/boot's own
// hookRerunDurationBuckets precedent (Finding 4's own reasoning, applied
// here too): the OTel SDK's own default boundaries would lump every
// realistic warm repo_image fetch (commonly sub-second to low-single-digit
// seconds) into one wide bucket, unable to resolve gradual erosion. These
// concentrate resolution in that expected range while still reaching
// GitFetchStepTimeout's own 90s ceiling at the top end.
var gitFetchDurationBuckets = []float64{
	0.1, 0.25, 0.5, 1, 2, 3, 5, 7.5, 10, 15, 20, 30, 45, 60, 90, 120,
}

var gitFetchDurationHistogram = sync.OnceValue(newGitFetchDurationHistogram)

func newGitFetchDurationHistogram() metric.Float64Histogram {
	h, err := otel.Meter(meterName).Float64Histogram(
		"sandbox_agent_git_fetch_duration_seconds",
		metric.WithDescription("Wall-clock duration of SyncAll's own §19.3 boot-time fetch step (the default-branch and target-branch git fetch calls together) for one repo."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(gitFetchDurationBuckets...),
	)
	if err != nil {
		// A Float64Histogram construction call can only ever fail for a
		// malformed static instrument name -- this one is a fixed,
		// well-formed literal, so this is not a runtime condition; logged
		// rather than silently swallowed, mirroring internal/sandboxagent/
		// boot's own newHookRerunDurationHistogram precedent exactly.
		slog.Error("gitclone: construct sandbox_agent_git_fetch_duration_seconds histogram failed", "error", err)
	}
	return h
}

// recordGitFetchDuration records one repo's own §19.3 fetch-step wall-clock
// duration (seconds), tagged by repo and whether the fetch degraded
// (fetchSucceeded == false at the call site in syncOne -- see that
// function's own genuineDegrade reasoning for what "degraded" does and does
// not imply about the underlying cause).
func recordGitFetchDuration(ctx context.Context, repoName string, seconds float64, degraded bool) {
	h := gitFetchDurationHistogram()
	if h == nil {
		return
	}
	h.Record(ctx, seconds, metric.WithAttributes(
		attribute.String("repo", repoName),
		attribute.Bool("degraded", degraded),
	))
}

// gitCheckoutDurationBuckets mirrors gitFetchDurationBuckets's own reasoning,
// shifted slightly lower: checkoutBranch is a local-only git operation (no
// network round trip, unlike the fetch step above), so it is typically
// faster still.
var gitCheckoutDurationBuckets = []float64{
	0.05, 0.1, 0.25, 0.5, 1, 2, 3, 5, 7.5, 10, 15, 20, 30, 60,
}

var gitCheckoutDurationHistogram = sync.OnceValue(newGitCheckoutDurationHistogram)

func newGitCheckoutDurationHistogram() metric.Float64Histogram {
	h, err := otel.Meter(meterName).Float64Histogram(
		"sandbox_agent_git_checkout_duration_seconds",
		metric.WithDescription("Wall-clock duration of SyncAll's own checkoutBranch call (checkout onto the session branch, creating it from a resolved base if absent) for one repo -- excludes the fetch step and any stash push/pop, each its own separately-timed phase."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(gitCheckoutDurationBuckets...),
	)
	if err != nil {
		slog.Error("gitclone: construct sandbox_agent_git_checkout_duration_seconds histogram failed", "error", err)
	}
	return h
}

// recordGitCheckoutDuration records one repo's own checkout wall-clock
// duration (seconds), tagged by repo and whether checkoutBranch itself
// failed.
func recordGitCheckoutDuration(ctx context.Context, repoName string, seconds float64, failed bool) {
	h := gitCheckoutDurationHistogram()
	if h == nil {
		return
	}
	h.Record(ctx, seconds, metric.WithAttributes(
		attribute.String("repo", repoName),
		attribute.Bool("failed", failed),
	))
}
