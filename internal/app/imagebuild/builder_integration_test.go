//go:build integration

// Integration tests proving Builder.PumpOnce against a REAL Postgres
// instance (§9.1) -- gated behind the "integration" build tag, matching
// internal/app/reconciler/reconciler_integration_test.go's own conventions
// exactly (testcontainers Postgres, embedded migrations via golang-migrate's
// iofs source driver, a real *pgxpool.Pool, a single global OTel
// MeterProvider wired once in TestMain). Run via `make test-integration`.
//
// internal/app/sessionactor/imagebuild_integration_test.go covers the
// spawn-side half of this Step end to end (scenarios a/b/d in this Step's
// own brief); this file covers the background-builder-side half in
// isolation: backoff/not-retried-before-due (scenario c) and the
// failure-streak alert threshold (scenario e).
package imagebuild_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/imagebuild"
	"github.com/khazaddev/narvi/internal/app/ports"
	domainimagebuild "github.com/khazaddev/narvi/internal/domain/imagebuild"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every embedded
// migration up, and returns a ready *pgxpool.Pool. t.Cleanup tears down
// both the pool and the container. Each DB-touching package builds its own
// copy rather than sharing one across package boundaries -- see
// internal/app/reconciler/reconciler_integration_test.go's own identical
// precedent.
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

// fakeBuildProvider is a test-only ports.SandboxProvider recording every
// BuildImage call and returning a caller-configured (ref, err) pair --
// mirrors internal/app/reconciler's own fakeReconcileProvider precedent
// exactly (configurable behavior + a recorded-calls slice, mutex-guarded),
// narrowed to the one method this package's own Builder actually calls.
type fakeBuildProvider struct {
	mu sync.Mutex

	buildCalls []ports.ImageSpec
	nextRef    ports.BuildRef
	nextErr    error
}

var _ ports.SandboxProvider = (*fakeBuildProvider)(nil)

func (f *fakeBuildProvider) Capabilities() ports.Capabilities {
	return ports.Capabilities{ImageBuilds: true}
}

func (f *fakeBuildProvider) CreateSandbox(context.Context, ports.CreateSpec) (ports.SandboxRef, error) {
	return ports.SandboxRef{}, errors.New("fakeBuildProvider: CreateSandbox not implemented")
}
func (f *fakeBuildProvider) StopSandbox(context.Context, ports.SandboxRef) error {
	return errors.New("fakeBuildProvider: StopSandbox not implemented")
}
func (f *fakeBuildProvider) ResumeSandbox(context.Context, ports.SandboxRef) error {
	return errors.New("fakeBuildProvider: ResumeSandbox not implemented")
}
func (f *fakeBuildProvider) TakeSnapshot(context.Context, ports.SandboxRef) (ports.SnapshotID, error) {
	return "", errors.New("fakeBuildProvider: TakeSnapshot not implemented")
}
func (f *fakeBuildProvider) RestoreFromSnapshot(context.Context, ports.SnapshotID, ports.CreateSpec) (ports.SandboxRef, error) {
	return ports.SandboxRef{}, errors.New("fakeBuildProvider: RestoreFromSnapshot not implemented")
}

func (f *fakeBuildProvider) BuildImage(_ context.Context, spec ports.ImageSpec) (ports.BuildRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buildCalls = append(f.buildCalls, spec)
	return f.nextRef, f.nextErr
}

func (f *fakeBuildProvider) DeleteImage(context.Context, ports.ImageRef) error {
	return errors.New("fakeBuildProvider: DeleteImage not implemented")
}
func (f *fakeBuildProvider) List(context.Context) ([]ports.SandboxRef, error) { return nil, nil }

func (f *fakeBuildProvider) buildCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.buildCalls)
}

// otelReader is the SINGLE ManualReader backing the SINGLE, GLOBAL SDK
// MeterProvider TestMain below registers for this whole test binary --
// mirrors internal/app/reconciler/reconciler_integration_test.go's own
// TestMain/otelReader precedent (and its own doc comment's full reasoning
// for why exactly-once-per-binary, not once-per-test) exactly, adapted to
// this package's own "narvi/imagebuild" meter / image_build_failure_streak
// counter.
var otelReader *sdkmetric.ManualReader

func TestMain(m *testing.M) {
	otelReader = sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(otelReader))
	otel.SetMeterProvider(mp)

	code := m.Run()

	_ = mp.Shutdown(context.Background())
	os.Exit(code)
}

