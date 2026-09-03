package linear

import (
	"net/http"

	"github.com/narvidev/narvi/internal/platform"
)

// Header names Linear itself sends with every webhook delivery -- all
// four verified live against Linear's own current developer documentation
// during this Step's investigation (the raw HTML response, not a
// secondary summary): "Linear-Delivery: <uuid v4>", "Linear-Event: Issue"
// (the entity/category that triggered the event -- "AgentSessionEvent"
// for this Step's own category), "Linear-Signature: <hex hmac-sha256>",
// "Linear-Timestamp: <unix ms>" (a header ALSO carrying the timestamp;
// this package deliberately verifies the body's own webhookTimestamp
// field instead -- see verifyTimestamp's own doc comment below -- since
// that is what Linear's own docs' worked verification example checks).
const (
	linearSignatureHeader = "Linear-Signature"
	linearDeliveryHeader  = "Linear-Delivery"
	linearEventHeader     = "Linear-Event"
)

// agentSessionEventType is the Linear-Event header value this package
// actually processes -- Linear's own real category name for the
// AgentSession webhook family (confirmed against Linear's real docs:
// "Once you subscribe to AgentSessionEvent webhooks..."). Any other
// Linear-Event value (a different webhook category this control plane's
// own Linear application setting might also have enabled, e.g.
// "PermissionChange") is acknowledged 200 and otherwise ignored by this
// package -- see webhook.go.
const agentSessionEventType = "AgentSessionEvent"

// verifySignatureHeader reports whether presented (the raw Linear-Signature
// header value) is the correct hex-encoded HMAC-SHA256 of rawBody under
// secret.
//
// Investigation note (this Step's own required confirmation of §5.2's
// generic "webhook ingress" HMAC line against Linear's REAL scheme,
// internal/platform/webhooksig.go's own doc comment): Linear's real,
// current developer documentation (fetched live during this Step, not
// assumed) states plainly: "Linear sends a Linear-Signature HTTP header
// with every webhook request. This header contains a hex-encoded
// HMAC-SHA256 signature of the raw body contents, signed using the
// webhook's signing secret." Linear's own worked Express example computes
// exactly `crypto.createHmac("sha256", SECRET).update(rawBody).digest()`
// and compares it (as raw bytes, via Buffer.from(header, "hex")) against
// the header's own hex decoding using a constant-time comparison. This is
// EXACTLY the shape internal/platform.VerifyWebhookSignature already
// expresses (GitHub's own shape: signedPayload = raw body, no
// timestamp-prefix, no "sha256="-style prefix on the header value to
// strip) -- so this function is a thin, Linear-specific wrapper around
// that existing, provider-agnostic primitive, never a second HMAC
// implementation. Confirmed: Linear's scheme IS plain HMAC-SHA256 over
// the raw body, matching this Step's own required confirmation.
func verifySignatureHeader(secret, rawBody []byte, presented string) error {
	return platform.VerifyWebhookSignature(secret, rawBody, presented)
}

// signatureHeaderFrom is a tiny extraction helper kept separate from
// verifySignatureHeader so webhook.go's own call site reads as "get the
// header, then verify it" without repeating r.Header.Get's own string
// literal at more than one call site.
func signatureHeaderFrom(r *http.Request) string {
	return r.Header.Get(linearSignatureHeader)
}

func deliveryIDFrom(r *http.Request) string {
	return r.Header.Get(linearDeliveryHeader)
}

func eventTypeFrom(r *http.Request) string {
	return r.Header.Get(linearEventHeader)
}
