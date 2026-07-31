package github

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// fakeCommenterIdentityLookup is a test-only CommenterIdentityLookup -- no
// real Postgres round trip, exactly the point of identity.go's own
// CommenterIdentityLookup interface being narrow and locally defined (a
// confirmed audit finding on this package noted that the concrete
// *postgres.IdentityStore type this used to be hard-coded to offered no
// seam to force a genuine, non-ErrNoRows lookup failure in a test at all).
type fakeCommenterIdentityLookup struct {
	identity sqlcgen.Identity
	err      error

	gotProvider   sqlcgen.IdentityProvider
	gotExternalID string
	calls         int
}

func (f *fakeCommenterIdentityLookup) GetByProviderAndExternalID(_ context.Context, provider sqlcgen.IdentityProvider, externalID string) (sqlcgen.Identity, error) {
	f.calls++
	f.gotProvider = provider
	f.gotExternalID = externalID
	return f.identity, f.err
}

// TestResolveCommenterActor_ZeroCommenterID proves the defensive
// commenterID == 0 short-circuit never touches identities at all --
// mirrors payload.go's own doc comment that a real GitHub delivery's
// comment.user.id is never actually zero, but this function does not
// assume that.
func TestResolveCommenterActor_ZeroCommenterID(t *testing.T) {
	lookup := &fakeCommenterIdentityLookup{}

	actor, err := resolveCommenterActor(context.Background(), lookup, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if actor.Valid {
		t.Errorf("actor.Valid = true, want false (commenterID == 0)")
	}
	if lookup.calls != 0 {
		t.Errorf("lookup.calls = %d, want 0 (commenterID == 0 must never call the lookup)", lookup.calls)
	}
}

// TestResolveCommenterActor_NeverLinked proves a genuine pgx.ErrNoRows --
// this commenter has never signed into Narvi -- resolves to (invalid
// actor, nil error): a real, resolved verdict about link state, not a
// backend failure.
func TestResolveCommenterActor_NeverLinked(t *testing.T) {
	lookup := &fakeCommenterIdentityLookup{err: pgx.ErrNoRows}

	actor, err := resolveCommenterActor(context.Background(), lookup, 555001)
	if err != nil {
		t.Fatalf("err = %v, want nil (pgx.ErrNoRows means genuinely never linked, not a backend failure)", err)
	}
	if actor.Valid {
		t.Errorf("actor.Valid = true, want false (never linked)")
	}
	if lookup.gotExternalID != "555001" {
		t.Errorf("gotExternalID = %q, want %q", lookup.gotExternalID, "555001")
	}
}

// TestResolveCommenterActor_LinkedMatch proves a clean identities match
// resolves to (that identity's own user_id, nil error).
func TestResolveCommenterActor_LinkedMatch(t *testing.T) {
	wantUserID := pgtype.UUID{Bytes: [16]byte{1, 2, 3, 4}, Valid: true}
	lookup := &fakeCommenterIdentityLookup{identity: sqlcgen.Identity{UserID: wantUserID}}

	actor, err := resolveCommenterActor(context.Background(), lookup, 555002)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if actor != wantUserID {
		t.Errorf("actor = %+v, want %+v", actor, wantUserID)
	}
}

// TestResolveCommenterActor_LookupFailureIsNotNeverLinked is this
// package's own regression test for a confirmed audit finding: a genuine
// identities-lookup failure OTHER than pgx.ErrNoRows (a transient
// Postgres error, say) must be reported as an error DISTINCT from "never
// linked" -- never silently folded into the same invalid pgtype.UUID{}
// with a nil error, which is exactly what would let handler.go mistake a
// backend hiccup on an ALREADY-linked, fully-authorized commenter for a
// genuine "not linked" verdict and post the (false) "I don't recognize
// your GitHub account" reply.
func TestResolveCommenterActor_LookupFailureIsNotNeverLinked(t *testing.T) {
	wantErr := errors.New("connection reset by peer")
	lookup := &fakeCommenterIdentityLookup{err: wantErr}

	actor, err := resolveCommenterActor(context.Background(), lookup, 555003)
	if err == nil {
		t.Fatal("err = nil, want a non-nil error distinct from \"never linked\"")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, does not wrap the original lookup error %v", err, wantErr)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("err wraps pgx.ErrNoRows, want it to be distinct from the genuine-never-linked case")
	}
	if actor.Valid {
		t.Errorf("actor.Valid = true, want false (still an invalid actor on error -- the caller must use err, not actor.Valid, to tell this apart from never-linked)")
	}
}