// readFailureStreak sums every data point of the narvi/imagebuild meter's
// own image_build_failure_streak counter -- CUMULATIVE across every test in
// this binary (see TestMain's own doc comment / reconciler's identical
// precedent), so callers must diff a "before" and "after" reading around
// their own PumpOnce call(s) rather than asserting on the absolute value.
func readFailureStreak(ctx context.Context, t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "narvi/imagebuild" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "image_build_failure_streak" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("image_build_failure_streak metric data = %T, want metricdata.Sum[int64]", m.Data)
			}
			var total int64
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
			return total
		}
	}
	return 0
}

// seedPendingImageBuild inserts a fresh 'pending' image_builds row directly
// (bypassing app/sessionactor entirely -- this package's own tests exercise
// Builder in isolation, matching its own doc.go's scope).
func seedPendingImageBuild(ctx context.Context, t *testing.T, store *narvipg.ImageBuildStore, fingerprint string) {
	t.Helper()

	repoSHAs, err := json.Marshal(map[string]string{"repo1": "sha-fixed"})
	if err != nil {
		t.Fatalf("marshal repo shas: %v", err)
	}
	if err := store.UpsertPending(ctx, sqlcgen.UpsertPendingImageBuildParams{
		Fingerprint:    fingerprint,
		Base:           "narvi/base:test",
		RepoShas:       repoSHAs,
		RuntimeVersion: "1.0.0-test",
	}); err != nil {
		t.Fatalf("seed pending image_builds row: %v", err)
	}
}

// TestPumpOnce_FailedBuild_BacksOffAndNotRetriedBeforeNextRetryAt proves
// scenario (c): a failed build attempt is NOT retried before its own
// next_retry_at, confirmed by DIRECT Postgres inspection (not log-reading)
// -- a second PumpOnce call immediately after the first must not re-claim
// the still-not-due row (attempt_count/BuildImage call count stay at 1),
// but once next_retry_at has genuinely elapsed, a later PumpOnce DOES pick
// it up again (attempt_count advances to 2).
func TestPumpOnce_FailedBuild_BacksOffAndNotRetriedBeforeNextRetryAt(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewImageBuildStore(pool)

	const fingerprint = "fp-scenario-c"
	seedPendingImageBuild(ctx, t, store, fingerprint)

	provider := &fakeBuildProvider{nextErr: errors.New("provider: build failed")}
	timeouts := platform.DefaultTimeouts()
	timeouts.ImageBuildBackoffBase = 200 * time.Millisecond
	timeouts.ImageBuildBackoffMax = 500 * time.Millisecond

	builder, err := imagebuild.NewBuilder(store, pool, provider, timeouts)
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}

	// First tick: claims the pending row, BuildImage fails, backs off.
	if err := builder.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce (1st): %v", err)
	}
	if provider.buildCallCount() != 1 {
		t.Fatalf("BuildImage call count after 1st PumpOnce = %d, want 1", provider.buildCallCount())
	}

	row, err := store.Get(ctx, fingerprint)
	if err != nil {
		t.Fatalf("get row after 1st PumpOnce: %v", err)
	}
	if row.Status != sqlcgen.ImageBuildStatusFailed {
		t.Fatalf("status after 1st failure = %q, want %q", row.Status, sqlcgen.ImageBuildStatusFailed)
	}
	if row.AttemptCount != 1 {
		t.Fatalf("attempt_count after 1st failure = %d, want 1", row.AttemptCount)
	}
	if !row.NextRetryAt.Valid {
		t.Fatal("next_retry_at is not set after a failed attempt")
	}
	nextRetryAt := row.NextRetryAt.Time
	if !nextRetryAt.After(time.Now()) {
		t.Fatalf("next_retry_at = %v, want strictly in the future immediately after recording the failure", nextRetryAt)
	}

	// Second tick, immediately: next_retry_at has NOT elapsed yet -- must
	// NOT be re-claimed. Confirmed by BOTH the provider's own call count
	// (still 1) AND a direct Postgres re-read (attempt_count still 1,
	// next_retry_at UNCHANGED).
	if err := builder.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce (2nd, immediate): %v", err)
	}
	if provider.buildCallCount() != 1 {
		t.Fatalf("BuildImage call count after 2nd (too-early) PumpOnce = %d, want still 1 (not yet due)", provider.buildCallCount())
	}
	row2, err := store.Get(ctx, fingerprint)
	if err != nil {
		t.Fatalf("get row after 2nd PumpOnce: %v", err)
	}
	if row2.AttemptCount != 1 {
		t.Fatalf("attempt_count after 2nd (too-early) PumpOnce = %d, want still 1", row2.AttemptCount)
	}
	if !row2.NextRetryAt.Time.Equal(nextRetryAt) {
		t.Fatalf("next_retry_at changed on a too-early tick: was %v, now %v", nextRetryAt, row2.NextRetryAt.Time)
	}

	// Wait out the real backoff window, then a THIRD tick must genuinely
	// retry (attempt_count advances to 2) -- proving this isn't merely
	// "never retries again", but specifically "not before next_retry_at".
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && provider.buildCallCount() < 2 {
		time.Sleep(20 * time.Millisecond)
		if err := builder.PumpOnce(ctx); err != nil {
			t.Fatalf("PumpOnce (retry loop): %v", err)
		}
	}
	if provider.buildCallCount() != 2 {
		t.Fatalf("BuildImage call count after waiting out the backoff = %d, want 2 (retried once due)", provider.buildCallCount())
	}
	row3, err := store.Get(ctx, fingerprint)
	if err != nil {
		t.Fatalf("get row after retry: %v", err)
	}
	if row3.AttemptCount != 2 {
		t.Fatalf("attempt_count after the due retry = %d, want 2", row3.AttemptCount)
	}
}

