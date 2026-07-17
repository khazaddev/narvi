package platform_test

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/platform"
)

// TestVerify is table-driven over the sentinel-error cases hmacauth.go
// specifies: valid round-trip, tampered payload, tampered signature,
// expired, and malformed token strings (no ".", empty, non-hex signature).
// Each failure case must return the SPECIFIC expected sentinel via
// errors.Is, not just "some error".
func TestVerify(t *testing.T) {
	const window = 5 * time.Minute // mirrors §5.2's 5-min window; a plain
	// literal is fine in a _test.go file (notimeliteral only forbids
	// time.Duration unit literals outside internal/platform and tests).

	secret := []byte("sandbox-to-cp-secret")
	otherSecret := []byte("a-completely-different-secret")
	payload := "session-id:abc123"
	signAt := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)

	validToken := platform.Sign(secret, payload, signAt)

	tests := []struct {
		name     string
		secret   []byte
		token    string
		payload  string
		verifyAt time.Time
		window   time.Duration
		wantErr  error
	}{
		{
			name:     "valid round-trip",
			secret:   secret,
			token:    validToken,
			payload:  payload,
			verifyAt: signAt,
			window:   window,
			wantErr:  nil,
		},
		{
			name:     "valid round-trip, still within window",
			secret:   secret,
			token:    validToken,
			payload:  payload,
			verifyAt: signAt.Add(window),
			window:   window,
			wantErr:  nil,
		},
		{
			name:     "tampered payload",
			secret:   secret,
			token:    validToken,
			payload:  payload + "-tampered",
			verifyAt: signAt,
			window:   window,
			wantErr:  platform.ErrInvalidHMACSignature,
		},
		{
			name:     "wrong secret (equivalent to a tampered signature)",
			secret:   otherSecret,
			token:    validToken,
			payload:  payload,
			verifyAt: signAt,
			window:   window,
			wantErr:  platform.ErrInvalidHMACSignature,
		},
		{
			name:     "tampered signature (flipped hex char)",
			secret:   secret,
			token:    flipLastHexChar(t, validToken),
			payload:  payload,
			verifyAt: signAt,
			window:   window,
			wantErr:  platform.ErrInvalidHMACSignature,
		},
		{
			name:     "expired (now - window - 1s)",
			secret:   secret,
			token:    validToken,
			payload:  payload,
			verifyAt: signAt.Add(window).Add(1 * time.Second),
			window:   window,
			wantErr:  platform.ErrExpiredHMACToken,
		},
		{
			name:     "expired, clock went backwards past the window",
			secret:   secret,
			token:    validToken,
			payload:  payload,
			verifyAt: signAt.Add(-window).Add(-1 * time.Second),
			window:   window,
			wantErr:  platform.ErrExpiredHMACToken,
		},
		{
			name:     "malformed: no separator",
			secret:   secret,
			token:    "not-a-valid-token-at-all",
			payload:  payload,
			verifyAt: signAt,
			window:   window,
			wantErr:  platform.ErrMalformedHMACToken,
		},
		{
			name:     "malformed: empty token",
			secret:   secret,
			token:    "",
			payload:  payload,
			verifyAt: signAt,
			window:   window,
			wantErr:  platform.ErrMalformedHMACToken,
		},
		{
			name:     "malformed: empty timestamp half",
			secret:   secret,
			token:    ".deadbeef",
			payload:  payload,
			verifyAt: signAt,
			window:   window,
			wantErr:  platform.ErrMalformedHMACToken,
		},
		{
			name:     "malformed: empty signature half",
			secret:   secret,
			token:    "1234567890.",
			payload:  payload,
			verifyAt: signAt,
			window:   window,
			wantErr:  platform.ErrMalformedHMACToken,
		},
		{
			name:     "malformed: non-numeric timestamp",
			secret:   secret,
			token:    "not-a-number.deadbeef",
			payload:  payload,
			verifyAt: signAt,
			window:   window,
			wantErr:  platform.ErrMalformedHMACToken,
		},
		{
			name:     "malformed: non-hex signature",
			secret:   secret,
			token:    "1234567890.not-hex-zzzz",
			payload:  payload,
			verifyAt: signAt,
			window:   window,
			wantErr:  platform.ErrMalformedHMACToken,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := platform.Verify(tc.secret, tc.token, tc.payload, tc.verifyAt, tc.window)

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Verify() = %v, want nil", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("Verify() = nil, want error satisfying errors.Is(err, %v)", tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Verify() = %v, want errors.Is(err, %v)", err, tc.wantErr)
			}
		})
	}
}

// TestSignDeterministicPrefix confirms Sign's output shape:
// "{unixSeconds}.{hexSignature}", so Verify's own parsing assumptions
// (strings.Cut on the first ".") are exercised against real Sign output,
// not just hand-built fixtures.
func TestSignDeterministicPrefix(t *testing.T) {
	secret := []byte("some-secret")
	now := time.Date(2026, time.July, 17, 0, 0, 0, 0, time.UTC)

	token := platform.Sign(secret, "payload", now)
	wantPrefix := strconv.FormatInt(now.Unix(), 10) + "."

	if len(token) <= len(wantPrefix) || token[:len(wantPrefix)] != wantPrefix {
		t.Fatalf("Sign() = %q, want prefix %q", token, wantPrefix)
	}
}

// flipLastHexChar mutates the last character of token's hex signature so
// the token still parses (still "timestamp.hex") but the signature no
// longer matches — proving Verify distinguishes a structurally well-formed
// but tampered signature (ErrInvalidHMACSignature) from a malformed one
// (ErrMalformedHMACToken).
func flipLastHexChar(t *testing.T, token string) string {
	t.Helper()
	if len(token) == 0 {
		t.Fatal("flipLastHexChar: empty token")
	}
	last := token[len(token)-1]
	flipped := byte('0')
	if last == '0' {
		flipped = '1'
	}
	return token[:len(token)-1] + string(flipped)
}
