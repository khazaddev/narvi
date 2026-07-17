package ports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestIsTransient(t *testing.T) {
	sentinel := errors.New("boom")

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "provider error transient true",
			err:  &ProviderError{Transient: true, Code: "UNAVAILABLE", Op: OpCreateSandbox, Err: sentinel},
			want: true,
		},
		{
			name: "provider error transient false",
			err:  &ProviderError{Transient: false, Code: "BAD_REQUEST", Op: OpCreateSandbox, Err: sentinel},
			want: false,
		},
		{
			name: "non-provider error defaults to transient",
			err:  sentinel,
			want: true,
		},
		{
			name: "wrapped provider error (transient) via fmt.Errorf %w",
			err:  fmt.Errorf("dispatch: %w", &ProviderError{Transient: true, Op: OpList, Err: sentinel}),
			want: true,
		},
		{
			name: "wrapped provider error (permanent) via fmt.Errorf %w",
			err:  fmt.Errorf("dispatch: %w", &ProviderError{Transient: false, Op: OpList, Err: sentinel}),
			want: false,
		},
		{
			name: "nil error is not transient",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsTransient(tt.err); got != tt.want {
				t.Errorf("IsTransient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestProviderError_Error(t *testing.T) {
	sentinel := errors.New("connection refused")
	err := &ProviderError{
		Transient: true,
		Code:      "UNAVAILABLE",
		Op:        OpCreateSandbox,
		Err:       sentinel,
	}

	got := err.Error()
	for _, want := range []string{string(OpCreateSandbox), "transient", "UNAVAILABLE", "connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}

	permanent := &ProviderError{Transient: false, Code: "BAD_REQUEST", Op: OpStopSandbox, Err: sentinel}
	if !strings.Contains(permanent.Error(), "permanent") {
		t.Errorf("Error() = %q, want it to contain %q", permanent.Error(), "permanent")
	}
}

func TestProviderError_Unwrap(t *testing.T) {
	sentinel := errors.New("underlying failure")
	err := &ProviderError{Transient: true, Op: OpCreateSandbox, Err: sentinel}

	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(err, sentinel) = false, want true (Unwrap should expose Err)")
	}

	var pe *ProviderError
	if !errors.As(fmt.Errorf("wrap: %w", err), &pe) {
		t.Fatal("errors.As failed to find wrapped *ProviderError")
	}
	if pe.Op != OpCreateSandbox {
		t.Errorf("pe.Op = %q, want %q", pe.Op, OpCreateSandbox)
	}
}
