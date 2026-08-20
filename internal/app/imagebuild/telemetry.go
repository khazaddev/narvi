package imagebuild

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// This file is the build-duration/failure-rate instrumentation
// (§19.9's closing paragraph): "Build-duration and failure-rate
// instrumentation is still worth having -- to size the win and catch a
// regression afterwards, the same role §19.5's telemetry now plays for
// (a)/(b)." §19.9 is explicit this does NOT gate shipping the build-time
// dependency cache (c) -- it ships ungated, alongside it, as the
// after-the-fact confirmation and regression signal, mirroring the
// "measure AFTER shipping, not as a precondition to ship" precedent
// internal/sandboxagent/boot/telemetry.go's own hookRerunDurationHistogram
// set for (a)/(b) -- that file (and its (a)/(b) histogram) is deleted as
// of §33.3, its recording moved control-plane-side, but the reasoning it
// established still applies here unchanged.
//
// Deliberately general-purpose, not cache-specific: neither metric carries
// a "cache used" attribute, because BuildImage's own signature -- by
// design, ports.CacheMount's own doc comment -- never reports whether a
// cache mount was actually honored (that is exactly the pure-accelerator
// property: a caller must never be able to tell). What IS attributable,
// and load-bearing for "size the win": every attempt's own `phase` (a
// brand-new claim, PumpOnce's own attempt, vs. an in-place refresh,
// RefreshOnce's own attemptRefresh, §19.2) and `outcome`
// (success/failure). Comparing this histogram/counter's own values from
// BEFORE this Step's cache-mount rollout against AFTER is what actually
// answers "did build duration and failure rate improve" -- a
// cache-specific label on an instrument that ships simultaneously with the
// feature it would be labeling could never itself provide a before/after
// baseline.

// buildDurationBuckets concentrates resolution across the range a real
// BuildImage call is expected to occupy: from a fast, already-warm
// (cache-hit) build, through an ordinary cold build, up to
// platform.Timeouts.ProviderHTTPClientTimeout's own ceiling (5 minutes) --
// and beyond it, since Provider.BuildImage's own pure-accelerator fallback
// (internal/adapters/outbound/modal's cache-mount-trouble retry) can issue
// TWO sequential HTTP calls, each individually bounded by that same
// client timeout, for a worst-case single BuildImage call approaching
// twice that ceiling. Mirrors the Finding-4 bucket-boundary reasoning
// internal/sandboxagent/boot/telemetry.go's own bootDurationBuckets used
// to apply (concentrate resolution where duration is actually expected to
// live, not the OTel SDK's own millisecond-oriented default boundaries) --
// that file is deleted as of §33.3 (its four histograms' recording moved
// control-plane-side), but the bucket-sizing reasoning itself is
// independent of where the recording happens, so it is extended further
// out here to cover this package's own two-call worst case.
var buildDurationBuckets = []float64{
	1, 2, 3, 5, 7.5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 300, 450, 600, 900, 1200,
}

// buildTelemetry bundles this Step's own two new instruments, constructed
// exactly once per Builder (NewBuilder) -- mirroring failureStreak/
// permanentlyFailed/refreshClaimReclaimed's own established "construct
// once at Builder-construction time, not per-tick, not per-row" precedent
// (builder.go) exactly, rather than the sync.OnceValue package-level
// singleton internal/sandboxagent/boot and internal/sandboxagent/gitclone
// use -- this package already has a natural per-process constructor object
// (Builder/NewBuilder) to anchor eager construction to, which those two
// packages do not.
type buildTelemetry struct {
	duration metric.Float64Histogram
	attempts metric.Int64Counter
}

// newBuildTelemetry constructs both instruments against meter, returning a
// wrapped error (never a panic, never a silently-nil buildTelemetry) on
// construction failure -- matching NewBuilder's own existing
// error-returning convention for its three pre-existing counters.
func newBuildTelemetry(meter metric.Meter) (buildTelemetry, error) {
	duration, err := meter.Float64Histogram(
		"image_build_duration_seconds",
		metric.WithDescription("Wall-clock duration of one real ports.SandboxProvider.BuildImage call, tagged by phase (claim/refresh) and outcome (success/failure) -- the build-duration half of §19.9's own closing-paragraph instrumentation for the build-time dependency cache: sizes the win and catches a regression, never a shipping gate."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(buildDurationBuckets...),
	)
	if err != nil {
		return buildTelemetry{}, fmt.Errorf("imagebuild: construct image_build_duration_seconds histogram: %w", err)
	}

	attempts, err := meter.Int64Counter(
		"image_build_attempt_total",
		metric.WithDescription("Count of every real ports.SandboxProvider.BuildImage attempt this package makes, tagged by phase (claim/refresh) and outcome (success/failure) -- the failure-RATE half of §19.9's own closing-paragraph instrumentation: failures/total over a window, computed from this counter's own outcome label, not merely image_build_failure_streak's streak-threshold trip."),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return buildTelemetry{}, fmt.Errorf("imagebuild: construct image_build_attempt_total counter: %w", err)
	}

	return buildTelemetry{duration: duration, attempts: attempts}, nil
}

// record is buildTelemetry's own single call site shape: one BuildImage
// attempt, its phase, its wall-clock duration in seconds, and whether it
// failed -- called from attempt (phase="claim") and attemptRefresh
// (phase="refresh"), each bracketing their own real
// b.provider.BuildImage call.
func (t buildTelemetry) record(ctx context.Context, phase string, seconds float64, failed bool) {
	outcome := "success"
	if failed {
		outcome = "failure"
	}
	attrs := metric.WithAttributes(
		attribute.String("phase", phase),
		attribute.String("outcome", outcome),
	)
	t.duration.Record(ctx, seconds, attrs)
	t.attempts.Add(ctx, 1, attrs)
}
