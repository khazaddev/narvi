//go:build integration

// Integration tests for Activate against a REAL Postgres instance --
// proving both halves of §30.8's own promotion contract: the quarantine
// refusal while unhandled shadow-era rows exist, and the actual flip +
// promotion fence + audit_log entry once none remain. Run via
// `make test-integration`.
package shadowoperator_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/shadowoperator"
)

// testActor creates a real users row and returns its id -- audit_log.
// actor_user_id carries a REFERENCES users(id) foreign key (migrations/
// 000013_audit_log.up.sql), so a hand-invented UUID with no backing row
// fails that constraint the moment auditlog.Record tries to insert it.
func testActor(ctx context.Context, t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()
	user, err := narvipg.NewUserStore(pool).Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "admin@example.test",
		DisplayName:  "Test Admin",
		Role:         sqlcgen.UserRoleAdmin,
	})
	if err != nil {
		t.Fatalf("create test actor user: %v", err)
	}
	return user.ID
}

// TestActivate_RefusesWhileUnhandledShadowEraRowsExist proves §30.8's own
// shadow-era artifact quarantine: Activate must not promote a repository
// while an outbox row it stamped suppressed_in_shadow is still
// unresolved -- and must leave repo_settings completely untouched when it
// refuses.
func TestActivate_RefusesWhileUnhandledShadowEraRowsExist(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/quarantine-repo"

	sessionID := createTestSessionWithRepo(ctx, t, pool, repoFullName)
	outbox := narvipg.NewOutboxStore(pool, false)
	if _, err := outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: sessionID,
		Kind:      "github_verdict",
		Payload:   []byte(`{}`),
	}); err != nil {
		t.Fatalf("create outbox row: %v", err)
	}

	reads := narvipg.NewShadowOperatorReadStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	_, err := shadowoperator.Activate(ctx, reads, repoSettings, auditLog, repoFullName, testActor(ctx, t, pool))
	if err == nil {
		t.Fatal("Activate() error = nil, want ErrUnhandledShadowEraRows while a pending suppressed row exists")
	}
	var unhandled *shadowoperator.ErrUnhandledShadowEraRows
	if !errors.As(err, &unhandled) {
		t.Fatalf("Activate() error = %v (%T), want *ErrUnhandledShadowEraRows", err, err)
	}
	if unhandled.Count != 1 {
		t.Errorf("unhandled.Count = %d, want 1", unhandled.Count)
	}

	settings, getErr := repoSettings.Get(ctx, repoFullName)
	if getErr == nil && settings.LiveEgressEnabled {
		t.Errorf("repo_settings.LiveEgressEnabled = true after a refused Activate, want untouched (false or absent row)")
	}
}

// TestActivate_PromotesAndStampsFenceAndAuditLog proves the happy path:
// with no unhandled rows, Activate flips live_egress_enabled, the
// promotion fence is stamped (UpsertLiveEgressEnabled's own CASE logic),
// and a "shadow.activated" audit_log entry lands attributed to the real
// caller.
func TestActivate_PromotesAndStampsFenceAndAuditLog(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/promote-repo"

	reads := narvipg.NewShadowOperatorReadStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	actor := testActor(ctx, t, pool)

	updated, err := shadowoperator.Activate(ctx, reads, repoSettings, auditLog, repoFullName, actor)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !updated.LiveEgressEnabled {
		t.Fatalf("updated.LiveEgressEnabled = false, want true")
	}
	if !updated.LiveEgressPromotedAt.Valid {
		t.Errorf("updated.LiveEgressPromotedAt.Valid = false, want true (a fresh promotion fence)")
	}

	entries, err := auditLog.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("list audit log: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "shadow.activated" && e.ResourceID == repoFullName {
			found = true
			if !e.ActorUserID.Valid || e.ActorUserID != actor {
				t.Errorf("audit log entry ActorUserID = %v, want %v", e.ActorUserID, actor)
			}
		}
	}
	if !found {
		t.Errorf("audit_log entries = %+v, want a shadow.activated entry for %s", entries, repoFullName)
	}
}

// TestActivate_ReactivatingAnAlreadyLiveRepoIsIdempotent proves a second
// Activate call on an already-promoted repository succeeds (never an
// error) and does not slide the promotion fence forward -- re-affirming
// an already-live repo must not exclude verdicts that were already valid
// candidates under the earlier promotion (queries/reposettings.sql's own
// UpsertLiveEgressEnabled doc comment).
func TestActivate_ReactivatingAnAlreadyLiveRepoIsIdempotent(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/reactivate-repo"

	reads := narvipg.NewShadowOperatorReadStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)
	actor := testActor(ctx, t, pool)

	first, err := shadowoperator.Activate(ctx, reads, repoSettings, auditLog, repoFullName, actor)
	if err != nil {
		t.Fatalf("first Activate: %v", err)
	}
	second, err := shadowoperator.Activate(ctx, reads, repoSettings, auditLog, repoFullName, actor)
	if err != nil {
		t.Fatalf("second Activate: %v", err)
	}
	if !second.LiveEgressEnabled {
		t.Errorf("second.LiveEgressEnabled = false, want true")
	}
	if first.LiveEgressPromotedAt.Time != second.LiveEgressPromotedAt.Time {
		t.Errorf("promotion fence moved on re-activation: first=%v second=%v, want unchanged", first.LiveEgressPromotedAt.Time, second.LiveEgressPromotedAt.Time)
	}
}

