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

// hookRerunDurationBuckets (audit-remediation batch B7, Finding 4)
// deliberately replaces the OTel SDK's own default histogram boundaries
// (0, 5, 10, 25, 50, 75, 100, 250, 500, 750, 1000, 2500, 5000, ...), which --
// since this instrument's own unit is seconds, not the SDK's usual implicit
// milliseconds -- lump EVERY reasonably-healthy rerun (sub-5-second, §19.4's
// own explicitly expected outcome for a warm-cache setup.sh) into a single
// [0, 5) bucket. §19.6's whole premise is catching erosion BEFORE it becomes
// obviously material -- e.g. p50 creeping from 1s to 3s to 4.5s over several
// weeks -- and a single wide [0, 5) bucket cannot resolve that trend at all:
// histogram_quantile interpolates linearly across whichever bucket a
// percentile falls into, so 1s and 4.5s would be indistinguishable. These
// boundaries instead concentrate resolution in the sub-5s/sub-30s range
// where a warm rerun is expected to live, while still keeping enough
// coarse, wide upper buckets to usefully bucket a genuinely cold
// BootModeBuild/BootModeFresh setup.sh run (expected to take minutes) --
// not given an explicit figure in the plan; chosen by hand for this
// specific "mostly fast, occasionally slow" shape.
var hookRerunDurationBuckets = []float64{
	0.1, 0.25, 0.5, 0.75, 1, 1.5, 2, 3, 4, 5, 7.5, 10, 15, 20, 30, 60, 120, 300, 600,
}

func newHookRerunDurationHistogram() metric.Float64Histogram {
	h, err := otel.Meter(meterName).Float64Histogram(
		"sandbox_agent_hook_rerun_duration_seconds",
		metric.WithDescription("Wall-clock duration of one sandbox-agent boot hook run (setup.sh/start.sh), including a workspaceMoved-triggered non-fatal setup.sh rerun under repo_image (§19.4/§19.5)."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(hookRerunDurationBuckets...),
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
// (seconds), tagged by repo/hook/outcome/bootMode/workspaceMoved, from
// runRepoHooks's own existing timing bracket around each runHook call.
//
// bootMode and workspaceMoved (both already in scope at the one call site,
// runRepoHooks) are load-bearing, not decorative: without them, a cold
// BootModeBuild/BootModeFresh setup.sh run (expected to take minutes,
// against an empty cache) lands in the exact same bucket set as a
// BootModeRepoImage+workspaceMoved rerun (expected to be fast, against an
// already-warm cache) -- the one distinction §19.6/Step 43's own adoption
// trigger ("full reruns eroding warm-boot latency") needs to query on.
func recordHookRerunDuration(ctx context.Context, repoName string, hook string, bootMode string, workspaceMoved bool, seconds float64, failed bool) {
	h := hookRerunDurationHistogram()
	if h == nil {
		return
	}
	h.Record(ctx, seconds, metric.WithAttributes(
		attribute.String("repo", repoName),
		attribute.String("hook", hook),
		attribute.Bool("failed", failed),
		attribute.String("boot_mode", bootMode),
		attribute.Bool("workspace_moved", workspaceMoved),
	))
}

// bootDurationHistogram is the audit-remediation batch B7 fix for Finding
// 3 (HIGH): sandbox_agent_hook_rerun_duration_seconds (above) measures only
// one hook's own wall time in isolation -- nothing anywhere in this
// codebase measured the TOTAL warm-boot-to-ready latency that rerun
// overhead actually eats into, so §19.6's gating question ("is a full
// setup.sh rerun MATERIALLY eroding the warm-boot latency win") could only
// ever be guessed at: it is a claim about a RATIO (rerun overhead vs. total
// boot time), not an absolute number, and this histogram is the missing
// denominator. Recorded once per boot, from cmd/sandbox-agent/main.go's own
// runBootSequence call site (RecordBootDuration, below) -- covering repo
// prepare (clone/sync, including the §19.3 fetch step and checkout, each
// separately timed too -- see internal/sandboxagent/gitclone's own
// telemetry.go) THROUGH RunBoot's own hook/service startup, i.e. the same
// span run()'s "boot sequence complete" log line already brackets.
//
// Resolved lazily via sync.OnceValue, for the exact same reason
// hookRerunDurationHistogram above is: no per-process constructor object
// exists in this package to anchor eager construction to, and this
// package's own MeterProvider is wired later, by cmd/sandbox-agent/main.go.
var bootDurationHistogram = sync.OnceValue(newBootDurationHistogram)

// bootDurationBuckets mirrors hookRerunDurationBuckets's own Finding-4
// reasoning (concentrate resolution where boot time is actually expected to
// live) but shifted to a coarser, longer-duration shape: a total boot
// sequence -- even a warm one -- includes network-bound git operations
// (fetch/checkout) and service readiness polling on top of the hook itself,
// so it is never sub-second the way a single warm hook rerun can be; the
// low end here starts at 1s rather than 0.1s, and the upper end reaches
// 900s (15min) to still usefully bucket a genuinely cold BootModeBuild
// image-build boot (setup.sh installing dependencies from scratch, §5.4's
// own up-to-2h ProviderHardCap notwithstanding -- a boot itself is expected
// to be a small fraction of that ceiling, not comparable to it).
var bootDurationBuckets = []float64{
	1, 2, 3, 5, 7.5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 300, 600, 900,
}

func newBootDurationHistogram() metric.Float64Histogram {
	h, err := otel.Meter(meterName).Float64Histogram(
		"sandbox_agent_boot_duration_seconds",
		metric.WithDescription("Wall-clock duration of one sandbox-agent boot-to-ready sequence (repo clone/sync through RunBoot's own hook/service startup) -- the total warm-boot latency §19.6's gating question needs as its own denominator when judging whether a hook rerun is materially eroding it."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(bootDurationBuckets...),
	)
	if err != nil {
		// See newHookRerunDurationHistogram's own identical comment: this
		// can only fail for a malformed static instrument name, which this
		// fixed literal is not -- logged defensively, not silently
		// swallowed, on the off chance a future SDK version ever does
		// reject it.
		slog.Error("boot: construct sandbox_agent_boot_duration_seconds histogram failed", "error", err)
	}
	return h
}

// RecordBootDuration records one boot-to-ready sequence's own total
// wall-clock duration (seconds), tagged by boot_mode and whether it failed.
// Exported (unlike recordHookRerunDuration's own package-private
// counterpart) because its one caller is cmd/sandbox-agent/main.go's own
// runBootSequence wrapper, outside this package, not this package itself.
func RecordBootDuration(ctx context.Context, bootMode string, seconds float64, failed bool) {
	h := bootDurationHistogram()
	if h == nil {
		return
	}
	h.Record(ctx, seconds, metric.WithAttributes(
		attribute.String("boot_mode", bootMode),
		attribute.Bool("failed", failed),
	))
}
