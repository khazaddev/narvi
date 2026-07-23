//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// This file proves Step 27's ("mocking + contract drift", §14.3) own
// end-to-end wiring: dispatch.go's checkContractDrift, called from
// handleEnsureDispatched at the same post-transact hook point as Step 26's
// resolveAndSetImage, against a REAL Postgres instance -- mirroring
// imagebuild_integration_test.go's own conventions exactly (newTestPool,
// fakeSpawnProvider, fakeSourceControl, sendEnsureDispatched, waitUntil).

// otelReader is the SINGLE ManualReader backing the SINGLE, GLOBAL SDK
// MeterProvider TestMain (below) registers for this whole test binary --
// mirrors internal/app/reconciler/reconciler_integration_test.go's own
// identical TestMain/otelReader precedent exactly (see that file's own doc
// comment for the full "why exactly once, not once per test" reasoning:
// otel.SetMeterProvider's own contract only honors the FIRST call in the
// process). Every NewRegistry call in this file therefore resolves to the
// exact SAME underlying contract_drift_detected instrument, so its value
// ACCUMULATES across this whole test binary's lifetime -- each test below
// reads the counter BEFORE and AFTER its own action and asserts on the
// DELTA, never an absolute value.
var otelReader *sdkmetric.ManualReader

// TestMain wires exactly ONE global OTel MeterProvider for this whole
// package's integration-test binary. If a future sibling _test.go file in
// this package (also built under the "integration" tag) ever adds its own
// TestMain, the two will conflict (Go only allows one TestMain per test
// binary) -- this repo has none today (grepped).
func TestMain(m *testing.M) {
	otelReader = sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(otelReader))
	otel.SetMeterProvider(mp)

	code := m.Run()

	_ = mp.Shutdown(context.Background())
	os.Exit(code)
}

// readContractDriftDetected collects reader's current metrics and sums
// every data point of the narvi/sessionactor meter's own
// contract_drift_detected counter (registry.go's own unexported meterName
// constant -- hardcoded here since it isn't exported; a future rename of
// that constant must update this literal too). Returns 0 if the
// instrument has not recorded anything yet.
//
// The returned value is CUMULATIVE across every test in this binary (see
// this file's own TestMain doc comment for why) -- callers must diff a
// "before" and "after" reading around their own action rather than
// asserting on the absolute value.
func readContractDriftDetected(ctx context.Context, t *testing.T, reader *sdkmetric.ManualReader) int64 {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "narvi/sessionactor" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "contract_drift_detected" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("contract_drift_detected metric data = %T, want metricdata.Sum[int64]", m.Data)
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

// createTestEnvironment inserts an environments row directly (bypassing
// httpapi.CreateSession, which this package never imports) with the given
// mock_configured/contracts_path -- pathScope stays NULL (nil []byte),
// this file's own tests never exercise path scoping.
func createTestEnvironment(ctx context.Context, t *testing.T, pool *pgxpool.Pool, mockConfigured bool, contractsPath *string) pgtype.UUID {
	t.Helper()

	env, err := narvipg.NewEnvironmentStore(pool).Create(ctx, sqlcgen.CreateEnvironmentParams{
		MockConfigured: mockConfigured,
		ContractsPath:  contractsPath,
	})
	if err != nil {
		t.Fatalf("create test environment: %v", err)
	}
	return env.ID
}

// createTestSessionWithRepoAndEnvironment mirrors createTestSessionWithRepos
// (pushpr_integration_test.go) exactly, with one addition: environmentID is
// set on the created session, so dispatch.go's planFreshSpawn/planRestore
// populate spawnPlan.environmentID from it (the same field checkContractDrift's
// own first early-return checks).
func createTestSessionWithRepoAndEnvironment(ctx context.Context, t *testing.T, pool *pgxpool.Pool, createdBy, environmentID pgtype.UUID, name, url, branch string) pgtype.UUID {
	t.Helper()

	created, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource:   sqlcgen.SessionSpawnSourceWeb,
		CreatedBy:     createdBy,
		Repos:         reposJSONForTest(t, name, url, branch),
		EnvironmentID: environmentID,
	})
	if err != nil {
		t.Fatalf("create test session with repo and environment: %v", err)
	}
	return created.ID
}

