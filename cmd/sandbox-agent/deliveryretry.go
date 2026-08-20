// This file (deliveryretry.go) implements an adversarial-review
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

// classifyMintTokenError is Step 73b's own ("cloud identity: sandbox-side
// consumption + kubeconfig injection", §27.3) sibling to
// classifyDeliveryFetchError, for cloud-identity-token minting
// (credentials.CPClient.MintCloudIdentityToken) specifically -- NOT a
// reuse of classifyDeliveryFetchError, because that endpoint's OWN 503
// means something classifyDeliveryFetchError's generic "5xx is always
// retryable" rule gets wrong for this one call: httpapi.
// MintCloudIdentityToken (internal/adapters/inbound/httpapi/
// cloudidentitytoken.go) returns 503 for EXACTLY two config-level, non-
// transient conditions -- "cloud identity federation is not configured"
// (platform.Config.CloudIdentityIssuerURL unset, the whole capability
// off) and "no active signing key configured" (nobody has ever called
// RotateCloudIdentitySigningKey) -- never for a generic internal error
// (that branch returns 500, same as every other handler in this
// codebase). Retrying either condition burns this call's own bounded
// retry budget for zero chance of a different outcome within one boot's
// own short window (an admin fixing either config mid-boot is not a
// scenario worth spending retries on) -- exactly the same
// "this session's own state is what's wrong, not a transient blip" logic
// classifyDeliveryFetchError's own doc comment already gives for
// 401/403/404/410.
//
// This Step's own explicit gap resolution (spec brief: "decide and
// document what sandbox-agent does when minting returns 503... 403...
// or a transient failure"):
//   - 401/403/404/410: this delivery's own terminal handshake fences,
//     IDENTICAL to classifyDeliveryFetchError -- 403 additionally covers
//     §27.3's own audience-allowlist refusal ("no cloud identity binding
//     for this session declares the requested audience"), which can
//     never resolve differently on a later attempt within the same boot
//     either (the binding CONFIGURATION is what would need to change,
//     not network conditions).
//   - 503: ALSO terminal, per this func's own doc comment above -- the
//     one deliberate divergence from classifyDeliveryFetchError.
//   - Any other non-5xx: terminal (conservative, matching
//     classifyDeliveryFetchError's own identical "unrecognized non-5xx is
//     more likely client-side" reasoning).
//   - Any OTHER 5xx (500, 502, 504, ...), or a transport-level failure
//     that never reached credentials.DeliveryStatusError at all: still
//     retryable -- a genuine transient CP-side blip, the SAME class
//     classifyDeliveryFetchError already retries.
//
// The caller (cmd/sandbox-agent/cloudidentity.go's mintCloudIdentityToken,
// shared by every mint site including kubeconfig.go's own
// applyClusterBinding for the §27.4 AuthKindOIDC cluster rung) degrades
// warn-and-continue on EITHER outcome (retries exhausted, or a
// first-attempt terminal classification) -- see that file's own doc
// comment for the full "what happens to the token file" resolution,
// consistent with the same posture Step 72 established.
func classifyMintTokenError(err error) error {
	if err == nil {
		return nil
	}
	var statusErr *credentials.DeliveryStatusError
	if !errors.As(err, &statusErr) {
		return err
	}
	switch statusErr.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusGone, http.StatusServiceUnavailable:
		return platform.Permanent(err)
	}
	if statusErr.StatusCode >= 500 && statusErr.StatusCode < 600 {
		return err
	}
	return platform.Permanent(err)
}
