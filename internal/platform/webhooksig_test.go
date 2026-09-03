package platform_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/narvidev/narvi/internal/platform"
)

// githubSign mirrors GitHub's own "X-Hub-Signature-256: sha256=<hex>"
// scheme (§ GitHub webhooks docs): HMAC-SHA256 over the raw body, no
// timestamp involved at all.
func githubSign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// slackSign mirrors Slack's own "X-Slack-Signature: v0=<hex>" scheme:
// HMAC-SHA256 over "v0:{timestamp}:{raw body}", timestamp carried in a
// separate header (here just folded into the assembled signed string,
// exactly like a real Slack adapter would).
func slackSign(secret []byte, ts int64, body []byte) string {
	signedPayload := "v0:" + itoa(ts) + ":" + string(body)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signedPayload))
	return hex.EncodeToString(mac.Sum(nil))
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}

// TestVerifyWebhookSignature is table-driven over GitHub-shaped (simple
// raw-body HMAC, no timestamp) and Slack-shaped (timestamp-prefixed
// signed payload) usage: valid signature accepted, tampered body
// rejected, tampered/wrong-secret signature rejected, malformed/missing
// signature rejected -- proving one generic helper expresses both
// providers' schemes without either rewriting it.
func TestVerifyWebhookSignature(t *testing.T) {
	secret := []byte("gh-or-slack-webhook-secret")
	otherSecret := []byte("a-completely-different-secret")
	body := []byte(`{"action":"opened","number":42}`)
	ts := int64(1_800_000_000)

	githubSig := githubSign(secret, body)
	slackSig := slackSign(secret, ts, body)
	slackSignedPayload := []byte("v0:" + itoa(ts) + ":" + string(body))

	tests := []struct {
		name          string
		secret        []byte
		signedPayload []byte
		presentedSig  string
		wantErr       error
	}{
		{
			name:          "github-shaped: valid signature accepted",
			secret:        secret,
			signedPayload: body,
			presentedSig:  githubSig,
			wantErr:       nil,
		},
		{
			name:          "github-shaped: tampered body rejected",
			secret:        secret,
			signedPayload: append([]byte(nil), append(body, '!')...),
			presentedSig:  githubSig,
			wantErr:       platform.ErrInvalidWebhookSignature,
		},
		{
			name:          "github-shaped: wrong secret rejected",
			secret:        otherSecret,
			signedPayload: body,
			presentedSig:  githubSig,
			wantErr:       platform.ErrInvalidWebhookSignature,
		},
		{
			name:          "github-shaped: tampered signature rejected",
			secret:        secret,
			signedPayload: body,
			presentedSig:  flipLastHexCharWebhook(t, githubSig),
			wantErr:       platform.ErrInvalidWebhookSignature,
		},
		{
			name:          "slack-shaped: valid signature accepted",
			secret:        secret,
			signedPayload: slackSignedPayload,
			presentedSig:  slackSig,
			wantErr:       nil,
		},
		{
			name:          "slack-shaped: tampered timestamp rejected",
			secret:        secret,
			signedPayload: []byte("v0:" + itoa(ts+1) + ":" + string(body)),
			presentedSig:  slackSig,
			wantErr:       platform.ErrInvalidWebhookSignature,
		},
		{
			name:          "slack-shaped: tampered body rejected",
			secret:        secret,
			signedPayload: []byte("v0:" + itoa(ts) + ":" + string(body) + "!"),
			presentedSig:  slackSig,
			wantErr:       platform.ErrInvalidWebhookSignature,
		},
		{
			name:          "malformed: empty signature",
			secret:        secret,
			signedPayload: body,
			presentedSig:  "",
			wantErr:       platform.ErrMalformedWebhookSignature,
		},
		{
			name:          "malformed: whitespace-only signature",
			secret:        secret,
			signedPayload: body,
			presentedSig:  "   ",
			wantErr:       platform.ErrMalformedWebhookSignature,
		},
		{
			name:          "malformed: non-hex signature",
			secret:        secret,
			signedPayload: body,
			presentedSig:  "not-hex-zzzz",
			wantErr:       platform.ErrMalformedWebhookSignature,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := platform.VerifyWebhookSignature(tc.secret, tc.signedPayload, tc.presentedSig)

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("VerifyWebhookSignature() = %v, want nil", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("VerifyWebhookSignature() = nil, want error satisfying errors.Is(err, %v)", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("VerifyWebhookSignature() = %v, want errors.Is(err, %v)", err, tc.wantErr)
			}
		})
	}
}

// TestVerifyWebhookTimestamp covers the Slack-shaped freshness-window
// case: a timestamp within window is accepted, one outside it (a
// possible replay) is rejected via the distinct
// ErrExpiredWebhookTimestamp sentinel -- checked independently of
// signature validity, matching Slack's own guidance to verify both.
func TestVerifyWebhookTimestamp(t *testing.T) {
	const window = 5 * time.Minute // a plain literal is fine in a
	// _test.go file (notimeliteral only forbids time.Duration unit
	// literals outside internal/platform and tests).

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name    string
		ts      int64
		now     time.Time
		window  time.Duration
		wantErr error
	}{
		{
			name:    "exactly now: fresh",
			ts:      now.Unix(),
			now:     now,
			window:  window,
			wantErr: nil,
		},
		{
			name:    "within window: fresh",
			ts:      now.Add(-window / 2).Unix(),
			now:     now,
			window:  window,
			wantErr: nil,
		},
		{
			name:    "exactly at window boundary: fresh",
			ts:      now.Add(-window).Unix(),
			now:     now,
			window:  window,
			wantErr: nil,
		},
		{
			name:    "expired: older than window",
			ts:      now.Add(-window).Add(-1 * time.Second).Unix(),
			now:     now,
			window:  window,
			wantErr: platform.ErrExpiredWebhookTimestamp,
		},
		{
			name:    "expired: clock skew, timestamp from the future past the window",
			ts:      now.Add(window).Add(1 * time.Second).Unix(),
			now:     now,
			window:  window,
			wantErr: platform.ErrExpiredWebhookTimestamp,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := platform.VerifyWebhookTimestamp(tc.ts, tc.now, tc.window)

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("VerifyWebhookTimestamp() = %v, want nil", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("VerifyWebhookTimestamp() = nil, want error satisfying errors.Is(err, %v)", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("VerifyWebhookTimestamp() = %v, want errors.Is(err, %v)", err, tc.wantErr)
			}
		})
	}
}

// flipLastHexCharWebhook mutates the last character of sig so it still
// decodes as valid hex (still structurally well-formed) but no longer
// matches -- proving VerifyWebhookSignature distinguishes a tampered
// signature (ErrInvalidWebhookSignature) from a malformed one
// (ErrMalformedWebhookSignature).
func flipLastHexCharWebhook(t *testing.T, sig string) string {
	t.Helper()
	if len(sig) == 0 {
		t.Fatal("flipLastHexCharWebhook: empty signature")
	}
	last := sig[len(sig)-1]
	flipped := byte('0')
	if last == '0' {
		flipped = '1'
	}
	return sig[:len(sig)-1] + string(flipped)
}
