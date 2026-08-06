// Package objstore implements ports.BlobStore (§28.1) against
// S3-compatible object storage (AWS S3, MinIO, R2, GCS) via
// github.com/aws/aws-sdk-go-v2, using SigV4-signed requests.
//
// Two request shapes, two different guarantees:
//
//   - PresignPut/PresignGet are pure local signing -- HMAC over a request
//     descriptor, no network round-trip -- so a failure here is always
//     classified permanent (§28.1: "a presign cannot meaningfully fail
//     transiently"). They are never bounded by
//     Config.Timeouts.ObjectStoreHTTPClientTimeout, which exists
//     specifically to bound the OTHER two methods' real network calls.
//   - Stat/Delete are real HeadObject/DeleteObject calls, bounded by that
//     timeout, and classified by HTTP status class -- never by
//     string-matching a human-readable message -- mirroring
//     internal/adapters/outbound/modal's own classifyErrorResponse/
//     classifyNetworkError split exactly (see errors.go). Stat's
//     not-found case returns the typed ports.ErrBlobNotFound sentinel
//     directly, never wrapped in a *ports.BlobStoreError; Delete's
//     not-found case is swallowed entirely (nil error) to keep it
//     idempotent, per ports.BlobStore's own doc comment.
//
// Two *s3.Client instances back one Store: one with BaseEndpoint =
// Config.Endpoint (the internal/private endpoint, used for Stat/Delete's
// real calls), and a second with BaseEndpoint = Config.PublicEndpoint
// (falling back to Config.Endpoint when unset) wrapped in an
// *s3.PresignClient, used only to SIGN PresignPut/PresignGet URLs (§28.7:
// "presigning binds the host" -- a signature minted against an internal
// hostname breaks the moment a browser or sandbox resolves the public
// one).
//
// This package also implements the "blob_delete" outbox kind (§28.4) via
// BlobDeleteNotifier (notifier.go): confirm-time verification failure
// enqueues one of these so a half-uploaded object is eventually reaped,
// even across a crash between the status write and the delete.
//
// Every exact AWS SDK v2 type/field used here (e.g. whether
// PutObjectInput.ContentLength is *int64 or int64, or which typed error
// HeadObject's own deserializer synthesizes for a 404) was verified
// directly against the pinned SDK version via `go doc` and a set of
// throwaway diagnostic calls against a real *s3.Client talking to an
// httptest.Server -- never assumed from memory or from a different SDK
// version's documented shape. See errors.go's own isNotFoundError doc
// comment for what that verification found.
package objstore
