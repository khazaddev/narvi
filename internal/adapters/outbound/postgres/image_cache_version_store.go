package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ImageCacheVersionStore is a thin, pass-through wrapper around the
// sqlc-generated image_cache_version_counters/image_cache_versions queries
// (Step 43(c), third iteration: immutable versioned cache snapshots --
// §19.1's closing paragraph). No caching, no retries, no business rules --
// retention policy lives in domain/imagebuild (RetainedCacheVersions,
// PruneCacheVersions), the mint-before-build/publish-after-success sequence
// lives in app/imagebuild.Builder. See migrations/
// 000087_image_cache_versions.up.sql for why minting and confirming
// publication are two separate tables/calls, and ports.CacheMount
// (internal/app/ports/createspec.go) for the port-level contract this
// backs.
type ImageCacheVersionStore struct {
	q *sqlcgen.Queries
}

// NewImageCacheVersionStore builds an ImageCacheVersionStore backed by
// pool.
func NewImageCacheVersionStore(pool *pgxpool.Pool) *ImageCacheVersionStore {
	return &ImageCacheVersionStore{q: sqlcgen.New(pool)}
}

// MintVersion atomically reserves the next version number for cacheKey --
// called BEFORE a real BuildImage attempt so the reserved number can travel
// as that attempt's own ports.CacheMount.PublishVersion. See
// MintCacheVersion's own generated doc comment for the full "reservation
// that is never confirmed is simply abandoned" contract.
func (s *ImageCacheVersionStore) MintVersion(ctx context.Context, cacheKey string) (int64, error) {
	return s.q.MintCacheVersion(ctx, cacheKey)
}

// PublishVersion records a CONFIRMED publication: version was minted by
// MintVersion above for the same cacheKey, and the build that named it as
// its own PublishVersion reported success AND confirmed
// (ports.BuildOutcome.PublishedCacheVersion) its cache mount was not
// silently dropped. See PublishCacheVersion's own generated doc comment.
func (s *ImageCacheVersionStore) PublishVersion(ctx context.Context, cacheKey string, version int64, fingerprint string) error {
	return s.q.PublishCacheVersion(ctx, sqlcgen.PublishCacheVersionParams{
		CacheKey:    cacheKey,
		Version:     version,
		Fingerprint: fingerprint,
	})
}

// LatestVersion returns the most recently CONFIRMED-published version for
// cacheKey -- what a NEW build attempting this key should mount read-only
// as its own MountVersion. Returns pgx.ErrNoRows when no version has ever
// been confirmed for this key yet (see LatestCacheVersion's own generated
// doc comment) -- callers MUST treat that as "no MountVersion yet", never
// propagate it as a failure (§19.1's own pure-accelerator rule).
func (s *ImageCacheVersionStore) LatestVersion(ctx context.Context, cacheKey string) (int64, error) {
	return s.q.LatestCacheVersion(ctx, cacheKey)
}

// ListVersions returns every CONFIRMED version currently tracked for
// cacheKey, newest first -- app/imagebuild.Builder's own retention-pruning
// input (domain/imagebuild.PruneCacheVersions decides which to drop).
func (s *ImageCacheVersionStore) ListVersions(ctx context.Context, cacheKey string) ([]int64, error) {
	return s.q.ListCacheVersions(ctx, cacheKey)
}

// DeleteVersions removes this control plane's OWN bookkeeping rows for the
// given (already decided by domain/imagebuild.PruneCacheVersions) versions.
// See DeleteCacheVersions' own generated doc comment for why this is never
// a request to reclaim the underlying provider-side bytes.
func (s *ImageCacheVersionStore) DeleteVersions(ctx context.Context, cacheKey string, versions []int64) error {
	if len(versions) == 0 {
		return nil
	}
	return s.q.DeleteCacheVersions(ctx, sqlcgen.DeleteCacheVersionsParams{
		CacheKey: cacheKey,
		Versions: versions,
	})
}
