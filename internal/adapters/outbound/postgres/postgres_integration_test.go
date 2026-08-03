//go:build integration

// Integration test proving the PR-04 pipeline (embedded migrations +
// sqlc-generated queries + store skeletons) actually works against a real
// Postgres instance. Gated behind the "integration" build tag (needs
// Docker) so it does not run as part of the fast `make test` used by
// PR-01-03 — run via `make test-integration`.
package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/migrations"
)

// newMigrate builds a *migrate.Migrate wired to the embedded migrations
// FS (via the iofs source driver) and a *sql.DB opened against connStr
// (via golang-migrate's postgres database driver), proving the
// migrations.FS embed (§1) actually works — no reading files off disk by
// path.
func newMigrate(t *testing.T, connStr string) (*migrate.Migrate, *sql.DB) {
	t.Helper()

	db, err := sql.Open("pgx", connStr)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	dbDriver, err := migratepg.WithInstance(db, &migratepg.Config{})
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

	return m, db
}

func TestSchemaSqlcStoresPipeline(t *testing.T) {
	ctx := context.Background()

	// startCtx bounds ONLY the container-startup call below (image pull +
	// Docker daemon round trip + Postgres's own internal ready-wait) --
	// an unbounded context.Background() here can hang for Go's own full
	// 10-minute test-binary panic timeout if the CI runner's Docker daemon
	// stalls (CONFIRMED: CI run 30831633470's own goroutine dump showed
	// exactly this, blocked in moby/moby client.ContainerStart via
	// net/http.(*persistConn).roundTrip, panicking the whole test binary
	// after 10m0s and burning that binary's entire remaining test budget).
	// A healthy container start normally takes single-digit seconds; 2
	// minutes is generous margin for a slow image pull on a cold runner
	// cache while still failing fast, with an honest error, well short of
	// that 10-minute ceiling. ctx itself (unbounded) is still used for
	// everything else below, unchanged.
	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	container, err := tcpostgres.Run(startCtx, "postgres:17-alpine",
		tcpostgres.WithDatabase("narvi_test"),
		tcpostgres.WithUsername("narvi"),
		tcpostgres.WithPassword("narvi"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start postgres container: %v", err)
	}
	defer func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Errorf("terminate container: %v", err)
		}
	}()

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	// (a) run the embedded migrations up.
	m, migrateDB := newMigrate(t, connStr)
	defer migrateDB.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := narvipg.NewPool(ctx, connStr)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	// (b) exercise each of the 5 stores' Create/Get (or Upsert/Get)
	// round-trip.

	sessionStore := narvipg.NewSessionStore(pool)
	createdSession, err := sessionStore.Create(ctx, sqlcgen.CreateSessionParams{
		Title:       nil,
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
		CreatedBy:   pgtype.UUID{}, // NULL: no seeded user in this test
	})
	if err != nil {
		t.Fatalf("SessionStore.Create: %v", err)
	}
	if createdSession.SpawnSource != sqlcgen.SessionSpawnSourceWeb {
		t.Fatalf("created session spawn_source = %q, want %q", createdSession.SpawnSource, sqlcgen.SessionSpawnSourceWeb)
	}

	gotSession, err := sessionStore.Get(ctx, createdSession.ID)
	if err != nil {
		t.Fatalf("SessionStore.Get: %v", err)
	}
	if gotSession.ID != createdSession.ID {
		t.Fatalf("SessionStore.Get returned id %v, want %v", gotSession.ID, createdSession.ID)
	}
	if gotSession.Status != sqlcgen.SessionStatusCreated {
		t.Fatalf("session status = %q, want %q", gotSession.Status, sqlcgen.SessionStatusCreated)
	}

	sandboxStore := narvipg.NewSandboxStore(pool)
	createdSandbox, err := sandboxStore.Create(ctx, createdSession.ID)
	if err != nil {
		t.Fatalf("SandboxStore.Create: %v", err)
	}
	if createdSandbox.Gen != 1 {
		t.Fatalf("sandbox gen = %d, want 1", createdSandbox.Gen)
	}

	gotSandbox, err := sandboxStore.Get(ctx, createdSession.ID)
	if err != nil {
		t.Fatalf("SandboxStore.Get: %v", err)
	}
	if gotSandbox.ID != createdSandbox.ID {
		t.Fatalf("SandboxStore.Get returned id %v, want %v", gotSandbox.ID, createdSandbox.ID)
	}

	outboxStore := narvipg.NewOutboxStore(pool)
	createdEntry, err := outboxStore.Create(ctx, sqlcgen.CreateOutboxEntryParams{
		SessionID: createdSession.ID,
		Kind:      "slack_notify",
		Payload:   []byte(`{"channel":"C123"}`),
	})
	if err != nil {
		t.Fatalf("OutboxStore.Create: %v", err)
	}
	gotEntry, err := outboxStore.Get(ctx, createdEntry.ID)
	if err != nil {
		t.Fatalf("OutboxStore.Get: %v", err)
	}
	if gotEntry.Kind != "slack_notify" {
		t.Fatalf("outbox entry kind = %q, want %q", gotEntry.Kind, "slack_notify")
	}
	if gotEntry.Status != sqlcgen.OutboxStatusPending {
		t.Fatalf("outbox entry status = %q, want %q", gotEntry.Status, sqlcgen.OutboxStatusPending)
	}

	turnStore := narvipg.NewTurnStore(pool)
	createdTurn, err := turnStore.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: createdSession.ID,
		Status:    sqlcgen.TurnStatusProcessing,
	})
	if err != nil {
		t.Fatalf("TurnStore.Create (first processing turn): %v", err)
	}
	gotTurn, err := turnStore.Get(ctx, createdTurn.ID)
	if err != nil {
		t.Fatalf("TurnStore.Get: %v", err)
	}
	if gotTurn.Status != sqlcgen.TurnStatusProcessing {
		t.Fatalf("turn status = %q, want %q", gotTurn.Status, sqlcgen.TurnStatusProcessing)
	}

	// (c) exercise turns_one_processing_per_session: a second 'processing'
	// turn for the same session must be rejected by Postgres.
	_, err = turnStore.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: createdSession.ID,
		Status:    sqlcgen.TurnStatusProcessing,
	})
	if err == nil {
		t.Fatal("TurnStore.Create (second processing turn) succeeded, want unique_violation error")
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("second processing turn error = %v (%T), want a *pgconn.PgError", err, err)
	}
	const uniqueViolation = "23505"
	if pgErr.Code != uniqueViolation {
		t.Fatalf("second processing turn error code = %q, want %q (unique_violation); constraint=%q", pgErr.Code, uniqueViolation, pgErr.ConstraintName)
	}
	if pgErr.ConstraintName != "turns_one_processing_per_session" {
		t.Fatalf("second processing turn violated constraint %q, want turns_one_processing_per_session", pgErr.ConstraintName)
	}

	// (d) exercise session_timers UNIQUE(session_id, name) + the
	// ON CONFLICT upsert: upserting the same (session_id, name) twice with
	// different fires_at values must update the row in place, not
	// duplicate it.
	timerStore := narvipg.NewTimerStore(pool)
	firstFiresAt := time.Now().Add(1 * time.Hour).Truncate(time.Microsecond)
	firstTimer, err := timerStore.Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: createdSession.ID,
		Name:      "turn_deadline",
		FiresAt:   pgtype.Timestamptz{Time: firstFiresAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("TimerStore.Upsert (first arm): %v", err)
	}

	secondFiresAt := firstFiresAt.Add(30 * time.Minute)
	secondTimer, err := timerStore.Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: createdSession.ID,
		Name:      "turn_deadline",
		FiresAt:   pgtype.Timestamptz{Time: secondFiresAt, Valid: true},
	})
	if err != nil {
		t.Fatalf("TimerStore.Upsert (re-arm): %v", err)
	}

	if secondTimer.ID != firstTimer.ID {
		t.Fatalf("re-arm produced a different row (id %v != %v) — expected an update in place, not a duplicate insert", secondTimer.ID, firstTimer.ID)
	}
	if !secondTimer.FiresAt.Time.Equal(secondFiresAt) {
		t.Fatalf("re-armed fires_at = %v, want %v", secondTimer.FiresAt.Time, secondFiresAt)
	}

	gotTimer, err := timerStore.Get(ctx, sqlcgen.GetSessionTimerParams{
		SessionID: createdSession.ID,
		Name:      "turn_deadline",
	})
	if err != nil {
		t.Fatalf("TimerStore.Get: %v", err)
	}
	if !gotTimer.FiresAt.Time.Equal(secondFiresAt) {
		t.Fatalf("stored fires_at = %v, want %v (the re-armed value, proving update-in-place)", gotTimer.FiresAt.Time, secondFiresAt)
	}

	// (e) run migrations Down() all the way and assert no error.
	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate down: %v", err)
	}
}
