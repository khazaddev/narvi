// This file (boottiming.go) implements §33.3's control-plane half of the
// sandbox boot-timing relay ("ship the fact, record centrally"): the four
// sandbox_agent_*_duration_seconds histograms (sandbox_agent_boot_duration_
// seconds, sandbox_agent_hook_rerun_duration_seconds, sandbox_agent_git_
// fetch_duration_seconds, sandbox_agent_git_checkout_duration_seconds) used
// to be recorded INSIDE the ephemeral sandbox process (internal/
// sandboxagent/boot/telemetry.go, internal/sandboxagent/gitclone/
// telemetry.go, both deleted by this Step) -- §33.1/§33.2 record why that
// is genuinely harder than the control plane's own long-lived-process
// instruments, and why deriving these durations control-plane-side from
// other wire signals was tried and rejected for all four. Instead,
// sandbox-agent sends one new best-effort "boot_timing" sandbox-ws event
// (contracts/sandbox-ws/v1/events.schema.json) carrying the seconds it has
// ALREADY measured (the same time.Since bracket the deleted local
// histogram used to record) plus that instrument's own low-cardinality
// tags, and this file records it here instead -- same instrument names,
// same hand-tuned bucket slices (opsmetrics.go), so SLO 1's documented
// semantics and every existing alert/dashboard reference survive
// unchanged.

package sessionactor

import (
	"context"
	"encoding/json"
	"math"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/narvidev/narvi/contracts/gen/go/sandboxws"
)

// recordBootTiming is handleSandboxEvent's own "boot_timing" case
// (sandboxevent.go), called from INSIDE that function's own transact,
// AFTER appendRawEvent has already persisted the raw event verbatim (that
// file's own top comment: "persist ALWAYS, for every recognized event
// type"). A decode failure here therefore never needs to retroactively
// touch that persisted row -- mirroring completeProcessingTurn's own
// execution_complete decode-failure branch and handleSnapshotReadyEvent's
// own decode-failure handling (pushpr.go/sandboxevent.go): warn, record
// nothing, never return an error (which would roll back the WHOLE
// transact, including the already-persisted raw event).
//
// inserted is handleSandboxEvent's own appendRawEvent result for THIS
// event, threaded through for the IDENTICAL reason completeProcessingTurn's
// own inserted parameter is gated (pushpr.go): §6.1's reconnect resend
// buffers and re-sends every not-yet-acked best-effort event verbatim on
// reconnect, and boot_timing carries no ackId at all (events.schema.json's
// own BootTiming def is not one of the 6 critical acked types) -- recording
// unconditionally would double-count every buffered timing on a forced
// reconnect replay. This is turn_false_failure_total's own precedent
// (recordFalseFailureIfApplicable's own inserted gate, pushpr.go) copied,
// not re-derived, per §33.3 point 2.
//
// Best-effort throughout: telemetry never earns a place among §6.1's
// critical acked types, and its loss must never fail a boot or a turn
// (§33.3 point 1) -- every exit from this function is a silent or
// warn-and-return no-op, never an error propagated to handleSandboxEvent.
func (a *Actor) recordBootTiming(ctx context.Context, raw json.RawMessage, inserted bool) {
	if !inserted {
		// A wire-level redelivery of an already-processed boot_timing (see
		// this function's own doc comment above) -- already recorded once,
		// on the first delivery; recording it again here would double-count
		// the same data point.
		return
	}

	var evt sandboxws.BootTiming
	if err := json.Unmarshal(raw, &evt); err != nil {
		a.logger.Warn("sessionactor: boot_timing failed schema decode; persisted verbatim, recording nothing",
			"error", err)
		return
	}

	seconds := clampBootTimingSeconds(evt.Seconds)

	// §33.3 point 3: repo (when present) rides the event for per-session
	// debugging via the generic appendRawEvent persist above -- it is
	// deliberately NEVER added as a metric attribute here, on any of the
	// four cases below, since it is unbounded cardinality.
	switch evt.Metric {
	case sandboxws.BootTimingMetricBootDuration:
		if a.opsMetrics.bootDuration == nil {
			return
		}
		a.opsMetrics.bootDuration.Record(ctx, seconds, metric.WithAttributes(
			attribute.String("boot_mode", stringOrEmpty(evt.BootMode)),
			attribute.Bool("failed", boolOrFalse(evt.Failed)),
		))
	case sandboxws.BootTimingMetricHookRerunDuration:
		if a.opsMetrics.hookRerunDuration == nil {
			return
		}
		a.opsMetrics.hookRerunDuration.Record(ctx, seconds, metric.WithAttributes(
			attribute.String("hook", stringOrEmpty(evt.Hook)),
			attribute.Bool("failed", boolOrFalse(evt.Failed)),
			attribute.String("boot_mode", stringOrEmpty(evt.BootMode)),
			attribute.Bool("workspace_moved", boolOrFalse(evt.WorkspaceMoved)),
		))
	case sandboxws.BootTimingMetricGitFetchDuration:
		if a.opsMetrics.gitFetchDuration == nil {
			return
		}
		a.opsMetrics.gitFetchDuration.Record(ctx, seconds, metric.WithAttributes(
			attribute.Bool("degraded", boolOrFalse(evt.Degraded)),
		))
	case sandboxws.BootTimingMetricGitCheckoutDuration:
		if a.opsMetrics.gitCheckoutDuration == nil {
			return
		}
		a.opsMetrics.gitCheckoutDuration.Record(ctx, seconds, metric.WithAttributes(
			attribute.Bool("failed", boolOrFalse(evt.Failed)),
		))
	default:
		// Defensive only: events.schema.json's own "metric" enum already
		// constrains every schema-valid producer to one of the four cases
		// above, but this control plane trusts nothing off the wire.
		a.logger.Warn("sessionactor: boot_timing carries an unrecognized metric; ignoring",
			"metric", string(evt.Metric))
	}
}

// clampBootTimingSeconds clamps a relayed boot_timing "seconds" value at
// ingest: a non-finite (NaN/±Inf) or negative value can only arise from a
// malformed or adversarial sandbox-agent process (a genuine time.Since
// bracket is never negative or non-finite) -- clamped to 0 rather than
// recorded verbatim, so it can never poison a histogram's own bucket
// aggregation or produce a nonsensical negative-duration data point.
func clampBootTimingSeconds(seconds float64) float64 {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
		return 0
	}
	return seconds
}

// boolOrFalse reads one of sandboxws.BootTiming's own per-metric optional
// bool tag fields (each a named nil-able pointer type -- e.g.
// BootTimingFailed is `*bool` under the hood, see that generated type's
// own doc comment) -- false for a metric that does not carry this tag at
// all (e.g. Degraded on a boot_duration data point), exactly as documented
// per-field in events.schema.json's own BootTiming def. stringOrEmpty
// (dispatch.go) already provides this package's own *string counterpart,
// reused here rather than redeclared.
func boolOrFalse(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}
