package ports

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestIsBlobStoreTransient(t *testing.T) {
	sentinel := errors.New("boom")

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "blob store error transient true",
			err:  &BlobStoreError{Transient: true, Code: "http_503", Op: BlobOpStat, Err: sentinel},
			want: true,
		},
		{
			name: "blob store error transient false",
			err:  &BlobStoreError{Transient: false, Code: "http_403", Op: BlobOpStat, Err: sentinel},
			want: false,
		},
		{
			name: "non-blob-store error defaults to transient",
			err:  sentinel,
			want: true,
		},
		{
			name: "ErrBlobNotFound defaults to transient (not a retry question)",
			err:  ErrBlobNotFound,
			want: true,
		},
		{
			name: "wrapped blob store error (transient) via fmt.Errorf %w",
			err:  fmt.Errorf("objstore: %w", &BlobStoreError{Transient: true, Op: BlobOpDelete, Err: sentinel}),
			want: true,
		},
		{
			name: "wrapped blob store error (permanent) via fmt.Errorf %w",
			err:  fmt.Errorf("objstore: %w", &BlobStoreError{Transient: false, Op: BlobOpDelete, Err: sentinel}),
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
			if got := IsBlobStoreTransient(tt.err); got != tt.want {
				t.Errorf("IsBlobStoreTransient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestBlobStoreError_Error(t *testing.T) {
	sentinel := errors.New("connection refused")
	err := &BlobStoreError{
		Transient: true,
		Code:      "NETWORK_ERROR",
		Op:        BlobOpStat,
		Err:       sentinel,
	}

	got := err.Error()
	for _, want := range []string{string(BlobOpStat), "transient", "NETWORK_ERROR", "connection refused"} {
		if !strings.Contains(got, want) {
			t.Errorf("Error() = %q, want it to contain %q", got, want)
		}
	}

	permanent := &BlobStoreError{Transient: false, Code: "http_403", Op: BlobOpDelete, Err: sentinel}
	if !strings.Contains(permanent.Error(), "permanent") {
		t.Errorf("Error() = %q, want it to contain %q", permanent.Error(), "permanent")
	}
}

func TestBlobStoreError_Unwrap(t *testing.T) {
	sentinel := errors.New("underlying failure")
	err := &BlobStoreError{Transient: true, Op: BlobOpStat, Err: sentinel}

	if !errors.Is(err, sentinel) {
		t.Errorf("errors.Is(err, sentinel) = false, want true (Unwrap should expose Err)")
	}

	var be *BlobStoreError
	if !errors.As(fmt.Errorf("wrap: %w", err), &be) {
		t.Fatal("errors.As failed to find wrapped *BlobStoreError")
	}
	if be.Op != BlobOpStat {
		t.Errorf("be.Op = %q, want %q", be.Op, BlobOpStat)
	}
}

func TestErrBlobNotFound_DistinctFromBlobStoreError(t *testing.T) {
	// §28.1: "Stat on an absent key returns a typed not-found sentinel
	// (ErrBlobNotFound), distinct from any transient failure" -- a caller
	// must be able to errors.Is against it directly, without it also
	// matching as a *BlobStoreError.
	if !errors.Is(ErrBlobNotFound, ErrBlobNotFound) {
		t.Fatal("errors.Is(ErrBlobNotFound, ErrBlobNotFound) = false, want true")
	}
	var be *BlobStoreError
	if errors.As(ErrBlobNotFound, &be) {
		t.Fatal("errors.As(ErrBlobNotFound, &BlobStoreError{}) succeeded, want false: ErrBlobNotFound must never be mistaken for a classified BlobStoreError")
	}
}
