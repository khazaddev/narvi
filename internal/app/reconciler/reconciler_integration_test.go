//go:build integration

// Integration tests proving Reconciler.ReconcileOnce against a REAL
// Postgres instance (§9.1) -- gated behind the "integration" build tag,
// matching internal/app/sessionactor/integration_helpers_test.go's own
// conventions exactly (testcontainers Postgres, embedded migrations via
// golang-migrate's iofs source driver, a real *pgxpool.Pool). Each
// DB-touching package builds its own copy of newTestPool/createTestSession
// rather than sharing one across package boundaries -- see that file's own
// doc comment, and internal/adapters/inbound/wshub/integration_helpers_test.go's
// identical precedent. Run via `make test-integration`.
package reconciler_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
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
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/app/reconciler"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/migrations"
)

// newTestPool spins up a throwaway Postgres container, runs every embedded
// migration up, and returns a ready *pgxpool.Pool. t.Cleanup tears down
// both the pool and the container.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
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

// createLiveSandbox inserts a session + sandbox row, records providerID on
// it, and moves it to status -- a helper for seeding the "expected still
// alive" rows a real ListLiveSandboxProviderIDs query needs to see.
func createLiveSandbox(ctx context.Context, t *testing.T, pool *pgxpool.Pool, providerID string, status sqlcgen.SandboxStatus) {
	t.Helper()

	sessionID := createTestSession(ctx, t, pool)
	store := narvipg.NewSandboxStore(pool)

	if _, err := store.Create(ctx, sessionID); err != nil {
		t.Fatalf("create test sandbox: %v", err)
	}
	if _, err := store.UpdateProviderID(ctx, sqlcgen.UpdateSandboxProviderIDParams{
		SessionID:  sessionID,
		ProviderID: &providerID,
	}); err != nil {
		t.Fatalf("set test sandbox provider id: %v", err)
	}
	if _, err := store.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID,
		Status:    status,
	}); err != nil {
		t.Fatalf("move test sandbox to %s: %v", status, err)
	}
}

// createMidSpawnSandbox inserts a session + sandbox row already in status
// 'spawning' (a LIVE status) with provider_id left NULL -- the exact real
// row shape internal/app/sessionactor/dispatch.go's own deliberate
// three-step spawn sequencing produces for the whole window between its
// first transact committing (gen bump, status='spawning', token_hash) and
// its SECOND, later transact (recordProviderOutcome) finally recording
// provider_id, once the real, network-bound CreateSandbox call in between
// has already returned successfully at the provider. Returns the session
// id so a caller can later simulate that second transact catching up via
// store.UpdateProviderID directly, without going through
// app/sessionactor's own actor machinery (out of scope here; see this
// package's own doc.go for the scope boundary).
func createMidSpawnSandbox(ctx context.Context, t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()

	sessionID := createTestSession(ctx, t, pool)
	store := narvipg.NewSandboxStore(pool)

	if _, err := store.Create(ctx, sessionID); err != nil {
		t.Fatalf("create test sandbox: %v", err)
	}
	if _, err := store.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID,
		Status:    sqlcgen.SandboxStatusSpawning,
	}); err != nil {
		t.Fatalf("move test sandbox to spawning: %v", err)
	}

	return sessionID
}

// fakeReconcileProvider is a test-only ports.SandboxProvider giving
// ReconcileOnce a caller-configured List() result and recording every
// StopSandbox call it receives, with an optional per-ProviderID error --
// mirrors internal/app/sessionactor/integration_helpers_test.go's own
// fakeSpawnProvider precedent (configurable behavior + a recorded-calls
// slice, mutex-guarded for concurrent-safe reads), narrowed to the two
// methods this package's own Reconciler actually calls.
type fakeReconcileProvider struct {
	mu sync.Mutex

	listRefs []ports.SandboxRef
	listErr  error

	stopCalls  []ports.SandboxRef
	stopErrFor map[string]error
}

