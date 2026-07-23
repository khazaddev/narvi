package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signRequest mirrors Slack's own real "v0:{timestamp}:{body}" HMAC-SHA256
// signing scheme (confirmed against Slack's own current documentation --
// see doc.go's own step 2 / mrkdwn.go's own analogous research note) and
// sets the two headers a real Slack request carries.
func signRequest(t *testing.T, req *http.Request, secret string, ts int64, body []byte) {
	t.Helper()
	signedPayload := "v0:" + strconv.FormatInt(ts, 10) + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signedPayload))
	sig := "v0=" + hex.EncodeToString(mac.Sum(nil))

	req.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-Slack-Signature", sig)
}

// newSignedRequest builds a POST request carrying body, signed with
// secret at timestamp ts.
func newSignedRequest(t *testing.T, secret string, ts int64, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/slack", strings.NewReader(body))
	signRequest(t, req, secret, ts, []byte(body))
	return req
}

// TestNewHandler_SignatureVerification is table-driven over valid/
// tampered/expired signed requests -- mirrors webhooksig_test.go's own
// Slack-shaped table exactly, at the full HTTP-handler level (this
// package's own doc.go step 2/3). None of these cases ever reach a real
// store (every one fails, or is the url_verification handshake, before
// WebhookDeliveryStore.Claim), so nil stores are safe here.
func TestNewHandler_SignatureVerification(t *testing.T) {
	const secret = "test-signing-secret"
	const window = 5 * time.Minute
	now := time.Now()

	handler := NewHandler(Deps{
		SigningSecret:   secret,
		TimestampWindow: window,
	})

	body := `{"type":"url_verification","challenge":"abc123","token":"xyz"}`

	tests := []struct {
		name       string
		mutate     func(req *http.Request)
		wantStatus int
	}{
		{
			name:       "valid signature and fresh timestamp accepted",
			mutate:     func(_ *http.Request) {},
			wantStatus: http.StatusOK,
		},
		{
			name: "tampered signature rejected",
			mutate: func(req *http.Request) {
				sig := req.Header.Get("X-Slack-Signature")
				req.Header.Set("X-Slack-Signature", sig[:len(sig)-1]+"0")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong secret rejected",
			mutate: func(req *http.Request) {
				ts := req.Header.Get("X-Slack-Request-Timestamp")
				tsInt, _ := strconv.ParseInt(ts, 10, 64)
				signRequest(t, req, "a-completely-different-secret", tsInt, []byte(body))
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "missing signature header rejected",
			mutate: func(req *http.Request) {
				req.Header.Del("X-Slack-Signature")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "missing timestamp header rejected",
			mutate: func(req *http.Request) {
				req.Header.Del("X-Slack-Request-Timestamp")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "expired timestamp rejected",
			mutate: func(req *http.Request) {
				staleTS := now.Add(-window).Add(-1 * time.Minute).Unix()
				signRequest(t, req, secret, staleTS, []byte(body))
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newSignedRequest(t, secret, now.Unix(), body)
			tc.mutate(req)

			rec := httptest.NewRecorder()
			handler(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body=%s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// TestNewHandler_URLVerification proves the url_verification handshake
// is handled by echoing the challenge back as JSON, 200, without ever
// reaching a real store (Deliveries/Threads/etc. are all nil here --
// a nil-pointer panic on any of them would fail this test just as surely
// as a wrong status/body would).
func TestNewHandler_URLVerification(t *testing.T) {
	const secret = "test-signing-secret"
	handler := NewHandler(Deps{
		SigningSecret:   secret,
		TimestampWindow: 5 * time.Minute,
	})

	body := `{"type":"url_verification","challenge":"3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P","token":"xyz"}`
	req := newSignedRequest(t, secret, time.Now().Unix(), body)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, "3eZbrw1aBm2rZgRNFdxV2595E9CY3gmdALWMmHkvFXO7tYXAYM8P") {
		t.Errorf("body = %q, want it to contain the echoed challenge", got)
	}
}
