package github

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// fakeActorLinkNoticeClaimer is a test-only ActorLinkNoticeClaimer -- no
// real Postgres round trip, exactly the point of actornotauthorizedreply.
// go's own ActorLinkNoticeClaimer interface being narrow and locally
// defined. This is the seam a confirmed audit finding on this file noted
// was previously missing entirely (the concrete *postgres.
// GitHubActorLinkNoticeStore type shouldPostActorNotAuthorizedReply used
// to be hard-coded to offered no way to force a genuine, non-ErrNoRows
// lookup failure in a test), which meant that branch's own documented
// fail-safe (fail toward NOT posting under a flaky DB) had zero coverage
// anywhere and a regression flipping it could ship undetected.
type fakeActorLinkNoticeClaimer struct {
	row sqlcgen.ClaimGitHubActorLinkNoticeRow
	err error

	gotRepoFullName string
	gotPRNumber     int32
	gotCommenterID  int64
	gotTTL          time.Duration
	calls           int
}

func (f *fakeActorLinkNoticeClaimer) Claim(_ context.Context, repoFullName string, prNumber int32, commenterID int64, ttl time.Duration) (sqlcgen.ClaimGitHubActorLinkNoticeRow, error) {
	f.calls++
	f.gotRepoFullName = repoFullName
	f.gotPRNumber = prNumber
	f.gotCommenterID = commenterID
	f.gotTTL = ttl
	return f.row, f.err
}

// TestClaimActorNotAuthorizedNotice_NoticesNil proves the "dedupe
// unavailable" default: notices == nil always reports true and never
// dereferences anything, mirroring Comments == nil simply skipping the
// post entirely elsewhere in this package.
func TestClaimActorNotAuthorizedNotice_NoticesNil(t *testing.T) {
	got := claimActorNotAuthorizedNotice(context.Background(), discardLogger(), nil, time.Hour, "acme/widgets", 1, 42)
	if !got {
		t.Errorf("claimActorNotAuthorizedNotice(nil notices) = false, want true")
	}
}

// TestClaimActorNotAuthorizedNotice_FreshClaimAllowsPost proves a
// successful Claim (a fresh insert, or a past-TTL refresh -- either way,
// notices.Claim itself already durably recorded the decision) reports
// true.
func TestClaimActorNotAuthorizedNotice_FreshClaimAllowsPost(t *testing.T) {
	claimer := &fakeActorLinkNoticeClaimer{row: sqlcgen.ClaimGitHubActorLinkNoticeRow{Inserted: true}}

	got := claimActorNotAuthorizedNotice(context.Background(), discardLogger(), claimer, time.Hour, "acme/widgets", 7, 90001)
	if !got {
		t.Errorf("claimActorNotAuthorizedNotice = false, want true (a successful Claim call already recorded the notice)")
	}
	if claimer.calls != 1 {
		t.Fatalf("claimer.calls = %d, want 1", claimer.calls)
	}
	if claimer.gotRepoFullName != "acme/widgets" || claimer.gotPRNumber != 7 || claimer.gotCommenterID != 90001 || claimer.gotTTL != time.Hour {
		t.Errorf("Claim called with (%q, %d, %d, %v), want (%q, %d, %d, %v)",
			claimer.gotRepoFullName, claimer.gotPRNumber, claimer.gotCommenterID, claimer.gotTTL,
			"acme/widgets", 7, 90001, time.Hour)
	}
}

// TestClaimActorNotAuthorizedNotice_AlreadyClaimedWithinTTLSkipsPost
// proves pgx.ErrNoRows (ClaimGitHubActorLinkNotice's own documented
// signal for "an existing notice is still within its TTL, left
// untouched") reports false -- no duplicate reply.
func TestClaimActorNotAuthorizedNotice_AlreadyClaimedWithinTTLSkipsPost(t *testing.T) {
	claimer := &fakeActorLinkNoticeClaimer{err: pgx.ErrNoRows}

	got := claimActorNotAuthorizedNotice(context.Background(), discardLogger(), claimer, time.Hour, "acme/widgets", 7, 90001)
	if got {
		t.Errorf("claimActorNotAuthorizedNotice = true, want false (pgx.ErrNoRows means already claimed within the TTL)")
	}
}

// TestClaimActorNotAuthorizedNotice_ClaimErrorFailsClosed is this
// package's own regression test for a confirmed audit finding: a genuine
// Claim failure OTHER than pgx.ErrNoRows (a transient Postgres error) must
// fail CLOSED -- report false, never post -- rather than risking a spam
// burst under a flaky DB. Before ActorLinkNoticeClaimer existed, this
// exact branch (the `default:` case) had no test anywhere in the tree,
// because the concrete store type offered no seam to force this failure;
// a regression flipping `return false` to `return true` here would have
// shipped undetected.
func TestClaimActorNotAuthorizedNotice_ClaimErrorFailsClosed(t *testing.T) {
	claimer := &fakeActorLinkNoticeClaimer{err: errors.New("connection reset by peer")}

	got := claimActorNotAuthorizedNotice(context.Background(), discardLogger(), claimer, time.Hour, "acme/widgets", 7, 90001)
	if got {
		t.Errorf("claimActorNotAuthorizedNotice = true, want false (a genuine Claim error must fail closed, never post)")
	}
}
