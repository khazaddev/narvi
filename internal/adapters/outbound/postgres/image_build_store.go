package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// ImageBuildStore is a thin, pass-through wrapper around the sqlc-generated
// image_builds queries ("image builds", §8.5-note/§10-P2; Step
// 41, "warm boot: shared fingerprint", §19.1). No caching, no retries, no
// business rules -- fingerprinting lives in domain/imagebuild, the
// spawn-time lookup/best-effort-upsert lives in
// app/sessionactor/imageresolve.go, and the claim/attempt/record loop
// lives in app/imagebuild.
type ImageBuildStore struct {
	q *sqlcgen.Queries
}

// NewImageBuildStore builds an ImageBuildStore backed by pool.
func NewImageBuildStore(pool *pgxpool.Pool) *ImageBuildStore {
	return &ImageBuildStore{q: sqlcgen.New(pool)}
}

// WithTx returns an ImageBuildStore whose queries run on tx instead of the
// pool this store was built with -- used by app/imagebuild's own claim step
// (a real Postgres transaction, exactly like app/sessionactor's transact
// and app/reconciler's claimDueTimers-style precedent).
func (s *ImageBuildStore) WithTx(tx pgx.Tx) *ImageBuildStore {
	return &ImageBuildStore{q: s.q.WithTx(tx)}
}

// Get fetches the image_builds row for fingerprint, or pgx.ErrNoRows if
// none exists yet.
func (s *ImageBuildStore) Get(ctx context.Context, fingerprint string) (sqlcgen.ImageBuild, error) {
	return s.q.GetImageBuild(ctx, fingerprint)
}

// UpsertPending best-effort inserts a fresh 'pending' tracking row for
// arg.Fingerprint -- a no-op (ON CONFLICT DO NOTHING) if one already
// exists under any status, see UpsertPendingImageBuild's own generated doc
// comment for why that's correct, not merely convenient.
func (s *ImageBuildStore) UpsertPending(ctx context.Context, arg sqlcgen.UpsertPendingImageBuildParams) error {
	return s.q.UpsertPendingImageBuild(ctx, arg)
}

// ListDue returns up to limit rows eligible to (re)attempt right now
// (pending, or failed with an elapsed next_retry_at), locked FOR UPDATE
// SKIP LOCKED -- callers MUST run this inside the same transaction that
// subsequently calls Claim on each returned row (see ListDueImageBuilds's
// own generated doc comment).
func (s *ImageBuildStore) ListDue(ctx context.Context, limit int32) ([]sqlcgen.ImageBuild, error) {
	return s.q.ListDueImageBuilds(ctx, limit)
}

// Claim flips fingerprint's row to 'building' and bumps attempt_count/
// last_attempt_at -- the commit-before-the-real-BuildImage-call half of
// app/imagebuild's own two-step (claim, then attempt outside any
// transaction) shape.
func (s *ImageBuildStore) Claim(ctx context.Context, fingerprint string) (sqlcgen.ImageBuild, error) {
	return s.q.ClaimImageBuild(ctx, fingerprint)
}

// RecordSuccess records a successful BuildImage call: status='ready',
// image_ref set, next_retry_at cleared. Returns pgx.ErrNoRows if
// fingerprint's row is no longer 'building' (an already-superseded/stale
// outcome -- see RecordImageBuildSuccess's own generated doc comment).
func (s *ImageBuildStore) RecordSuccess(ctx context.Context, arg sqlcgen.RecordImageBuildSuccessParams) (sqlcgen.ImageBuild, error) {
	return s.q.RecordImageBuildSuccess(ctx, arg)
}

// RecordFailure records a failed BuildImage call: status='failed',
// next_retry_at set to the caller's own domain/imagebuild.EvaluateBackoff
// decision. Returns pgx.ErrNoRows if fingerprint's row is no longer
// 'building', mirroring RecordSuccess's own identical guard.
func (s *ImageBuildStore) RecordFailure(ctx context.Context, arg sqlcgen.RecordImageBuildFailureParams) (sqlcgen.ImageBuild, error) {
	return s.q.RecordImageBuildFailure(ctx, arg)
}

// RecordPermanentFailure records a TERMINALLY failed attempt (audit-
// remediation batch B3 round 2, finding #3): status stays 'failed', but
// permanently_failed flips to true and next_retry_at is cleared -- this
// fingerprint is excluded from every future ListDue poll until an operator
// fixes the underlying repo config and manually clears the column. See
// RecordImageBuildPermanentFailure's own generated doc comment. Returns
// pgx.ErrNoRows if fingerprint's row is no longer 'building', mirroring
// RecordFailure's own identical guard.
func (s *ImageBuildStore) RecordPermanentFailure(ctx context.Context, fingerprint string) (sqlcgen.ImageBuild, error) {
	return s.q.RecordImageBuildPermanentFailure(ctx, fingerprint)
}

// ListReady returns up to limit SHARED (repo-bearing) 'ready' rows -- Step
// 42's own freshness-pump poll query (§19.2), bounded by a real LIMIT
// (mirroring ListDue's own limit parameter shape exactly) so one tick's own
// strictly-sequential per-row refresh attempts can never stack up an
// unbounded number of slow, network-bound BuildImage calls. A base-only
// row is never stale in the sense this design cares about, so it is
// excluded at the SQL level (see ListReadyImageBuilds's own generated doc
// comment for both exclusions). staleClaimCutoff (audit-remediation batch
// B2, platform.Timeouts.ImageRefreshClaimStaleAfter ago) mirrors
// ClaimForRefresh's own identical cutoff below: a row another pod is
// ACTIVELY, non-stale-ly refreshing is excluded, but a row whose claim has
// gone stale is still returned so it can be reclaimed.
func (s *ImageBuildStore) ListReady(ctx context.Context, limit int32, staleClaimCutoff pgtype.Timestamptz) ([]sqlcgen.ImageBuild, error) {
	return s.q.ListReadyImageBuilds(ctx, sqlcgen.ListReadyImageBuildsParams{
		Limit:            limit,
		StaleClaimCutoff: staleClaimCutoff,
	})
}

