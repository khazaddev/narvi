// This file (hmacauth.go) implements the single HMAC helper named in §5.2:
// "single HMAC helper (platform/auth), bearer timestamp.signature, 5-min
// window, fail closed." It lives in the platform package itself (rather
// than a separate platform/auth subpackage) since that's where every other
// cross-cutting primitive (config, timeouts, logging, correlation, OTel)
// already lives.
//
// Sign/Verify are secret-agnostic: §5.2 requires "separate secrets per
// direction (sandbox→CP, CP→bots, webhook ingress) so one rotation doesn't
// touch everything" — that separation is the caller's responsibility (three
// distinct []byte secrets, e.g. Config.HMACSandboxSecret /
// Config.HMACBotsSecret / Config.HMACWebhookSecret), not this file's. This
// file only implements the shared signing/verification mechanism once.

package platform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Sentinel errors Verify can return. Each failure mode is distinct and
// checked via errors.Is — never a single generic "invalid token" error —
// so callers (and tests) can tell a malformed token apart from a tampered
// one apart from an expired one.
var (
	// ErrMalformedHMACToken means the token string itself could not be
	// parsed as "{unixSeconds}.{hexSignature}": missing separator, empty
	// timestamp/signature half, non-numeric timestamp, or non-hex
	// signature. Fails closed on any parse ambiguity — never falls back to
	// "assume valid".
	ErrMalformedHMACToken = errors.New("hmacauth: malformed token")

	// ErrInvalidHMACSignature means the token parsed fine but the
	// recomputed HMAC over "{timestamp}.{payload}" does not match the
	// signature in the token — either secret, timestamp, or payload
	// differs from what was signed.
	ErrInvalidHMACSignature = errors.New("hmacauth: invalid signature")

	// ErrExpiredHMACToken means the signature is valid but |now -
	// timestamp| exceeds the freshness window.
	ErrExpiredHMACToken = errors.New("hmacauth: expired token")
)

// Sign returns a bearer token of the form "{unixSeconds}.{hexSignature}"
// where hexSignature is hex(HMAC-SHA256(secret, "{unixSeconds}.{payload}")).
// The signature covers BOTH the timestamp and the payload, so a captured
// token can't be replayed against a different payload within the freshness
// window (the timestamp alone would only bound replay time, not scope).
func Sign(secret []byte, payload string, now time.Time) string {
	ts := now.Unix()
	signature := computeHMAC(secret, ts, payload)
	return fmt.Sprintf("%d.%s", ts, hex.EncodeToString(signature))
}

// Verify parses token as "{unixSeconds}.{hexSignature}", recomputes the
// HMAC over the same "{timestamp}.{payload}" string Sign covered, compares
// it to the token's signature via hmac.Equal (constant-time — never == or
// bytes.Equal on the raw signature), and checks that the token's timestamp
// is within window of now. Fails closed on every path: a malformed token, a
// signature mismatch, and an expired-but-otherwise-valid token are three
// distinct sentinel errors (checked via errors.Is), never a single generic
// error and never a silent "assume valid" fallback on any parse ambiguity.
func Verify(secret []byte, token string, payload string, now time.Time, window time.Duration) error {
	tsPart, sigPart, ok := strings.Cut(token, ".")
	if !ok || tsPart == "" || sigPart == "" {
		return ErrMalformedHMACToken
	}

	ts, err := strconv.ParseInt(tsPart, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: timestamp %q: %v", ErrMalformedHMACToken, tsPart, err)
	}

	gotSignature, err := hex.DecodeString(sigPart)
	if err != nil {
		return fmt.Errorf("%w: signature %q: %v", ErrMalformedHMACToken, sigPart, err)
	}

	wantSignature := computeHMAC(secret, ts, payload)
	if !hmac.Equal(gotSignature, wantSignature) {
		return ErrInvalidHMACSignature
	}

	age := now.Sub(time.Unix(ts, 0))
	if age < 0 {
		age = -age
	}
	if age > window {
		return ErrExpiredHMACToken
	}

	return nil
}

// computeHMAC computes HMAC-SHA256(secret, "{ts}.{payload}") — the exact
// string both Sign and Verify sign/recompute over, kept in this one place
// so the two can never drift apart.
func computeHMAC(secret []byte, ts int64, payload string) []byte {
	mac := hmac.New(sha256.New, secret)
	// hash.Hash.Write never returns an error (documented contract of
	// io.Writer implementations backed by an in-memory hash state).
	_, _ = fmt.Fprintf(mac, "%d.%s", ts, payload)
	return mac.Sum(nil)
}
