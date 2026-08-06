package objstore

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/khazaddev/narvi/internal/app/ports"
)

// --- Config errors (New) -- named, structured, fail-fast, matching the
// established platform/config.go pattern (mirrored from
// modal.MissingConfigError exactly, see internal/adapters/outbound/modal/
// errors.go). ---

// MissingConfigError is returned by New when a required Config field
// (Endpoint, Region, Bucket) is empty.
type MissingConfigError struct {
	Field string
}

func (e *MissingConfigError) Error() string {
	return fmt.Sprintf("objstore: missing required Config.%s", e.Field)
}

// DefaultCredentialsError is returned by New when Config.AccessKeyID and
// Config.SecretAccessKey are both empty (the "use the AWS SDK's default
// credential chain" path, §28.7) and assembling that chain
// (config.LoadDefaultConfig) itself fails -- e.g. a malformed shared
// config/credentials file in the ambient environment. This is distinct
// from a credential ultimately failing to RESOLVE (an expired IMDS role,
// a missing env var): that failure happens lazily, inside the SDK, on the
// first real Stat/Delete/PresignPut/PresignGet call, and surfaces as a
// *ports.BlobStoreError from that call, never from New.
type DefaultCredentialsError struct {
	Err error
}

func (e *DefaultCredentialsError) Error() string {
	return fmt.Sprintf("objstore: load default AWS credentials chain: %v", e.Err)
}

// Unwrap exposes the underlying error to errors.Is/errors.As.
func (e *DefaultCredentialsError) Unwrap() error { return e.Err }

// --- Response classification (§28.1's crux, mirroring modal/errors.go's
// isTransientStatus/classifyErrorResponse/classifyNetworkError split
// exactly) ---
//
// Classification table (never by string-matching the human message):
//
//	Network-level failure (no HTTP response at all -- timeout, connection
//	refused, DNS failure, ...):                                 Transient=true
//	HTTP 429 (Too Many Requests):                                Transient=true
//	HTTP 5xx:                                                    Transient=true
//	HTTP 400, 401, 403, 404, 409, 413, 422:                      Transient=false (permanent)
//	Any other/unrecognized status:                               Transient=true
//	  (§3.2: "Unknown provider errors default to transient, never
//	  permanent -- a novel transient failure must not trip the breaker.")
//
// 413 (Request Entity Too Large) is this table's own addition over
// modal's (§28.7 names oversize classification as an exit bar): an
// object that is too large will never succeed by retrying the identical
// request, exactly like a 400/422 -- permanent.
//
// 404 is listed here for documentation/defense-in-depth completeness
// only: in practice, Stat/Delete both intercept a not-found signal via
// isNotFoundError BEFORE ever calling classify (see those methods) --
// Stat returns ports.ErrBlobNotFound directly (never a *BlobStoreError),
// and Delete swallows it (idempotent). classify() ever seeing a 404 at
// all would mean isNotFoundError's own detection regressed; it still
// classifies correctly (permanent) if that ever happens.

// isTransientStatus classifies an HTTP response status code per the table
// above. status is assumed to already be outside the 2xx success range.
func isTransientStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests:
		return true
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden,
		http.StatusNotFound, http.StatusConflict, http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity:
		return false
	}
	if status >= http.StatusInternalServerError {
		return true
	}
	// Anything else -- a status this table has no explicit entry for --
	// defaults to transient, never permanent (§3.2).
	return true
}

