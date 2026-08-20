// This file (otel.go) bootstraps the OpenTelemetry SDK named in the
// stack-choices line (§1) and required "day one, not later" by §5.3: a
// TracerProvider and a MeterProvider, registered globally, each wrapping
// either a stdout exporter (the baseline/dev-friendly default every
// deployment got until §33) or, when a caller supplies a non-empty
// otlpEndpoint, a real OTLP/HTTP exporter pointed at an operator's own
// collector (§33: "control-plane OTLP export"). "Baseline" was this file's
// whole scope originally -- no specific spans or instruments are defined
// here; those arrive with the features that need them (§5.3's metric list --
// spawn latency, boot phase durations, liveness gaps, watchdog activations,
// outbox lag, orphan GC count -- are recorded by PR-11/PR-12, PR-13/14,
// PR-24, PR-35, PR-25 respectively, not by this file. Spawn latency in
// particular belongs to the app/adapter layer (sessionactor or the Modal
// provider), never to domain/sandbox itself -- §1/§11 make domain purity a
// hard rule ("zero external dependencies", no I/O), and recording an OTel
// metric is an external dependency a pure decision function must never
// carry). §33 changes WHERE those instruments end up, never what gets
// registered here.
//
// PR-25 ("reconciler + GC") landed §5.3's "orphan GC count" as the
// orphans_reaped Int64Counter internal/app/reconciler.NewReconciler
// constructs via otel.Meter("narvi/reconciler") -- this codebase's first
// custom OTel instrument built on top of the MeterProvider this file
// registers globally. See that package's own doc.go for the full writeup;
// this file's own scope is still unchanged -- bootstrap only, no
// instruments defined here.
//
// §5.3 ("ops: dashboards, alerts, runbooks") found the three remaining
// names on this comment's own PR-11/12/13/14/24/35 list -- spawn latency,
// liveness gaps, watchdog activations -- had never actually landed at any
// of those PRs, or anywhere else: a repo-wide audit at that Step turned up
// zero registered instruments for any of the three. All three (plus a
// watchdog-false-alarm counter and a false-turn-failure counter §5.3
// itself doesn't name but IMPLEMENTATION_PLAN.md row 77 does) now live in
// internal/app/sessionactor's own opsmetrics.go, exactly where spawn
// latency was always meant to (this comment's own "app/adapter layer"
// call, above) -- see that file's own top comment for the full gap
// analysis and internal/ops's own doc.go for the CI check that now keeps
// every dashboard/alert honest against whatever this file's global
// MeterProvider actually has registered.
//
// §33 ("metrics export path") closed the gap that analysis then left
// standing: every one of those correctly-registered instruments still went
// to its own process's stdout as JSON and was aggregated by nobody, because
// this file wired stdouttrace/stdoutmetric and nothing else, with no OTLP
// module in go.mod at all. SetupOTel now takes an otlpEndpoint parameter --
// empty (the default, every existing deployment) keeps the ORIGINAL stdout
// behavior byte-identical; a real value swaps in otlptracehttp/
// otlpmetrichttp instead, both pointed at the same base URL (see each
// exporter's own construction below for why one shared value is sound: they
// append different well-known suffixes to it). cmd/control-plane/main.go is
// the only caller ever given a real value -- cmd/sandbox-agent shares this
// function but hardcodes an empty string at its own call site, structurally
// unable to reach an operator's collector even by accident (see that call
// site's own doc comment; §27.6's server-appended egress allowlist floor
// admits the control-plane host plus the session's git hosts, never one,
// and that is a property this file preserves rather than routes around).
//
// §33.3 then closed the OTHER half of the same story: sandbox-agent's own
// four boot-phase-duration histograms (this comment's own PR-13/14
// reference -- sandbox_agent_boot_duration_seconds/hook_rerun_duration_
// seconds/git_fetch_duration_seconds/git_checkout_duration_seconds) used
// to be recorded INSIDE the ephemeral sandbox process itself (internal/
// sandboxagent/boot/telemetry.go, internal/sandboxagent/gitclone/
// telemetry.go, both now deleted) -- an OTLP endpoint was never a fix for
// those four, since a sandbox process can live only minutes and vanish
// (SIGKILL, provider teardown) before its own periodic export interval
// ever elapses, and widening §27.6's egress floor to admit a collector
// host in every customer sandbox was rejected outright (§33.4: the exact
// secret-in-customer-code-path class this codebase strips from every
// child environment). sandbox-agent now sends one best-effort boot_timing
// sandbox-ws event per data point instead of recording locally, over the
// authenticated WS connection that is already open before boot begins,
// and the control plane records all four histograms in internal/app/
// sessionactor's own opsmetrics.go/boottiming.go -- alongside the three
// §5.3 gap-analysis instruments the paragraph above already describes.
// Every sandbox-agent-side instrument this platform ever registered is
// now recorded control-plane-side; none is left inside the ephemeral
// process. This file's own SetupOTel bootstrap is unchanged by that move
// (traces may still come later) -- cmd/sandbox-agent still hardcodes an
// empty otlpEndpoint at its own call site, stdout-only, exactly as before.

