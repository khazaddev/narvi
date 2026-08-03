//go:build integration

// Resilience test #1 (§9.3, docs/IMPLEMENTATION_PLAN.md row 21, design
// decision 12): "Kill the CP pod mid-turn -> actor rehydrates, turn
// resumes or fails-with-reason; no stuck processing." This Step's own
// resilience test targets the "fails-with-reason" branch ONLY -- real
// turn RESUME (continuing a Processing turn on a fresh actor without
// failing it) is explicitly Steps 23 ("resume") and 28 ("turn recovery")'s
// own job; no turn-resume machinery of any kind exists anywhere in this
// codebase today (confirmed: domain/turn's own transition table has no
// edge out of Processing except Completed/Failed/Cancelled), and §9.3
// scenario #1's own "or" phrasing already permits targeting only this
// branch.
//
// This is a genuinely self-contained integration test using this
// codebase's own already-established testcontainers-Postgres pattern
// (matching every prior Step's own *_integration_test.go convention
// exactly) -- it does NOT build reusable resilience-harness
// infrastructure, which is explicitly Step 30's own job
// (test/resilience/README.md, left completely untouched by this Step).
package sessionactor

import (
	"context"
	"database/sql"
	"errors"
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

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPoolPair spins up ONE throwaway Postgres container (running every
// embedded migration up), then returns TWO INDEPENDENT *pgxpool.Pool
// instances connected to it -- mirroring exactly how two real pods would
// each hold their own connection pool to one shared database. This
// matters for THIS test specifically: "kill pod A" must abruptly drop
// only pod A's own connections (including whichever one held the
// session's advisory lock), never pod B's -- a single shared pool would
// make that distinction impossible to express at all. Neither pool is
// registered for auto-cleanup via t.Cleanup here: poolA is deliberately
// NEVER closed by this test at all (see killAdvisoryLockHolder's own call
// site for why -- a real killed process leaves nothing to gracefully
// close either, and pgxpool.Pool.Close() would otherwise hang the test
// itself); poolB's own cleanup is registered explicitly by the test body,
// ordered relative to registryB.Shutdown().
func newTestPoolPair(t *testing.T) (poolA, poolB *pgxpool.Pool) {
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
	defer func() { _ = migrateDB.Close() }()

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

	poolA, err = narvipg.NewPool(ctx, connStr)
	if err != nil {
		t.Fatalf("NewPool (A): %v", err)
	}
	poolB, err = narvipg.NewPool(ctx, connStr)
	if err != nil {
		t.Fatalf("NewPool (B): %v", err)
	}
	return poolA, poolB
}

// killAdvisoryLockHolder terminates the Postgres backend(s) currently
// holding a granted session-level advisory lock (via a SEPARATE
// administrative connection, adminPool) -- see this test's own inline
// comment at its call site for why this, not pgxpool.Pool.Close(), is
// the correct way to simulate "pod A" abruptly disappearing. Fails the
// test if no such backend is found (a real bug in the test's own setup,
// not a condition this test is designed to tolerate).
func killAdvisoryLockHolder(ctx context.Context, t *testing.T, adminPool *pgxpool.Pool) {
	t.Helper()

	rows, err := adminPool.Query(ctx,
		`SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND granted = true`)
	if err != nil {
		t.Fatalf("query advisory lock holders: %v", err)
	}
	var pids []int32
	for rows.Next() {
		var pid int32
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			t.Fatalf("scan pid: %v", err)
		}
		pids = append(pids, pid)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate advisory lock holders: %v", err)
	}
	if len(pids) == 0 {
		t.Fatal("no granted advisory lock found to kill -- did pod A actually acquire it?")
	}

	for _, pid := range pids {
		if _, err := adminPool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
			t.Fatalf("pg_terminate_backend(%d): %v", pid, err)
		}
	}
}