// contractsPathPtr is a tiny helper so test bodies can inline a *string
// literal without a separate local variable each time.
func contractsPathPtr(s string) *string { return &s }

// createTestSessionWithRepoAndEnvironmentNilBranch mirrors
// createTestSessionWithRepoAndEnvironment exactly, except the repo's own
// "branch" key is explicit JSON null (rather than reposJSONForTest's
// always-present string) -- unmarshaling into sessionconfig.
// SessionConfigReposElem.Branch (a *string) then leaves it genuinely nil,
// exactly like a real session created via httpapi.CreateSession with no
// branch named for this repo (create.go only validates Branch "ONLY when
// Branch is non-nil"). The key itself must still be PRESENT (json: null,
// not omitted): SessionConfigReposElem.UnmarshalJSON (contracts/gen/go/
// sessionconfig) treats "branch" as required-nullable -- present-but-null
// is valid and means "unscoped", but an absent key is itself a schema
// validation error, surfacing downstream as assembleSessionConfig's own
// "field branch in SessionConfigReposElem: required" the moment a spawn
// actually tries to build this session's SessionConfig.
func createTestSessionWithRepoAndEnvironmentNilBranch(ctx context.Context, t *testing.T, pool *pgxpool.Pool, createdBy, environmentID pgtype.UUID, name, url string) pgtype.UUID {
	t.Helper()

	raw, err := json.Marshal([]map[string]any{
		{"name": name, "url": url, "branch": nil},
	})
	if err != nil {
		t.Fatalf("marshal nil-branch test repos: %v", err)
	}

	created, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource:   sqlcgen.SessionSpawnSourceWeb,
		CreatedBy:     createdBy,
		Repos:         raw,
		EnvironmentID: environmentID,
	})
	if err != nil {
		t.Fatalf("create test session with nil-branch repo and environment: %v", err)
	}
	return created.ID
}