var _ ports.SandboxProvider = (*fakeReconcileProvider)(nil)

func (f *fakeReconcileProvider) Capabilities() ports.Capabilities {
	return ports.Capabilities{ExplicitStop: true}
}

func (f *fakeReconcileProvider) CreateSandbox(context.Context, ports.CreateSpec) (ports.SandboxRef, error) {
	return ports.SandboxRef{}, errors.New("fakeReconcileProvider: CreateSandbox not implemented")
}

func (f *fakeReconcileProvider) StopSandbox(_ context.Context, ref ports.SandboxRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.stopCalls = append(f.stopCalls, ref)
	if err, ok := f.stopErrFor[ref.ProviderID]; ok {
		return err
	}
	return nil
}

func (f *fakeReconcileProvider) ResumeSandbox(context.Context, ports.SandboxRef) error {
	return errors.New("fakeReconcileProvider: ResumeSandbox not implemented")
}

func (f *fakeReconcileProvider) TakeSnapshot(context.Context, ports.SandboxRef) (ports.SnapshotID, error) {
	return "", errors.New("fakeReconcileProvider: TakeSnapshot not implemented")
}

func (f *fakeReconcileProvider) RestoreFromSnapshot(context.Context, ports.SnapshotID, ports.CreateSpec) (ports.SandboxRef, error) {
	return ports.SandboxRef{}, errors.New("fakeReconcileProvider: RestoreFromSnapshot not implemented")
}

func (f *fakeReconcileProvider) BuildImage(context.Context, ports.ImageSpec) (ports.BuildRef, error) {
	return "", errors.New("fakeReconcileProvider: BuildImage not implemented")
}

func (f *fakeReconcileProvider) DeleteImage(context.Context, ports.ImageRef) error {
	return errors.New("fakeReconcileProvider: DeleteImage not implemented")
}

func (f *fakeReconcileProvider) List(context.Context) ([]ports.SandboxRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.listRefs, f.listErr
}

func (f *fakeReconcileProvider) stopCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stopCalls)
}

func (f *fakeReconcileProvider) stoppedProviderIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	ids := make([]string, len(f.stopCalls))
	for i, ref := range f.stopCalls {
		ids[i] = ref.ProviderID
	}
	return ids
}

// otelReader is the SINGLE ManualReader backing the SINGLE, GLOBAL SDK
// MeterProvider TestMain below registers for this whole test binary --
// see TestMain's own doc comment for why this must happen exactly once
// per process, not once per test.
var otelReader *sdkmetric.ManualReader

// TestMain wires exactly ONE global OTel MeterProvider (backed by
// otelReader) for this entire test binary, via a SINGLE otel.
// SetMeterProvider call -- the only way to intercept a package-level
// otel.Meter(...) call like NewReconciler's own, since this codebase has
// no dependency-injectable MeterProvider seam. No prior otel_test.go
// precedent exists anywhere in this codebase (grepped) for asserting on a
// real OTel counter's value -- this is that pattern's first use, following
// the OTel SDK's own documented test-reader mechanism.
//
// This fixes a real, previously-flaky failure
// (TestReconcileOnce_ReapsOrphansLeavesLiveRowAlone and
// TestReconcileOnce_OneStopSandboxFailureDoesNotAbortBatch failed on the
// orphans_reaped counter assertion 4 of 5 runs when run together in one
// process -- exactly how `go test ./...`/`make test-integration` run
// them, though each was reliable run alone). Root cause: the PREVIOUS
// version of this helper called otel.SetMeterProvider once per test (plus
// once more per test's own t.Cleanup, restoring whatever was global
// before). go.opentelemetry.io/otel's own internal/global package only
// re-delegates a pre-existing (pre-SDK) placeholder Meter to whichever
// MeterProvider is passed to the FIRST otel.SetMeterProvider call in the
// whole process, gated by a package-level sync.Once
// (internal/global/state.go's delegateMeterOnce) -- meterProvider.
// setDelegate's own doc comment in that package says outright "It is
// guaranteed by the caller that this happens only once." Calling
// SetMeterProvider repeatedly, once per test, violates that exact
// contract: every call after the first silently skips re-delegating,
// which is undefined/unsupported behavior per the library's own contract,
// not merely an aesthetic concern -- hence the flakiness.
//
// The fix: call otel.SetMeterProvider exactly once, here, for the whole
// binary -- satisfying the contract instead of fighting it. Because the
// SDK's own MeterProvider.Meter and Meter.Int64Counter both cache by
// identity (scope name, then name+kind+unit -- see go.opentelemetry.io/
// otel/sdk/metric's own provider.go/meter.go), every NewReconciler call in
// every test in this file resolves to the exact SAME underlying
// orphans_reaped instrument/stream, so its value ACCUMULATES across the
// whole test binary's lifetime, not per-test. Each test therefore reads
// the counter BEFORE and AFTER its own ReconcileOnce call and asserts on
// the DELTA (readOrphansReaped's own absolute return value, diffed by the
// caller), never on an absolute value -- correct regardless of test
// execution order, and still correct if a future test is added to this
// file.
func TestMain(m *testing.M) {
	otelReader = sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(otelReader))
	otel.SetMeterProvider(mp)

	code := m.Run()

	_ = mp.Shutdown(context.Background())
	os.Exit(code)
}