// isNotFoundError reports whether err represents "the object does not
// exist" for a real HeadObject/DeleteObject call -- checked BEFORE any
// transient/permanent classification, since not-found is its own typed
// signal (ports.ErrBlobNotFound for Stat; swallowed entirely for Delete's
// own idempotency), never a *BlobStoreError (§28.1: "distinct from any
// transient failure").
//
// Three signals are checked, empirically verified against this exact
// pinned aws-sdk-go-v2/service/s3 version (go doc alone does not say
// which shape a given operation actually produces at runtime -- this was
// confirmed directly against a real *s3.Client talking to an
// httptest.Server):
//
//   - *types.NotFound: what HeadObject's own response deserializer
//     synthesizes for a 404 -- S3 HEAD responses never carry an XML error
//     body (there is nothing else to deserialize), so the SDK manufactures
//     this typed sentinel instead of a generic API error.
//   - *types.NoSuchKey: GetObject's own typed not-found shape. This
//     adapter never calls GetObject itself (§28.2: bytes never transit the
//     control plane), but is checked defensively in case a future call
//     site or backend divergence ever surfaces it here too.
//   - A plain HTTP 404 status, via smithy-go's own structured
//     ResponseError.HTTPStatusCode() -- never a string match. Empirically,
//     DeleteObject's 404 does NOT deserialize into *types.NotFound the way
//     HeadObject's does (confirmed directly): it surfaces only as a
//     generic smithy API error wrapped in a ResponseError, so the status
//     code is the only backend-agnostic signal Delete's own idempotency
//     check can rely on -- exactly the "a plain HTTP 404 status must ALSO
//     map to ErrBlobNotFound" requirement this adapter is built to.
func isNotFoundError(err error) bool {
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var noSuchKey *types.NoSuchKey
	if errors.As(err, &noSuchKey) {
		return true
	}
	var respErr *smithyhttp.ResponseError
	return errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusNotFound
}

// classify converts err -- as returned by a real *s3.Client network call
// (HeadObject or DeleteObject) -- into a *ports.BlobStoreError for op.
// Callers must check isNotFoundError(err) FIRST and handle that
// separately; classify is only ever reached for every OTHER failure, and
// never itself special-cases not-found.
//
// Classification uses ONLY structural signals -- smithy-go's typed
// RequestSendError (a transport-level failure: no HTTP response was ever
// received, mirroring modal's own classifyNetworkError) and
// smithyhttp.ResponseError.HTTPStatusCode() (a real HTTP status,
// mirroring modal's own classifyErrorResponse) -- never by
// string-matching err's own message (§28.1).
//
// The raw response body is never available here to embed even by
// accident: unlike modal's hand-rolled HTTP client (which reads and
// JSON-decodes the body itself), the S3 SDK's own XML deserializer
// already reduces any response body to structured fields (an error Code/
// Message, or nothing at all for HeadObject's bodyless 404s) before this
// adapter ever sees a Go error value -- there is no raw-bytes handle left
// to leak by the time classify runs. What DOES reach the caller is the
// SDK's own decoded Message (via err's Error() string), exactly the
// "decoded detail is fine, raw body is not" line modal's own
// classifyErrorResponse draws.
func classify(op ports.BlobOp, err error) *ports.BlobStoreError {
	var sendErr *smithyhttp.RequestSendError
	if errors.As(err, &sendErr) {
		return &ports.BlobStoreError{
			Transient: true,
			Code:      networkErrorCode(err),
			Op:        op,
			Err:       err,
		}
	}

	var respErr *smithyhttp.ResponseError
	if errors.As(err, &respErr) {
		status := respErr.HTTPStatusCode()
		return &ports.BlobStoreError{
			Transient: isTransientStatus(status),
			Code:      fmt.Sprintf("http_%d", status),
			Op:        op,
			Err:       err,
		}
	}

	// Unrecognized shape entirely -- should not happen in practice against
	// a real *s3.Client call, but defaults to transient, never permanent
	// (§3.2's "a novel failure must not trip the breaker"), mirroring
	// modal's own identical default.
	return &ports.BlobStoreError{Transient: true, Code: "UNKNOWN_ERROR", Op: op, Err: err}
}

// networkErrorCode distinguishes a client-side timeout from every other
// transport-level failure for logging purposes only (via the structural
// net.Error.Timeout() check -- no string matching, mirroring modal's own
// classifyNetworkError exactly); it never changes the Transient verdict,
// which is always true for a smithyhttp.RequestSendError regardless.
func networkErrorCode(err error) string {
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "NETWORK_TIMEOUT"
	}
	return "NETWORK_ERROR"
}

// presignError wraps a PresignPut/PresignGet failure as a permanent
// *ports.BlobStoreError. Under SigV4, presigning is pure local
// computation -- HMAC over the request descriptor, no network round trip
// -- so §28.1 is explicit that "a presign cannot meaningfully fail
// transiently": unlike classify above, this never inspects err's shape at
// all, since every presign failure is permanent unconditionally.
func presignError(op ports.BlobOp, err error) *ports.BlobStoreError {
	return &ports.BlobStoreError{Transient: false, Code: "PRESIGN_ERROR", Op: op, Err: err}
}
