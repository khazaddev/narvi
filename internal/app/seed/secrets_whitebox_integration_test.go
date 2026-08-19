//go:build integration

// White-box (package seed, not seed_test) integration test: exercises
// seedSecret directly with a manifest value that internal/domain/
// seedmanifest.Validate would already reject at load time -- deliberately
// bypassing that layer, so this test fails specifically if seedSecret's
// OWN re-validation (secrets.go's own sandboxsecret.ValidateName call,
// immediately before the encrypt-and-insert) is ever removed or
// bypassed, independent of whatever LoadManifest does. This is the
// mutation-guard file for point D ("secrets go through the existing
// machinery, not around it") -- see the top-level report for the actual
// mutation performed against this test.
package seed

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/internal/domain/seedmanifest"
	"github.com/khazaddev/narvi/migrations"
)

// testWhiteBoxTokenEncryptionKey mirrors seed_test's own
// testTokenEncryptionKey -- a SEPARATE copy, deliberately: this file is
// package seed (white-box), which cannot see package seed_test's own
// unexported test helpers (the two are linked into one test binary, but
// package seed_test only ever imports package seed, never the reverse) --
// mirrors internal/adapters/inbound/httpapi's own documented precedent
// for exactly this situation (sharedpool_integration_test.go's own top
// doc comment: "package httpapi's own test files... cannot import
// package httpapi_test's unexported newTestPool... at all -- that would
// be a reverse import of a test-only package, which Go does not allow").
// This package's white-box suite is a single test function, so a second,
// full container-per-run helper (rather than that file's own shared-pool
// machinery, built for ~170 test functions) is the right-sized copy here.
var testWhiteBoxTokenEncryptionKey = []byte("test-key-not-for-real-use-000000")[:32]

// newWhiteBoxTestPool is newTestPool's (seed_integration_test.go) own
// twin, duplicated into this package on purpose -- see
// testWhiteBoxTokenEncryptionKey's own doc comment immediately above.
func newWhiteBoxTestPool(t *testing.T) *pgxpool.Pool {
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
		t.Fatalf("start postgres container: tcpostgres.Run did not return within %s", containerStartWatchdog)
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
		t.Fatalf("migrate postgres driver: %v", err)
	}
	srcDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("migrate iofs source: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", srcDriver, "pgx", dbDriver)
	if err != nil {
		t.Fatalf("migrate.NewWithInstance: %v", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

func TestSeedSecret_RevalidatesNameImmediatelyBeforeWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := newWhiteBoxTestPool(t)
	deps := NewDeps(pool, testWhiteBoxTokenEncryptionKey, nil)

	// NARVI_ is a reserved namespace (sandboxsecret.ValidateName) --
	// seedmanifest.Validate would already reject this manifest at load
	// time; calling seedSecret directly here bypasses that layer on
	// purpose.
	s := seedmanifest.Secret{Scope: seedmanifest.SecretScopeGlobal, Name: "NARVI_SHOULD_BE_REJECTED", Value: "v"}

	item := seedSecret(ctx, deps, s, false)
	if item.Outcome != OutcomeError {
		t.Fatalf("seedSecret with reserved NARVI_ name = %+v, want Outcome=error (ValidateName must reject it even bypassing manifest-level validation)", item)
	}

	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM sandbox_secrets WHERE name = $1", s.Name).Scan(&count); err != nil {
		t.Fatalf("count sandbox_secrets: %v", err)
	}
	if count != 0 {
		t.Fatalf("sandbox_secrets rows named %q = %d, want 0 (a rejected name must never be written)", s.Name, count)
	}
}