// readOrphansReaped collects reader's current metrics and sums every data
// point of the narvi/reconciler meter's own orphans_reaped counter
// (reconciler.go's own unexported meterName constant -- hardcoded here
// since it isn't exported; a future rename of that constant must update
// this literal too). Returns 0 if the instrument has not recorded anything
// yet (e.g. before the first ReconcileOnce call in the whole test binary).
//
// The returned value is CUMULATIVE across every test in this binary (see
// TestMain's own doc comment for why) -- callers must diff a "before" and
// "after" reading around their own ReconcileOnce call rather than
// asserting on the absolute value.
func readOrphansReaped(ctx context.Context, t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "narvi/reconciler" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "orphans_reaped" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("orphans_reaped metric data = %T, want metricdata.Sum[int64]", m.Data)
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

// TestReconcileOnce_ReapsOrphansLeavesLiveRowAlone proves ReconcileOnce's
// core decision (§5.3, §9.3 scenario 5: "two concurrent spawns ... loser
// sandbox reaped by GC"), covering both ways a real orphan arises:
//
//   - "alive-1" backs a Ready sandbox row (a live status) -- provider.List
//     returns it too, so it is in the expected-alive set: StopSandbox must
//     NEVER be called for it.
//   - "orphan-stale-epoch" is a ref provider.List returns with NO matching
//     Postgres row at all -- exactly the stale-epoch-takeover scenario
//     app/sessionactor/dispatch.go's own "Step 25's reconciler" comments
//     describe (the write that would have recorded it was rolled back
//     entirely): StopSandbox must be called for it exactly once, once
//     CONFIRMED (see below).
//   - "leaked-old" backs a Stopped sandbox row (a TERMINAL status, whose
//     provider_id UpsertSandboxForSpawn's own doc comment notes is never
//     cleared) that provider.List STILL returns -- simulating "nothing
//     ever explicitly stopped it", the concrete proof StopSandbox is now
//     genuinely wired for terminal rows too, not just races: StopSandbox
//     must be called for it exactly once, once confirmed.
//
// Drives TWO consecutive ticks (ReconcilerOrphanConfirmationPeriod set to
// 0, not the real 30s default, so the second call reliably clears it
// without an actual test sleep -- see
// TestReconcileOnce_DebouncesOrphanConfirmationBeforeReaping's own doc
// comment for why that's safe): tick 1 must reap NOTHING (both
// orphans are only unexplained for the first time), tick 2 must reap
// EXACTLY the two real orphans, still never alive-1. The orphans_reaped
// counter must increment by exactly 2 across the two ticks combined --
// the number of REAL orphans ever stopped -- read back via the OTel SDK's
// own shared ManualReader (TestMain/readOrphansReaped above) as a DELTA
// (before vs. after), not an absolute value (see TestMain's own doc
// comment for why).
func TestReconcileOnce_ReapsOrphansLeavesLiveRowAlone(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sandboxes := narvipg.NewSandboxStore(pool)

	createLiveSandbox(ctx, t, pool, "alive-1", sqlcgen.SandboxStatusReady)
	createLiveSandbox(ctx, t, pool, "leaked-old", sqlcgen.SandboxStatusStopped)

	provider := &fakeReconcileProvider{
		listRefs: []ports.SandboxRef{
			{ProviderID: "alive-1"},
			{ProviderID: "orphan-stale-epoch"},
			{ProviderID: "leaked-old"},
		},
	}

	timeouts := platform.DefaultTimeouts()
	timeouts.ReconcilerOrphanConfirmationPeriod = 0

	r, err := reconciler.NewReconciler(sandboxes, provider, timeouts)
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}

	before := readOrphansReaped(ctx, t, otelReader)

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce (tick 1): %v", err)
	}
	if got := provider.stopCallCount(); got != 0 {
		t.Fatalf("StopSandbox call count after tick 1 = %d, want 0 (first sighting must only be recorded, never reaped) -- stoppedProviderIDs=%v", got, provider.stoppedProviderIDs())
	}

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce (tick 2): %v", err)
	}

	if got := provider.stopCallCount(); got != 2 {
		t.Fatalf("StopSandbox call count after tick 2 = %d, want 2 (stoppedProviderIDs=%v)", got, provider.stoppedProviderIDs())
	}

	stopped := make(map[string]bool)
	for _, id := range provider.stoppedProviderIDs() {
		stopped[id] = true
	}
	if stopped["alive-1"] {
		t.Fatalf("StopSandbox was called for alive-1 (a live row's own provider_id) -- must never be")
	}
	if !stopped["orphan-stale-epoch"] {
		t.Fatalf("StopSandbox was never called for orphan-stale-epoch (a provider ref with no matching Postgres row)")
	}
	if !stopped["leaked-old"] {
		t.Fatalf("StopSandbox was never called for leaked-old (a terminal row's own never-torn-down provider_id)")
	}

	after := readOrphansReaped(ctx, t, otelReader)
	if delta := after - before; delta != 2 {
		t.Fatalf("orphans_reaped counter delta = %d, want 2 (before=%d, after=%d)", delta, before, after)
	}
}

