package objstore

import (
	"context"
	"mime"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/narvidev/narvi/internal/app/ports"
)

// PresignPut mints a time-limited URL the caller may PUT object bytes to,
// via s.presignClient (signed against Config.PublicEndpoint when set,
// Config.Endpoint otherwise -- §28.7). This is pure local SigV4 signing,
// no network round-trip, so it is never bounded by httpTimeout, and any
// failure is always classified permanent (presignError) -- §28.1: "a
// presign cannot meaningfully fail transiently".
func (s *Store) PresignPut(ctx context.Context, spec ports.PresignPutSpec) (ports.PresignedURL, error) {
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(string(spec.Key)),
	}
	if spec.ContentType != "" {
		input.ContentType = aws.String(spec.ContentType)
	}
	// ContentLength is signed into the request when the caller declared
	// one (> 0): the presigned PUT then pins this value where the backend
	// honors it. Nothing downstream relies on that honoring, though --
	// backends diverge on whether/how strictly they enforce a signed
	// Content-Length -- Stat-at-confirm (internal/domain/upload, not this
	// package) is the actual check of record, per §28.4's own explicit
	// "the design never relies on that honoring" language. A zero value
	// means the caller did not declare a size and is treated the same as
	// an empty ContentType above: simply omitted from the request.
	if spec.ContentLength > 0 {
		input.ContentLength = aws.Int64(spec.ContentLength)
	}

	req, err := s.presignClient.PresignPutObject(ctx, input, s3.WithPresignExpires(spec.TTL))
	if err != nil {
		return ports.PresignedURL{}, presignError(ports.BlobOpPresignPut, err)
	}

	return ports.PresignedURL{
		URL:       req.URL,
		ExpiresAt: time.Now().Add(spec.TTL),
		Headers:   flattenHeader(req.SignedHeader),
	}, nil
}

// PresignGet mints a time-limited URL the caller may GET object bytes
// from. Same locally-signed, never-transient contract as PresignPut.
//
// When spec.ResponseFilename is set, it is rendered into the presigned
// URL's response-content-disposition parameter via mime.FormatMediaType
// -- never naive string concatenation. This matters: ResponseFilename
// ultimately originates from a user-supplied upload's filename
// (attacker-influenced), so it must be correctly escaped (quotes,
// backslashes, non-ASCII falls back to RFC 2231 percent-encoding) rather
// than hand-built into a header-shaped string.
func (s *Store) PresignGet(ctx context.Context, spec ports.PresignGetSpec) (ports.PresignedURL, error) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(string(spec.Key)),
	}
	if spec.ResponseFilename != "" {
		if disposition := mime.FormatMediaType("attachment", map[string]string{"filename": spec.ResponseFilename}); disposition != "" {
			input.ResponseContentDisposition = aws.String(disposition)
		}
		// mime.FormatMediaType returns "" only on a standards violation
		// (e.g. an invalid attribute name/token) -- never for the value
		// itself, which it always either quotes or RFC-2231-encodes
		// (verified directly: control characters, embedded quotes,
		// backslashes, and invalid UTF-8 all still produce a valid
		// header value). "attachment"/"filename" are both fixed, valid
		// tokens this call always supplies, so this branch is not
		// expected to be reachable in practice; when it nonetheless
		// isn't, ResponseContentDisposition is simply left unset rather
		// than risking a malformed header value -- the object still
		// downloads, just without the forced-filename override.
	}

	req, err := s.presignClient.PresignGetObject(ctx, input, s3.WithPresignExpires(spec.TTL))
	if err != nil {
		return ports.PresignedURL{}, presignError(ports.BlobOpPresignGet, err)
	}

	return ports.PresignedURL{
		URL:       req.URL,
		ExpiresAt: time.Now().Add(spec.TTL),
		Headers:   flattenHeader(req.SignedHeader),
	}, nil
}

// flattenHeader converts an http.Header (string -> []string) into the
// map[string]string shape ports.PresignedURL.Headers requires, taking the
// first value for any key with more than one -- SigV4 never signs a
// multi-valued header for PutObject/GetObject, so there is always exactly
// one value in practice; this is a defensive, not a lossy-in-practice,
// choice.
func flattenHeader(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}