// ClaimForRefresh implements the freshness pump's own single-flight claim
// (§19.2): a CAS entirely independent of status/attempt_count/
// next_retry_at, flipping refresh_in_progress to true (and stamping
// refresh_started_at with this claim's own moment) when the row is still
// 'ready' and EITHER not already being refreshed elsewhere, OR its
// existing claim has gone stale (audit-remediation batch B2: older than
// staleClaimCutoff, i.e. platform.Timeouts.ImageRefreshClaimStaleAfter
// ago) -- the lease that heals a crash between a previous claim and its
// own RecordRefreshSuccess/RecordRefreshFailure. Returns pgx.ErrNoRows on
// a lost race against a still-fresh concurrent claim (a normal, expected
// outcome, never logged as an error by the caller).
func (s *ImageBuildStore) ClaimForRefresh(ctx context.Context, fingerprint string, staleClaimCutoff pgtype.Timestamptz) (sqlcgen.ImageBuild, error) {
	return s.q.ClaimImageBuildForRefresh(ctx, sqlcgen.ClaimImageBuildForRefreshParams{
		Fingerprint:      fingerprint,
		StaleClaimCutoff: staleClaimCutoff,
	})
}

// RecordRefreshSuccess atomically swaps image_ref + built_repo_shas +
// built_at and releases the refresh_in_progress claim -- status stays
// 'ready' throughout (§19.2's own "never degrades availability"
// guarantee): a session mid-spawn always reads either the OLD ref or the
// NEW one, never a gap with neither.
//
// arg.ClaimedRefreshStartedAt (audit-remediation batch B2 round 2) is a
// FENCING TOKEN -- the exact refresh_started_at value the caller's own
// ClaimForRefresh call returned to IT, never a freshly computed now(). It
// scopes this write to the SAME claim instance the caller took: if this
// fingerprint's lease has since gone stale and been reclaimed by a
// concurrent tick (this pod's or another pod's), that reclaim stamped a
// NEW refresh_started_at, this call's own WHERE clause no longer matches,
// and pgx.ErrNoRows is returned -- the same harmless "lost the race"
// outcome a caller already treats a stale/superseded row as, rather than
// silently overwriting whatever the reclaiming tick has since written. See
// RecordImageRefreshSuccess's own generated doc comment and
// app/imagebuild/builder.go's own attemptRefresh doc comment for the full
// failure mode this closes.
func (s *ImageBuildStore) RecordRefreshSuccess(ctx context.Context, arg sqlcgen.RecordImageRefreshSuccessParams) (sqlcgen.ImageBuild, error) {
	return s.q.RecordImageRefreshSuccess(ctx, arg)
}

// RecordRefreshFailure releases the refresh_in_progress claim (and clears
// refresh_started_at back to NULL, audit-remediation batch B2), touching
// nothing else -- the row is left exactly as it was (still 'ready', still
// serving its own old image_ref), picked up again at the next
// ImageRefreshCheckInterval tick (the refresh path's own natural retry
// cadence). This is app/imagebuild.Builder's own SHARED release path --
// releaseRefreshClaim calls this from EVERY one of attemptRefresh's own
// post-claim failure branches, by INVARIANT, not a fixed enumerated list.
//
// claimedRefreshStartedAt (audit-remediation batch B2 round 2) is the SAME
// fencing token RecordRefreshSuccess above takes, for the identical
// reason: this release must only ever touch the SAME claim instance the
// caller originally took, never whatever claim (possibly a different
// tick's own, since-reclaimed, still-legitimately-held one) happens to be
// current by the time this call finally lands. A mismatch is a harmless
// no-op (pgx.ErrNoRows) -- see RecordImageRefreshFailure's own generated
// doc comment.
func (s *ImageBuildStore) RecordRefreshFailure(ctx context.Context, fingerprint string, claimedRefreshStartedAt pgtype.Timestamptz) (sqlcgen.ImageBuild, error) {
	return s.q.RecordImageRefreshFailure(ctx, sqlcgen.RecordImageRefreshFailureParams{
		Fingerprint:             fingerprint,
		ClaimedRefreshStartedAt: claimedRefreshStartedAt,
	})
}

// TouchChecked bumps ONLY fingerprint's own updated_at -- no other
// column -- app/imagebuild's own genuine-round-robin fairness mechanism
// (§19.2, a correctness review finding on the batch-cap fix): attemptRefresh
// calls this from EVERY one of its own early-return branches that does not
// otherwise advance updated_at some other way (an INVARIANT -- "every
// inspected row's ordering key advances, one way or another" -- not a
// fixed enumerated list; the exact set of branches has already grown more
// than once), so that ListReady's own ORDER BY updated_at reflects genuine
// "last looked at this tick", not merely "last mutated", for the WHOLE
// 'ready' population -- see TouchImageBuildChecked's own generated doc
// comment for the full starvation this rules out. A no-op, never an error
// worth surfacing, if fingerprint is no longer 'ready' (or gone entirely).
func (s *ImageBuildStore) TouchChecked(ctx context.Context, fingerprint string) error {
	return s.q.TouchImageBuildChecked(ctx, fingerprint)
}