// TestActivate_DeliveredPassThroughRowDoesNotBlockForever is the case
// that made Activate impossible for whole classes of repository.
//
// Every outbox row is stamped suppressed_in_shadow regardless of kind,
// but a PASS-THROUGH kind (blob_delete, sentinel_auto_fix, linear_digest)
// is never diverted to the ledger, so it reaches "delivered" with
// delivered_to_ledger still false. The gate required BOTH, so one swept
// upload or one sentinel verdict during an evaluation left a row that was
// finished, would never change again, and counted as in flight forever.
// Activate returned 409 permanently — under a message telling the
// operator to wait for it to settle.
//
// A repository that ran the sentinel lane in shadow could never graduate.
func TestActivate_DeliveredPassThroughRowDoesNotBlockForever(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/passthrough-repo"

	sessionID := createTestSessionWithRepo(ctx, t, pool, repoFullName)
	outbox := narvipg.NewOutboxStore(pool, false)
	row, err := outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: sessionID,
		Kind:      "blob_delete",
		Payload:   []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create outbox row: %v", err)
	}
	if !row.SuppressedInShadow {
		t.Fatalf("the row was not stamped suppressed_in_shadow; this test's premise is that EVERY kind is stamped")
	}
	// Delivered the way a pass-through kind really is: the outbox worker
	// never touches delivered_to_ledger for it.
	if _, err := pool.Exec(ctx, `UPDATE outbox SET status = 'delivered', delivered_at = now() WHERE id = $1`, row.ID); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}

	reads := narvipg.NewShadowOperatorReadStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	updated, err := shadowoperator.Activate(ctx, reads, repoSettings, auditLog, repoFullName, testActor(ctx, t, pool))
	if err != nil {
		t.Fatalf("Activate() error = %v, want nil: a delivered pass-through row is finished and will never change again, so blocking on it blocks forever", err)
	}
	if !updated.LiveEgressEnabled {
		t.Error("LiveEgressEnabled = false after a successful Activate")
	}
}

// TestActivate_SeesAnUnsettledRowOlderThanTheDisplayLimit closes a
// fail-OPEN in the gate.
//
// The display query orders newest-first across the WHOLE deployment and
// only then filters to one repository in Go. Counting from it meant this
// repository's own OLDEST unsettled row — the one most likely to be
// genuinely stuck, and exactly what a promotion gate exists to notice —
// was the first to fall off the end, and Activate proceeded as if nothing
// were outstanding.
//
// Here the repo's pending row is buried under more recent suppressed rows
// belonging to a different repository. The gate must still see it.
func TestActivate_SeesAnUnsettledRowOlderThanTheDisplayLimit(t *testing.T) {
	pool := newTestPool(t)
	ctx := context.Background()
	const repoFullName = "acme/buried-repo"
	const noisyRepo = "acme/noisy-repo"

	buriedSession := createTestSessionWithRepo(ctx, t, pool, repoFullName)
	outbox := narvipg.NewOutboxStore(pool, false)
	buried, err := outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: buriedSession, Kind: "github_verdict", Payload: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create buried outbox row: %v", err)
	}
	// Age it so every later row sorts ahead of it.
	if _, err := pool.Exec(ctx, `UPDATE outbox SET created_at = now() - interval '30 days' WHERE id = $1`, buried.ID); err != nil {
		t.Fatalf("age the buried row: %v", err)
	}

	// Bury it under more suppressed rows than the gate would ever have
	// read, all belonging to a DIFFERENT repository.
	noisySession := createTestSessionWithRepo(ctx, t, pool, noisyRepo)
	for i := 0; i < int(shadowoperator.DefaultEntryLimit)+10; i++ {
		if _, err := outbox.Create(ctx, sqlcgen.CreateOutboxEntryParams{
			SessionID: noisySession, Kind: "github_verdict", Payload: []byte(`{}`),
		}); err != nil {
			t.Fatalf("create noise row %d: %v", i, err)
		}
	}

	reads := narvipg.NewShadowOperatorReadStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	_, err = shadowoperator.Activate(ctx, reads, repoSettings, auditLog, repoFullName, testActor(ctx, t, pool))
	var unhandled *shadowoperator.ErrUnhandledShadowEraRows
	if !errors.As(err, &unhandled) {
		t.Fatalf("Activate() error = %v (%T), want *ErrUnhandledShadowEraRows: this repo's own pending row must be visible to the gate however many newer rows other repositories have produced", err, err)
	}

	settings, getErr := repoSettings.Get(ctx, repoFullName)
	if getErr == nil && settings.LiveEgressEnabled {
		t.Error("repo_settings.LiveEgressEnabled = true: a repository was armed for live egress while one of its own shadow-era rows was still in flight")
	}
}