// TestResilience_KillPodMidTurn_TurnFailsWithReason_NoStuckProcessing is
// §9.3 scenario #1's own "fails-with-reason; no stuck processing" half.
//
// Sequencing (each step's own reasoning matters, see inline comments):
//  1. Seed a session + a turn already Processing, with dispatched_at
//     comfortably in the past against a tiny injected TurnDeadline, and
//     an already-overdue turn_deadline row -- directly via the stores,
//     bypassing the live dispatch path entirely (mirrors
//     TestTurnDeadlineTimerFired_FullRoundTrip's own EXISTING precedent
//     exactly): this test's own narrow purpose is proving REHYDRATION
//     correctness (a fresh actor on a fresh pod picks up and correctly
//     resolves a timer nobody else is going to handle), not dispatch
//     correctness (already covered by dispatch_integration_test.go and
//     timerfired_integration_test.go respectively).
//  2. Hydrate an actor for this session via registryA.GetOrSpawn -- this
//     is what makes "pod A" a genuine, real owner of the session (holding
//     the real Postgres advisory lock on a real connection from poolA),
//     not a hypothetical one.
//  3. "Kill pod A": terminate the exact Postgres BACKEND holding the
//     advisory lock directly, via a separate administrative connection
//     (killAdvisoryLockHolder, using pg_terminate_backend), WITHOUT
//     calling registryA.Shutdown() first. A real `kill -9` never runs
//     graceful shutdown code either -- calling Shutdown() here would test
//     the ALREADY-PROVEN graceful path (registry_integration_test.go),
//     not the scenario this test exists for. This is the actual, precise
//     causal mechanism a real `kill -9` relies on too (the OS closes the
//     dead process's TCP sockets; Postgres notices the connection is gone
//     and reaps that backend, releasing every lock it held) -- reproduced
//     faithfully at exactly the layer that matters for this test's own
//     assertions, without fighting puddle's (pgxpool's own underlying
//     connection pool) cooperative-release bookkeeping: actor A's own
//     lock connection is held for its entire lifetime by design
//     (hydrate.go) and is never released except by its own graceful
//     shutdown() path, so pgxpool.Pool.Close() itself would hang the TEST
//     ITSELF forever waiting on that same connection -- an artifact of
//     simulating the kill from WITHIN the same test process, not a real
//     second pod -- see this test's own inline comment at the call site
//     for the full reasoning. Postgres auto-releases an advisory lock the
//     instant the backend connection holding it terminates (this is what
//     makes the simulation FAITHFUL, not merely "stop referencing a Go
//     object": if this test instead just dropped its own reference to
//     registryA/poolA without closing the underlying connections, the
//     advisory lock would remain held by a live-but-orphaned Postgres
//     backend process indefinitely, and pod B's own hydration attempt
//     below would correctly, but unhelpfully, report
//     ErrSessionActorElsewhere forever -- exactly the failure this test
//     must NOT exhibit).
//  4. Construct registryB on a FRESH pool (poolB) and run ITS OWN
//     PumpOnce (exported specifically for deterministic test-driving)
//     until the overdue timer is claimed and delivered to a freshly
//     hydrated actor on pod B.
//  5. Assert, reading everything back from Postgres (never from in-memory
//     state): the turn transitioned to Failed with a real failure reason,
//     the session's own derived status/failure_reason followed, and a
//     synthetic execution_complete event was appended (§3.3's own
//     "clients always see one terminal event per turn" contract) -- i.e.
//     exactly the "fails-with-reason; no stuck processing" half of §9.3
//     #1, honestly not attempting the "resumes" half (out of scope, see
//     this file's own top comment).
func TestResilience_KillPodMidTurn_TurnFailsWithReason_NoStuckProcessing(t *testing.T) {
	ctx := context.Background()
	poolA, poolB := newTestPoolPair(t)

	sessionID := createTestSession(ctx, t, poolA)

	timeouts := platform.DefaultTimeouts()
	timeouts.TurnDeadline = 50 * time.Millisecond // tiny, injected -- not the real 60m default

	turnStore := narvipg.NewTurnStore(poolA)
	created, err := turnStore.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusPending,
	})
	if err != nil {
		t.Fatalf("create turn: %v", err)
	}

	dispatchedAt := time.Now().Add(-1 * time.Hour) // comfortably past the tiny deadline
	if _, err := turnStore.UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID:           created.ID,
		Status:       sqlcgen.TurnStatusProcessing,
		DispatchedAt: pgtype.Timestamptz{Time: dispatchedAt, Valid: true},
	}); err != nil {
		t.Fatalf("move turn to processing: %v", err)
	}

	timerStore := narvipg.NewTimerStore(poolA)
	overdue := time.Now().Add(-1 * time.Minute) // already due
	if _, err := timerStore.Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: sessionID,
		Name:      TimerTurnDeadline,
		FiresAt:   pgtype.Timestamptz{Time: overdue, Valid: true},
	}); err != nil {
		t.Fatalf("arm overdue turn_deadline timer: %v", err)
	}

	// --- Step 2: pod A hydrates and genuinely owns this session (a real
	// advisory lock on a real poolA connection). ---
	registryA, err := NewRegistry(ctx, poolA, timeouts, nil, nil, nil, "", nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, err := registryA.GetOrSpawn(ctx, sessionID); err != nil {
		t.Fatalf("registryA.GetOrSpawn: %v", err)
	}

	// --- Step 3: kill pod A. Deliberately NOT registryA.Shutdown() first
	// -- see this test's own doc comment for why. ---
	//
	// pgxpool.Pool.Close() itself is NOT the right tool here, and
	// deliberately not used: puddle (pgxpool's own underlying connection
	// pool) blocks Close() until every ACQUIRED connection is returned --
	// but actor A's own lock connection is HELD for its entire lifetime
	// by design (hydrate.go) and is never released except by its own
	// graceful shutdown() path, which this test deliberately never
	// triggers (a real `kill -9` has no graceful-release phase either).
	// Calling poolA.Close() here would therefore hang the TEST ITSELF
	// forever waiting on that same connection -- an artifact of
	// simulating the kill from WITHIN the same test process, not a
	// real second pod.
	//
	// Instead, terminate the exact Postgres BACKEND holding the advisory
	// lock directly (via a separate administrative connection, poolB) --
	// this is the actual, precise causal mechanism a real `kill -9`
	// relies on too (the OS closes the dead process's TCP sockets;
	// Postgres notices the connection is gone and reaps that backend,
	// releasing every lock it held) reproduced faithfully at exactly the
	// layer that matters for this test's own assertions, without fighting
	// puddle's own cooperative-release bookkeeping. poolA's own Go-level
	// resources are deliberately never Close()'d afterward, matching a
	// real killed process leaving nothing to gracefully clean up either.
	killAdvisoryLockHolder(ctx, t, poolB)

	// --- Step 4: pod B, a genuinely fresh pool, claims and delivers the
	// now-orphaned overdue timer via its own timer pump. ---
	// Cleanup order matters here: t.Cleanup runs LIFO, so registering
	// poolB.Close() FIRST and registryB.Shutdown() SECOND means Shutdown
	// actually runs FIRST at test end -- gracefully stopping actor B (and
	// releasing its own lock connection back to poolB) BEFORE poolB
	// itself is closed. Closing poolB first would deadlock: Pool.Close
	// blocks until every checked-out connection is returned, but actor
	// B's lock connection is never returned until its own run() loop
	// exits via Shutdown.
	registryB, err := NewRegistry(ctx, poolB, timeouts, nil, nil, nil, "", nil, nil, "", nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(poolB.Close)
	t.Cleanup(func() { _ = registryB.Shutdown() })

	waitUntil(t, 10*time.Second, func() bool {
		if err := registryB.PumpOnce(ctx); err != nil {
			t.Logf("PumpOnce: %v (retrying)", err)
			return false
		}
		got, err := turnStore.Get(ctx, created.ID)
		return err == nil && got.Status == sqlcgen.TurnStatusFailed
	})

	// --- Step 5: assert, from Postgres, that the turn failed with a real
	// reason and nothing is left stuck in Processing. ---
	gotTurn, err := turnStore.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if gotTurn.Status != sqlcgen.TurnStatusFailed {
		t.Fatalf("turn status = %q, want %q (must not be stuck in %q)",
			gotTurn.Status, sqlcgen.TurnStatusFailed, sqlcgen.TurnStatusProcessing)
	}
	if !gotTurn.CompletedAt.Valid {
		t.Error("turn completed_at not set")
	}

	sessionStore := narvipg.NewSessionStore(poolB)
	gotSession, err := sessionStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if gotSession.Status != sqlcgen.SessionStatusFailed {
		t.Errorf("session status = %q, want %q", gotSession.Status, sqlcgen.SessionStatusFailed)
	}
	if gotSession.FailureReason == nil || *gotSession.FailureReason != sqlcgen.SessionFailureReasonTimeout {
		t.Errorf("session failure_reason = %v, want %q (a real failure reason, not stuck/unset)",
			gotSession.FailureReason, sqlcgen.SessionFailureReasonTimeout)
	}

	var eventCount int
	if err := poolB.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE session_id = $1 AND type = 'execution_complete'`,
		sessionID,
	).Scan(&eventCount); err != nil {
		t.Fatalf("count execution_complete events: %v", err)
	}
	if eventCount != 1 {
		t.Errorf("execution_complete event count = %d, want 1 (synthetic completion, §3.3 -- "+
			"clients must always see exactly one terminal event per turn, even across a pod kill)", eventCount)
	}

	var timerCount int
	if err := poolB.QueryRow(ctx,
		`SELECT count(*) FROM session_timers WHERE session_id = $1 AND name = $2`,
		sessionID, TimerTurnDeadline,
	).Scan(&timerCount); err != nil {
		t.Fatalf("count turn_deadline timers: %v", err)
	}
	if timerCount != 0 {
		t.Errorf("turn_deadline timer count = %d, want 0 (pod B's handler must delete it once handled, "+
			"exactly like every other timer handler's own re-arm-or-delete contract)", timerCount)
	}
}
