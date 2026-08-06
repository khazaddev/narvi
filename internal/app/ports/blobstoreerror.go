// This file (blobstoreerror.go) mirrors providererror.go's shape exactly
// for BlobStore (§28.1): {Transient, Code, Op, Err}, one Op constant per
// BlobStore method, and an IsBlobStoreTransient helper defaulting
// unclassified errors to transient — the same discipline §4.1 requires of
// ProviderError, applied to storage.
//
// BlobOp is its own type, distinct from ProviderError's Op: the two error
// types classify failures from two unrelated external systems (a sandbox
// compute provider vs. S3-compatible object storage), and sharing one enum
// type across them would let a SandboxProvider Op constant type-check as a
// valid BlobStoreError.Op value (and vice versa) despite meaning nothing
// there — a mistake purely structural typing would not catch.
package ports

import (
	"errors"
	"fmt"
)

// BlobOp names the BlobStore method that produced a BlobStoreError — one
// constant per interface method (blobstore.go), so objstore (and any
// future second BlobStore adapter) logs/reports failures against the same
// fixed vocabulary instead of inventing its own strings.
type BlobOp string

// The complete set of BlobOp values, one per BlobStore method.
const (
	BlobOpPresignPut BlobOp = "PresignPut"
	BlobOpPresignGet BlobOp = "PresignGet"
	BlobOpStat       BlobOp = "Stat"
	BlobOpDelete     BlobOp = "Delete"
)

// BlobStoreError is the typed error every BlobStore method returns on
// failure (§28.1: "Errors are typed BlobStoreError{Transient bool} —
// classification by storage error code / HTTP status class, never by
// string-matching messages").
type BlobStoreError struct {
	// Transient reports whether the caller should retry (true) or the
	// failure is permanent (false).
	Transient bool

	// Code is the storage-specific error code that drove the
	// classification (an HTTP status class, an S3 error code, ...) — kept
	// for logging/debugging. Callers must never re-parse this to
	// reclassify the error themselves; that would reintroduce the
	// string-matching §28.1 forbids. Consult Transient instead.
	Code string

	// Op is which BlobStore method produced this error (one of the BlobOp
	// constants above).
	Op BlobOp

	// Err is the wrapped underlying error (an HTTP transport error, a
	// decode error, ...).
	Err error
}

func (e *BlobStoreError) Error() string {
	class := "permanent"
	if e.Transient {
		class = "transient"
	}
	return fmt.Sprintf("objstore: %s: %s (code=%s): %v", e.Op, class, e.Code, e.Err)
}

// Unwrap exposes the wrapped underlying error to errors.Is/errors.As.
func (e *BlobStoreError) Unwrap() error { return e.Err }

// ErrBlobNotFound is Stat's typed not-found sentinel (§28.1) — distinct
// from any transient/permanent BlobStoreError, since confirm-time
// verification (§28.4) branches on "does this key exist at all", never on
// a string. Check with errors.Is(err, ErrBlobNotFound); never string-match
// Error().
var ErrBlobNotFound = errors.New("ports: blob not found")

// IsBlobStoreTransient reports whether err should be retried with backoff
// rather than treated as a permanent failure.
//
// Contract: an err that is not a *BlobStoreError at all (whether directly
// or wrapped, via errors.As) is treated as TRANSIENT — the same "safer
// default in both directions an unclassified error could go" reasoning
// ports.IsTransient documents for ProviderError, applied identically here.
//
// ErrBlobNotFound is its own sentinel, not a *BlobStoreError, so it falls
// into this helper's unclassified/transient default branch too — but
// callers asking "does this key exist" must check errors.Is(err,
// ErrBlobNotFound) directly instead of calling this helper, which only
// ever answers a different question ("should I retry the call").
//
// A nil err is treated as not transient (false): there is no failure to
// retry.
func IsBlobStoreTransient(err error) bool {
	if err == nil {
		return false
	}
	var be *BlobStoreError
	if errors.As(err, &be) {
		return be.Transient
	}
	return true
}
