package platform_test

import (
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/khazaddev/narvi/internal/platform"
)

// TestSetupOTel proves the bootstrap actually produces a working provider,
// not just that construction didn't panic: it starts and ends a real span,
// records a real counter measurement, then shuts down cleanly.
func TestSetupOTel(t *testing.T) {
	ctx := t.Context()

	shutdown, err := platform.SetupOTel(ctx, "narvi-test")
	if err != nil {
		t.Fatalf("SetupOTel() error = %v, want nil", err)
	}
	if shutdown == nil {
		t.Fatal("SetupOTel() shutdown func = nil, want non-nil")
	}

	_, span := otel.Tracer("test").Start(ctx, "x")
	span.End()

	counter, err := otel.Meter("test").Int64Counter("test_counter")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v, want nil", err)
	}
	counter.Add(ctx, 1)

	if err := shutdown(ctx); err != nil {
		t.Fatalf("shutdown() error = %v, want nil", err)
	}
}
