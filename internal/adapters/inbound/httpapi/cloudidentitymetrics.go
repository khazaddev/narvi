// This file (cloudidentitymetrics.go) implements Step 73a's own ("cloud
// identity: OIDC issuer, bindings, minting", §27.3) minting metric:
// "Minting is logged with correlation_id (§5.3) and counted as a metric."
// correlation_id is already carried by every log line cloudidentitytoken.
// go emits (platform.Logger(ctx), fed by CorrelationIDMiddleware -- see
// that middleware's own doc comment, mounted globally in cmd/
// control-plane/main.go) -- this file is the metric half.

package httpapi

import (
	"context"
	"log/slog"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// cloudIdentityMeterName is this package's own OTel meter name for the
// cloud-identity minting metric, mirroring internal/app/imagebuild's
// "narvi/imagebuild" and internal/sandboxagent/boot's
// "narvi/sandboxagent-boot" precedent exactly (§5.3: one named meter per
// major subsystem).
const cloudIdentityMeterName = "narvi/httpapi-cloudidentity"

// cloudIdentityMintTotalCounter is resolved LAZILY, on first use
// (sync.OnceValue), mirroring internal/sandboxagent/boot's
// hookRerunDurationHistogram/internal/sandboxagent/gitclone's
// gitFetchDurationHistogram precedent exactly: httpapi has no
// per-process constructor object to anchor eager construction to the way
// internal/app/imagebuild's NewBuilder does (MintCloudIdentityToken is a
// free function, called once at boot by cmd/control-plane/main.go, but
// resolving otel.Meter at package-init time would permanently bind this
// instrument to whatever MeterProvider happens to be globally registered
// at THAT moment -- which main.go's own real OTel SDK setup, or a test's
// own TestMain, may not have installed yet). Lazy, first-use resolution
// instead reads otel.Meter(cloudIdentityMeterName) against whatever
// MeterProvider is globally registered at the moment the FIRST mint
// actually happens.
var cloudIdentityMintTotalCounter = sync.OnceValue(newCloudIdentityMintTotalCounter)

func newCloudIdentityMintTotalCounter() metric.Int64Counter {
	c, err := otel.Meter(cloudIdentityMeterName).Int64Counter(
		"cloud_identity_mint_total",
		metric.WithDescription("Count of every successful POST /sessions/{id}/cloud-identity-token mint (§27.3's own \"counted as a metric\" requirement) -- incremented only after every fail-closed/audience-allowlist gate has already passed, never for a refused or errored request."),
		metric.WithUnit("{mint}"),
	)
	if err != nil {
		// An Int64Counter construction call can only ever fail for a
		// malformed static instrument name -- this one is a fixed,
		// well-formed literal, so this is not a runtime condition; logged
		// rather than silently swallowed on the off chance a future SDK
		// ever does reject it, mirroring internal/sandboxagent/boot's own
		// identical defensive-logging precedent for the same class of
		// "structurally cannot fail" construction call.
		slog.Error("httpapi: construct cloud_identity_mint_total counter failed", "error", err)
	}
	return c
}

// recordCloudIdentityMint increments the mint counter by one -- called
// exactly once per successful mint, after every gate in
// MintCloudIdentityToken's own outcome table has already passed.
func recordCloudIdentityMint(ctx context.Context) {
	cloudIdentityMintTotalCounter().Add(ctx, 1)
}
