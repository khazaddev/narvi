// This file (opsmetrics.go) is Step 77's ("ops: dashboards, alerts,
// runbooks", §5.3) own instrumentation fix: before this Step, §5.3's own
// metric list -- "spawn latency, boot phase durations, liveness gaps,
// watchdog activations (and how many were false alarms -- target: ~0),
// outbox lag, orphan GC count" -- was only PARTIALLY real. outbox lag
// (outboxworker's outbox_lag_seconds) and orphan GC count (reconciler's
// orphans_reaped) existed; boot phase durations existed sandbox-agent-side
// (internal/sandboxagent/boot/telemetry.go, internal/sandboxagent/gitclone/
// telemetry.go). Spawn latency, liveness gaps, and watchdog activations
// did not exist anywhere in this codebase -- internal/platform/otel.go's
// own top comment names PR-11/12, PR-13/14, PR-24, PR-35 as where they
// were EXPECTED to land, and none of those landings actually happened; a
// repo-wide grep for "watchdog" + a metric instrument, or "spawn" +
// "latency"/"duration" + a metric instrument, returns nothing production
// code ever registers.
//
// A fourth gap is specific to this Step's own plan row ("false failures"):
// nothing anywhere counts a turn the control plane terminalized Failed
// that the sandbox later, genuinely, reports as having succeeded. See
// recordFalseFailureIfApplicable's own doc comment (pushpr.go) for the
// full semantics this instrument captures and why.
//
// This file constructs the five new instruments those four gaps need,
// exactly once, mirroring registry.go's own contractDriftDetected
// precedent: built by newOpsMetrics from the SAME otel.Meter(meterName)
// NewRegistry already resolves for contractDriftDetected, then threaded
// through to every Actor this Registry hydrates via hydrate.go, exactly
// like every other Actor-shared field.
package sessionactor

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// watchdogKind names which of the three watchdog-style named timers (§2:
// inactivity, connecting_deadline, liveness_check) drove a
// transitionSandboxToSuspect call (timerfired.go) -- or "" for
// recordSpawnFailure's own DISTINCT permanent-provider-error path
// (dispatch.go), which enters Suspect too but is not a watchdog silence:
// the provider already returned a definite, classified error, there is no
// "how long has it been quiet" gap to measure, and grouping it into the
// SAME activation/false-alarm instruments as a genuine liveness watchdog
// would blur exactly the "how many were false alarms" ratio §5.3 asks
// for. transitionSandboxToSuspect only records watchdog_activation_total/
// sandbox_liveness_gap_seconds when watchdog is non-empty.
type watchdogKind string

const (
	watchdogInactivity         watchdogKind = "inactivity"
	watchdogConnectingDeadline watchdogKind = "connecting_deadline"
	watchdogLivenessCheck      watchdogKind = "liveness_check"
)

// opsMetrics bundles Step 77's five new OTel instruments, constructed
// exactly once per Registry (newOpsMetrics, called from NewRegistry) --
// mirroring internal/app/imagebuild's own buildTelemetry bundling
// precedent (telemetry.go) for a package that, like this one, already has
// a natural per-process constructor object (Registry/NewRegistry) to
// anchor eager construction to.
type opsMetrics struct {
	// spawnDuration is §5.3's "spawn latency": the wall-clock duration of
	// one real SandboxProvider.CreateSandbox/RestoreFromSnapshot/
	// ResumeSandbox call (dispatch.go's executeSpawn/executeRestore/
	// executeResume) -- the control-plane/adapter-side half of "boot"
	// internal/platform/otel.go's own top comment names ("Spawn latency in
	// particular belongs to the app/adapter layer (sessionactor or the
	// Modal provider), never to domain/sandbox"). Tagged by action
	// (spawn/restore/resume) and outcome (success/failure) -- both a
	// fixed, small enum, never session- or provider-id-scoped.
	spawnDuration metric.Float64Histogram

	// livenessGap is §5.3's "liveness gaps": how long a sandbox had shown
	// no sign of life by the moment a watchdog timer actually fired and
	// suspected it (EvaluateConnectingTimeout's own Elapsed /
	// EvaluateHeartbeatHealth's own Age, or the equivalent computed inline
	// for EvaluateInactivityTimeout, which returns neither) -- the
	// DURATION half of watchdog activity, complementing
	// watchdogActivation's own COUNT. Tagged by watchdog (the same three
	// values watchdogActivation uses).
	livenessGap metric.Float64Histogram

	// watchdogActivation is §5.3's "watchdog activations": incremented
	// every time transitionSandboxToSuspect (timerfired.go) runs for a
	// genuine watchdog-kind trigger (not recordSpawnFailure's permanent-
	// provider-error path -- see watchdogKind's own doc comment). Tagged
	// by watchdog.
	watchdogActivation metric.Int64Counter

	// watchdogFalseAlarm is §5.3's "(and how many were false alarms --
	// target: ~0)": incremented every time a Suspect sandbox recovers
	// DURING its own terminal_grace window (sandboxevent.go's own Step-24
	// Suspect-recovery-during-grace branch, §3.2's "any liveness signal
	// during grace returns to previous state") -- proof, after the fact,
	// that the watchdog that suspected it was WRONG: the sandbox was
	// alive the whole time. Tagged by recovered_from (the sandbox's own
	// pre_suspect_status -- ready/connecting/booting/spawning/
	// snapshotting, a fixed 5-value enum), NOT by watchdog: pre_suspect_
	// status alone cannot always distinguish inactivity from
	// liveness_check (both only ever fire from Ready, sandboxevent.go/
	// timerfired.go), and adding a persisted per-Suspect-episode watchdog
	// column just to disambiguate a false-alarm RATE this Step only needs
	// in aggregate (watchdogFalseAlarm / watchdogActivation, both summed
	// across every watchdog value) is more schema than this gap needs --
	// see this Step's own PR description for the full reasoning.
	watchdogFalseAlarm metric.Int64Counter

	// falseFailure is this Step's own plan-row-specific instrument ("false
	// failures"): incremented when a late, real, wire-level
	// execution_complete{outcome:completed} arrives for a session whose
	// own currently-derived state is already Failed with failure_reason =
	// timeout -- see recordFalseFailureIfApplicable's own doc comment
	// (pushpr.go) for the full semantics. Deliberately carries NO
	// attribute: the gate itself is already narrowed to exactly one
	// failure_reason value (timeout), so a label here would be constant,
	// not informative.
	falseFailure metric.Int64Counter
}