// TestReconcileOnce_OneStopSandboxFailureDoesNotAbortBatch proves the
// per-item error isolation ReconcileOnce's own doc comment describes --
// matching timerpump.go's own deliver() precedent exactly: one failed
// StopSandbox call among several orphans must not stop the rest of the
// batch from being reaped, and must not itself count toward
// orphans_reaped (only a SUCCESSFUL stop does). Drives TWO consecutive
// ticks (ReconcilerOrphanConfirmationPeriod set to 0, exactly like
// TestReconcileOnce_ReapsOrphansLeavesLiveRowAlone above, so both refs
// are actually confirmed and reaping is attempted on tick 2) -- tick 1
// must attempt nothing (first sighting, record only).
func TestReconcileOnce_OneStopSandboxFailureDoesNotAbortBatch(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sandboxes := narvipg.NewSandboxStore(pool)

	// No live Postgres rows at all -- both refs below are unconditionally
	// orphans regardless of which one StopSandbox fails for.
	provider := &fakeReconcileProvider{
		listRefs: []ports.SandboxRef{
			{ProviderID: "orphan-a"},
			{ProviderID: "orphan-b"},
		},
		stopErrFor: map[string]error{
			"orphan-a": errors.New("provider: stop failed"),
		},
	}

	timeouts := platform.DefaultTimeouts()
	timeouts.ReconcilerOrphanConfirmationPeriod = 0

	r, err := reconciler.NewReconciler(sandboxes, provider, timeouts)
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}

	before := readOrphansReaped(ctx, t, otelReader)

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce (tick 1): %v, want nil", err)
	}
	if got := provider.stopCallCount(); got != 0 {
		t.Fatalf("StopSandbox call count after tick 1 = %d, want 0 (first sighting must only be recorded, never reaped)", got)
	}

	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce (tick 2): %v, want nil (a per-orphan StopSandbox failure must be logged, not propagated)", err)
	}

	if got := provider.stopCallCount(); got != 2 {
		t.Fatalf("StopSandbox call count after tick 2 = %d, want 2 (orphan-a's own failure must not stop orphan-b from being attempted)", got)
	}

	after := readOrphansReaped(ctx, t, otelReader)
	if delta := after - before; delta != 1 {
		t.Fatalf("orphans_reaped counter delta = %d, want 1 (only orphan-b's own successful stop counts; before=%d, after=%d)", delta, before, after)
	}
}