// TestPumpOnce_FailureStreak_FiresAtThresholdNotBefore proves scenario (e):
// the streak-threshold log/metric (image_build_failure_streak) fires after
// domain/imagebuild.ImageBuildStreakThreshold consecutive failed attempts
// for the SAME fingerprint, and NOT before -- driven by real,
// consecutive PumpOnce ticks against real Postgres, asserted via the real
// OTel counter's delta (readFailureStreak).
func TestPumpOnce_FailureStreak_FiresAtThresholdNotBefore(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	store := narvipg.NewImageBuildStore(pool)

	const fingerprint = "fp-scenario-e"
	seedPendingImageBuild(ctx, t, store, fingerprint)

	provider := &fakeBuildProvider{nextErr: errors.New("provider: build always fails")}
	// A tiny, test-only backoff so consecutive due ticks resolve almost
	// immediately -- this test cares about attempt_count/streak behavior,
	// not real backoff timing (already covered directly by
	// TestPumpOnce_FailedBuild_BacksOffAndNotRetriedBeforeNextRetryAt
	// above).
	timeouts := platform.DefaultTimeouts()
	timeouts.ImageBuildBackoffBase = 1 * time.Millisecond
	timeouts.ImageBuildBackoffMax = 5 * time.Millisecond

	builder, err := imagebuild.NewBuilder(store, pool, provider, timeouts)
	if err != nil {
		t.Fatalf("NewBuilder: %v", err)
	}

	before := readFailureStreak(ctx, t, otelReader)

	tickUntilDue := func(wantAttempt int32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if err := builder.PumpOnce(ctx); err != nil {
				t.Fatalf("PumpOnce: %v", err)
			}
			row, err := store.Get(ctx, fingerprint)
			if err != nil {
				t.Fatalf("get row: %v", err)
			}
			if row.AttemptCount == wantAttempt {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("attempt_count never reached %d (stuck at %d)", wantAttempt, row.AttemptCount)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Attempts 1 and 2 (below domain/imagebuild.ImageBuildStreakThreshold,
	// which is 3): the streak counter must NOT move.
	tickUntilDue(1)
	if got := readFailureStreak(ctx, t, otelReader) - before; got != 0 {
		t.Fatalf("failure streak delta after attempt 1 = %d, want 0 (below threshold %d)", got, domainimagebuild.ImageBuildStreakThreshold)
	}

	tickUntilDue(2)
	if got := readFailureStreak(ctx, t, otelReader) - before; got != 0 {
		t.Fatalf("failure streak delta after attempt 2 = %d, want 0 (still below threshold %d)", got, domainimagebuild.ImageBuildStreakThreshold)
	}

	// Attempt 3 crosses the threshold: the counter must increment.
	tickUntilDue(3)
	if got := readFailureStreak(ctx, t, otelReader) - before; got != 1 {
		t.Fatalf("failure streak delta after attempt 3 (at threshold %d) = %d, want 1", domainimagebuild.ImageBuildStreakThreshold, got)
	}

	// Attempt 4 (still at/beyond threshold): fires again.
	tickUntilDue(4)
	if got := readFailureStreak(ctx, t, otelReader) - before; got != 2 {
		t.Fatalf("failure streak delta after attempt 4 (beyond threshold) = %d, want 2", got)
	}
}
