//go:build integration

// Integration test for OIDCSigningKeyStore.Rotate's own concurrent-
// rotation safety ("cloud identity: OIDC issuer, bindings,
// minting", §27.3) -- gated behind the "integration" build tag, matching
// this package's own conventions (testcontainers Postgres, embedded
// migrations).
package postgres_test

import (
	"context"
	"testing"
	"time"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
)

// TestOIDCSigningKeyStoreRotate_ConcurrentRotation_ReturnedRetiredRowIsAuthoritative
// pins the fix for a defect an adversarial review found in this Step's
// own httpapi.RotateCloudIdentitySigningKey handler: an EARLIER version
// of that handler took an ADDITIONAL, separate GetActive read BEFORE
// calling Rotate, purely to learn which key it was ABOUT to retire, for
// its own response/audit-log construction -- with a comment claiming "a
// race between this read and Rotate's own is impossible: both run inside
// the same, not-yet-committed transaction, so no other writer can
// interleave." That claim was false: pool.Begin's own TxOptions are
// empty, which is READ COMMITTED, not REPEATABLE READ/SERIALIZABLE --
// each STATEMENT takes its own fresh snapshot of whatever is already
// committed, regardless of when the surrounding transaction itself
// began, so being "inside the same not-yet-committed transaction" does
// NOT make two reads consistent with each other.
//
// This test reproduces the exact race an adversarial review's own
// "Interleaved" walkthrough describes, DETERMINISTICALLY -- no sleeps, no
// reliance on natural goroutine-scheduling timing. It calls the store's
// real, exported methods directly (GetActive, Rotate), in the exact
// sequence the removed handler code used, interleaved with a second,
// independent, fully-committing rotation from an entirely separate
// connection -- proving two things:
//
//  1. oldStyleRetiredKid (computed by literally calling the SAME
//     GetActive method the removed handler code called, BEFORE the
//     second rotation runs) ends up STALE and WRONG -- it names a key
//     that ANOTHER transaction already retired, not the key THIS
//     rotation's own Rotate call actually retired.
//  2. newStyleRetiredKid (Rotate's own returned retired row -- the fix)
//     is ALWAYS the key ACTUALLY retired by THIS call, independently
//     confirmed against the real oidc_signing_keys table.
//
// httpapi.RotateCloudIdentitySigningKey (cloudidentitykeys.go) no longer
// performs a separate GetActive read at all -- it uses ONLY Rotate's own
// returned retired row (case 2 above) for its response and audit_log
// entry. Restoring that removed read (and the store's old
// 2-return-value Rotate signature that made it necessary) would make the
// handler's own response/audit data equal oldStyleRetiredKid -- the
// value this test proves is wrong in this exact, deterministic scenario.
func TestOIDCSigningKeyStoreRotate_ConcurrentRotation_ReturnedRetiredRowIsAuthoritative(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewOIDCSigningKeyStore(pool)

	// Bootstrap: the first-ever rotation. k0 becomes the active key.
	k0, _, hadPrevious0, err := store.Rotate(ctx, time.Now(), "k0-"+t.Name(), []byte("priv0"), []byte(`{"kid":"k0"}`))
	if err != nil {
		t.Fatalf("bootstrap rotate: %v", err)
	}
	if hadPrevious0 {
		t.Fatalf("bootstrap rotate reported hadPrevious=true, want false (nothing to retire yet)")
	}

	// Goroutine A's own transaction -- opened, but A's own Rotate call is
	// deliberately delayed below until AFTER B's own rotation fully
	// commits, to force the exact interleaving.
	txA, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txA: %v", err)
	}
	defer func() { _ = txA.Rollback(ctx) }()
	keysA := store.WithTx(txA)

	// A's own "what's currently active" read -- the EXACT call the
	// removed handler code made, before ever calling Rotate. Sees k0 (B
	// has not run yet).
	previousActive, err := keysA.GetActive(ctx)
	if err != nil {
		t.Fatalf("keysA.GetActive: %v", err)
	}
	if previousActive.Kid != k0.Kid {
		t.Fatalf("previousActive.Kid = %q, want %q (k0)", previousActive.Kid, k0.Kid)
	}

	// B: a FULL, independent, COMMITTING second rotation -- runs to
	// completion on the plain (non-tx) store, i.e. an entirely separate
	// connection from txA -- retiring k0 and activating k1, entirely
	// BEFORE A's own Rotate call below.
	k1, retiredByB, hadPreviousB, err := store.Rotate(ctx, time.Now(), "k1-"+t.Name(), []byte("priv1"), []byte(`{"kid":"k1"}`))
	if err != nil {
		t.Fatalf("B's own rotate: %v", err)
	}
	if !hadPreviousB || retiredByB.Kid != k0.Kid {
		t.Fatalf("B's own rotate retired %+v (hadPrevious=%v), want k0 (%q)", retiredByB, hadPreviousB, k0.Kid)
	}

	// A resumes: calls Rotate on ITS OWN still-open transaction (txA).
	// Under READ COMMITTED, this internal read now sees B's own
	// already-committed k1 as the active key -- NOT k0, which A's own
	// EARLIER previousActive read (above) saw.
	createdByA, retiredByA, hadPreviousA, err := keysA.Rotate(ctx, time.Now(), "k2-"+t.Name(), []byte("priv2"), []byte(`{"kid":"k2"}`))
	if err != nil {
		t.Fatalf("A's own rotate: %v", err)
	}
	if err := txA.Commit(ctx); err != nil {
		t.Fatalf("commit txA: %v", err)
	}

	oldStyleRetiredKid := previousActive.Kid // what the REMOVED handler code would have reported/audited.
	newStyleRetiredKid := retiredByA.Kid     // what Rotate's own return (the fix) actually reports.

	if !hadPreviousA {
		t.Fatalf("A's own rotate reported hadPrevious=false, want true (k1 was active)")
	}
	if newStyleRetiredKid != k1.Kid {
		t.Fatalf("Rotate's own returned retired row = %q, want %q (k1, the key A's own transaction ACTUALLY retired)", newStyleRetiredKid, k1.Kid)
	}
	if oldStyleRetiredKid != k0.Kid {
		t.Fatalf("oldStyleRetiredKid = %q, want %q (k0 -- already retired by B by the time A's own Rotate ran)", oldStyleRetiredKid, k0.Kid)
	}
	if oldStyleRetiredKid == newStyleRetiredKid {
		t.Fatalf("oldStyleRetiredKid (%q) == newStyleRetiredKid (%q) -- this scenario is supposed to make them diverge; the test itself failed to exercise the race", oldStyleRetiredKid, newStyleRetiredKid)
	}

	// Independently confirm against the real table: k1 (not k0 again) is
	// the key A's own commit actually retired.
	var retiredAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT retired_at FROM oidc_signing_keys WHERE kid = $1`, k1.Kid).Scan(&retiredAt); err != nil {
		t.Fatalf("query k1 retired_at: %v", err)
	}
	if retiredAt == nil {
		t.Fatalf("k1 (%q) has no retired_at after A's own rotation committed -- A's own commit did not actually retire it", k1.Kid)
	}

	// createdByA is the new active key -- confirm it, and only it, is
	// currently active.
	activeNow, err := store.GetActive(ctx)
	if err != nil {
		t.Fatalf("get active: %v", err)
	}
	if activeNow.Kid != createdByA.Kid {
		t.Errorf("currently active kid = %q, want %q (k2, A's own newly-created key)", activeNow.Kid, createdByA.Kid)
	}
}
