// Plain unit tests for NewPool/NewPoolWithMaxConns's own MaxConns
// resolution -- deliberately NOT behind the "integration" build tag
// (postgres_integration_test.go's own precedent): pgxpool.New/
// NewWithConfig connect lazily (confirmed against the vendored
// github.com/jackc/pgx/v5@v5.10.0/pgxpool/pool.go source: the constructor
// only opens a connection on demand, and MinConns/MinIdleConns both
// default to 0, so no idle connection is even attempted eagerly), so
// asserting on pool.Config().MaxConns needs no real, reachable Postgres.
package postgres_test

import (
	"context"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres"
)

// dummyDSN is syntactically valid but never actually dialed by either test
// below (both only inspect the resolved *pgxpool.Config, never Acquire).
const dummyDSN = "postgres://user:pass@localhost:1/db"

// TestNewPool proves MaxConns is left untouched (whatever dsn/pgxpool's own
// default resolves to) -- every existing caller in this codebase (~25 test
// files, plus every production code path other than cmd/control-plane/
// main.go) depends on this exact, unchanged behavior.
func TestNewPool(t *testing.T) {
	pool, err := postgres.NewPool(context.Background(), dummyDSN)
	if err != nil {
		t.Fatalf("NewPool() error = %v, want nil", err)
	}
	defer pool.Close()

	if pool.Config().MaxConns <= 0 {
		t.Errorf("pool.Config().MaxConns = %d, want > 0 (pgxpool's own default)", pool.Config().MaxConns)
	}
}

// TestNewPoolWithMaxConns covers both NewPoolWithMaxConns outcomes: a
// positive maxConns overrides dsn/pgxpool's own resolution, and 0 means "no
// override" -- identical to NewPool above (platform.Config.DBPoolMaxConns's
// own doc comment explains why the real production wiring needs this
// override at all).
func TestNewPoolWithMaxConns(t *testing.T) {
	t.Run("positive value overrides", func(t *testing.T) {
		pool, err := postgres.NewPoolWithMaxConns(context.Background(), dummyDSN, 42)
		if err != nil {
			t.Fatalf("NewPoolWithMaxConns() error = %v, want nil", err)
		}
		defer pool.Close()

		if pool.Config().MaxConns != 42 {
			t.Errorf("pool.Config().MaxConns = %d, want 42", pool.Config().MaxConns)
		}
	})

	t.Run("zero means no override", func(t *testing.T) {
		pool, err := postgres.NewPoolWithMaxConns(context.Background(), dummyDSN, 0)
		if err != nil {
			t.Fatalf("NewPoolWithMaxConns() error = %v, want nil", err)
		}
		defer pool.Close()

		if pool.Config().MaxConns <= 0 {
			t.Errorf("pool.Config().MaxConns = %d, want > 0 (pgxpool's own default, unmodified)", pool.Config().MaxConns)
		}
	})
}
