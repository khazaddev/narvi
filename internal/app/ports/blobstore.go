// This file (blobstore.go) is the BlobStore port (§28.1): the complete
// interface ("no out-of-interface operations") a control-plane caller uses
// to mint scoped, time-limited credentials against S3-compatible object
// storage and to verify/reap objects after the fact.
// internal/adapters/outbound/objstore (§8.6) is, today, its only
// implementation — see doc.go and CLAUDE.md's "don't couple a port to a
// single adapter" for why this interface must still hold for a second one
// (a different S3-compatible backend, or a wholly different object-storage
// API) without changing shape.
//
// BlobStore is deliberately NOT sqlc-backed alongside SessionStore/
// TurnStore/SandboxStore (§4.3's own grouping) — it is an outbound adapter
// over the S3 HTTP API, exactly as githubapi is an adapter over GitHub's.
// The split preserves §5.1: object storage holds bytes only, addressed by
// keys Postgres owns; every fact ABOUT an upload (status, size, who minted
// it, why it failed) lives on the artifacts row, sqlc-backed like every
// other store. Object storage is never a second authority over state — a
// blob with no row is an orphan to reap, never a record.
//
// Transport (§28.2): the control plane mints presigned URLs and bytes move
// directly between client (browser or sandbox) and storage — the control
// plane never proxies a payload. Every BlobStore method below either signs
// a request locally (PresignPut/PresignGet) or asks the backend a
// metadata-only question (Stat/Delete); no method here reads or writes
// object bytes.

package ports

import (
	"context"
	"time"
)

// BlobKey opaquely identifies one object inside the single bucket a
// BlobStore is configured against (§28.3: "one configured bucket per
// deployment"). Only the control plane's own key builder
// (internal/domain/upload.BuildBlobKey — §28.3's "sessions/{session_id}/
// uploads/{upload_id}" convention, zero client-controlled bytes) ever
// produces a BlobKey; no BlobStore adapter parses or constructs one.
type BlobKey string

// PresignPutSpec is PresignPut's argument.
type PresignPutSpec struct {
	Key BlobKey

	// ContentType is signed into the presigned URL where the backend
	// supports it — the uploader must send this exact Content-Type header
	// for the signature to verify; PresignedURL.Headers carries it back so
	// callers never have to duplicate this value themselves.
	ContentType string

	// ContentLength is the declared size in bytes. Presigned PUTs pin this
	// where the backend honors it (§28.4), but nothing in this design
	// relies on that honoring (backend divergence) — Stat-at-confirm is
	// the check of record, never the signature.
	ContentLength int64

	// TTL is how long the presigned URL remains valid, supplied by the
	// caller from platform/timeouts.go (UploadPresignPutTTL) — this
	// package holds no timeout literal of its own (§11's grep-test).
	TTL time.Duration
}

// PresignGetSpec is PresignGet's argument.
type PresignGetSpec struct {
	Key BlobKey

	// TTL is supplied by the caller from platform/timeouts.go
	// (UploadPresignGetTTL).
	TTL time.Duration

	// ResponseFilename, when non-empty, is rendered into the presigned
	// URL's response-content-disposition query parameter as
	// `attachment; filename="<ResponseFilename>"` — forcing the browser or
	// curl to download rather than render the object inline off the
	// storage origin (§28.5: user-supplied content must never be served as
	// a page someone can be linked to).
	ResponseFilename string
}

// PresignedURL is PresignPut/PresignGet's result.
type PresignedURL struct {
	URL       string
	ExpiresAt time.Time

	// Headers are the exact headers the caller must send with the request
	// to URL for the signature to verify (e.g. Content-Type on a PUT) —
	// mint responses forward these verbatim to the uploader.
	Headers map[string]string
}

// BlobInfo is Stat's result — confirm-time verification (§28.4) reads
// exactly these two fields: does the object exist, and does its actual
// size match what was declared at mint.
type BlobInfo struct {
	SizeBytes int64
	ETag      string
}

// BlobStore is the complete port (§28.1: no out-of-interface operations)
// a control-plane caller uses against S3-compatible object storage.
//
// Deliberately absent, with reasons (§28.1 — re-litigate only alongside a
// concrete feature that needs it, never speculatively):
//
//   - No Put/Get streaming methods: nothing in this design requires the
//     control plane to touch bytes at all (§28.2 — bytes move directly
//     between client and storage); confirm-time verification is
//     metadata-only (Stat).
//   - No multipart-upload surface: the per-file cap
//     (platform.Config.ObjectStorage.MaxUploadBytes, 100 MiB by default)
//     sits far below every supported backend's own single-PUT limit.
//   - No List: every object's key embeds the artifact row id that minted
//     it (internal/domain/upload.BuildBlobKey), so there is no
//     orphan-blob class a bucket scan would find that the row-driven
//     abandonment sweep doesn't already cover.
type BlobStore interface {
	// PresignPut mints a time-limited URL the caller may PUT object bytes
	// to. Under SigV4 this is pure local signing — no network round-trip —
	// so a PresignPut error is always classified permanent
	// (BlobStoreError.Transient == false): there is no transient failure
	// mode for a computation that never leaves the process.
	PresignPut(ctx context.Context, spec PresignPutSpec) (PresignedURL, error)

	// PresignGet mints a time-limited URL the caller may GET object bytes
	// from. Same locally-signed, never-transient contract as PresignPut.
	PresignGet(ctx context.Context, spec PresignGetSpec) (PresignedURL, error)

	// Stat is a real network call asking the backend whether key exists
	// and, if so, its size/ETag — confirm-time verification's check of
	// record (§28.4). Returns ErrBlobNotFound (never a string-matched
	// message) when key does not exist; any other failure is a typed
	// *BlobStoreError.
	Stat(ctx context.Context, key BlobKey) (BlobInfo, error)

	// Delete removes key. A real network call, idempotent: deleting an
	// already-absent key succeeds (nil error) rather than surfacing
	// ErrBlobNotFound — a redelivered blob_delete outbox entry must never
	// itself become the reason a delivery fails.
	Delete(ctx context.Context, key BlobKey) error
}
