package boot

import (
	"context"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meterName is this package's own OTel meter name, mirroring
// internal/app/imagebuild's "narvi/imagebuild" and internal/app/
// reconciler's "narvi/reconciler" precedent exactly (§5.3: one named
// meter per major subsystem).
const meterName = "narvi/sandboxagent-boot"

// hookRerunDurationHistogram is this Step's (§19.5(b)) own per-hook
// wall-clock duration metric, joining the boot-phase-duration OTel
// metrics §5.3 already lists ("boot phase durations") -- this is the
// concrete measurement §19.4's own "expected to be fast" claim needs, and
// the input §19.6's graduated setup-rerun ladder is gated on (Step 43
// starts only once this shows full reruns eroding warm-boot latency).
//
// Resolved LAZILY, on first use (sync.OnceValue), rather than eagerly at
// package-init time: this package has no per-process constructor object
// (RunHooks/RunBoot are free functions, unlike internal/app/imagebuild's
// NewBuilder, which constructs its own instrument once at Builder
// construction time) to anchor eager construction to, and cmd/sandbox-
// agent/main.go's own run() never wires an OTel SDK MeterProvider before
// otel.Meter would otherwise be called -- resolving eagerly at
// package-init would permanently bind this instrument to whatever
// (no-op) global MeterProvider happened to be registered at THAT moment,
// which is never the real one a test (or, if wired later, a real
// process) installs afterward. Lazy, first-use resolution instead reads
// otel.Meter(meterName) against whatever MeterProvider is globally
// registered at the moment the FIRST hook actually runs -- exactly what a
// test's own TestMain (registering a real SDK MeterProvider before
// invoking RunHooks/RunBoot for the first time) needs to observe anything
// at all.
var hookRerunDurationHistogram = sync.OnceValue(newHookRerunDurationHistogram)

func newHookRerunDurationHistogram() metric.Float64Histogram {
	h, err := otel.Meter(meterName).Float64Histogram(
		"sandbox_agent_hook_rerun_duration_seconds",
		metric.WithDescription("Wall-clock duration of one sandbox-agent boot hook run (setup.sh/start.sh), including a workspaceMoved-triggered non-fatal setup.sh rerun under repo_image (§19.4/§19.5)."),
		metric.WithUnit("s"),
	)
	if err != nil {
		// A Float64Histogram construction call can only ever fail for a
		// malformed static instrument name -- this one is a fixed,
		// well-formed literal, so this is not a runtime condition; logged
		// rather than silently swallowed on the off chance a future SDK
		// ever does reject it (mirroring internal/domain/imagebuild/
		// fingerprint.go's own writeField comment for the identical
		// "structurally cannot fail, but don't silently swallow if it
		// somehow does" reasoning).
		slog.Error("boot: construct sandbox_agent_hook_rerun_duration_seconds histogram failed", "error", err)
	}
	return h
}

// recordHookRerunDuration records one hook run's own wall-clock duration
// (seconds), tagged by repo/hook/outcome, from runRepoHooks's own existing
// timing bracket around each runHook call.
func recordHookRerunDuration(ctx context.Context, repoName string, hook string, seconds float64, failed bool) {
	h := hookRerunDurationHistogram()
	if h == nil {
		return
	}
	h.Record(ctx, seconds, metric.WithAttributes(
		attribute.String("repo", repoName),
		attribute.String("hook", hook),
		attribute.Bool("failed", failed),
	))
}
