//go:build integration

// Integration tests proving Sweeper.SweepOnce against a REAL Postgres
// instance (§9.1) -- gated behind the "integration" build tag, matching
// internal/app/reconciler/reconciler_integration_test.go's own conventions
// exactly (testcontainers Postgres, embedded migrations via golang-migrate's
// iofs source driver, a real *pgxpool.Pool). This package builds its own
// copy of newTestPool/createTestSession rather than sharing one across
// package boundaries -- see that file's own doc comment for why. Run via
// `make test-integration`.
package uploadsweep_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/app/ports"
	"github.com/narvidev/narvi/internal/app/uploadsweep"
	"github.com/narvidev/narvi/internal/platform"
	"github.com/narvidev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every embedded
// migration up, and returns a ready *pgxpool.Pool. t.Cleanup tears down
// both the pool and the container. Mirrors
// internal/app/reconciler/reconciler_integration_test.go's own identical
// helper (including its own container-start watchdog, kept verbatim: see
// that file's own doc comment for the real CI hangs this defends against).
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	const containerStartWatchdog = 2*time.Minute + 15*time.Second
	type containerStartResult struct {
		container *tcpostgres.PostgresContainer
		err       error
	}
	startCh := make(chan containerStartResult, 1)
	var startGroup errgroup.Group
	startGroup.Go(func() error {
		container, err := tcpostgres.Run(startCtx, "postgres:17-alpine",
			tcpostgres.WithDatabase("narvi_test"),
			tcpostgres.WithUsername("narvi"),
			tcpostgres.WithPassword("narvi"),
			tcpostgres.BasicWaitStrategies(),
		)
		startCh <- containerStartResult{container: container, err: err}
		return nil
	})

	var container *tcpostgres.PostgresContainer
	var err error
	select {
	case res := <-startCh:
		container, err = res.container, res.err
		if err != nil {
			t.Fatalf("start postgres container: %v", err)
		}
	case <-time.After(containerStartWatchdog):
		t.Fatalf("start postgres container: tcpostgres.Run did not return within %s -- Docker daemon likely "+
			"stalled without honoring context cancellation (see this function's own doc comment)", containerStartWatchdog)
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

// createTestSessionWithSandbox inserts a session + sandbox row (the
// abandonment sweep's own resolveAbandoned reads the sandbox's gen for the
// synthesized artifact event) and returns the session id.
func createTestSessionWithSandbox(ctx context.Context, t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()

	session, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
	})
	if err != nil {
		t.Fatalf("create test session: %v", err)
	}
	if _, err := narvipg.NewSandboxStore(pool).Create(ctx, session.ID); err != nil {
		t.Fatalf("create test sandbox: %v", err)
	}
	return session.ID
}

// createUploadRow inserts an upload artifact row directly (bypassing the
// mint endpoint, which this package does not depend on) at the given
// status and createdAt -- backdating createdAt via a direct UPDATE after
// insert, since CreateUploadArtifact always stamps now().
func createUploadRow(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, status sqlcgen.ArtifactStatus, createdAt time.Time) sqlcgen.Artifact {
	t.Helper()

	artifacts := narvipg.NewArtifactStore(pool)
	blobKey := "sessions/" + sessionID.String() + "/uploads/test"
	sizeBytes := int64(42)
	contentType := "text/plain"
	filename := "test.txt"
	url := "/api/sessions/" + sessionID.String() + "/uploads/test/content"

	var id pgtype.UUID
	if err := id.Scan(newTestUUID()); err != nil {
		t.Fatalf("scan test artifact id: %v", err)
	}

	row, err := artifacts.CreateUpload(ctx, sqlcgen.CreateUploadArtifactParams{
		ID:          id,
		SessionID:   sessionID,
		Url:         url,
		BlobKey:     &blobKey,
		SizeBytes:   &sizeBytes,
		ContentType: &contentType,
		Filename:    &filename,
	})
	if err != nil {
		t.Fatalf("create test upload artifact: %v", err)
	}

	if status != sqlcgen.ArtifactStatusPending {
		if _, err := pool.Exec(ctx, "UPDATE artifacts SET status = $1 WHERE id = $2", status, row.ID); err != nil {
			t.Fatalf("backdate test artifact status: %v", err)
		}
	}
	if _, err := pool.Exec(ctx, "UPDATE artifacts SET created_at = $1 WHERE id = $2", createdAt, row.ID); err != nil {
		t.Fatalf("backdate test artifact created_at: %v", err)
	}

	got, err := artifacts.GetForSession(ctx, row.ID, sessionID)
	if err != nil {
		t.Fatalf("re-read backdated test artifact: %v", err)
	}
	return got
}

var testUUIDCounter int

// newTestUUID returns a syntactically valid, distinct-per-call UUID
// string without pulling in google/uuid as a new dependency for this
// package's own tests.
func newTestUUID() string {
	testUUIDCounter++
	return sqlcTestUUIDPrefix + padHex(testUUIDCounter)
}

const sqlcTestUUIDPrefix = "aaaaaaaa-bbbb-cccc-dddd-"

func padHex(n int) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 12)
	for i := 11; i >= 0; i-- {
		out[i] = hexDigits[n&0xf]
		n >>= 4
	}
	return string(out)
}

