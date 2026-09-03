// This file is the only way to build an HTTP client for this adapter, and
// that is the point.
//
// §30.2: the live, pass-through transport is constructible only from the
// package that resolves the shadow flag -- a capability token for egress,
// the same shape egressmode.Capability already uses one level down. A new
// construction site cannot compile without a gate in hand, because there
// is no exported way to obtain a client that lacks one.

package githubapi

import (
	"context"
	"net/http"

	"github.com/narvidev/narvi/internal/app/shadowledger"
)

// NewGatedClient builds the http.Client this adapter must be constructed
// with. Every request it carries passes through the shadow gate: reads go
// out untouched, and every mutating verb is intercepted, recorded, and
// answered with a synthesized success unless the resolver reports the
// target repository live.
//
// isLive is called per request rather than once here. A client that
// resolved the mode at construction would keep suppressing after a
// promotion and, worse, keep emitting after a demotion.
func NewGatedClient(ledger shadowledger.Store, isLive func(ctx context.Context, repoFullName string) bool) *http.Client {
	return &http.Client{
		Transport: &shadowRoundTripper{
			next:    http.DefaultTransport,
			ledger:  ledger,
			resolve: isLive,
		},
	}
}
