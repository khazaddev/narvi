package modal

import (
	"errors"
	"net/http"
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

// TestIsCacheMountTrouble covers isCacheMountTrouble's own three signals
// (errors.go's doc comment has the full reasoning) — the audit-remediation
// finding this test is written against: "the cold-build fallback fires
// only on three structured provider error codes... a degraded volume
// subsystem typically hangs... breaking the pure-accelerator rule the port
// states absolutely." Table-driven, one entry per signal plus the guard
// that must survive broadening it.
func TestIsCacheMountTrouble(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error is never cache trouble",
			err:  nil,
			want: false,
		},
		{
			name: "not a ProviderError at all",
			err:  errors.New("some unrelated error"),
			want: false,
		},
		{
			name: "structured CACHE_MOUNT_CORRUPTED code",
			err:  &ports.ProviderError{Transient: false, Code: "CACHE_MOUNT_CORRUPTED", Op: ports.OpBuildImage, Err: errors.New("corrupted")},
			want: true,
		},
		{
			name: "structured CACHE_MOUNT_LOCKED code",
			err:  &ports.ProviderError{Transient: false, Code: "CACHE_MOUNT_LOCKED", Op: ports.OpBuildImage, Err: errors.New("locked")},
			want: true,
		},
		{
			name: "structured CACHE_MOUNT_UNAVAILABLE code",
			err:  &ports.ProviderError{Transient: false, Code: "CACHE_MOUNT_UNAVAILABLE", Op: ports.OpBuildImage, Err: errors.New("unavailable")},
			want: true,
		},
		{
			name: "transport-level NETWORK_ERROR (connection never established)",
			err:  classifyNetworkError(ports.OpBuildImage, errors.New("dial tcp: connection refused")),
			want: true,
		},
		{
			name: "transport-level NETWORK_TIMEOUT (a degraded cache volume subsystem typically hangs, not errors cleanly)",
			err:  classifyNetworkError(ports.OpBuildImage, &timeoutError{}),
			want: true,
		},
		{
			name: "unparseable body on a transient 503 (e.g. a plain-text upstream error page)",
			err:  classifyErrorResponse(ports.OpBuildImage, http.StatusServiceUnavailable, []byte("<html>Service Unavailable</html>")),
			want: true,
		},
		{
			name: "empty body on a transient 500",
			err:  classifyErrorResponse(ports.OpBuildImage, http.StatusInternalServerError, nil),
			want: true,
		},
		{
			name: "unparseable body on a PERMANENT 422 must NOT be treated as cache trouble (guard against masking a real, non-retryable rejection)",
			err:  classifyErrorResponse(ports.OpBuildImage, http.StatusUnprocessableEntity, []byte("not json at all")),
			want: false,
		},
		{
			name: "recognized, structured, non-cache build-failure code must NOT be treated as cache trouble even on a transient status",
			err:  classifyErrorResponse(ports.OpBuildImage, http.StatusInternalServerError, []byte(`{"error":{"code":"INTERNAL_BUILD_ERROR","message":"the build sandbox itself crashed"}}`)),
			want: false,
		},
		{
			name: "recognized SETUP_SCRIPT_FAILED on a permanent 422 must NOT be treated as cache trouble (the exact 'genuine build defect' case the guard exists for)",
			err:  classifyErrorResponse(ports.OpBuildImage, http.StatusUnprocessableEntity, []byte(`{"error":{"code":"SETUP_SCRIPT_FAILED","message":"setup.sh exited 1"}}`)),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := isCacheMountTrouble(tt.err); got != tt.want {
				t.Errorf("isCacheMountTrouble(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// timeoutError is a minimal net.Error stand-in whose Timeout() reports
// true, so classifyNetworkError classifies it as networkTimeoutCode
// exactly the way http.Client reports a client-side deadline exceeded —
// without needing a real network round trip in this unit test.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

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