type recordingBroadcaster struct {
	events []string
}

func (r *recordingBroadcaster) Broadcast(sessionID string, payload json.RawMessage) {
	r.events = append(r.events, sessionID+":"+string(payload))
}

var _ ports.EventBroadcaster = (*recordingBroadcaster)(nil)

func TestSweepOnce_AbandonsOnlyOldPendingRows(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	artifacts := narvipg.NewArtifactStore(pool)
	events := narvipg.NewEventStore(pool)
	outbox := narvipg.NewOutboxStore(pool, false)
	sandboxes := narvipg.NewSandboxStore(pool)
	broadcaster := &recordingBroadcaster{}

	timeouts := platform.DefaultTimeouts()
	sweeper, err := uploadsweep.NewSweeper(pool, artifacts, events, outbox, sandboxes, broadcaster, timeouts)
	if err != nil {
		t.Fatalf("NewSweeper: %v", err)
	}

	sessionOld := createTestSessionWithSandbox(ctx, t, pool)
	oldRow := createUploadRow(ctx, t, pool, sessionOld, sqlcgen.ArtifactStatusPending, time.Now().Add(-timeouts.UploadPendingSweepAfter-time.Hour))

	sessionRecent := createTestSessionWithSandbox(ctx, t, pool)
	recentRow := createUploadRow(ctx, t, pool, sessionRecent, sqlcgen.ArtifactStatusPending, time.Now())

	sessionReady := createTestSessionWithSandbox(ctx, t, pool)
	readyRow := createUploadRow(ctx, t, pool, sessionReady, sqlcgen.ArtifactStatusReady, time.Now().Add(-timeouts.UploadPendingSweepAfter-time.Hour))

	if err := sweeper.SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce: %v", err)
	}

	gotOld, err := artifacts.GetForSession(ctx, oldRow.ID, sessionOld)
	if err != nil {
		t.Fatalf("re-read old row: %v", err)
	}
	if gotOld.Status != sqlcgen.ArtifactStatusFailed {
		t.Errorf("old pending row status = %q, want %q", gotOld.Status, sqlcgen.ArtifactStatusFailed)
	}
	if gotOld.FailureReason == nil || *gotOld.FailureReason != sqlcgen.ArtifactFailureReasonAbandoned {
		t.Errorf("old pending row failure_reason = %v, want %q", gotOld.FailureReason, sqlcgen.ArtifactFailureReasonAbandoned)
	}

	gotRecent, err := artifacts.GetForSession(ctx, recentRow.ID, sessionRecent)
	if err != nil {
		t.Fatalf("re-read recent row: %v", err)
	}
	if gotRecent.Status != sqlcgen.ArtifactStatusPending {
		t.Errorf("recent pending row status = %q, want unchanged %q", gotRecent.Status, sqlcgen.ArtifactStatusPending)
	}

	gotReady, err := artifacts.GetForSession(ctx, readyRow.ID, sessionReady)
	if err != nil {
		t.Fatalf("re-read ready row: %v", err)
	}
	if gotReady.Status != sqlcgen.ArtifactStatusReady {
		t.Errorf("old ready row status = %q, want unchanged %q", gotReady.Status, sqlcgen.ArtifactStatusReady)
	}

	// A blob_delete outbox entry was enqueued for the abandoned row.
	pending, err := outbox.ListDuePending(ctx, 10)
	if err != nil {
		t.Fatalf("list due pending outbox entries: %v", err)
	}
	foundBlobDelete := false
	for _, entry := range pending {
		if entry.Kind == string(ports.NotificationKindBlobDelete) {
			foundBlobDelete = true
			var payload struct {
				Key string `json:"key"`
			}
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				t.Fatalf("unmarshal blob_delete payload: %v", err)
			}
			if payload.Key == "" {
				t.Error("blob_delete outbox payload has an empty key")
			}
		}
	}
	if !foundBlobDelete {
		t.Error("no blob_delete outbox entry was enqueued for the abandoned upload")
	}

	// The artifact event was broadcast after commit.
	if len(broadcaster.events) != 1 {
		t.Fatalf("broadcaster recorded %d events, want 1 (only the abandoned row)", len(broadcaster.events))
	}
	if !containsAll(broadcaster.events[0], sessionOld.String(), `"status":"failed"`, `"failureReason":"abandoned"`) {
		t.Errorf("broadcast event = %q, missing expected fields", broadcaster.events[0])
	}
}

func TestSweepOnce_NoRowsIsANoOp(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	artifacts := narvipg.NewArtifactStore(pool)
	events := narvipg.NewEventStore(pool)
	outbox := narvipg.NewOutboxStore(pool, false)
	sandboxes := narvipg.NewSandboxStore(pool)

	sweeper, err := uploadsweep.NewSweeper(pool, artifacts, events, outbox, sandboxes, nil, platform.DefaultTimeouts())
	if err != nil {
		t.Fatalf("NewSweeper: %v", err)
	}

	if err := sweeper.SweepOnce(ctx); err != nil {
		t.Fatalf("SweepOnce on an empty table: %v", err)
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
