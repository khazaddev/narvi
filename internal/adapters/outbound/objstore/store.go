package objstore

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/narvidev/narvi/internal/app/ports"
)

// Store implements ports.BlobStore via SigV4-signed requests against
// S3-compatible object storage (AWS S3, MinIO, R2, GCS -- §28.1).
type Store struct {
	bucket string

	// client is used for Stat/Delete's real network calls. Its
	// BaseEndpoint is ALWAYS Config.Endpoint (the internal/private
	// endpoint) -- never Config.PublicEndpoint.
	client *s3.Client

	// presignClient is used for PresignPut/PresignGet's local signing
	// only -- it wraps a SEPARATE *s3.Client whose BaseEndpoint is
	// Config.PublicEndpoint (falling back to Config.Endpoint when unset).
	// §28.7: "Presigning binds the host: URLs are signed against
	// PublicEndpoint when set -- a signature minted against an internal
	// hostname breaks the moment a browser or sandbox resolves the public
	// one."
	presignClient *s3.PresignClient

	// httpTimeout is Config.Timeouts.ObjectStoreHTTPClientTimeout,
	// applied per-call via context.WithTimeout in Stat/Delete only --
	// PresignPut/PresignGet are pure local signing with no network
	// round-trip and are never bounded by it.
	httpTimeout time.Duration
}

// var _ ports.BlobStore = (*Store)(nil) makes a BlobStore signature drift
// a build error, not a runtime surprise.
var _ ports.BlobStore = (*Store)(nil)

// New constructs a Store from cfg. It validates cfg fail-fast (named
// errors, matching platform/config.go's established pattern -- mirroring
// modal.New's own identical shape) when Endpoint/Region/Bucket is empty.
//
// No network I/O happens in New itself. Even the AWS SDK's own
// default-credential-chain path (when Config.AccessKeyID/SecretAccessKey
// are both empty) only ASSEMBLES the provider chain here
// (config.LoadDefaultConfig) -- real credential resolution (an IMDS hit,
// reading a shared credentials file's key material, an STS AssumeRole
// call, ...) happens lazily inside the SDK on first actual Stat/Delete/
// PresignPut/PresignGet call, never at config-load time.
func New(cfg Config) (*Store, error) {
	if cfg.Endpoint == "" {
		return nil, &MissingConfigError{Field: "Endpoint"}
	}
	if cfg.Region == "" {
		return nil, &MissingConfigError{Field: "Region"}
	}
	if cfg.Bucket == "" {
		return nil, &MissingConfigError{Field: "Bucket"}
	}

	credsProvider, err := resolveCredentials(cfg)
	if err != nil {
		return nil, err
	}

	publicEndpoint := cfg.PublicEndpoint
	if publicEndpoint == "" {
		publicEndpoint = cfg.Endpoint
	}

	client := s3.New(s3.Options{
		Region:       cfg.Region,
		Credentials:  credsProvider,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: cfg.UsePathStyle,
	})
	presignSourceClient := s3.New(s3.Options{
		Region:       cfg.Region,
		Credentials:  credsProvider,
		BaseEndpoint: aws.String(publicEndpoint),
		UsePathStyle: cfg.UsePathStyle,
	})

	return &Store{
		bucket:        cfg.Bucket,
		client:        client,
		presignClient: s3.NewPresignClient(presignSourceClient),
		httpTimeout:   cfg.Timeouts.ObjectStoreHTTPClientTimeout,
	}, nil
}

// resolveCredentials returns Config's static credentials when both
// AccessKeyID and SecretAccessKey are set, or assembles the AWS SDK's
// default credential chain (env vars, shared config files, IMDS/IRSA)
// via config.LoadDefaultConfig when both are empty (§28.7).
// context.Background() is used here (New itself takes no context, by
// design -- see New's own doc comment): LoadDefaultConfig only assembles
// the provider chain synchronously (env var reads, at most local shared-
// config-file parsing); it never performs the network round trip that
// actually retrieves credential material, so there is nothing here for a
// request-scoped context to usefully cancel.
func resolveCredentials(cfg Config) (aws.CredentialsProvider, error) {
	if cfg.AccessKeyID == "" && cfg.SecretAccessKey == "" {
		awsCfg, err := config.LoadDefaultConfig(context.Background(), config.WithRegion(cfg.Region))
		if err != nil {
			return nil, &DefaultCredentialsError{Err: err}
		}
		return awsCfg.Credentials, nil
	}
	return credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""), nil
}

// Stat asks the backend for key's size/ETag via a real HeadObject call,
// bounded by httpTimeout. Returns ports.ErrBlobNotFound directly (never
// wrapped in a *ports.BlobStoreError) when key does not exist -- §28.1:
// "distinct from any transient failure". Any other failure is a typed
// *ports.BlobStoreError classified by HTTP status class.
func (s *Store) Stat(ctx context.Context, key ports.BlobKey) (ports.BlobInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, s.httpTimeout)
	defer cancel()

	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(string(key)),
	})
	if err != nil {
		if isNotFoundError(err) {
			return ports.BlobInfo{}, ports.ErrBlobNotFound
		}
		return ports.BlobInfo{}, classify(ports.BlobOpStat, err)
	}

	return ports.BlobInfo{
		SizeBytes: aws.ToInt64(out.ContentLength),
		// S3-compatible backends return ETag as a quoted string (the HTTP
		// ETag header's own syntax, RFC 9110 §8.8.3) -- the SDK passes
		// that quoting through into the *string field literally rather
		// than stripping it, so it is trimmed here: BlobInfo.ETag is a
		// bare identifier, not an HTTP header value.
		ETag: strings.Trim(aws.ToString(out.ETag), `"`),
	}, nil
}

// Delete removes key via a real DeleteObject call, bounded by
// httpTimeout. Idempotent per ports.BlobStore's own doc comment:
// deleting an already-absent key returns nil, never
// ports.ErrBlobNotFound (that sentinel is Stat-only) -- a redelivered
// blob_delete outbox entry must never itself become the reason a
// delivery fails.
func (s *Store) Delete(ctx context.Context, key ports.BlobKey) error {
	ctx, cancel := context.WithTimeout(ctx, s.httpTimeout)
	defer cancel()

	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(string(key)),
	})
	if err != nil {
		if isNotFoundError(err) {
			return nil
		}
		return classify(ports.BlobOpDelete, err)
	}
	return nil
}
