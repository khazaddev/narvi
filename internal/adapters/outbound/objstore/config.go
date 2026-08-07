package objstore

import (
	"github.com/khazaddev/narvi/internal/platform"
)

// Config configures a Store (New). Every field is sourced from the
// caller's own configuration -- New never hardcodes an endpoint, bucket,
// or credential (§28.7: "The root storage credential exists in exactly
// one place: platform.Config").
type Config struct {
	// Endpoint is the internal/private S3-compatible endpoint used for
	// Stat/Delete's real network calls, and for signing PresignPut/
	// PresignGet when PublicEndpoint is empty. Required.
	Endpoint string

	// PublicEndpoint, when set, is used to SIGN PresignPut/PresignGet
	// URLs instead of Endpoint (§28.7: "presigning binds the host" -- a
	// signature minted against an internal hostname breaks the moment a
	// browser or sandbox resolves the public one). Stat/Delete never use
	// this field; they always call Endpoint directly. Optional.
	PublicEndpoint string

	// Region is required by SigV4 signing even against a non-AWS backend
	// (MinIO accepts any string, e.g. "us-east-1"). Required.
	Region string

	// Bucket is the single configured bucket per deployment (§28.3: "one
	// configured bucket per deployment"). Required.
	Bucket string

	// AccessKeyID/SecretAccessKey are static credentials. When BOTH are
	// empty, the AWS SDK's default credential chain is used instead
	// (§28.7: "access key/secret (or ambient IAM where the deployment
	// provides one)") -- env vars, shared config files, or IMDS/IRSA,
	// resolved lazily by the SDK on first real call, never eagerly here
	// (New itself only ever assembles the provider chain, via
	// config.LoadDefaultConfig -- it never calls Retrieve).
	AccessKeyID     string
	SecretAccessKey string

	// UsePathStyle selects path-style addressing (bucket in the URL path,
	// e.g. http://host/bucket/key) instead of virtual-hosted-style
	// (bucket.host/key) -- required for MinIO-style backends.
	UsePathStyle bool

	// Timeouts.ObjectStoreHTTPClientTimeout bounds Stat/Delete's real
	// network calls (never PresignPut/PresignGet, which are pure local
	// signing with no network round-trip -- see ports.BlobStore's own doc
	// comment). This field lives on platform.Timeouts (§11's "every
	// timeout lives in platform/timeouts.go") -- this package holds no
	// timeout literal of its own.
	Timeouts platform.Timeouts
}
