// This file (otel.go) bootstraps the OpenTelemetry SDK named in the
// stack-choices line (§1) and required "day one, not later" by §5.3: a
// TracerProvider and a MeterProvider, each wrapping a stdout exporter as the
// baseline/dev-friendly default, registered globally. "Baseline" is this
// PR's whole scope — no specific spans or instruments are defined here;
// those arrive with the features that need them (§5.3's metric list —
// spawn latency, boot phase durations, liveness gaps, watchdog activations,
// outbox lag, orphan GC count — are recorded by PR-11/PR-12, PR-13/14,
// PR-24, PR-35, PR-25 respectively, not by this file. Spawn latency in
// particular belongs to the app/adapter layer (sessionactor or the Modal
// provider), never to domain/sandbox (PR-07) itself — §1/§11 make domain
// purity a hard rule ("zero external dependencies", no I/O), and recording
// an OTel metric is an external dependency a pure decision function must
// never carry). Batching/export
// intervals use the OTel SDK's own library defaults (no Timeouts field
// added for them — nothing in the technical plan specifies values here).
//
// PR-25 ("reconciler + GC") has now landed: §5.3's "orphan GC count" is
// the orphans_reaped Int64Counter internal/app/reconciler.NewReconciler
// constructs via otel.Meter("narvi/reconciler") — this codebase's first
// custom OTel instrument built on top of the MeterProvider this file
// registers globally. See that package's own doc.go for the full writeup;
// this file's own scope is still unchanged — bootstrap only, no
// instruments defined here.

package platform

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
)

// SetupOTel builds a resource identifying this process as serviceName,
// constructs a TracerProvider and a MeterProvider each wrapping a stdout
// exporter (go.opentelemetry.io/otel/exporters/stdout/{stdouttrace,
// stdoutmetric}), registers both globally (otel.SetTracerProvider /
// otel.SetMeterProvider), and returns a shutdown func that flushes and
// shuts down both providers, joining any errors from either.
//
// Callers should defer/invoke the returned shutdown before exiting so
// buffered spans/metrics are flushed rather than lost.
func SetupOTel(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	res, err := resource.New(ctx,
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)
	if err != nil {
		return nil, err
	}

	traceExporter, err := stdouttrace.New()
	if err != nil {
		return nil, err
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExporter), // library-default batch timeout (§ scope note: no new Timeouts field)
		sdktrace.WithResource(res),
	)

	metricExporter, err := stdoutmetric.New()
	if err != nil {
		return nil, err
	}

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
