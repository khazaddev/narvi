// This file (deliveryretry.go) implements Step 72's own adversarial-review
// MEDIUM fix (§27.1: "with bounded retry") -- the shared retry-
// classification policy fetchSandboxSecrets (sandboxsecrets.go) and
// fetchOpenCodeConfig (opencodeconfig.go) both apply around their own
// single-attempt CPClient.FetchSandboxSecrets/FetchOpenCodeConfig calls,
// via platform.Retry. Centralized here (rather than duplicated in each
// file) since the classification RULE itself is identical for both
// endpoints -- they share the exact same sandbox-bearer/gen handshake
// (mirroring providercredentialsdelivery.go's own four-way fence:
// 401 malformed/absent bearer, 403 gen mismatch or "nothing usable",
// 404 unknown session, 410 dead sandbox) -- only the fetch closure itself
// differs between callers.
package main

import (
	"errors"
	"net/http"

	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// classifyDeliveryFetchError marks err as platform.Permanent when it is a
// *credentials.DeliveryStatusError carrying one of this delivery
// handshake's own terminal fences -- 401/403/404/410 -- so platform.Retry
// stops immediately rather than burning its remaining attempts against a
// deliberately-rejecting endpoint (§27.1's own explicit "retry transport
// errors and 5xx only" rule; these four codes can never resolve
// differently on a later attempt -- this sandbox's own identity/
// generation is what's wrong, not a transient condition).
//
// Every OTHER case is left unmarked (i.e. still retryable):
//   - err is not even a *DeliveryStatusError at all -- a transport-level
//     failure (the request never got a response: connection refused,
//     DNS failure, TLS handshake failure, context deadline) or a
//     malformed-2xx-body decode failure. Both are cheap to simply retry.
//   - A *DeliveryStatusError whose StatusCode is 5xx -- CP's own real
//     internal-error branches (sandboxsecretsdelivery.go/
//     opencodeconfigdelivery.go both have one), exactly the class of
//     failure most likely to be transient (a control-plane rolling
//     restart, an ingress 502, ...).
//   - A *DeliveryStatusError whose StatusCode is some OTHER, unrecognized
//     non-2xx value this client did not anticipate. Treated as TERMINAL
//     (not retried) -- conservative: §27.1's own instruction is "5xx
//     only", not "everything except 4 named codes", and an unrecognized
//     non-5xx status is more likely a client-side problem (this session
//     is simply wrong in some new way) than a transient blip.
func classifyDeliveryFetchError(err error) error {
	if err == nil {
		return nil
	}
	var statusErr *credentials.DeliveryStatusError
	if !errors.As(err, &statusErr) {
		return err
	}
	switch statusErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone:
		return platform.Permanent(err)
	}
	if statusErr.StatusCode >= 500 && statusErr.StatusCode < 600 {
		return err
	}
	return platform.Permanent(err)
}
