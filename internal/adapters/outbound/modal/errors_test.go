package modal

import (
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/app/ports"
)

func TestConfigErrors_Error(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "MissingConfigError",
			err:  &MissingConfigError{Field: "BaseURL"},
			want: []string{"BaseURL"},
		},
		{
			name: "InvalidBaseURLError",
			err:  &InvalidBaseURLError{Value: "not-a-url"},
			want: []string{"not-a-url"},
		},
		{
			name: "InvalidEgressProxyURLError",
			err:  &InvalidEgressProxyURLError{Value: "://bad"},
			want: []string{"://bad"},
		},
		{
			name: "ColdStartTimeoutError",
			err: &ColdStartTimeoutError{
				HTTPClientTimeout: 100 * time.Millisecond,
				WorstColdStart:    200 * time.Millisecond,
			},
			want: []string{"100ms", "200ms"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got == "" {
				t.Fatal("Error() returned empty string")
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("Error() = %q, want it to contain %q", got, want)
				}
			}
		})
	}
}

// TestClassifyErrorResponse_NeverEmbedsRawBody proves classifyErrorResponse
// never puts the raw response body bytes into the returned ProviderError.
// SESSION_CONFIG (§6.4) carries a plaintext sandbox bearer token (§5.2), and
// ProviderError.Error() is logged on every transition (§5.3) — a
// validation-failure response that echoes the submitted document back
// (a common API pattern) must never leak that token into a log line.
func TestClassifyErrorResponse_NeverEmbedsRawBody(t *testing.T) {
	const secretToken = "sandbox-token-plaintext-super-secret"

	tests := []struct {
		name string
		body string
	}{
		{
			name: "error envelope with an echoed request field alongside code/message",
			body: `{"error":{"code":"VALIDATION_FAILED","message":"bad request"},"echoedRequest":{"sessionConfig":{"sandboxToken":"` + secretToken + `"}}}`,
		},
		{
			name: "body is not the expected envelope at all",
			body: `{"sandboxToken":"` + secretToken + `"}`,
		},
		{
			name: "body is not valid JSON",
			body: `not json, but contains ` + secretToken,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pe := classifyErrorResponse(ports.OpCreateSandbox, 422, []byte(tt.body))
			if strings.Contains(pe.Error(), secretToken) {
				t.Errorf("ProviderError.Error() = %q, must never contain the raw response body/secret token", pe.Error())
			}
		})
	}
}

// TestClassifyErrorResponse_UsesDecodedMessage proves the provider's own
// decoded Message still reaches the caller for legitimate debugging —
// the fix here is "never raw body bytes," not "no error detail at all."
func TestClassifyErrorResponse_UsesDecodedMessage(t *testing.T) {
	pe := classifyErrorResponse(ports.OpCreateSandbox, 400, []byte(`{"error":{"code":"INVALID_ARGUMENT","message":"image not found"}}`))
	if !strings.Contains(pe.Error(), "image not found") {
		t.Errorf("Error() = %q, want it to contain the decoded message %q", pe.Error(), "image not found")
	}
	if pe.Code != "INVALID_ARGUMENT" {
		t.Errorf("Code = %q, want %q", pe.Code, "INVALID_ARGUMENT")
	}
}

// TestRedactURLCredentials covers both the case url.URL.Redacted already
// handles (a well-formed absolute URL) and the case it does NOT (a
// missing scheme, which is exactly what trips InvalidBaseURLError /
// InvalidEgressProxyURLError in the first place — see redactURLCredentials's
// own doc comment).
func TestRedactURLCredentials(t *testing.T) {
	tests := []struct {
		name          string
		in            string
		wantRedacted  string
		wantNoRawPass string
	}{
		{
			name:          "absolute URL with credentials",
			in:            "http://user:hunter2@proxy.internal:3128",
			wantRedacted:  "http://user:xxxxx@proxy.internal:3128",
			wantNoRawPass: "hunter2",
		},
		{
			name:          "missing scheme with credentials (the exact malformed shape that triggers these errors)",
			in:            "user:hunter2@proxy.internal:3128",
			wantRedacted:  "user:xxxxx@proxy.internal:3128",
			wantNoRawPass: "hunter2",
		},
		{
			name:          "no credentials present",
			in:            "http://proxy.internal:3128",
			wantRedacted:  "http://proxy.internal:3128",
			wantNoRawPass: "hunter2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactURLCredentials(tt.in)
			if got != tt.wantRedacted {
				t.Errorf("redactURLCredentials(%q) = %q, want %q", tt.in, got, tt.wantRedacted)
			}
			if strings.Contains(got, tt.wantNoRawPass) {
				t.Errorf("redactURLCredentials(%q) = %q, must not contain the raw password %q", tt.in, got, tt.wantNoRawPass)
			}
		})
	}
}