// TestReconcileOnce_DebouncesOrphanConfirmationBeforeReaping proves the
// two-tick debounce ReconcileOnce's own doc comment describes (the Step 25
// fix closing the real spawn-mid-flight race platform.Timeouts.
// ReconcilerOrphanConfirmationPeriod's own doc comment details in full):
//
//   - "still-spawning" -- created via createMidSpawnSandbox, so it exists
//     in Postgres already in a LIVE status ('spawning') but with
//     provider_id still NULL, exactly like a real in-flight spawn between
//     dispatch.go's own two transacts -- is unexplained on tick 1 (its own
//     provider_id is not yet recorded, so it is absent from the
//     expected-alive set) but must NOT be reaped on that first sighting.
//   - if "still-spawning" is STILL unexplained on tick 2 (no Postgres
//     update happened in between, and the confirmation period has now
//     elapsed), it IS now reaped: StopSandbox called exactly once,
//     orphans_reaped incremented by exactly 1.
//   - "resolves-before-confirm" -- created the SAME way, unexplained on
//     tick 1 exactly like still-spawning -- has its provider_id recorded
//     via store.UpdateProviderID BETWEEN tick 1 and tick 2, simulating
//     dispatch.go's own recordProviderOutcome transact catching up before
//     the reconciler's next tick (the exact race this debounce exists to
//     survive). It must be cleared from tracking and NEVER reaped, on
//     tick 2 or any later tick -- proving the debounce genuinely
//     re-checks each tick rather than blindly reaping everything recorded
//     one tick later regardless of whether it resolved.
//
// Uses ReconcilerOrphanConfirmationPeriod set to 0 (not the real 30s
// default) so the second ReconcileOnce call always reaches its
// confirmation threshold without an actual test sleep -- time.Since
// against a monotonic clock reading is never negative for two genuinely
// sequential calls, so a 0 threshold is met the instant any measurable
// time at all has passed between them, which real Postgres round-trip I/O
// between the two calls already guarantees here regardless of how fast
// the underlying container/network happens to be. This is safe because
// the debounce's core guarantees (never reap on first sighting; only reap
// once continuously unexplained for at least the confirmation period) do
// not depend on the period's specific value at all -- only the DEFAULT
// (30s) is chosen for production, per its own doc comment in
// platform/timeouts.go.
//
// Whether this test would fail against the OLD (pre-debounce)
// single-tick-reap logic: yes, in two different ways. (1) tick 1's own
// assertion (stopCallCount == 0) would fail outright -- the old code
// called StopSandbox for EVERY unexplained ref on its very first sighting,
// so both still-spawning and resolves-before-confirm would already have
// been stopped after tick 1, incorrectly killing a legitimate in-flight
// spawn exactly as this whole fix exists to prevent. (2) even ignoring
// (1), resolves-before-confirm would never reach tick 2 un-reaped under
// the old logic to demonstrate scenario 3 at all -- it would already be
// gone by tick 1, before its own provider_id update ever had a chance to
// resolve it. This was confirmed empirically during development by
// temporarily reverting ReconcileOnce to the old unconditional-reap loop
// and re-running this exact test: it failed at the tick-1 assertion as
// predicted, then the revert was discarded.
func TestReconcileOnce_DebouncesOrphanConfirmationBeforeReaping(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sandboxes := narvipg.NewSandboxStore(pool)

	createMidSpawnSandbox(ctx, t, pool) // "still-spawning"'s own Postgres row
	resolvingSessionID := createMidSpawnSandbox(ctx, t, pool)

	provider := &fakeReconcileProvider{
		listRefs: []ports.SandboxRef{
			{ProviderID: "still-spawning"},
			{ProviderID: "resolves-before-confirm"},
		},
	}

	timeouts := platform.DefaultTimeouts()
	timeouts.ReconcilerOrphanConfirmationPeriod = 0

	r, err := reconciler.NewReconciler(sandboxes, provider, timeouts)
	if err != nil {
		t.Fatalf("NewReconciler: %v", err)
	}

	before := readOrphansReaped(ctx, t, otelReader)

	// Tick 1: both refs are unexplained for the FIRST time -- neither may
	// be reaped yet.
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce (tick 1): %v", err)
	}
	if got := provider.stopCallCount(); got != 0 {
		t.Fatalf("StopSandbox call count after tick 1 = %d, want 0 (first sighting must only be recorded, never reaped) -- stoppedProviderIDs=%v", got, provider.stoppedProviderIDs())
	}

	// Between tick 1 and tick 2: "resolves-before-confirm" gets its
	// provider_id recorded on its EXISTING row -- simulating
	// recordProviderOutcome's own later transact catching up, exactly
	// like a real in-flight spawn resolving before the reconciler's next
	// tick. "still-spawning" gets no such update, so it remains
	// unexplained.
	providerID := "resolves-before-confirm"
	if _, err := sandboxes.UpdateProviderID(ctx, sqlcgen.UpdateSandboxProviderIDParams{
		SessionID:  resolvingSessionID,
		ProviderID: &providerID,
	}); err != nil {
		t.Fatalf("resolve resolves-before-confirm's own provider id: %v", err)
	}

	// Tick 2: "still-spawning" is unexplained again with the confirmation
	// period now elapsed -- it must be reaped. "resolves-before-confirm"
	// now has a live Postgres row -- it must be cleared from tracking and
	// left alone.
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce (tick 2): %v", err)
	}

	stopped := make(map[string]bool)
	for _, id := range provider.stoppedProviderIDs() {
		stopped[id] = true
	}
	if got := provider.stopCallCount(); got != 1 {
		t.Fatalf("StopSandbox call count after tick 2 = %d, want 1 -- stoppedProviderIDs=%v", got, provider.stoppedProviderIDs())
	}
	if !stopped["still-spawning"] {
		t.Fatalf("StopSandbox was never called for still-spawning on tick 2 (confirmed unexplained across two consecutive ticks)")
	}
	if stopped["resolves-before-confirm"] {
		t.Fatalf("StopSandbox was called for resolves-before-confirm -- it resolved (got a live Postgres row) before tick 2 and must never be reaped")
	}

	after := readOrphansReaped(ctx, t, otelReader)
	if delta := after - before; delta != 1 {
		t.Fatalf("orphans_reaped counter delta = %d, want 1 (before=%d, after=%d)", delta, before, after)
	}

	// Tick 3: "resolves-before-confirm" must stay alive and unreaped on
	// every SUBSEQUENT tick too -- proving it was genuinely cleared from
	// tracking, not merely delayed by one more tick.
	if err := r.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce (tick 3): %v", err)
	}
	if got := provider.stopCallCount(); got != 1 {
		t.Fatalf("StopSandbox call count after tick 3 = %d, want still 1 (resolves-before-confirm must never be reaped on any later tick either) -- stoppedProviderIDs=%v", got, provider.stoppedProviderIDs())
	}
}
