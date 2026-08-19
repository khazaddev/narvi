// This file (deliverystatus.go) implements a small, shared typed error --
// DeliveryStatusError -- used by this package's own two boot-time
// delivery-endpoint fetches (FetchSandboxSecrets, sandboxsecrets.go;
// FetchOpenCodeConfig, opencodeconfig.go) to report a non-2xx CP response.
// Step 72's own adversarial-review MEDIUM fix (§27.1: "with bounded
// retry") needs a caller (cmd/sandbox-agent's fetchSandboxSecrets/
// fetchOpenCodeConfig) to classify a failure as retryable (a transport
// error, or a 5xx) vs terminal (401/403/404/410 -- this delivery
// handshake's own fences, mirroring providercredentialsdelivery.go's
// identical four-way shape) WITHOUT parsing an error's own free-text
// message -- exactly the same problem internal/adapters/outbound/modal's
// own classifyErrorResponse (errors.go) solves for a different transport.
// StatusCode is the only field: the raw response body is deliberately
// NEVER attached here either, mirroring every Fetch* method in this
// package's own established "never echo the body back" discipline (a
// validation-failure response is exactly the kind of body that can echo
// request/secret data back verbatim).

package credentials

import "fmt"

// DeliveryStatusError reports a non-2xx HTTP response from one of this
// package's own boot-time delivery-endpoint fetches. Endpoint names which
// one (e.g. "sandbox-secrets", "opencode-config") purely for a readable
// Error() string -- callers that need to branch on retryability use
// errors.As to read StatusCode directly, never string-matching.
type DeliveryStatusError struct {
	Endpoint   string
	StatusCode int
}

func (e *DeliveryStatusError) Error() string {
	return fmt.Sprintf("credentials: %s request returned http %d", e.Endpoint, e.StatusCode)
}
