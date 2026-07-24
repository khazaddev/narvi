//go:build integration

// Integration tests for the 3 new stores this Step (19) adds:
// EventStore.ListForSession, ArtifactStore (new), and WSTokenStore (new)
// -- gated behind the "integration" build tag, matching this package's
// own postgres_integration_test.go conventions (testcontainers Postgres,
// embedded migrations via golang-migrate's iofs source driver). Kept in
// its own file (rather than growing postgres_integration_test.go's own
// single pipeline test) so each of the 3 new stores gets its own focused,
// independently-runnable test.
package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every
// embedded migration up (proving migration 000016's ws_tokens table lands
// correctly alongside the rest), and returns a ready *pgxpool.Pool.
// t.Cleanup tears down both the pool and the container. Mirrors
// internal/app/sessionactor's and internal/adapters/inbound/wshub's own
// identical helper -- each DB-touching package builds its own copy rather
// than sharing one across package boundaries, per those packages' own
// established precedent.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("narvi_test"),
		tcpostgres.WithUsername("narvi"),
		tcpostgres.WithPassword("narvi"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Errorf("terminate container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	migrateDB, err := sql.Open("pgx", connStr)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = migrateDB.Close() })

	dbDriver, err := migratepg.WithInstance(migrateDB, &migratepg.Config{})
	if err != nil {
		t.Fatalf("migratepg.WithInstance: %v", err)
	}
	srcDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("iofs.New: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", srcDriver, "pgx", dbDriver)
	if err != nil {
		t.Fatalf("migrate.NewWithInstance: %v", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := narvipg.NewPool(ctx, connStr)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// createTestSession inserts a minimal session row and returns its id.
func createTestSession(ctx context.Context, t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()

	created, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
	})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	return created.ID
}

// TestEventStore_ListForSession proves cursor-paginated reads: oldest
// first, afterID=0 means "from the beginning", afterID > 0 excludes
// everything up to and including that id, and limit is honored exactly.
func TestEventStore_ListForSession(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	events := narvipg.NewEventStore(pool)

	var created []sqlcgen.CreateEventRow
	for i := 0; i < 5; i++ {
		row, err := events.Create(ctx, sqlcgen.CreateEventParams{
			SessionID: sessionID,
			Type:      "token",
			MessageID: fmt.Sprintf("msg-%d", i),
			Payload:   []byte(fmt.Sprintf(`{"n":%d}`, i)),
		})
		if err != nil {
			t.Fatalf("create event %d: %v", i, err)
		}
		created = append(created, row)
	}

	// From the beginning, unbounded page: all 5, oldest first.
	all, err := events.ListForSession(ctx, sessionID, 0, 10)
	if err != nil {
		t.Fatalf("ListForSession(0, 10): %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("len(all) = %d, want 5", len(all))
	}
	for i, e := range all {
		if e.ID != created[i].ID {
			t.Errorf("all[%d].ID = %d, want %d (order must be oldest-first)", i, e.ID, created[i].ID)
		}
	}

	// A small limit returns exactly that many, still oldest-first.
	page, err := events.ListForSession(ctx, sessionID, 0, 2)
	if err != nil {
		t.Fatalf("ListForSession(0, 2): %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("len(page) = %d, want 2", len(page))
	}
	if page[0].ID != created[0].ID || page[1].ID != created[1].ID {
		t.Errorf("page = %+v, want the first two created events", page)
	}

	// afterID excludes everything up to and including that id.
	rest, err := events.ListForSession(ctx, sessionID, page[len(page)-1].ID, 10)
	if err != nil {
		t.Fatalf("ListForSession(afterID, 10): %v", err)
	}
	if len(rest) != 3 {
		t.Fatalf("len(rest) = %d, want 3", len(rest))
	}
	if rest[0].ID != created[2].ID {
		t.Errorf("rest[0].ID = %d, want %d (first event after the cursor)", rest[0].ID, created[2].ID)
	}

	// A DIFFERENT session sees none of this session's events.
	otherSessionID := createTestSession(ctx, t, pool)
	none, err := events.ListForSession(ctx, otherSessionID, 0, 10)
	if err != nil {
		t.Fatalf("ListForSession for other session: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("other session's events = %d, want 0", len(none))
	}
}

// TestEventStore_ListRecentForSession proves ListRecentForSession's own
// mirror-image pagination direction from ListForSession above: newest id
// first, limit honored exactly, and scoped to sessionID alone -- the
// mechanism sessionactor.planContentText (Step 38, "plan mode,
// cross-channel") relies on to find a plan-mode turn's own final token
// event within a bounded window regardless of how much EARLIER history a
// long-lived session has already accumulated.
func TestEventStore_ListRecentForSession(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	events := narvipg.NewEventStore(pool)

	var created []sqlcgen.CreateEventRow
	for i := 0; i < 5; i++ {
		row, err := events.Create(ctx, sqlcgen.CreateEventParams{
			SessionID: sessionID,
			Type:      "token",
			MessageID: fmt.Sprintf("recent-msg-%d", i),
			Payload:   []byte(fmt.Sprintf(`{"n":%d}`, i)),
		})
		if err != nil {
			t.Fatalf("create event %d: %v", i, err)
		}
		created = append(created, row)
	}

	// Unbounded page: all 5, NEWEST first (the mirror image of
	// ListForSession's own oldest-first order).
	all, err := events.ListRecentForSession(ctx, sessionID, 10)
	if err != nil {
		t.Fatalf("ListRecentForSession(10): %v", err)
	}
	if len(all) != 5 {
		t.Fatalf("len(all) = %d, want 5", len(all))
	}
	for i, e := range all {
		want := created[len(created)-1-i]
		if e.ID != want.ID {
			t.Errorf("all[%d].ID = %d, want %d (order must be newest-first)", i, e.ID, want.ID)
		}
	}

	// A small limit returns exactly the NEWEST that many, e.g. the last 2
	// created (still newest-first) -- exactly what lets a caller bounded
	// to a fixed limit still reach a long session's own most RECENT
	// activity, unlike ListForSession's own oldest-first limit.
	page, err := events.ListRecentForSession(ctx, sessionID, 2)
	if err != nil {
		t.Fatalf("ListRecentForSession(2): %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("len(page) = %d, want 2", len(page))
	}
	if page[0].ID != created[4].ID || page[1].ID != created[3].ID {
		t.Errorf("page = %+v, want the last two created events, newest first", page)
	}

	// A DIFFERENT session sees none of this session's events.
	otherSessionID := createTestSession(ctx, t, pool)
	none, err := events.ListRecentForSession(ctx, otherSessionID, 10)
	if err != nil {
		t.Fatalf("ListRecentForSession for other session: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("other session's events = %d, want 0", len(none))
	}
}

// TestEventStore_Create_DedupesOnSessionIDAndMessageID proves Finding 2's
// own upsert contract end to end against a real Postgres instance: two
// CreateEvent calls carrying the SAME (session_id, message_id) return the
// identical row (same id and created_at) both times -- never a duplicate
// row, never a unique-constraint-violation error surfacing to the caller
// -- with Inserted true on the genuinely-fresh first call and false on the
// resend. Also proves the dedupe key is scoped to (session_id,
// message_id) together, not message_id alone: the SAME message_id under a
// DIFFERENT session_id is a distinct row entirely.
func TestEventStore_Create_DedupesOnSessionIDAndMessageID(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	events := narvipg.NewEventStore(pool)

	first, err := events.Create(ctx, sqlcgen.CreateEventParams{
		SessionID: sessionID,
		Type:      "heartbeat",
		MessageID: "dup-msg-1",
		Payload:   []byte(`{"n":1}`),
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	if !first.Inserted {
		t.Error("first.Inserted = false, want true (a genuinely fresh row)")
	}

	// A resend of the SAME (session_id, message_id) -- e.g. the sandbox
	// never saw the original's ack and retried it verbatim -- must return
	// the SAME row, not error and not create a second one, even though the
	// payload/type given this time differ (the DO UPDATE is a deliberate
	// no-op on type; whichever payload arrived first is what persists).
	second, err := events.Create(ctx, sqlcgen.CreateEventParams{
		SessionID: sessionID,
		Type:      "heartbeat",
		MessageID: "dup-msg-1",
		Payload:   []byte(`{"n":2}`),
	})
	if err != nil {
		t.Fatalf("create second (resend): %v", err)
	}
	if second.Inserted {
		t.Error("second.Inserted = true, want false (a deduped resend, not a fresh insert)")
	}
	if second.ID != first.ID {
		t.Errorf("second.ID = %d, want %d (same row as the first insert)", second.ID, first.ID)
	}
	if !second.CreatedAt.Time.Equal(first.CreatedAt.Time) {
		t.Errorf("second.CreatedAt = %v, want %v (same row, created_at must not change)", second.CreatedAt.Time, first.CreatedAt.Time)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE session_id = $1 AND message_id = $2`,
		sessionID, "dup-msg-1",
	).Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Errorf("event count for this (session_id, message_id) = %d, want 1 (no duplicate row)", count)
	}

	// The SAME message_id under a DIFFERENT session is a genuinely
	// distinct event, not a dedupe collision -- the unique index is on
	// (session_id, message_id) together, not message_id alone.
	otherSessionID := createTestSession(ctx, t, pool)
	third, err := events.Create(ctx, sqlcgen.CreateEventParams{
		SessionID: otherSessionID,
		Type:      "heartbeat",
		MessageID: "dup-msg-1",
		Payload:   []byte(`{"n":3}`),
	})
	if err != nil {
		t.Fatalf("create third (same message_id, different session): %v", err)
	}
	if !third.Inserted {
		t.Error("third.Inserted = false, want true (a different session's own row, not a dedupe)")
	}
	if third.ID == first.ID {
		t.Error("third.ID == first.ID, want a distinct row for a different session_id")
	}
}

// TestArtifactStore_ListForSession proves ordering (oldest first) and
// session scoping.
func TestArtifactStore_ListForSession(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	// No sqlc Create query exists yet for artifacts (see
	// artifact_store.go's own doc comment) -- insert directly via raw SQL,
	// test-fixture setup only.
	insertArtifact := func(artifactType, url string) pgtype.UUID {
		t.Helper()
		var id pgtype.UUID
		if err := pool.QueryRow(ctx,
			`INSERT INTO artifacts (session_id, type, url) VALUES ($1, $2, $3) RETURNING id`,
			sessionID, artifactType, url,
		).Scan(&id); err != nil {
			t.Fatalf("insert artifact: %v", err)
		}
		return id
	}

	firstID := insertArtifact("pr", "https://example.com/pr/1")
	time.Sleep(5 * time.Millisecond) // ensure a distinguishable, later created_at
	secondID := insertArtifact("preview", "https://example.com/preview/1")

	artifacts := narvipg.NewArtifactStore(pool)

	rows, err := artifacts.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListForSession: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	if rows[0].ID != firstID || rows[1].ID != secondID {
		t.Errorf("artifact order = [%v, %v], want [%v, %v] (oldest first)", rows[0].ID, rows[1].ID, firstID, secondID)
	}
	if rows[0].Type != sqlcgen.ArtifactTypePr {
		t.Errorf("rows[0].Type = %v, want %v", rows[0].Type, sqlcgen.ArtifactTypePr)
	}
	if rows[1].Type != sqlcgen.ArtifactTypePreview {
		t.Errorf("rows[1].Type = %v, want %v", rows[1].Type, sqlcgen.ArtifactTypePreview)
	}

	otherSessionID := createTestSession(ctx, t, pool)
	none, err := artifacts.ListForSession(ctx, otherSessionID)
	if err != nil {
		t.Fatalf("ListForSession for other session: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("other session's artifacts = %d, want 0", len(none))
	}
}

// TestWSTokenStore_CreateAndGetByHash proves Create/GetByHash's round
// trip plus the two scenarios internal/adapters/inbound/wshub's own
// client-subscribe verification depends on at the store level directly
// (not just through the WS handshake): a token hash that simply doesn't
// exist, and a token hash that DOES exist but for a different session --
// both distinguishable by the caller comparing the returned row's own
// SessionID, exactly as internal/adapters/inbound/wshub's
// verifyClientToken does. Also proves an expired row is still returned as-
// is by GetByHash (expiry enforcement is the CALLER's job, not the
// store's -- GetByHash is a plain lookup).
func TestWSTokenStore_CreateAndGetByHash(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)
	otherSessionID := createTestSession(ctx, t, pool)

	wsTokens := narvipg.NewWSTokenStore(pool)

	expiresAt := time.Now().Add(24 * time.Hour).Truncate(time.Microsecond)
	created, err := wsTokens.Create(ctx, sqlcgen.CreateWSTokenParams{
		SessionID: sessionID,
		UserID:    pgtype.UUID{}, // NULL -- no auth mechanism exists yet.
		TokenHash: "deadbeef",
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.UserID.Valid {
		t.Error("created.UserID.Valid = true, want false (NULL) -- no auth mechanism exists yet")
	}

	// (a) round trip: the exact hash created is found, for the right
	// session, with the expiry preserved.
	got, err := wsTokens.GetByHash(ctx, "deadbeef")
	if err != nil {
		t.Fatalf("GetByHash(deadbeef): %v", err)
	}
	if got.ID != created.ID {
		t.Errorf("GetByHash returned id %v, want %v", got.ID, created.ID)
	}
	if got.SessionID != sessionID {
		t.Errorf("GetByHash.SessionID = %v, want %v", got.SessionID, sessionID)
	}
	if !got.ExpiresAt.Time.Equal(expiresAt) {
		t.Errorf("GetByHash.ExpiresAt = %v, want %v", got.ExpiresAt.Time, expiresAt)
	}

	// (b) an unknown hash: pgx.ErrNoRows.
	_, err = wsTokens.GetByHash(ctx, "not-a-real-hash")
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("GetByHash(unknown) error = %v, want pgx.ErrNoRows", err)
	}

	// (c) a hash that exists but for a DIFFERENT session: found by hash,
	// but its own SessionID does not match otherSessionID -- the caller
	// (wshub's verifyClientToken) is what turns this into a rejection, not
	// the store itself; this test only proves the store returns the row
	// with an honestly-mismatched SessionID (not otherSessionID).
	got2, err := wsTokens.GetByHash(ctx, "deadbeef")
	if err != nil {
		t.Fatalf("GetByHash(deadbeef) again: %v", err)
	}
	if got2.SessionID == otherSessionID {
		t.Error("GetByHash returned a row scoped to otherSessionID -- token/session scoping is broken")
	}

	// (d) an EXPIRED row is still returned as-is by GetByHash -- expiry
	// enforcement is the caller's own job (§6.2 close code 4002), not
	// this store's.
	expiredAt := time.Now().Add(-1 * time.Hour).Truncate(time.Microsecond)
	if _, err := wsTokens.Create(ctx, sqlcgen.CreateWSTokenParams{
		SessionID: sessionID,
		UserID:    pgtype.UUID{},
		TokenHash: "expired-hash",
		ExpiresAt: pgtype.Timestamptz{Time: expiredAt, Valid: true},
	}); err != nil {
		t.Fatalf("Create (expired): %v", err)
	}
	gotExpired, err := wsTokens.GetByHash(ctx, "expired-hash")
	if err != nil {
		t.Fatalf("GetByHash(expired-hash): %v", err)
	}
	if !gotExpired.ExpiresAt.Time.Before(time.Now()) {
		t.Errorf("gotExpired.ExpiresAt = %v, want a time in the past", gotExpired.ExpiresAt.Time)
	}
}