// newOpsMetrics constructs all five instruments against meter -- the SAME
// meter NewRegistry already resolves once via otel.Meter(meterName) for
// contractDriftDetected, passed in rather than re-resolved, so this
// package still calls otel.Meter exactly once per Registry construction
// (mirroring imagebuild.NewBuilder's own newBuildTelemetry(meter) call
// shape). Returns a wrapped error (never a partially-nil opsMetrics) on
// the first construction failure -- matching NewBuilder's own established
// error-returning convention for its own counters.
func newOpsMetrics(meter metric.Meter) (opsMetrics, error) {
	spawnDuration, err := meter.Float64Histogram(
		"sandbox_spawn_duration_seconds",
		metric.WithDescription("Wall-clock duration of one real ports.SandboxProvider.CreateSandbox/RestoreFromSnapshot/ResumeSandbox call (§5.3: spawn latency) -- the control-plane/adapter-side provider round trip, distinct from the sandbox-agent-side sandbox_agent_boot_duration_seconds (boot phase durations). Tagged by action (spawn/restore/resume) and outcome (success/failure)."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(spawnDurationBuckets...),
	)
	if err != nil {
		return opsMetrics{}, fmt.Errorf("sessionactor: construct sandbox_spawn_duration_seconds histogram: %w", err)
	}

	livenessGap, err := meter.Float64Histogram(
		"sandbox_liveness_gap_seconds",
		metric.WithDescription("How long a sandbox had shown no sign of life by the moment a watchdog timer (inactivity/connecting_deadline/liveness_check) suspected it (§5.3: liveness gaps) -- the duration half of watchdog_activation_total's own count. Tagged by watchdog."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(livenessGapBuckets...),
	)
	if err != nil {
		return opsMetrics{}, fmt.Errorf("sessionactor: construct sandbox_liveness_gap_seconds histogram: %w", err)
	}

	watchdogActivation, err := meter.Int64Counter(
		"watchdog_activation_total",
		metric.WithDescription("Count of every sandbox suspected (transitioned to Suspect) by a liveness/inactivity watchdog (§5.3: watchdog activations). Tagged by watchdog (inactivity/connecting_deadline/liveness_check); excludes recordSpawnFailure's own distinct permanent-provider-error path, which is not a watchdog silence."),
		metric.WithUnit("{activation}"),
	)
	if err != nil {
		return opsMetrics{}, fmt.Errorf("sessionactor: construct watchdog_activation_total counter: %w", err)
	}

	watchdogFalseAlarm, err := meter.Int64Counter(
		"watchdog_false_alarm_total",
		metric.WithDescription("Count of every Suspect sandbox that recovered during its own terminal_grace window (§3.2: 'any liveness signal during grace returns to previous state') -- proof the watchdog that suspected it was wrong (§5.3: watchdog activations '...and how many were false alarms -- target: ~0'). Tagged by recovered_from (the sandbox's own pre_suspect_status)."),
		metric.WithUnit("{alarm}"),
	)
	if err != nil {
		return opsMetrics{}, fmt.Errorf("sessionactor: construct watchdog_false_alarm_total counter: %w", err)
	}

	falseFailure, err := meter.Int64Counter(
		"turn_false_failure_total",
		metric.WithDescription("Count of every late, real execution_complete{outcome:completed} that arrived for a session already terminalized Failed with failure_reason=timeout (§9.3 scenario #4's own late-execution_complete class; IMPLEMENTATION_PLAN.md row 77: 'false failures') -- the control plane's own turn_deadline inference, contradicted after the fact by the sandbox genuinely finishing."),
		metric.WithUnit("{turn}"),
	)
	if err != nil {
		return opsMetrics{}, fmt.Errorf("sessionactor: construct turn_false_failure_total counter: %w", err)
	}

	return opsMetrics{
		spawnDuration:      spawnDuration,
		livenessGap:        livenessGap,
		watchdogActivation: watchdogActivation,
		watchdogFalseAlarm: watchdogFalseAlarm,
		falseFailure:       falseFailure,
	}, nil
}

// spawnDurationBuckets mirrors internal/app/imagebuild's own
// buildDurationBuckets Finding-4 reasoning (concentrate resolution where
// duration is actually expected to live, not the OTel SDK's own
// millisecond-oriented default boundaries), scaled to a real provider
// spawn/restore/resume call's own expected range: sub-second to a few
// seconds typically, up to platform.Timeouts.ProviderHTTPClientTimeout's
// own ceiling (5 minutes) for a genuinely slow cold start.
var spawnDurationBuckets = []float64{
	0.1, 0.25, 0.5, 1, 2, 3, 5, 7.5, 10, 15, 20, 30, 45, 60, 90, 120, 180, 300,
}

// livenessGapBuckets covers the full range EITHER budget in
// domain/sandbox.LivenessConfig can produce: SteadyHeartbeatBudget
// (default 90s) up through FirstConnectBudget (default 240s), plus
// headroom for a config override or a genuinely pathological gap.
var livenessGapBuckets = []float64{
	10, 20, 30, 45, 60, 90, 120, 180, 240, 300, 450, 600, 900,
}

// recordSpawnDuration records one real provider spawn/restore/resume
// call's own wall-clock duration -- executeSpawn/executeRestore/
// executeResume's (dispatch.go) one shared call site.
func (a *Actor) recordSpawnDuration(ctx context.Context, action string, seconds float64, failed bool) {
	if a.opsMetrics.spawnDuration == nil {
		// Defensive only: every real Actor is hydrated with a non-nil
		// opsMetrics (hydrate.go), but some tests construct an Actor
		// directly via a struct literal (mirroring commander/provider's
		// own documented "may be nil in tests" convention) without one.
		return
	}
	outcome := "success"
	if failed {
		outcome = "failure"
	}
	a.opsMetrics.spawnDuration.Record(ctx, seconds, metric.WithAttributes(
		attribute.String("action", action),
		attribute.String("outcome", outcome),
	))
}

// recordWatchdogActivation increments watchdog_activation_total and
// records gap into sandbox_liveness_gap_seconds, both tagged by watchdog
// -- transitionSandboxToSuspect's (timerfired.go) one shared call site,
// skipped entirely when watchdog == "" (recordSpawnFailure's own
// non-watchdog path -- see watchdogKind's own doc comment).
func (a *Actor) recordWatchdogActivation(ctx context.Context, watchdog watchdogKind, gap float64) {
	if watchdog == "" || a.opsMetrics.watchdogActivation == nil {
		return
	}
	attrs := metric.WithAttributes(attribute.String("watchdog", string(watchdog)))
	a.opsMetrics.watchdogActivation.Add(ctx, 1, attrs)
	if a.opsMetrics.livenessGap != nil {
		a.opsMetrics.livenessGap.Record(ctx, gap, attrs)
	}
}

// recordWatchdogFalseAlarm increments watchdog_false_alarm_total, tagged
// by recoveredFrom (the sandbox's own pre_suspect_status) --
// handleSandboxEvent's (sandboxevent.go) own Suspect-recovery-during-grace
// success branch is this method's one call site.
func (a *Actor) recordWatchdogFalseAlarm(ctx context.Context, recoveredFrom string) {
	if a.opsMetrics.watchdogFalseAlarm == nil {
		return
	}
	a.opsMetrics.watchdogFalseAlarm.Add(ctx, 1, metric.WithAttributes(
		attribute.String("recovered_from", recoveredFrom),
	))
}

// recordFalseFailure increments turn_false_failure_total --
// recordFalseFailureIfApplicable's (pushpr.go) one call site, once it has
// already confirmed the session's own currently-derived state is Failed
// with failure_reason=timeout.
func (a *Actor) recordFalseFailure(ctx context.Context) {
	if a.opsMetrics.falseFailure == nil {
		return
	}
	a.opsMetrics.falseFailure.Add(ctx, 1)
}
