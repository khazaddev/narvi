package rwx

import (
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/app/ports"
)

func TestMissingConfigError_Error(t *testing.T) {
	err := &MissingConfigError{Field: "AccessToken"}
	if !strings.Contains(err.Error(), "AccessToken") {
		t.Errorf("Error() = %q, want it to contain %q", err.Error(), "AccessToken")
	}
}

// --- CLI-path classification ---

func TestClassifyCLIError_ProcessNeverCompleted(t *testing.T) {
	underlying := errors.New("exec: \"rwx\": executable file not found in $PATH")
	pe := classifyCLIError(ports.OpCreateSandbox, -1, nil, nil, underlying)

	if !pe.Transient {
		t.Error("Transient = false, want true (a process-level failure is always transient)")
	}
	if pe.Code != "PROCESS_ERROR" {
		t.Errorf("Code = %q, want %q", pe.Code, "PROCESS_ERROR")
	}
	if pe.Op != ports.OpCreateSandbox {
		t.Errorf("Op = %q, want %q", pe.Op, ports.OpCreateSandbox)
	}
	if !errors.Is(pe.Err, underlying) {
		t.Error("Err does not wrap the underlying process error")
	}
}

// TestClassifyCLIError_EveryNonzeroExitDefaultsTransient proves §4.1.3's
// own "no published error-code table... unknown -> transient" rule: since
// no real pinned `rwx` binary is reachable from this codebase to observe
// a real exit-code taxonomy from, EVERY nonzero exit classifies transient
// today -- this table is where a future real-binary-verified permanent
// exit code would get its own case (classifyCLIError's own top comment).
func TestClassifyCLIError_EveryNonzeroExitDefaultsTransient(t *testing.T) {
	for _, exitCode := range []int{1, 2, 127, 255} {
		pe := classifyCLIError(ports.OpStopSandbox, exitCode, nil, nil, nil)
		if !pe.Transient {
			t.Errorf("exit %d: Transient = false, want true (unpinned exit code defaults to transient)", exitCode)
		}
		if pe.Code != "exit_"+strconv.Itoa(exitCode) {
			t.Errorf("exit %d: Code = %q, want %q", exitCode, pe.Code, "exit_"+strconv.Itoa(exitCode))
		}
	}
}

func TestClassifyCLIError_DecodesEnvelopeMessage(t *testing.T) {
	stdout := []byte(`{"status":"error","error":"sandbox not found"}`)
	pe := classifyCLIError(ports.OpStopSandbox, 1, stdout, nil, nil)
	if !strings.Contains(pe.Error(), "sandbox not found") {
		t.Errorf("Error() = %q, want it to contain the decoded envelope message", pe.Error())
	}
}

// TestClassifyCLIError_NeverEmbedsRawStdoutStderr proves classifyCLIError
// never puts raw stdout/stderr bytes into the returned error -- SESSION_CONFIG
// (§6.4) carries a plaintext sandbox bearer token (§5.2), and
// ProviderError.Error() is logged on every transition (§5.3): a CLI that
// echoes its own invalid input back on a validation failure must never
// leak that token into a log line, mirroring modal's identical
// TestClassifyErrorResponse_NeverEmbedsRawBody discipline.
func TestClassifyCLIError_NeverEmbedsRawStdoutStderr(t *testing.T) {
	const secretToken = "sandbox-token-plaintext-super-secret"

	tests := []struct {
		name   string
		stdout []byte
		stderr []byte
	}{
		{
			name:   "envelope with an echoed field alongside error",
			stdout: []byte(`{"status":"error","error":"validation failed","echoedSessionConfig":{"sandboxToken":"` + secretToken + `"}}`),
		},
		{
			name:   "stdout is not the expected envelope at all",
			stdout: []byte(`{"sandboxToken":"` + secretToken + `"}`),
		},
		{
			name:   "stdout is not valid JSON, secret only in stderr",
			stdout: []byte(`not json`),
			stderr: []byte(`fatal: config contains ` + secretToken),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pe := classifyCLIError(ports.OpCreateSandbox, 1, tt.stdout, tt.stderr, nil)
			if strings.Contains(pe.Error(), secretToken) {
				t.Errorf("Error() = %q, must never contain the raw secret token", pe.Error())
			}
		})
	}
}

// --- Dispatches-API-path classification ---

func TestIsTransientDispatchStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   bool
	}{
		{"500 internal server error", http.StatusInternalServerError, true},
		{"502 bad gateway", http.StatusBadGateway, true},
		{"429 too many requests", http.StatusTooManyRequests, true},
		{"400 bad request", http.StatusBadRequest, false},
		{"401 unauthorized", http.StatusUnauthorized, false},
		{"403 forbidden", http.StatusForbidden, false},
		{"404 not found", http.StatusNotFound, false},
		{"409 conflict", http.StatusConflict, false},
		{"422 unprocessable entity", http.StatusUnprocessableEntity, false},
		{"418 unknown/unclassifiable defaults to transient", http.StatusTeapot, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientDispatchStatus(tt.status); got != tt.want {
				t.Errorf("isTransientDispatchStatus(%d) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestClassifyDispatchErrorResponse(t *testing.T) {
	de := classifyDispatchErrorResponse(http.StatusUnauthorized, []byte(`{"status":"error","error":"invalid token"}`))
	if de.Transient {
		t.Error("Transient = true, want false (401 is permanent)")
	}
	if !strings.Contains(de.Error(), "invalid token") {
		t.Errorf("Error() = %q, want it to contain the decoded message", de.Error())
	}
	if de.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want %d", de.Status, http.StatusUnauthorized)
	}
}

func TestClassifyDispatchErrorResponse_MalformedBody(t *testing.T) {
	de := classifyDispatchErrorResponse(http.StatusInternalServerError, []byte(`not json`))
	if !de.Transient {
		t.Error("Transient = false, want true (500 is transient regardless of body)")
	}
}

func TestClassifyDispatchNetworkError(t *testing.T) {
	de := classifyDispatchNetworkError(errFakeTimeoutNetError{})
	if !de.Transient {
		t.Error("Transient = false, want true (every network-level failure is transient, §4.1)")
	}
}

// errFakeTimeoutNetError is a minimal net.Error double for
// TestClassifyDispatchNetworkError.
type errFakeTimeoutNetError struct{}

func (errFakeTimeoutNetError) Error() string   { return "fake timeout" }
func (errFakeTimeoutNetError) Timeout() bool   { return true }
func (errFakeTimeoutNetError) Temporary() bool { return true }

var _ net.Error = errFakeTimeoutNetError{}
