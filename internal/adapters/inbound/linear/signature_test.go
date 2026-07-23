package linear

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/platform"
)

// linearSign mirrors Linear's own real "Linear-Signature" scheme,
// confirmed live against Linear's current developer documentation during
// this Step's investigation: a hex-encoded HMAC-SHA256 of the RAW request
// body, using the webhook's own signing secret -- no timestamp folded
// into the signed string (unlike Slack's own scheme) and no "sha256="-
// style prefix on the header value (unlike GitHub's own scheme, which
// does prefix it).
func linearSign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// TestVerifySignatureHeader proves this package's own thin Linear-
// specific wrapper around platform.VerifyWebhookSignature correctly
// expresses Linear's real scheme: valid signature accepted; a tampered
// body, a wrong secret, and a malformed (non-hex/empty) header are all
// rejected.
func TestVerifySignatureHeader(t *testing.T) {
	secret := []byte("linear-webhook-signing-secret")
	otherSecret := []byte("a-completely-different-secret")
	body := []byte(`{"action":"created","type":"AgentSessionEvent"}`)
	tamperedBody := []byte(`{"action":"created","type":"AgentSessionEvent","injected":true}`)

	validSig := linearSign(secret, body)

	tests := []struct {
		name         string
		body         []byte
		presentedSig string
		wantErr      error
	}{
		{name: "valid signature accepted", body: body, presentedSig: validSig, wantErr: nil},
		{name: "tampered body rejected", body: tamperedBody, presentedSig: validSig, wantErr: platform.ErrInvalidWebhookSignature},
		{name: "wrong secret rejected", body: body, presentedSig: linearSign(otherSecret, body), wantErr: platform.ErrInvalidWebhookSignature},
		{name: "empty signature rejected", body: body, presentedSig: "", wantErr: platform.ErrMalformedWebhookSignature},
		{name: "non-hex signature rejected", body: body, presentedSig: "not-hex!!", wantErr: platform.ErrMalformedWebhookSignature},
		{name: "github-style prefixed signature rejected (Linear has no prefix)", body: body, presentedSig: "sha256=" + validSig, wantErr: platform.ErrMalformedWebhookSignature},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySignatureHeader(secret, tc.body, tc.presentedSig)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("verifySignatureHeader() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("verifySignatureHeader() error = %v, want errors.Is(_, %v)", err, tc.wantErr)
			}
		})
	}
}

// TestSignatureDeliveryEventHeaderExtraction proves the three tiny header
// extraction helpers read the exact header names Linear's own real
// webhook delivery uses (Linear-Signature, Linear-Delivery, Linear-Event
// -- confirmed live against Linear's own current developer docs).
func TestSignatureDeliveryEventHeaderExtraction(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/linear", nil)
	req.Header.Set("Linear-Signature", "abc123")
	req.Header.Set("Linear-Delivery", "234d1a4e-b617-4388-90fe-adc3633d6b72")
	req.Header.Set("Linear-Event", agentSessionEventType)

	if got := signatureHeaderFrom(req); got != "abc123" {
		t.Errorf("signatureHeaderFrom() = %q, want %q", got, "abc123")
	}
	if got := deliveryIDFrom(req); got != "234d1a4e-b617-4388-90fe-adc3633d6b72" {
		t.Errorf("deliveryIDFrom() = %q, want %q", got, "234d1a4e-b617-4388-90fe-adc3633d6b72")
	}
	if got := eventTypeFrom(req); got != agentSessionEventType {
		t.Errorf("eventTypeFrom() = %q, want %q", got, agentSessionEventType)
	}
}