// TestCheckContractDrift_NoMockConfig_NeverTouchesSnapshots proves the
// critical scope-boundary guarantee: spawning an ORDINARY session (no
// environment_id at all) never creates or touches any
// contract_drift_snapshots row, and never even calls
// ResolveContractsFingerprint -- ordinary, unscoped sessions are
// completely unaffected by this Step.
func TestCheckContractDrift_NoMockConfig_NeverTouchesSnapshots(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-no-mock-config")
	sessionID := createTestSessionWithRepos(ctx, t, pool, creator,
		"repo-no-mock", "https://github.com/acme/repo-no-mock.git", "main")

	sourceControl := &fakeSourceControl{nextSHA: "sha-1", nextFingerprint: "fp-1", nextFingerprintExists: true}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-no-mock"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	// checkContractDrift runs synchronously, before executeSpawn's own
	// CreateSandbox call, inside the SAME handleEnsureDispatched
	// invocation (dispatch.go) -- by the time provider.callCount() == 1
	// has been observed, checkContractDrift has already run to completion
	// for this spawn attempt, so no extra wait is needed here.
	if got := sourceControl.fingerprintCallCount(); got != 0 {
		t.Errorf("ResolveContractsFingerprint call count = %d, want 0 (ordinary session must never check contract drift)", got)
	}

	contractDriftStore := narvipg.NewContractDriftStore(pool)
	if _, err := contractDriftStore.Get(ctx, "acme/repo-no-mock@main"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("contract_drift_snapshots row for acme/repo-no-mock: err = %v, want pgx.ErrNoRows (no row must ever be created)", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM contract_drift_snapshots`).Scan(&count); err != nil {
		t.Fatalf("count contract_drift_snapshots: %v", err)
	}
	if count != 0 {
		t.Errorf("contract_drift_snapshots row count = %d, want 0", count)
	}
}

// TestCheckContractDrift_MockConfigured_FirstSpawn_RecordsBaselineNoDrift
// proves a mock-configured Environment's FIRST spawn records a baseline
// contract_drift_snapshots row and does NOT flag drift (first sighting --
// contractdrift.HasDrifted's own "previous.RepoSHA == ''" case).
func TestCheckContractDrift_MockConfigured_FirstSpawn_RecordsBaselineNoDrift(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironment(ctx, t, pool, true, contractsPathPtr("contracts/api"))
	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-baseline")
	sessionID := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-baseline", "https://github.com/acme/repo-baseline.git", "main")

	sourceControl := &fakeSourceControl{nextSHA: "sha-1", nextFingerprint: "fp-1", nextFingerprintExists: true}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-baseline"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	before := readContractDriftDetected(ctx, t, otelReader)

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	contractDriftStore := narvipg.NewContractDriftStore(pool)
	var row sqlcgen.ContractDriftSnapshot
	waitUntil(t, 5*time.Second, func() bool {
		row, err = contractDriftStore.Get(ctx, "acme/repo-baseline@main")
		return err == nil
	})

	if row.LastRepoSha != "sha-1" {
		t.Errorf("last_repo_sha = %q, want %q", row.LastRepoSha, "sha-1")
	}
	if row.LastContractsFingerprint != "fp-1" {
		t.Errorf("last_contracts_fingerprint = %q, want %q", row.LastContractsFingerprint, "fp-1")
	}

	// A brief settle window: the snapshot write above already proves
	// checkContractDrift ran to completion for this repo, so the drift
	// decision (made just before that same write) is already final.
	after := readContractDriftDetected(ctx, t, otelReader)
	if after != before {
		t.Errorf("contract_drift_detected counter delta = %d, want 0 (first sighting must never flag drift)", after-before)
	}
}

// TestCheckContractDrift_SecondSpawn_SameFingerprint_FlagsDrift proves the
// actual drift signal (§14.3): a SECOND spawn (a different session naming
// the SAME repo) whose SourceControl returns a DIFFERENT branch SHA but
// the SAME contracts fingerprint as before increments the
// contract_drift_detected counter and updates the snapshot's own
// last_repo_sha (fingerprint unchanged).
func TestCheckContractDrift_SecondSpawn_SameFingerprint_FlagsDrift(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironment(ctx, t, pool, true, contractsPathPtr("contracts/api"))
	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-drift")
	turnStore := narvipg.NewTurnStore(pool)
	contractDriftStore := narvipg.NewContractDriftStore(pool)

	const repoURL = "https://github.com/acme/repo-drift.git"
	const repoKey = "acme/repo-drift@main"

	// First spawn: records the baseline (sha-1, fp-1).
	session1 := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-drift", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session1, "prompt 1")

	sourceControl1 := &fakeSourceControl{nextSHA: "sha-1", nextFingerprint: "fp-1", nextFingerprintExists: true}
	provider1 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-drift-1"}}
	r1 := newImageBuildTestRegistry(t, ctx, pool, provider1, sourceControl1)
	t.Cleanup(func() { _ = r1.Shutdown() })

	a1, err := r1.GetOrSpawn(ctx, session1)
	if err != nil {
		t.Fatalf("GetOrSpawn(session1): %v", err)
	}
	sendEnsureDispatched(ctx, t, a1)
	waitUntil(t, 5*time.Second, func() bool { return provider1.callCount() == 1 })

	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := contractDriftStore.Get(ctx, repoKey)
		return getErr == nil && row.LastRepoSha == "sha-1"
	})

	before := readContractDriftDetected(ctx, t, otelReader)

	// Second spawn: a DIFFERENT session naming the SAME repo, a DIFFERENT
	// sha, the SAME fingerprint.
	session2 := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-drift", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session2, "prompt 2")

	sourceControl2 := &fakeSourceControl{nextSHA: "sha-2", nextFingerprint: "fp-1", nextFingerprintExists: true}
	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-drift-2"}}
	r2 := newImageBuildTestRegistry(t, ctx, pool, provider2, sourceControl2)
	t.Cleanup(func() { _ = r2.Shutdown() })

	a2, err := r2.GetOrSpawn(ctx, session2)
	if err != nil {
		t.Fatalf("GetOrSpawn(session2): %v", err)
	}
	sendEnsureDispatched(ctx, t, a2)
	waitUntil(t, 5*time.Second, func() bool { return provider2.callCount() == 1 })

	var row sqlcgen.ContractDriftSnapshot
	waitUntil(t, 5*time.Second, func() bool {
		row, err = contractDriftStore.Get(ctx, repoKey)
		return err == nil && row.LastRepoSha == "sha-2"
	})
	if row.LastContractsFingerprint != "fp-1" {
		t.Errorf("last_contracts_fingerprint = %q, want %q (unchanged)", row.LastContractsFingerprint, "fp-1")
	}

	after := readContractDriftDetected(ctx, t, otelReader)
	if after-before != 1 {
		t.Errorf("contract_drift_detected counter delta = %d, want 1 (repo changed, contract fingerprint did not)", after-before)
	}
}

// TestCheckContractDrift_SecondSpawn_DifferentFingerprint_NoDrift proves
// the adversarial "properly updated together" case (§14.3, the easiest row
// to get backwards): a second spawn whose SourceControl returns BOTH a
// different sha AND a different contracts fingerprint records the new
// snapshot but must NOT flag drift.
func TestCheckContractDrift_SecondSpawn_DifferentFingerprint_NoDrift(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironment(ctx, t, pool, true, contractsPathPtr("contracts/api"))
	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-no-drift")
	turnStore := narvipg.NewTurnStore(pool)
	contractDriftStore := narvipg.NewContractDriftStore(pool)

	const repoURL = "https://github.com/acme/repo-no-drift.git"
	const repoKey = "acme/repo-no-drift@main"

	session1 := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-no-drift", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session1, "prompt 1")

	sourceControl1 := &fakeSourceControl{nextSHA: "sha-1", nextFingerprint: "fp-1", nextFingerprintExists: true}
	provider1 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-no-drift-1"}}
	r1 := newImageBuildTestRegistry(t, ctx, pool, provider1, sourceControl1)
	t.Cleanup(func() { _ = r1.Shutdown() })

	a1, err := r1.GetOrSpawn(ctx, session1)
	if err != nil {
		t.Fatalf("GetOrSpawn(session1): %v", err)
	}
	sendEnsureDispatched(ctx, t, a1)
	waitUntil(t, 5*time.Second, func() bool { return provider1.callCount() == 1 })

	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := contractDriftStore.Get(ctx, repoKey)
		return getErr == nil && row.LastRepoSha == "sha-1"
	})

	before := readContractDriftDetected(ctx, t, otelReader)

	// Second spawn: repo changed AND its own contract fingerprint ALSO
	// changed -- the backend and its contract were updated together.
	session2 := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-no-drift", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session2, "prompt 2")

	sourceControl2 := &fakeSourceControl{nextSHA: "sha-2", nextFingerprint: "fp-2", nextFingerprintExists: true}
	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-no-drift-2"}}
	r2 := newImageBuildTestRegistry(t, ctx, pool, provider2, sourceControl2)
	t.Cleanup(func() { _ = r2.Shutdown() })

	a2, err := r2.GetOrSpawn(ctx, session2)
	if err != nil {
		t.Fatalf("GetOrSpawn(session2): %v", err)
	}
	sendEnsureDispatched(ctx, t, a2)
	waitUntil(t, 5*time.Second, func() bool { return provider2.callCount() == 1 })

	var row sqlcgen.ContractDriftSnapshot
	waitUntil(t, 5*time.Second, func() bool {
		row, err = contractDriftStore.Get(ctx, repoKey)
		return err == nil && row.LastRepoSha == "sha-2"
	})
	if row.LastContractsFingerprint != "fp-2" {
		t.Errorf("last_contracts_fingerprint = %q, want %q", row.LastContractsFingerprint, "fp-2")
	}

	after := readContractDriftDetected(ctx, t, otelReader)
	if after != before {
		t.Errorf("contract_drift_detected counter delta = %d, want 0 (repo AND contract both changed together -- not drift)", after-before)
	}
}

// TestCheckContractDrift_NoContractsDirectory_FingerprintStoredEmptyNeverDrifts
// proves a repo where ResolveContractsFingerprint's fake reports
// exists=false (no contracts directory at that path/ref): the snapshot's
// own fingerprint is stored as the "" sentinel, and drift is never flagged
// for that repo regardless of how many times its SHA changes across
// spawns.
func TestCheckContractDrift_NoContractsDirectory_FingerprintStoredEmptyNeverDrifts(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironment(ctx, t, pool, true, contractsPathPtr("contracts/api"))
	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-no-contracts-dir")
	turnStore := narvipg.NewTurnStore(pool)
	contractDriftStore := narvipg.NewContractDriftStore(pool)

	const repoURL = "https://github.com/acme/repo-no-contracts-dir.git"
	const repoKey = "acme/repo-no-contracts-dir@main"

	session1 := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-no-contracts-dir", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session1, "prompt 1")

	sourceControl1 := &fakeSourceControl{nextSHA: "sha-a", nextFingerprintExists: false}
	provider1 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-no-contracts-dir-1"}}
	r1 := newImageBuildTestRegistry(t, ctx, pool, provider1, sourceControl1)
	t.Cleanup(func() { _ = r1.Shutdown() })

	a1, err := r1.GetOrSpawn(ctx, session1)
	if err != nil {
		t.Fatalf("GetOrSpawn(session1): %v", err)
	}
	sendEnsureDispatched(ctx, t, a1)
	waitUntil(t, 5*time.Second, func() bool { return provider1.callCount() == 1 })

	var row sqlcgen.ContractDriftSnapshot
	waitUntil(t, 5*time.Second, func() bool {
		row, err = contractDriftStore.Get(ctx, repoKey)
		return err == nil && row.LastRepoSha == "sha-a"
	})
	if row.LastContractsFingerprint != "" {
		t.Errorf("last_contracts_fingerprint = %q, want empty (no contracts directory exists)", row.LastContractsFingerprint)
	}

	before := readContractDriftDetected(ctx, t, otelReader)

	// Second spawn, a genuinely different sha, contracts dir STILL absent.
	session2 := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-no-contracts-dir", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, session2, "prompt 2")

	sourceControl2 := &fakeSourceControl{nextSHA: "sha-b", nextFingerprintExists: false}
	provider2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-no-contracts-dir-2"}}
	r2 := newImageBuildTestRegistry(t, ctx, pool, provider2, sourceControl2)
	t.Cleanup(func() { _ = r2.Shutdown() })

	a2, err := r2.GetOrSpawn(ctx, session2)
	if err != nil {
		t.Fatalf("GetOrSpawn(session2): %v", err)
	}
	sendEnsureDispatched(ctx, t, a2)
	waitUntil(t, 5*time.Second, func() bool { return provider2.callCount() == 1 })

	waitUntil(t, 5*time.Second, func() bool {
		row, err = contractDriftStore.Get(ctx, repoKey)
		return err == nil && row.LastRepoSha == "sha-b"
	})
	if row.LastContractsFingerprint != "" {
		t.Errorf("last_contracts_fingerprint = %q, want empty (still no contracts directory)", row.LastContractsFingerprint)
	}

	after := readContractDriftDetected(ctx, t, otelReader)
	if after != before {
		t.Errorf("contract_drift_detected counter delta = %d, want 0 (no contracts dir at current ref -- nothing to drift from)", after-before)
	}
}

// TestCheckContractDrift_NilSourceControl_StillSpawnsSuccessfully proves a
// mock-configured session whose Registry has NO SourceControl configured
// at all still spawns successfully -- checkContractDrift's own early
// return never blocks or fails a spawn, mirroring resolveAndSetImage's own
// already-tested "never blocks a spawn" precedent exactly.
func TestCheckContractDrift_NilSourceControl_StillSpawnsSuccessfully(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironment(ctx, t, pool, true, contractsPathPtr("contracts/api"))
	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-nil-sc")
	sessionID := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-nil-sc", "https://github.com/acme/repo-nil-sc.git", "main")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-nil-sc"}}
	// No SourceControl at all (nil), unlike every other test in this file.
	r := newImageBuildTestRegistry(t, ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	sandboxStore := narvipg.NewSandboxStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := sandboxStore.Get(ctx, sessionID)
		return getErr == nil && sqlcgen.SandboxStatus(row.Status) == sqlcgen.SandboxStatusConnecting
	})

	if _, err := narvipg.NewContractDriftStore(pool).Get(ctx, "acme/repo-nil-sc@main"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("contract_drift_snapshots row: err = %v, want pgx.ErrNoRows (nil SourceControl means nothing was ever checked)", err)
	}
}

// TestCheckContractDrift_NoUsableGitHubToken_StillSpawnsSuccessfully proves
// a mock-configured session whose creator has no usable GitHub token still
// spawns successfully -- mirrors TestCheckContractDrift_
// NilSourceControl_StillSpawnsSuccessfully exactly, except SourceControl
// IS configured (proving the early return is specifically the token check,
// not an incidental nil-SourceControl skip).
func TestCheckContractDrift_NoUsableGitHubToken_StillSpawnsSuccessfully(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironment(ctx, t, pool, true, contractsPathPtr("contracts/api"))
	sessionID := createTestSessionWithRepoAndEnvironment(ctx, t, pool, pgtype.UUID{}, // no created_by
		environmentID, "repo-no-token", "https://github.com/acme/repo-no-token.git", "main")

	sourceControl := &fakeSourceControl{nextSHA: "sha-should-never-be-used", nextFingerprint: "fp-should-never-be-used", nextFingerprintExists: true}
	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-no-token"}}
	r := newImageBuildTestRegistry(t, ctx, pool, provider, sourceControl)
	t.Cleanup(func() { _ = r.Shutdown() })

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool { return provider.callCount() == 1 })

	sandboxStore := narvipg.NewSandboxStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := sandboxStore.Get(ctx, sessionID)
		return getErr == nil && sqlcgen.SandboxStatus(row.Status) == sqlcgen.SandboxStatusConnecting
	})

	if got := sourceControl.fingerprintCallCount(); got != 0 {
		t.Errorf("ResolveContractsFingerprint call count = %d, want 0 (no usable token -> never attempted)", got)
	}
	if _, err := narvipg.NewContractDriftStore(pool).Get(ctx, "acme/repo-no-token@main"); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("contract_drift_snapshots row: err = %v, want pgx.ErrNoRows", err)
	}
}

// TestCheckContractDrift_DifferentBranches_SameRepo_NoFalsePositiveDrift
// proves audit finding F5's own fix: two mock-configured sessions naming
// the SAME repo but DIFFERENT branches no longer see each other's SHA as
// "previous" and wrongly report drift. Before the fix, repoKey was a bare
// "owner/repo" (no branch), so branch-b's spawn here would have read
// branch-a's just-recorded snapshot as its own "previous", seen a
// different SHA (branches always resolve to different SHAs) with a
// coincidentally-matching fingerprint, and flagged false-positive drift --
// even though nothing on branch-b itself ever changed (this was in fact
// branch-b's OWN first sighting).
func TestCheckContractDrift_DifferentBranches_SameRepo_NoFalsePositiveDrift(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironment(ctx, t, pool, true, contractsPathPtr("contracts/api"))
	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-multibranch")
	turnStore := narvipg.NewTurnStore(pool)
	contractDriftStore := narvipg.NewContractDriftStore(pool)

	const repoURL = "https://github.com/acme/repo-multibranch.git"
	const keyBranchA = "acme/repo-multibranch@feature-a"
	const keyBranchB = "acme/repo-multibranch@feature-b"

	// branch-a's spawn: records its own baseline (sha-a1, fp-shared).
	sessionA := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-multibranch", repoURL, "feature-a")
	createPendingTurn(ctx, t, turnStore, sessionA, "prompt a")

	sourceControlA := &fakeSourceControl{nextSHA: "sha-a1", nextFingerprint: "fp-shared", nextFingerprintExists: true}
	providerA := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-multibranch-a"}}
	rA := newImageBuildTestRegistry(t, ctx, pool, providerA, sourceControlA)
	t.Cleanup(func() { _ = rA.Shutdown() })

	aA, err := rA.GetOrSpawn(ctx, sessionA)
	if err != nil {
		t.Fatalf("GetOrSpawn(sessionA): %v", err)
	}
	sendEnsureDispatched(ctx, t, aA)
	waitUntil(t, 5*time.Second, func() bool { return providerA.callCount() == 1 })

	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := contractDriftStore.Get(ctx, keyBranchA)
		return getErr == nil && row.LastRepoSha == "sha-a1"
	})

	before := readContractDriftDetected(ctx, t, otelReader)

	// branch-b's spawn: a DIFFERENT session, SAME repo, DIFFERENT branch --
	// a different SHA (branches always resolve differently) but the SAME
	// fingerprint as branch-a's baseline purely by coincidence. This is
	// branch-b's own FIRST sighting and must not be flagged as drift
	// against branch-a's snapshot.
	sessionB := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-multibranch", repoURL, "feature-b")
	createPendingTurn(ctx, t, turnStore, sessionB, "prompt b")

	sourceControlB := &fakeSourceControl{nextSHA: "sha-b1", nextFingerprint: "fp-shared", nextFingerprintExists: true}
	providerB := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-multibranch-b"}}
	rB := newImageBuildTestRegistry(t, ctx, pool, providerB, sourceControlB)
	t.Cleanup(func() { _ = rB.Shutdown() })

	aB, err := rB.GetOrSpawn(ctx, sessionB)
	if err != nil {
		t.Fatalf("GetOrSpawn(sessionB): %v", err)
	}
	sendEnsureDispatched(ctx, t, aB)
	waitUntil(t, 5*time.Second, func() bool { return providerB.callCount() == 1 })

	var rowB sqlcgen.ContractDriftSnapshot
	waitUntil(t, 5*time.Second, func() bool {
		rowB, err = contractDriftStore.Get(ctx, keyBranchB)
		return err == nil && rowB.LastRepoSha == "sha-b1"
	})
	if rowB.LastContractsFingerprint != "fp-shared" {
		t.Errorf("branch-b last_contracts_fingerprint = %q, want %q", rowB.LastContractsFingerprint, "fp-shared")
	}

	after := readContractDriftDetected(ctx, t, otelReader)
	if after != before {
		t.Errorf("contract_drift_detected counter delta = %d, want 0 (branch-b's own first sighting must never be flagged as drift against branch-a's snapshot)", after-before)
	}

	// branch-a's own snapshot must be untouched by branch-b's spawn --
	// each branch owns an independent row.
	rowA, err := contractDriftStore.Get(ctx, keyBranchA)
	if err != nil {
		t.Fatalf("get branch-a snapshot: %v", err)
	}
	if rowA.LastRepoSha != "sha-a1" {
		t.Errorf("branch-a last_repo_sha = %q, want %q (must not be overwritten by branch-b's spawn)", rowA.LastRepoSha, "sha-a1")
	}

	// A genuine SECOND spawn on branch-a itself (same branch, repo SHA
	// changed, contracts fingerprint did not) must still correctly detect
	// drift -- the fix scopes the key to (repo, branch), it does not
	// disable same-branch drift detection.
	beforeSameBranch := readContractDriftDetected(ctx, t, otelReader)

	sessionA2 := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-multibranch", repoURL, "feature-a")
	createPendingTurn(ctx, t, turnStore, sessionA2, "prompt a2")

	sourceControlA2 := &fakeSourceControl{nextSHA: "sha-a2", nextFingerprint: "fp-shared", nextFingerprintExists: true}
	providerA2 := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-multibranch-a2"}}
	rA2 := newImageBuildTestRegistry(t, ctx, pool, providerA2, sourceControlA2)
	t.Cleanup(func() { _ = rA2.Shutdown() })

	aA2, err := rA2.GetOrSpawn(ctx, sessionA2)
	if err != nil {
		t.Fatalf("GetOrSpawn(sessionA2): %v", err)
	}
	sendEnsureDispatched(ctx, t, aA2)
	waitUntil(t, 5*time.Second, func() bool { return providerA2.callCount() == 1 })

	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := contractDriftStore.Get(ctx, keyBranchA)
		return getErr == nil && row.LastRepoSha == "sha-a2"
	})

	afterSameBranch := readContractDriftDetected(ctx, t, otelReader)
	if afterSameBranch-beforeSameBranch != 1 {
		t.Errorf("contract_drift_detected counter delta = %d, want 1 (genuine same-branch drift must still be detected)", afterSameBranch-beforeSameBranch)
	}
}

// TestCheckContractDrift_NilBranchAndExplicitDefaultBranchName_ShareOneKey
// is the F5 follow-up regression test: a session left with no explicit
// branch (r.Branch == nil) and a later session that explicitly names the
// repo's own real default branch by name must be tracked as the SAME
// branch's drift state -- both resolve to the identical underlying ref,
// via ports.SourceControl.ResolveBranchSHA's own "empty Branch resolves to
// the repo's real default" contract. Before this fix, checkContractDriftForRepo
// built its repoKey from the raw/possibly-nil branch string, so the
// nil-branch session keyed on "owner/repo@" while the explicit-"main"
// session keyed on "owner/repo@main" -- two independent rows for what is
// actually one branch, so a genuine SHA change on that branch (this test's
// second spawn) would have been silently missed as drift (read back as a
// fresh "first sighting" on the second, different key instead).
func TestCheckContractDrift_NilBranchAndExplicitDefaultBranchName_ShareOneKey(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	environmentID := createTestEnvironment(ctx, t, pool, true, contractsPathPtr("contracts/api"))
	creator := createTestUserWithGitHubToken(ctx, t, pool, "gh-fake-token-nilbranch")
	turnStore := narvipg.NewTurnStore(pool)
	contractDriftStore := narvipg.NewContractDriftStore(pool)

	const repoURL = "https://github.com/acme/repo-nilbranch.git"
	const key = "acme/repo-nilbranch@main"

	// Session A: no explicit branch at all (nil) -- resolves to the
	// repo's own real default branch, "main", via the fake's own
	// defaultBranchName (mirroring the real adapter's empty-Branch
	// resolution).
	sessionA := createTestSessionWithRepoAndEnvironmentNilBranch(ctx, t, pool, creator, environmentID,
		"repo-nilbranch", repoURL)
	createPendingTurn(ctx, t, turnStore, sessionA, "prompt a")

	sourceControlA := &fakeSourceControl{
		nextSHA: "sha-nil-1", defaultBranchName: "main",
		nextFingerprint: "fp-1", nextFingerprintExists: true,
	}
	providerA := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-nilbranch-a"}}
	rA := newImageBuildTestRegistry(t, ctx, pool, providerA, sourceControlA)
	t.Cleanup(func() { _ = rA.Shutdown() })

	aA, err := rA.GetOrSpawn(ctx, sessionA)
	if err != nil {
		t.Fatalf("GetOrSpawn(sessionA): %v", err)
	}
	sendEnsureDispatched(ctx, t, aA)
	waitUntil(t, 5*time.Second, func() bool { return providerA.callCount() == 1 })

	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := contractDriftStore.Get(ctx, key)
		return getErr == nil && row.LastRepoSha == "sha-nil-1"
	})

	before := readContractDriftDetected(ctx, t, otelReader)

	// Session B: a DIFFERENT session, explicitly naming that SAME real
	// default branch ("main") by name. A genuinely new SHA, but the SAME
	// fingerprint -- must be flagged as real drift against session A's
	// snapshot, because it IS the same branch, not a fresh first sighting
	// on a separate key.
	sessionB := createTestSessionWithRepoAndEnvironment(ctx, t, pool, creator, environmentID,
		"repo-nilbranch", repoURL, "main")
	createPendingTurn(ctx, t, turnStore, sessionB, "prompt b")

	sourceControlB := &fakeSourceControl{nextSHA: "sha-nil-2", nextFingerprint: "fp-1", nextFingerprintExists: true}
	providerB := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-nilbranch-b"}}
	rB := newImageBuildTestRegistry(t, ctx, pool, providerB, sourceControlB)
	t.Cleanup(func() { _ = rB.Shutdown() })

	aB, err := rB.GetOrSpawn(ctx, sessionB)
	if err != nil {
		t.Fatalf("GetOrSpawn(sessionB): %v", err)
	}
	sendEnsureDispatched(ctx, t, aB)
	waitUntil(t, 5*time.Second, func() bool { return providerB.callCount() == 1 })

	waitUntil(t, 5*time.Second, func() bool {
		row, getErr := contractDriftStore.Get(ctx, key)
		return getErr == nil && row.LastRepoSha == "sha-nil-2"
	})

	after := readContractDriftDetected(ctx, t, otelReader)
	if after-before != 1 {
		t.Errorf("contract_drift_detected counter delta = %d, want 1 (nil-branch session and explicit-\"main\" session must share one key, so this SHA change is genuine drift, not a second first-sighting)", after-before)
	}
}
