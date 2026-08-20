// This file (webhooksig.go) implements a generic, provider-agnostic
// raw-body HMAC-SHA256 verification primitive for THIRD-PARTY webhook
// signatures (GitHub, Slack, Linear, ...) -- deliberately a DISTINCT
// mechanism from this package's own Sign/Verify (hmacauth.go), which
// implements Narvi's OWN internal "{unixSeconds}.{hexSignature}" bearer
// wire format for INTERNAL traffic only (§5.2: sandbox→CP, CP→bots).
// Conflating the two would be wrong: no real webhook provider signs
// "{timestamp}.{payload}" the way Sign/Verify does.
//
// # Design note: resolving the HMACWebhookSecret question (Step 31)
//
// §5.2 lists three directions for Narvi's own internal HMAC scheme:
// "separate secrets per direction (sandbox→CP, CP→bots, webhook
// ingress)". Read literally, "webhook ingress" sits alongside the other
// two INTERNAL directions, sharing the SAME "single HMAC helper ...
// bearer timestamp.signature" sentence -- so Config.HMACWebhookSecret /
// platform.Sign+Verify's own bearer format is Narvi's OWN webhook-shaped
// scheme, for a call that has no third-party provider signature to
// match at all. The most concrete, already-planned consumer of exactly
// that shape is IMPLEMENTATION_PLAN.md's own §8.2 ("automations:
// triggers & extras"), which lists a generic user-configured "webhook"
// trigger condition alongside GitHub/Linear/cron ones -- unlike
// GitHub/Slack/Linear's own fixed, provider-defined signature formats, a
// generic/custom automation-triggering webhook has nothing to verify
// against except a secret Narvi itself hands the caller, making Narvi's
// own bearer scheme (mint via Sign, verify via Verify) the natural fit.
//
// GitHub ("X-Hub-Signature-256: sha256=<hex>", HMAC-SHA256 over the raw
// body, no timestamp), Slack ("X-Slack-Signature: v0=<hex>", HMAC-SHA256
// over "v0:{timestamp}:{raw body}", timestamp in a SEPARATE header), and
// Linear (its own header, likewise expected to be a raw-body
// HMAC-SHA256 -- Step 34 confirms the exact header/format at
// implementation time) do NOT match that bearer format, so Steps 32/33/
// 34 must NOT authenticate their real provider's webhook using
// Config.HMACWebhookSecret/platform.Verify. Each provider adapter
// instead verifies its OWN provider signature with its OWN
// provider-specific secret (a new config field that Step introduces,
// e.g. GitHubWebhookSecret/SlackSigningSecret/LinearWebhookSecret --
// deliberately NOT HMACWebhookSecret) using VerifyWebhookSignature
// below, which both GitHub's (simple) and Slack's (timestamp-prefixed)
// shapes can express without rewriting anything: the caller assembles
// whatever exact byte string its own provider actually signs (the raw
// body for GitHub; "v0:{ts}:{body}" for Slack) and this function only
// ever computes/compares the HMAC over exactly that.

package platform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Sentinel errors VerifyWebhookSignature / VerifyWebhookTimestamp can
// return -- distinct from hmacauth.go's own ErrMalformedHMACToken/
// ErrInvalidHMACSignature/ErrExpiredHMACToken sentinels (checked via
// errors.Is, never conflated with these), since the two mechanisms guard
// two genuinely different wire formats.
var (
	// ErrMalformedWebhookSignature means the presented signature string
	// itself could not be used at all: empty, or not valid hex. Fails
	// closed on any parse ambiguity -- never falls back to "assume
	// valid".
	ErrMalformedWebhookSignature = errors.New("webhooksig: malformed signature")

	// ErrInvalidWebhookSignature means the presented signature parsed
	// fine (valid hex) but does not match the HMAC-SHA256 recomputed
	// over the exact signed-payload bytes the caller supplied -- either
	// the secret, the payload, or the signature itself differs from
	// what was actually signed.
	ErrInvalidWebhookSignature = errors.New("webhooksig: invalid signature")

	// ErrExpiredWebhookTimestamp means a provider-supplied timestamp
	// (e.g. Slack's X-Slack-Request-Timestamp) is further from now than
	// the caller's freshness window allows -- returned only by
	// VerifyWebhookTimestamp, never by VerifyWebhookSignature itself
	// (GitHub's scheme carries no timestamp at all).
	ErrExpiredWebhookTimestamp = errors.New("webhooksig: expired timestamp")
)

// VerifyWebhookSignature reports whether presentedSignatureHex is the
// correct lowercase-or-uppercase hex encoding of HMAC-SHA256(secret,
// signedPayload), via a constant-time comparison (hmac.Equal -- never ==
// or bytes.Equal on the raw signature bytes). Fails closed: an empty or
// non-hex presentedSignatureHex returns ErrMalformedWebhookSignature
// without ever reaching the comparison.
//
// signedPayload is the EXACT byte string the provider actually computed
// its own signature over -- this function has no opinion on its shape,
// which is what makes it reusable across providers with genuinely
// different signed-string conventions:
//   - GitHub: signedPayload is simply the raw request body (no
//     timestamp involved at all).
//   - Slack: signedPayload is "v0:{timestamp}:{raw body}" (§ Slack's own
//     signing-secrets doc) -- the caller assembles this string itself
//     from its own already-read timestamp header and body, then passes
//     the result here unchanged.
//
// presentedSignatureHex is the signature the provider actually sent,
// with any provider-specific prefix ("sha256=", "v0=", ...) already
// stripped by the caller -- this function only ever handles the bare hex
// digest, keeping it agnostic to each provider's own header-value
// framing.
func VerifyWebhookSignature(secret, signedPayload []byte, presentedSignatureHex string) error {
	presentedSignatureHex = strings.TrimSpace(presentedSignatureHex)
	if presentedSignatureHex == "" {
		return ErrMalformedWebhookSignature
	}

	got, err := hex.DecodeString(presentedSignatureHex)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrMalformedWebhookSignature, err)
	}

	mac := hmac.New(sha256.New, secret)
	// hash.Hash.Write never returns an error (documented contract of
	// io.Writer implementations backed by an in-memory hash state).
	_, _ = mac.Write(signedPayload)
	want := mac.Sum(nil)

	if !hmac.Equal(got, want) {
		return ErrInvalidWebhookSignature
	}
	return nil
}

// VerifyWebhookTimestamp reports whether a provider-supplied unix-seconds
// timestamp (e.g. Slack's X-Slack-Request-Timestamp header, checked
// SEPARATELY from -- and in addition to -- the signature itself per
// Slack's own replay-protection guidance) is within window of now.
// Fails closed on an expired timestamp via the distinct
// ErrExpiredWebhookTimestamp sentinel.
//
// Only providers whose scheme actually carries a timestamp call this at
// all -- GitHub's signature has no timestamp component, so a GitHub
// adapter never calls this func.
func VerifyWebhookTimestamp(ts int64, now time.Time, window time.Duration) error {
	age := now.Sub(time.Unix(ts, 0))
	if age < 0 {
		age = -age
	}
	if age > window {
		return ErrExpiredWebhookTimestamp
	}
	return nil
}
