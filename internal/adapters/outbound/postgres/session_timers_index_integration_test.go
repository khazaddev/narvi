//go:build integration

// Integration test proving migration 000020's new
// session_timers_fires_at_idx index is actually usable by ListDueTimers'
// own query shape (audit-remediation config/platform-hardening batch,
// Finding 5) -- gated behind the "integration" build tag, matching this
// package's own postgres_integration_test.go /
// event_artifact_wstoken_integration_test.go conventions (testcontainers
// Postgres, embedded migrations via golang-migrate's iofs source driver,
// newTestPool/createTestSession shared helpers from the latter file).
package postgres_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestListDueTimers_UsesFiresAtIndex proves the new index is a genuine,
// usable access path for ListDueTimers' exact query shape (`WHERE fires_at
// <= now() ORDER BY fires_at ... FOR UPDATE SKIP LOCKED`), not merely that
// the migration applies cleanly. A tiny test table's planner would
// normally still cost a sequential scan as cheaper than an index scan
// (too few rows/pages to matter) -- `SET LOCAL enable_seqscan = off`
// forces the planner to prefer any usable index over a sequential scan,
// which is exactly the deterministic way to prove "this index IS a valid,
// usable plan for this query" independent of the test table's own tiny
// row count. Also proves ListDueTimers still returns correct rows/order
// with the index in place.
func TestListDueTimers_UsesFiresAtIndex(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	timers := narvipg.NewTimerStore(pool)

	past := pgtype.Timestamptz{Time: time.Now().Add(-1 * time.Hour).Truncate(time.Microsecond), Valid: true}
	if _, err := timers.Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: sessionID,
		Name:      "turn_deadline",
		FiresAt:   past,
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	// (a) correctness: the due timer is still returned via the normal
	// store path with the index in place.
	due, err := timers.ListDue(ctx, 50)
	if err != nil {
		t.Fatalf("ListDue: %v", err)
	}
	found := false
	for _, d := range due {
		if d.SessionID == sessionID && d.Name == "turn_deadline" {
			found = true
		}
	}
	if !found {
		t.Fatal("ListDue did not return the due timer just armed")
	}

	// (b) the index is a genuinely usable plan for this exact query shape.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SET LOCAL enable_seqscan = off"); err != nil {
		t.Fatalf("SET LOCAL enable_seqscan = off: %v", err)
	}

	rows, err := tx.Query(ctx, `
		EXPLAIN
		SELECT * FROM session_timers
		WHERE fires_at <= now()
		ORDER BY fires_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`, 50)
	if err != nil {
		t.Fatalf("EXPLAIN: %v", err)
	}
	defer rows.Close()

	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan EXPLAIN line: %v", err)
		}
		plan.WriteString(line)
		plan.WriteString("\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("EXPLAIN rows: %v", err)
	}

	if !strings.Contains(plan.String(), "session_timers_fires_at_idx") {
		t.Fatalf("query plan does not reference session_timers_fires_at_idx (enable_seqscan=off), want an index scan on it; plan:\n%s", plan.String())
	}
}
