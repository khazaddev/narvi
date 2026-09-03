package objstore

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/narvidev/narvi/internal/app/ports"
)

func TestConfigErrors_Error(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want []string
	}{
		{
			name: "MissingConfigError",
			err:  &MissingConfigError{Field: "Endpoint"},
			want: []string{"Endpoint"},
		},
		{
			name: "DefaultCredentialsError",
			err:  &DefaultCredentialsError{Err: errors.New("bad shared config file")},
			want: []string{"bad shared config file"},
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

func TestDefaultCredentialsError_Unwrap(t *testing.T) {
	sentinel := errors.New("underlying failure")
	err := &DefaultCredentialsError{Err: sentinel}
	if !errors.Is(err, sentinel) {
		t.Error("errors.Is(err, sentinel) = false, want true (Unwrap should expose Err)")
	}
}

// --- isTransientStatus: the classification table itself, in isolation. ---

func TestIsTransientStatus(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		wantTransient bool
	}{
		{"400 bad request", http.StatusBadRequest, false},
		{"401 unauthorized", http.StatusUnauthorized, false},
		{"403 forbidden", http.StatusForbidden, false},
		{"404 not found", http.StatusNotFound, false},
		{"409 conflict", http.StatusConflict, false},
		{"413 request entity too large", http.StatusRequestEntityTooLarge, false},
		{"422 unprocessable entity", http.StatusUnprocessableEntity, false},
		{"429 too many requests", http.StatusTooManyRequests, true},
		{"500 internal server error", http.StatusInternalServerError, true},
		{"503 service unavailable", http.StatusServiceUnavailable, true},
		{"418 unrecognized code defaults to transient", http.StatusTeapot, true},
		{"402 unrecognized code defaults to transient", http.StatusPaymentRequired, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransientStatus(tt.status); got != tt.wantTransient {
				t.Errorf("isTransientStatus(%d) = %v, want %v", tt.status, got, tt.wantTransient)
			}
		})
	}
}

// --- isNotFoundError / classify: pure unit tests against synthetic
// smithy-go error values (no real network, no httptest server) --
// see store_test.go for the end-to-end equivalent exercised through real
// Stat/Delete calls against an httptest.Server, which is what actually
// proves this adapter's real SDK integration produces these shapes. ---

func responseErrorWithStatus(status int, wrapped error) *smithyhttp.ResponseError {
	return &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      wrapped,
	}
}

func TestIsNotFoundError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"types.NotFound (HeadObject's own synthesized shape)", &types.NotFound{}, true},
		{"types.NoSuchKey (GetObject's own shape)", &types.NoSuchKey{}, true},
		{"generic ResponseError with 404 status (DeleteObject's own empirical shape)", responseErrorWithStatus(http.StatusNotFound, errors.New("not found")), true},
		{"ResponseError with 403 status", responseErrorWithStatus(http.StatusForbidden, errors.New("forbidden")), false},
		{"unrelated plain error", errors.New("boom"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNotFoundError(tt.err); got != tt.want {
				t.Errorf("isNotFoundError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassify_ResponseError(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		wantTransient bool
	}{
		{"400", http.StatusBadRequest, false},
		{"403", http.StatusForbidden, false},
		{"409", http.StatusConflict, false},
		{"413", http.StatusRequestEntityTooLarge, false},
		{"429", http.StatusTooManyRequests, true},
		{"500", http.StatusInternalServerError, true},
		{"503", http.StatusServiceUnavailable, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := responseErrorWithStatus(tt.status, errors.New("wrapped"))
			be := classify(ports.BlobOpStat, err)
			if be.Transient != tt.wantTransient {
				t.Errorf("classify status %d: Transient = %v, want %v", tt.status, be.Transient, tt.wantTransient)
			}
			if be.Op != ports.BlobOpStat {
				t.Errorf("classify status %d: Op = %q, want %q", tt.status, be.Op, ports.BlobOpStat)
			}
			if got := ports.IsBlobStoreTransient(be); got != tt.wantTransient {
				t.Errorf("ports.IsBlobStoreTransient = %v, want %v", got, tt.wantTransient)
			}
		})
	}
}

func TestClassify_NetworkError(t *testing.T) {
	sendErr := &smithyhttp.RequestSendError{Err: errors.New("dial tcp: connection refused")}
	be := classify(ports.BlobOpDelete, sendErr)
	if !be.Transient {
		t.Error("classify(RequestSendError) Transient = false, want true")
	}
	if be.Code != "NETWORK_ERROR" {
		t.Errorf("classify(RequestSendError) Code = %q, want %q", be.Code, "NETWORK_ERROR")
	}
	if be.Op != ports.BlobOpDelete {
		t.Errorf("classify(RequestSendError) Op = %q, want %q", be.Op, ports.BlobOpDelete)
	}
}

// timeoutError is a minimal net.Error-shaped fake used only to prove
// networkErrorCode's structural (never string-matched) Timeout() check.
type timeoutError struct{ msg string }

func (e *timeoutError) Error() string   { return e.msg }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

func TestClassify_NetworkTimeout(t *testing.T) {
	sendErr := &smithyhttp.RequestSendError{Err: &timeoutError{msg: "context deadline exceeded"}}
	be := classify(ports.BlobOpStat, sendErr)
	if !be.Transient {
		t.Error("classify(RequestSendError wrapping a timeout) Transient = false, want true")
	}
	if be.Code != "NETWORK_TIMEOUT" {
		t.Errorf("classify(RequestSendError wrapping a timeout) Code = %q, want %q", be.Code, "NETWORK_TIMEOUT")
	}
}

func TestClassify_UnrecognizedErrorShape(t *testing.T) {
	be := classify(ports.BlobOpStat, errors.New("something the SDK never actually returns"))
	if !be.Transient {
		t.Error("classify(unrecognized error) Transient = false, want true (§3.2: unknown defaults transient)")
	}
	if be.Code != "UNKNOWN_ERROR" {
		t.Errorf("classify(unrecognized error) Code = %q, want %q", be.Code, "UNKNOWN_ERROR")
	}
}

func TestPresignError_AlwaysPermanent(t *testing.T) {
	// §28.1: "a presign cannot meaningfully fail transiently" -- unlike
	// classify, presignError never inspects err's shape at all.
	for _, err := range []error{
		errors.New("plain error"),
		&smithyhttp.RequestSendError{Err: errors.New("would be transient via classify")},
		responseErrorWithStatus(http.StatusInternalServerError, errors.New("would be transient via classify")),
	} {
		be := presignError(ports.BlobOpPresignPut, err)
		if be.Transient {
			t.Errorf("presignError(%v).Transient = true, want false (always permanent)", err)
		}
		if be.Code != "PRESIGN_ERROR" {
			t.Errorf("presignError(%v).Code = %q, want %q", err, be.Code, "PRESIGN_ERROR")
		}
	}
}
