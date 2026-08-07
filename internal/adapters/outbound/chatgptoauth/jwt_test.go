package chatgptoauth

import (
	"encoding/base64"
	"testing"
)

// fakeJWT builds a syntactically-real JWT (header.payload.signature) with
// an arbitrary payload -- decodeIDTokenAccountID never checks the
// signature (see its own doc comment), so "sig" here is just a
// placeholder segment proving the 3-segment split logic itself, not a
// real cryptographic signature.
func fakeJWT(payloadJSON string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	return header + "." + payload + ".sig"
}

func TestDecodeIDTokenAccountID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		token   string
		want    string
		wantErr bool
	}{
		{
			name:  "real-shaped token carries the claim",
			token: fakeJWT(`{"sub":"user-123","chatgpt_account_id":"acct-abc-789","aud":"app_EMoamEEZ73f0CkXaXp7hrann"}`),
			want:  "acct-abc-789",
		},
		{
			name:    "wrong segment count",
			token:   "not.a.jwt.at.all",
			wantErr: true,
		},
		{
			name:    "not base64url",
			token:   "!!!.!!!.!!!",
			wantErr: true,
		},
		{
			name:    "payload is not valid JSON",
			token:   base64.RawURLEncoding.EncodeToString([]byte(`{}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`not json`)) + ".sig",
			wantErr: true,
		},
		{
			name:    "claim present but empty",
			token:   fakeJWT(`{"chatgpt_account_id":""}`),
			wantErr: true,
		},
		{
			name:    "claim entirely absent",
			token:   fakeJWT(`{"sub":"user-123"}`),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := decodeIDTokenAccountID(tc.token)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decodeIDTokenAccountID(%q) error = nil, want non-nil", tc.token)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeIDTokenAccountID(%q) error = %v, want nil", tc.token, err)
			}
			if got != tc.want {
				t.Errorf("decodeIDTokenAccountID(%q) = %q, want %q", tc.token, got, tc.want)
			}
		})
	}
}