package platform

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// SetupOTel builds a resource identifying this process as serviceName,
// constructs a TracerProvider and a MeterProvider together, registers both
// globally (otel.SetTracerProvider / otel.SetMeterProvider), and returns a
// shutdown func that flushes and shuts down both providers, joining any
// errors from either.
//
// otlpEndpoint is §33's control-plane-only opt-in (platform.Config.
// OTLPEndpoint's own doc comment has the full gating rule and every
// validation detail). Empty (the default) keeps building the stdouttrace/
// stdoutmetric exporters this file has always used -- byte-identical to
// every deployment before §33, dev and CI included. A non-empty value (a
// validated absolute http(s) URL with no path of its own, guaranteed by
// Config.OTLPEndpoint's own boot-time validation, never accepted raw from a
// caller) swaps in otlptracehttp/otlpmetrichttp instead, both pointed at
// the SAME base URL: each OTLP/HTTP exporter appends its own well-known
// "/v1/traces" or "/v1/metrics" suffix (the OTel spec's own "general
// endpoint" behavior), so one shared value correctly reaches both routes on
// one collector rather than needing two separately-configured endpoints.
//
// Every batching/export timing and retry knob -- the batch span
// processor's own flush interval, each OTLP exporter's own per-batch
// WithTimeout (10s) and retry backoff (5s initial / 30s max interval / 1
// minute max elapsed) -- is left at the SDK's own library default. Nothing
// in §33 specifies different values, and the bounded shutdown path (this
// func's own returned shutdown, wrapped by every caller in a
// platform.Timeouts.OTelShutdownTimeout-bounded context) already caps the
// worst case a caller ever waits regardless of what these SDK-internal
// retries would otherwise keep attempting on their own -- see that field's
// own doc comment.
//
// Callers should defer/invoke the returned shutdown before exiting so
// buffered spans/metrics are flushed rather than lost.
func SetupOTel(ctx context.Context, serviceName, otlpEndpoint string) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, err
	}

	var traceExporter sdktrace.SpanExporter
	var metricExporter metric.Exporter
	if otlpEndpoint == "" {
		// Baseline/dev-friendly default (§5.3), unchanged since before §33:
		// every instrument this process registers is written as JSON to
		// its own stdout.
		traceExporter, err = stdouttrace.New()
		if err != nil {
			return nil, err
		}
		metricExporter, err = stdoutmetric.New()
		if err != nil {
			return nil, err
		}
	} else {
		traceExporter, err = otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(otlpEndpoint))
		if err != nil {
			return nil, fmt.Errorf("construct otlp trace exporter: %w", err)
		}
		metricExporter, err = otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(otlpEndpoint))
		if err != nil {
			return nil, fmt.Errorf("construct otlp metric exporter: %w", err)
		}
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter), // library-default batch timeout (§ scope note: no new Timeouts field)
		sdktrace.WithResource(res),
	)

	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(metricExporter)), // library-default export interval
		metric.WithResource(res),
	)

	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)

	shutdown := func(ctx context.Context) error {
		// Shutdown on both providers already flushes any buffered
		// spans/metrics before stopping (that's the SDK's documented
		// contract for both the batch span processor and the periodic
		// metric reader) — an extra explicit ForceFlush first would just
		// export everything twice.
		//
		// Every caller wraps this in a platform.Timeouts.
		// OTelShutdownTimeout-bounded context (never left to the caller's
		// own possibly-already-canceled long-lived ctx) and treats any
		// returned error as warn-and-continue, never fatal -- see each
		// binary's own bounded-shutdown helper (cmd/control-plane/main.go's
		// shutdownControlPlaneOTel, cmd/sandbox-agent/main.go's
		// shutdownSandboxAgentOTel) for why: with otlpEndpoint set, this
		// flush is a real network call to an operator's collector, and a
		// down/unreachable collector must cost a bounded wait, never hang
		// process shutdown and never fail the process outright.
		var errs []error

		if err := tracerProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
		if err := meterProvider.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}

		return errors.Join(errs...)
	}

	return shutdown, nil
}
