//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// fakeSpawnProvider is a test-only ports.SandboxProvider recording every
// CreateSandbox call it receives and returning a caller-configured
// (ref, err) pair -- this package's own EnsureDispatched decision-tree
// tests never talk to a real cloud provider.
//
// Step 22 ("snapshots & restore") extends this same fake with an identical
// recording shape for RestoreFromSnapshot (restoreCalls/nextRestoreRef/
// nextRestoreErr) rather than introducing a second, parallel fake -- the
// restore-path tests below need the exact same CreateSandbox-call-recording
// precedent, just for the other provider method dispatch.go's executeRestore
// now calls.
//
// Step 23 ("resume") extends this same fake again with the identical
// recording shape for ResumeSandbox (resumeCalls/nextResumeErr -- no
// "nextResumeRef" counterpart, since ports.SandboxProvider.ResumeSandbox
// returns only an error, no SandboxRef: it resumes the SAME provider
// object already on the sandbox row, rather than minting a new one), plus
// a resumeSupported field so a test can flip Capabilities().Resume on a
// per-test basis (defaulting to false -- the SAME value Capabilities()
// already hardcoded before this Step, so every pre-existing test's
// behavior is unchanged).
//
// Step 23's own concurrency-fix follow-up adds resumeBlock: an optional,
// test-supplied channel that, when non-nil, ResumeSandbox blocks on
// (after recording the call) until the test closes it or ctx is done --
// this is what lets TestResilience_ConcurrentResumeAcrossActors_
// ResumeSandboxCalledAtMostOnce (below) hold a real ResumeSandbox call
// "in flight" for actor A while it hydrates a second actor instance for
// the same session and proves that second actor's own EnsureDispatched
// does NOT call ResumeSandbox a second time -- exactly the live
// reproduction shape the adversarial review this fix responds to used.
// nil (the zero value) for every OTHER test in this file, so
// ResumeSandbox returns immediately for all of them, unchanged.
type fakeSpawnProvider struct {
	mu      sync.Mutex
	calls   []ports.CreateSpec
	nextRef ports.SandboxRef
	nextErr error

	restoreCalls   []fakeRestoreCall
	nextRestoreRef ports.SandboxRef
	nextRestoreErr error

	resumeSupported bool
	resumeCalls     []ports.SandboxRef
	nextResumeErr   error
	resumeBlock     chan struct{}
}

// fakeRestoreCall records one RestoreFromSnapshot invocation's own
// arguments, mirroring how calls ([]ports.CreateSpec) records CreateSandbox's
// own single argument -- RestoreFromSnapshot takes two, so this small struct
// keeps both together per call.
type fakeRestoreCall struct {
	snapshotID ports.SnapshotID
	spec       ports.CreateSpec
}

var _ ports.SandboxProvider = (*fakeSpawnProvider)(nil)

func (f *fakeSpawnProvider) Capabilities() ports.Capabilities {
	return ports.Capabilities{Snapshots: true, Resume: f.resumeSupported, ExplicitStop: false, ImageBuilds: false}
}

func (f *fakeSpawnProvider) CreateSandbox(_ context.Context, spec ports.CreateSpec) (ports.SandboxRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, spec)
	return f.nextRef, f.nextErr
}

func (f *fakeSpawnProvider) StopSandbox(context.Context, ports.SandboxRef) error { return nil }
func (f *fakeSpawnProvider) ResumeSandbox(ctx context.Context, ref ports.SandboxRef) error {
	f.mu.Lock()
	f.resumeCalls = append(f.resumeCalls, ref)
	block := f.resumeBlock
	err := f.nextResumeErr
	f.mu.Unlock()

	// The call is already recorded (above) BEFORE any blocking -- a test
	// polling resumeCallCount() sees this call the instant it starts, not
	// only once it returns, which is exactly what lets
	// TestResilience_ConcurrentResumeAcrossActors_ResumeSandboxCalledAtMostOnce
	// (below) know actor A's own call has genuinely started before it goes
	// on to hydrate a second actor instance.
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}
func (f *fakeSpawnProvider) TakeSnapshot(context.Context, ports.SandboxRef) (ports.SnapshotID, error) {
	return "", errors.New("not implemented")
}

func (f *fakeSpawnProvider) RestoreFromSnapshot(_ context.Context, id ports.SnapshotID, spec ports.CreateSpec) (ports.SandboxRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restoreCalls = append(f.restoreCalls, fakeRestoreCall{snapshotID: id, spec: spec})
	return f.nextRestoreRef, f.nextRestoreErr
}
func (f *fakeSpawnProvider) BuildImage(context.Context, ports.ImageSpec) (ports.BuildRef, error) {
	return "", errors.New("not implemented")
}
func (f *fakeSpawnProvider) DeleteImage(context.Context, ports.ImageRef) error {
	return errors.New("not implemented")
}
func (f *fakeSpawnProvider) List(context.Context) ([]ports.SandboxRef, error) { return nil, nil }

func (f *fakeSpawnProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSpawnProvider) lastSpec() ports.CreateSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *fakeSpawnProvider) restoreCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.restoreCalls)
}

func (f *fakeSpawnProvider) lastRestoreCall() fakeRestoreCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.restoreCalls[len(f.restoreCalls)-1]
}

func (f *fakeSpawnProvider) resumeCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.resumeCalls)
}

func (f *fakeSpawnProvider) lastResumeCall() ports.SandboxRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resumeCalls[len(f.resumeCalls)-1]
}

// fakeSendCommander is a test-only ports.SandboxCommander recording every
// SendCommand call and returning a caller-configured error.
type fakeSendCommander struct {
	mu       sync.Mutex
	sessions []string
	payloads []json.RawMessage
	nextErr  error
}

var _ ports.SandboxCommander = (*fakeSendCommander)(nil)

func (f *fakeSendCommander) SendCommand(sessionID string, payload json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, sessionID)
	f.payloads = append(f.payloads, payload)
	return f.nextErr
}

func (f *fakeSendCommander) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.payloads)
}

func (f *fakeSendCommander) lastPayload() json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.payloads[len(f.payloads)-1]
}

// newDispatchTestRegistry builds a Registry wired with provider/commander
// fakes and a real publicBaseURL (needed for assembleSessionConfig's own
// ws-scheme derivation).
func newDispatchTestRegistry(ctx context.Context, pool *pgxpool.Pool, provider ports.SandboxProvider, commander ports.SandboxCommander) *Registry {
	return NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, commander, provider, "http://localhost:8080", nil, nil)
}

// createPendingTurn inserts a Pending turn carrying prompt for sessionID.
func createPendingTurn(ctx context.Context, t *testing.T, turns *narvipg.TurnStore, sessionID pgtype.UUID, prompt string) sqlcgen.Turn {
	t.Helper()
	created, err := turns.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusPending,
		Prompt:    &prompt,
	})
	if err != nil {
		t.Fatalf("create pending turn: %v", err)
	}
	return created
}

func sendEnsureDispatched(ctx context.Context, t *testing.T, a *Actor) {
	t.Helper()
	if err := a.Send(ctx, EnsureDispatched{}); err != nil {
		t.Fatalf("Send(EnsureDispatched{}): %v", err)
	}
}

// TestHandleEnsureDispatched_NoSandbox_Spawns proves: no sandbox row +
// pending turn + circuit breaker allowing -> a real CreateSandbox call
// with a Validate()-passing CreateSpec, and the sandbox row lands in
// Connecting (Spawning->Connecting via TriggerProviderAck) once the fake
// provider's own success response is recorded.
func TestHandleEnsureDispatched_NoSandbox_Spawns(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-object-1"}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	sandboxStore := narvipg.NewSandboxStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		return provider.callCount() == 1
	})

	spec := provider.lastSpec()
	if err := spec.Validate(); err != nil {
		t.Errorf("CreateSpec.Validate() = %v, want nil", err)
	}
	if spec.Gen != 1 {
		t.Errorf("CreateSpec.Gen = %d, want 1", spec.Gen)
	}
	if spec.SessionConfig.SandboxToken == "" {
		t.Error("CreateSpec.SessionConfig.SandboxToken is empty, want a real minted token")
	}

	waitUntil(t, 5*time.Second, func() bool {
		row, err := sandboxStore.Get(ctx, sessionID)
		return err == nil && row.Status == sqlcgen.SandboxStatusConnecting
	})

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.ProviderID == nil || *row.ProviderID != "provider-object-1" {
		t.Errorf("sandbox provider_id = %v, want %q", row.ProviderID, "provider-object-1")
	}
}

// TestHandleEnsureDispatched_CircuitBreakerOpen_DoesNotSpawn proves a
// sandbox row already recording 3 recent permanent failures (the circuit
// breaker's own threshold) blocks a further spawn attempt entirely -- the
// fake provider's CreateSandbox is never called, and the sandbox row is
// left exactly as it was.
func TestHandleEnsureDispatched_CircuitBreakerOpen_DoesNotSpawn(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusFailed,
	}); err != nil {
		t.Fatalf("move sandbox to failed: %v", err)
	}
	if _, err := sandboxStore.UpdateCircuitBreaker(ctx, sqlcgen.UpdateSandboxCircuitBreakerParams{
		SessionID:          sessionID,
		SpawnFailureCount:  3,
		LastSpawnFailureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("seed circuit breaker: %v", err)
	}

	provider := &fakeSpawnProvider{}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	// Give the actor's mailbox a moment to process, then assert the
	// circuit breaker actually blocked the attempt (a fixed sleep is used
	// here deliberately -- there is no positive DB signal to poll FOR when
	// asserting something did NOT happen).
	time.Sleep(300 * time.Millisecond)

	if got := provider.callCount(); got != 0 {
		t.Errorf("provider.CreateSandbox called %d times, want 0 (circuit breaker should have blocked it)", got)
	}
	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Status != sqlcgen.SandboxStatusFailed {
		t.Errorf("sandbox status = %s, want unchanged %s", row.Status, sqlcgen.SandboxStatusFailed)
	}
	if row.SpawnFailureCount != 3 {
		t.Errorf("spawn_failure_count = %d, want unchanged 3", row.SpawnFailureCount)
	}
}

// TestHandleEnsureDispatched_SandboxReady_DispatchesTurn proves: a Ready
// sandbox + a Pending turn -> both turn transitions happen (Pending->
// Dispatched->Processing), turn_deadline is armed, and SendCommand is
// called with a real, schema-valid sandboxws.Prompt payload.
func TestHandleEnsureDispatched_SandboxReady_DispatchesTurn(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	created := createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusReady,
	}); err != nil {
		t.Fatalf("move sandbox to ready: %v", err)
	}

	commander := &fakeSendCommander{}
	r := newDispatchTestRegistry(ctx, pool, nil, commander)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool {
		return commander.callCount() == 1
	})

	got, err := turnStore.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if got.Status != sqlcgen.TurnStatusProcessing {
		t.Errorf("turn status = %s, want %s", got.Status, sqlcgen.TurnStatusProcessing)
	}
	if !got.DispatchedAt.Valid {
		t.Error("turn dispatched_at not set")
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_timers WHERE session_id = $1 AND name = $2`,
		sessionID, TimerTurnDeadline,
	).Scan(&n); err != nil {
		t.Fatalf("count turn_deadline timers: %v", err)
	}
	if n != 1 {
		t.Errorf("turn_deadline timer count = %d, want 1", n)
	}

	var prompt sandboxws.Prompt
	if err := json.Unmarshal(commander.lastPayload(), &prompt); err != nil {
		t.Fatalf("unmarshal SendCommand payload as sandboxws.Prompt: %v", err)
	}
	if prompt.Type != "prompt" {
		t.Errorf("Prompt.Type = %q, want %q", prompt.Type, "prompt")
	}
	if prompt.SessionId != sessionID.String() {
		t.Errorf("Prompt.SessionId = %q, want %q", prompt.SessionId, sessionID.String())
	}
	if prompt.Text != "do the thing" {
		t.Errorf("Prompt.Text = %q, want %q", prompt.Text, "do the thing")
	}
	if prompt.ScmName == "" || prompt.ScmEmail == "" {
		t.Error("Prompt.ScmName/ScmEmail must be non-empty")
	}
	if commander.sessions[0] != sessionID.String() {
		t.Errorf("SendCommand sessionID = %q, want %q", commander.sessions[0], sessionID.String())
	}
}

// TestHandleEnsureDispatched_SendCommandNoLiveConnection_FailsTurnForward
// proves the restructured dispatchTurn (design decision 3b's own fix: a
// real network call must never run while the transact's own FOR UPDATE
// lock on the session row is held -- see dispatch.go's own top comment):
// SendCommand returning ports.ErrNoLiveSandboxConnection no longer rolls
// anything back (the turn is already committed Pending->Dispatched->
// Processing by the time SendCommand is ever attempted, and domain/turn
// has no reverse edge to revert it with) -- instead the turn is failed
// FORWARD, Processing->Failed, with a synthetic execution_complete event
// and the session's own derived status/failure_reason following, and
// turn_deadline (armed, then deleted once the turn resolves) ends up
// absent.
func TestHandleEnsureDispatched_SendCommandNoLiveConnection_FailsTurnForward(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	created := createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusReady,
	}); err != nil {
		t.Fatalf("move sandbox to ready: %v", err)
	}

	commander := &fakeSendCommander{nextErr: ports.ErrNoLiveSandboxConnection}
	r := newDispatchTestRegistry(ctx, pool, nil, commander)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool {
		got, err := turnStore.Get(ctx, created.ID)
		return err == nil && got.Status == sqlcgen.TurnStatusFailed
	})

	got, err := turnStore.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if got.Status != sqlcgen.TurnStatusFailed {
		t.Errorf("turn status = %s, want %s (failed forward, never reverted to pending)", got.Status, sqlcgen.TurnStatusFailed)
	}
	if !got.DispatchedAt.Valid {
		t.Error("turn dispatched_at not set, want set (the pending->dispatched->processing transitions DID commit before the send failure)")
	}
	if !got.CompletedAt.Valid {
		t.Error("turn completed_at not set")
	}

	sessionStore := narvipg.NewSessionStore(pool)
	gotSession, err := sessionStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if gotSession.Status != sqlcgen.SessionStatusFailed {
		t.Errorf("session status = %q, want %q", gotSession.Status, sqlcgen.SessionStatusFailed)
	}
	if gotSession.FailureReason == nil {
		t.Error("session failure_reason is nil, want set")
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_timers WHERE session_id = $1 AND name = $2`,
		sessionID, TimerTurnDeadline,
	).Scan(&n); err != nil {
		t.Fatalf("count turn_deadline timers: %v", err)
	}
	if n != 0 {
		t.Errorf("turn_deadline timer count = %d, want 0 (armed then deleted once the turn resolved)", n)
	}

	var eventCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM events WHERE session_id = $1 AND type = 'execution_complete'`,
		sessionID,
	).Scan(&eventCount); err != nil {
		t.Fatalf("count execution_complete events: %v", err)
	}
	if eventCount != 1 {
		t.Errorf("execution_complete event count = %d, want 1 (synthetic completion, §3.3 -- the turn never reached a live sandbox, so no real terminal event ever arrives)", eventCount)
	}
}

// TestHandleEnsureDispatched_PermanentProviderError_IncrementsCircuitBreaker
// proves a *ports.ProviderError with Transient=false increments the
// persisted circuit-breaker columns and moves the sandbox to Suspect
// (never straight to Failed -- §3.2's "a watchdog never writes failed
// directly" rule, reused here via transitionSandboxToSuspect).
func TestHandleEnsureDispatched_PermanentProviderError_IncrementsCircuitBreaker(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{nextErr: &ports.ProviderError{
		Transient: false, Code: "INVALID_IMAGE", Op: ports.OpCreateSandbox, Err: errors.New("boom"),
	}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	sandboxStore := narvipg.NewSandboxStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		row, err := sandboxStore.Get(ctx, sessionID)
		return err == nil && row.Status == sqlcgen.SandboxStatusSuspect
	})

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.SpawnFailureCount != 1 {
		t.Errorf("spawn_failure_count = %d, want 1", row.SpawnFailureCount)
	}
	if !row.LastSpawnFailureAt.Valid {
		t.Error("last_spawn_failure_at not set")
	}
}

// TestHandleEnsureDispatched_TransientProviderError_DoesNotIncrementCircuitBreaker
// proves a *ports.ProviderError with Transient=true leaves the sandbox in
// Spawning (for a later retry) and does NOT touch the persisted circuit
// breaker columns at all (§3.2: "a novel transient failure must not trip
// the breaker").
func TestHandleEnsureDispatched_TransientProviderError_DoesNotIncrementCircuitBreaker(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{nextErr: &ports.ProviderError{
		Transient: true, Code: "http_503", Op: ports.OpCreateSandbox, Err: errors.New("temporarily unavailable"),
	}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool {
		return provider.callCount() == 1
	})
	time.Sleep(300 * time.Millisecond)

	sandboxStore := narvipg.NewSandboxStore(pool)
	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Status != sqlcgen.SandboxStatusSpawning {
		t.Errorf("sandbox status = %s, want unchanged %s (transient failure retries later)", row.Status, sqlcgen.SandboxStatusSpawning)
	}
	if row.SpawnFailureCount != 0 {
		t.Errorf("spawn_failure_count = %d, want 0 (transient failures must not trip the breaker)", row.SpawnFailureCount)
	}
	if row.LastSpawnFailureAt.Valid {
		t.Error("last_spawn_failure_at is set, want unchanged/invalid (transient failures must not trip the breaker)")
	}
}

// TestExecuteSpawn_StaleEpochOnRecord_PropagatesErrStaleEpoch proves
// Finding 2's own fix: when a real CreateSandbox call genuinely succeeds
// but this actor's own epoch has already gone stale (a legitimate
// pod-handoff race) by the time executeSpawn's own second transact tries
// to record the outcome, that transact correctly rolls back (proven here
// by the sandbox row showing no provider_id at all), and executeSpawn
// still propagates ErrStaleEpoch UNCHANGED (proven exactly like
// TestActorTransact_StaleEpochEvictsSelf does) rather than swallowing it
// -- the fix adds an observability log at this call site, it must not
// change the actor's own eviction behavior for a genuinely stale epoch.
// This package's own test conventions assert on durable Postgres state,
// not log output (see e.g. the circuit-breaker/transient-failure tests
// above), so this test proves the fix's OBSERVABLE contract: the real
// provider call still happens exactly once, its result is never recorded
// anywhere once the epoch is stale, and the error is neither swallowed
// nor turned into a panic.
func TestExecuteSpawn_StaleEpochOnRecord_PropagatesErrStaleEpoch(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "orphaned-provider-object"}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	// Drive planDispatch directly (bypassing the mailbox) so this test can
	// deterministically bump actor_epoch AFTER the spawn plan's own first
	// transact has already committed but BEFORE executeSpawn's second
	// transact ever runs -- exactly the race window the finding this test
	// covers is about. Calling these unexported methods directly (this
	// file is package sessionactor, not sessionactor_test) mirrors
	// TestActorTransact_StaleEpochEvictsSelf's own precedent of testing
	// transact's fencing behavior directly rather than only through the
	// mailbox.
	spawn, dispatch, err := a.planDispatch(ctx)
	if err != nil {
		t.Fatalf("planDispatch: %v", err)
	}
	if dispatch != nil {
		t.Fatal("planDispatch returned a dispatch plan, want a spawn plan (no sandbox row exists yet)")
	}
	if spawn == nil {
		t.Fatal("planDispatch returned no spawn plan, want one (no sandbox row + a pending turn + circuit breaker allowing)")
	}
	if spawn.resume {
		t.Fatal("planDispatch returned a resume plan, want a plain spawn plan (no sandbox row exists yet, not resume-eligible)")
	}

	// Simulate a legitimate pod-handoff race: a newer actor has since
	// taken over this session (bumping actor_epoch), exactly like
	// TestActorTransact_StaleEpochEvictsSelf's own setup.
	sessionStore := narvipg.NewSessionStore(pool)
	if _, err := sessionStore.BumpActorEpoch(ctx, sessionID); err != nil {
		t.Fatalf("BumpActorEpoch: %v", err)
	}

	err = a.executeSpawn(ctx, spawn)
	if !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("executeSpawn() after epoch bump = %v, want ErrStaleEpoch", err)
	}
	if got := provider.callCount(); got != 1 {
		t.Errorf("provider.CreateSandbox called %d times, want 1 (the real cloud call must still happen; only the RECORD half is fenced)", got)
	}

	// The whole point of the finding this test covers: a real provider_id
	// was returned by CreateSandbox above, but the write that would have
	// recorded it was rolled back by the stale-epoch fencing check, so it
	// is durably recorded NOWHERE in Postgres -- proving the leak this
	// fix's log line exists to make observable, not the log line itself.
	sandboxStore := narvipg.NewSandboxStore(pool)
	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.ProviderID != nil {
		t.Errorf("sandbox provider_id = %v, want nil (the record-outcome transact must have rolled back)", *row.ProviderID)
	}
}

// --- Step 22 ("snapshots & restore"), design decision 6: executeRestore's
// own decision-tree coverage, mirroring this file's own existing
// spawn-path tests exactly (same rig helpers, same fakeSpawnProvider, same
// waitUntil/fixed-sleep conventions) for the parallel Restore path
// EvaluateSpawnDecision's own Restore branch (already built, dispatch.go's
// own new caller) makes reachable for the first time.

// seedStoppedSandboxWithSnapshot creates a sandbox row, moves it to Stopped,
// and records snapshotID on it -- the exact (status, snapshot_id) precondition
// EvaluateSpawnDecision's Restore branch requires (Stopped/Failed/Stale +
// SnapshotImageID != ""). Returns the store so callers can re-Get afterward.
func seedStoppedSandboxWithSnapshot(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, snapshotID string) *narvipg.SandboxStore {
	t.Helper()
	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusStopped,
	}); err != nil {
		t.Fatalf("move sandbox to stopped: %v", err)
	}
	if _, err := sandboxStore.UpdateSnapshotID(ctx, sqlcgen.UpdateSandboxSnapshotIDParams{
		SessionID: sessionID, SnapshotID: &snapshotID,
	}); err != nil {
		t.Fatalf("seed snapshot_id: %v", err)
	}
	return sandboxStore
}

// TestHandleEnsureDispatched_StoppedWithSnapshot_Restores proves the full
// real Restore path: a Stopped sandbox carrying a real snapshot_id + a
// pending turn -> EvaluateSpawnDecision's Restore branch fires -> a real
// RestoreFromSnapshot call (not CreateSandbox) with a Validate()-passing
// CreateSpec whose BootMode is SnapshotRestore (design decision 6b) and
// whose SnapshotID argument is the sandbox row's own persisted one -- gen
// is genuinely bumped (1 -> 2, the SAME UpsertSandboxForSpawn write a plain
// spawn uses, per planRestore's own doc comment), and once the fake
// provider's success response is recorded, the sandbox lands in Connecting
// via the SAME Spawning->Connecting TriggerProviderAck transition a fresh
// spawn also uses.
func TestHandleEnsureDispatched_StoppedWithSnapshot_Restores(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := seedStoppedSandboxWithSnapshot(ctx, t, pool, sessionID, "snap-restore-1")

	provider := &fakeSpawnProvider{nextRestoreRef: ports.SandboxRef{ProviderID: "restored-provider-object-1"}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool {
		return provider.restoreCallCount() == 1
	})
	// CreateSandbox must never be called on the restore path.
	if got := provider.callCount(); got != 0 {
		t.Errorf("provider.CreateSandbox called %d times, want 0 (restore must call RestoreFromSnapshot, never CreateSandbox)", got)
	}

	call := provider.lastRestoreCall()
	if call.snapshotID != "snap-restore-1" {
		t.Errorf("RestoreFromSnapshot snapshotID = %q, want %q", call.snapshotID, "snap-restore-1")
	}
	if err := call.spec.Validate(); err != nil {
		t.Errorf("CreateSpec.Validate() = %v, want nil", err)
	}
	if call.spec.Gen != 2 {
		t.Errorf("CreateSpec.Gen = %d, want 2 (bumped from the Stopped row's own gen 1)", call.spec.Gen)
	}
	if call.spec.SessionConfig.BootMode != sessionconfig.SessionConfigBootModeSnapshotRestore {
		t.Errorf("CreateSpec.SessionConfig.BootMode = %q, want %q",
			call.spec.SessionConfig.BootMode, sessionconfig.SessionConfigBootModeSnapshotRestore)
	}
	if call.spec.SessionConfig.SandboxToken == "" {
		t.Error("CreateSpec.SessionConfig.SandboxToken is empty, want a real freshly minted token")
	}

	waitUntil(t, 5*time.Second, func() bool {
		row, err := sandboxStore.Get(ctx, sessionID)
		return err == nil && row.Status == sqlcgen.SandboxStatusConnecting
	})

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Gen != 2 {
		t.Errorf("sandbox gen = %d, want 2 (a real gen bump, TriggerRestore's own gen-fencing check passing)", row.Gen)
	}
	if row.ProviderID == nil || *row.ProviderID != "restored-provider-object-1" {
		t.Errorf("sandbox provider_id = %v, want %q", row.ProviderID, "restored-provider-object-1")
	}
	if row.SnapshotID == nil || *row.SnapshotID != "snap-restore-1" {
		t.Errorf("sandbox snapshot_id = %v, want unchanged %q (a restore does not clear it)", row.SnapshotID, "snap-restore-1")
	}
}

// TestExecuteRestore_PermanentProviderError_IncrementsCircuitBreaker mirrors
// TestHandleEnsureDispatched_PermanentProviderError_IncrementsCircuitBreaker
// exactly, but drives it via a permanent RestoreFromSnapshot failure instead
// of a permanent CreateSandbox one -- proving design decision 6's own
// required circuit-breaker reuse: recordSpawnFailure is the SAME function
// for both, so a permanent restore failure increments the SAME
// spawn_failure_count/last_spawn_failure_at columns and moves the sandbox to
// Suspect (never straight to Failed). The gen bump from planRestore's own
// write already committed before this failure was ever returned (mirroring
// how a plain spawn's own gen/token/status write commits before CreateSandbox
// is attempted) -- proven here by asserting gen == 2 even though the restore
// itself failed.
func TestExecuteRestore_PermanentProviderError_IncrementsCircuitBreaker(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := seedStoppedSandboxWithSnapshot(ctx, t, pool, sessionID, "snap-restore-2")

	provider := &fakeSpawnProvider{nextRestoreErr: &ports.ProviderError{
		Transient: false, Code: "INVALID_SNAPSHOT", Op: ports.OpRestoreFromSnapshot, Err: errors.New("boom"),
	}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool {
		row, err := sandboxStore.Get(ctx, sessionID)
		return err == nil && row.Status == sqlcgen.SandboxStatusSuspect
	})

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.SpawnFailureCount != 1 {
		t.Errorf("spawn_failure_count = %d, want 1 (the SAME circuit-breaker columns a permanent CreateSandbox failure uses)", row.SpawnFailureCount)
	}
	if !row.LastSpawnFailureAt.Valid {
		t.Error("last_spawn_failure_at not set")
	}
	if row.Gen != 2 {
		t.Errorf("sandbox gen = %d, want 2 (planRestore's own gen bump already committed before the failed provider call)", row.Gen)
	}
	if got := provider.restoreCallCount(); got != 1 {
		t.Errorf("provider.RestoreFromSnapshot called %d times, want 1", got)
	}
}

// TestHandleEnsureDispatched_RestoreCircuitBreakerOpen_DoesNotRestore mirrors
// TestHandleEnsureDispatched_CircuitBreakerOpen_DoesNotSpawn exactly (same
// seeded-3-failures-then-a-4th-is-blocked proof Step 21's own spawn circuit
// breaker test already established), but on a Stopped+snapshot_id sandbox --
// proving the circuit breaker gate in tryPlanSpawn runs BEFORE
// EvaluateSpawnDecision is ever consulted, so it blocks a would-be Restore
// exactly as completely as it blocks a would-be Spawn: RestoreFromSnapshot is
// never called, and the sandbox row (status, gen, snapshot_id, circuit
// breaker columns) is left completely untouched by this 4th attempt.
func TestHandleEnsureDispatched_RestoreCircuitBreakerOpen_DoesNotRestore(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := seedStoppedSandboxWithSnapshot(ctx, t, pool, sessionID, "snap-restore-3")
	if _, err := sandboxStore.UpdateCircuitBreaker(ctx, sqlcgen.UpdateSandboxCircuitBreakerParams{
		SessionID:          sessionID,
		SpawnFailureCount:  3,
		LastSpawnFailureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("seed circuit breaker: %v", err)
	}

	provider := &fakeSpawnProvider{}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	// Same fixed-sleep-then-assert-nothing-happened shape this file's own
	// TestHandleEnsureDispatched_CircuitBreakerOpen_DoesNotSpawn already uses
	// -- there is no positive DB signal to poll FOR when asserting something
	// did NOT happen.
	time.Sleep(300 * time.Millisecond)

	if got := provider.restoreCallCount(); got != 0 {
		t.Errorf("provider.RestoreFromSnapshot called %d times, want 0 (circuit breaker should have blocked the 4th attempt)", got)
	}
	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Status != sqlcgen.SandboxStatusStopped {
		t.Errorf("sandbox status = %s, want unchanged %s", row.Status, sqlcgen.SandboxStatusStopped)
	}
	if row.Gen != 1 {
		t.Errorf("sandbox gen = %d, want unchanged 1 (no restore attempt should have run at all)", row.Gen)
	}
	if row.SpawnFailureCount != 3 {
		t.Errorf("spawn_failure_count = %d, want unchanged 3", row.SpawnFailureCount)
	}
}

// planSpawnDirect calls a.tryPlanSpawn directly, inside its own real
// transact (the same commit/epoch-fencing path planDispatch itself uses),
// bypassing planDispatch's own branch (a)/(b)/(c) routing entirely. This is
// required (not merely convenient) for several of the tests below:
// planDispatch's own branch (a) only ever calls tryPlanSpawn when
// !hasSandbox or sandbox.IsDeadSandboxStatus(status) -- i.e. Pending (no
// row) or Stopped/Failed/Stale -- so a sandbox stuck Spawning/Connecting/
// Booting is routed to branch (c)'s no-op, and a Ready sandbox (with or
// without a live WebSocket) is always routed to branch (b)'s
// tryPlanDispatch instead, NEVER to tryPlanSpawn. Driving those two
// scenarios through the mailbox (EnsureDispatched) would therefore never
// reach tryPlanSpawn's own new validation gate at all -- exactly like
// TestExecuteSpawn_StaleEpochOnRecord_PropagatesErrStaleEpoch's own
// precedent of calling an unexported method directly to reach an otherwise
// unreachable-via-the-mailbox code path, tryPlanSpawn is called directly
// here instead.
func planSpawnDirect(
	ctx context.Context, a *Actor,
	sessionRow sqlcgen.Session, sandboxRow sqlcgen.Sandbox, hasSandbox bool, now time.Time,
) (*spawnPlan, error) {
	var plan *spawnPlan
	err := a.transact(ctx, func(ctx context.Context, tx pgx.Tx) error {
		sp, err := a.tryPlanSpawn(ctx, tx, sessionRow, sandboxRow, hasSandbox, now)
		plan = sp
		return err
	})
	return plan, err
}

// TestTryPlanSpawn_FromPending_NoSandboxRow_UsesSpawnTrigger proves the
// ordinary "no sandbox row yet" spawn path is unchanged by this fix: the
// FROM state is StatePending, sandbox.SpawnTrigger is the trigger actually
// used, sandbox.Transition accepts it, and the persisted write is
// byte-identical to before this batch (gen 1, status spawning) -- querying
// Postgres directly, not just trusting the returned plan.
func TestTryPlanSpawn_FromPending_NoSandboxRow_UsesSpawnTrigger(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-object-1"}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sessionRow, err := narvipg.NewSessionStore(pool).Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}

	plan, err := planSpawnDirect(ctx, a, sessionRow, sqlcgen.Sandbox{}, false, time.Now())
	if err != nil {
		t.Fatalf("tryPlanSpawn (no sandbox row): unexpected error %v", err)
	}
	if plan == nil {
		t.Fatal("tryPlanSpawn returned no spawn plan, want one")
	}
	if plan.gen != 1 {
		t.Errorf("spawnPlan.gen = %d, want 1", plan.gen)
	}

	row, err := narvipg.NewSandboxStore(pool).Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Status != sqlcgen.SandboxStatusSpawning {
		t.Errorf("sandbox status = %s, want %s (TriggerSpawn, pending->spawning)", row.Status, sqlcgen.SandboxStatusSpawning)
	}
	if row.Gen != 1 {
		t.Errorf("sandbox gen = %d, want 1", row.Gen)
	}
}

// TestTryPlanSpawn_FromDeadStatus_UsesSpawnTrigger proves a sandbox already
// Stopped or Failed still respawns via sandbox.SpawnTrigger (not the new
// ForceRespawnTrigger) exactly as before this batch: gen bumped by exactly
// 1, status written to spawning.
//
// Stale is deliberately not exercised here: migrations/000006_sandboxes.up.
// sql's own sandbox_status Postgres enum does not define a "stale" value
// (that classification is domain-only until a later Step's own migration
// adds it) -- StateStale's legal SpawnTrigger/illegal ForceRespawnTrigger
// edges are proven instead at the pure domain level (state_test.go's
// TestTransition_LegalEdges "stale -> spawning (respawn)" and
// TestTransition_GenFencing "stale + spawn").
func TestTryPlanSpawn_FromDeadStatus_UsesSpawnTrigger(t *testing.T) {
	tests := []struct {
		name   string
		status sqlcgen.SandboxStatus
	}{
		{"stopped", sqlcgen.SandboxStatusStopped},
		{"failed", sqlcgen.SandboxStatusFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)
			sessionID := createTestSession(ctx, t, pool)

			turnStore := narvipg.NewTurnStore(pool)
			createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

			sandboxStore := narvipg.NewSandboxStore(pool)
			if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
				t.Fatalf("create sandbox: %v", err)
			}
			if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
				SessionID: sessionID, Status: tc.status,
			}); err != nil {
				t.Fatalf("move sandbox to %s: %v", tc.status, err)
			}

			provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-object-1"}}
			r := newDispatchTestRegistry(ctx, pool, provider, nil)
			t.Cleanup(func() { _ = r.Shutdown() })

			a, err := r.GetOrSpawn(ctx, sessionID)
			if err != nil {
				t.Fatalf("GetOrSpawn: %v", err)
			}

			sessionRow, err := narvipg.NewSessionStore(pool).Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			sandboxRow, err := sandboxStore.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get sandbox: %v", err)
			}
			if sandboxRow.Gen != 1 {
				t.Fatalf("precondition: sandbox gen = %d, want 1", sandboxRow.Gen)
			}

			plan, err := planSpawnDirect(ctx, a, sessionRow, sandboxRow, true, time.Now())
			if err != nil {
				t.Fatalf("tryPlanSpawn (from %s): unexpected error %v", tc.status, err)
			}
			if plan == nil {
				t.Fatal("tryPlanSpawn returned no spawn plan, want one")
			}
			if plan.gen != 2 {
				t.Errorf("spawnPlan.gen = %d, want 2 (bumped from 1)", plan.gen)
			}

			row, err := sandboxStore.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get sandbox: %v", err)
			}
			if row.Status != sqlcgen.SandboxStatusSpawning {
				t.Errorf("sandbox status = %s, want %s (TriggerSpawn respawn)", row.Status, sqlcgen.SandboxStatusSpawning)
			}
			if row.Gen != 2 {
				t.Errorf("sandbox gen = %d, want 2", row.Gen)
			}
		})
	}
}

// TestTryPlanSpawn_StuckLiveState_PastRecoveryWindow_UsesForceRespawnTrigger
// proves this fix's central new case: a sandbox stuck Spawning/Connecting/
// Booting past platform.Timeouts.SpawnStuckTimeout -- EvaluateSpawnDecision's
// own documented "a spawn interrupted before the sandbox connects... can pin
// the status ... forever" recovery carve-out -- resolves via the write
// actually succeeding (gen bumped, status spawning), proven by querying
// Postgres directly rather than trusting the returned plan alone.
//
// The write succeeding at all is itself the proof that ForceRespawnTrigger
// (not TriggerSpawn) was the trigger genuinely used: state.go's own
// transition table has NO TriggerSpawn edge from Spawning/Connecting/
// Booting at all (state_test.go's TestTransition_IllegalFromTriggerCombos
// "spawning cannot spawn again" proves TriggerSpawn is illegal from
// Spawning) -- TriggerForceRespawn is the ONLY legal edge from any of these
// three states, so tryPlanSpawn's own sandbox.Transition call could only
// have succeeded here via it.
func TestTryPlanSpawn_StuckLiveState_PastRecoveryWindow_UsesForceRespawnTrigger(t *testing.T) {
	tests := []struct {
		name   string
		status sqlcgen.SandboxStatus
	}{
		{"spawning", sqlcgen.SandboxStatusSpawning},
		{"connecting", sqlcgen.SandboxStatusConnecting},
		{"booting", sqlcgen.SandboxStatusBooting},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)
			sessionID := createTestSession(ctx, t, pool)

			turnStore := narvipg.NewTurnStore(pool)
			createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

			sandboxStore := narvipg.NewSandboxStore(pool)
			if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
				t.Fatalf("create sandbox: %v", err)
			}
			if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
				SessionID: sessionID, Status: tc.status,
			}); err != nil {
				t.Fatalf("move sandbox to %s: %v", tc.status, err)
			}

			// Seed created_at/last_seen_at far enough in the past to clear
			// SpawnStuckTimeout (120s by default) -- EvaluateSpawnDecision's
			// own guard measures from max(CreatedAt, LastSeenAt), so both
			// must move for a "genuinely stuck, no sign of life" sandbox.
			stuckSince := time.Now().Add(-2 * platform.DefaultTimeouts().SpawnStuckTimeout)
			if _, err := pool.Exec(ctx,
				`UPDATE sandboxes SET created_at = $2, last_seen_at = $2 WHERE session_id = $1`,
				sessionID, stuckSince,
			); err != nil {
				t.Fatalf("seed stuck timestamps: %v", err)
			}

			provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-object-1"}}
			r := newDispatchTestRegistry(ctx, pool, provider, nil)
			t.Cleanup(func() { _ = r.Shutdown() })

			a, err := r.GetOrSpawn(ctx, sessionID)
			if err != nil {
				t.Fatalf("GetOrSpawn: %v", err)
			}

			sessionRow, err := narvipg.NewSessionStore(pool).Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get session: %v", err)
			}
			sandboxRow, err := sandboxStore.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get sandbox: %v", err)
			}

			// planDispatch itself would never route this status to
			// tryPlanSpawn at all (see planSpawnDirect's own doc comment) --
			// called directly to exercise EvaluateSpawnDecision's documented
			// recovery carve-out and this fix's validation gate around it.
			plan, err := planSpawnDirect(ctx, a, sessionRow, sandboxRow, true, time.Now())
			if err != nil {
				t.Fatalf("tryPlanSpawn (stuck %s): unexpected error %v", tc.status, err)
			}
			if plan == nil {
				t.Fatal("tryPlanSpawn returned no spawn plan, want one (EvaluateSpawnDecision should return Spawn for a stuck spawn/connect past SpawningTimeout)")
			}
			if plan.gen != 2 {
				t.Errorf("spawnPlan.gen = %d, want 2 (bumped from 1)", plan.gen)
			}

			row, err := sandboxStore.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get sandbox: %v", err)
			}
			if row.Status != sqlcgen.SandboxStatusSpawning {
				t.Errorf("sandbox status = %s, want %s (force-respawn)", row.Status, sqlcgen.SandboxStatusSpawning)
			}
			if row.Gen != 2 {
				t.Errorf("sandbox gen = %d, want 2", row.Gen)
			}
		})
	}
}

// TestTryPlanSpawn_ReadyPastReadyWait_UsesForceRespawnTrigger proves this
// fix's other new case: a Ready sandbox whose WebSocket never reconnected,
// past platform.Timeouts.SpawnReadyWait -- EvaluateSpawnDecision's own
// documented "no WebSocket ... last spawn was Nds ago" -> eventually Spawn
// carve-out -- also resolves via a genuinely successful write (gen bumped,
// status spawning), for the same "success itself proves ForceRespawnTrigger
// was used" reason as the stuck-live-state test above (Ready has no legal
// TriggerSpawn edge either).
func TestTryPlanSpawn_ReadyPastReadyWait_UsesForceRespawnTrigger(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusReady,
	}); err != nil {
		t.Fatalf("move sandbox to ready: %v", err)
	}

	readySince := time.Now().Add(-2 * platform.DefaultTimeouts().SpawnReadyWait)
	if _, err := pool.Exec(ctx,
		`UPDATE sandboxes SET created_at = $2 WHERE session_id = $1`,
		sessionID, readySince,
	); err != nil {
		t.Fatalf("seed ready-since timestamp: %v", err)
	}

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-object-1"}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sessionRow, err := narvipg.NewSessionStore(pool).Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	sandboxRow, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}

	// planDispatch itself would ALWAYS route a Ready sandbox to
	// tryPlanDispatch (branch (b)), regardless of whether a WebSocket is
	// actually connected -- see planSpawnDirect's own doc comment. Called
	// directly here for the same reason as the stuck-live-state test above.
	plan, err := planSpawnDirect(ctx, a, sessionRow, sandboxRow, true, time.Now())
	if err != nil {
		t.Fatalf("tryPlanSpawn (ready past ReadyWait): unexpected error %v", err)
	}
	if plan == nil {
		t.Fatal("tryPlanSpawn returned no spawn plan, want one (EvaluateSpawnDecision should return Spawn for Ready past ReadyWait with no WebSocket)")
	}
	if plan.gen != 2 {
		t.Errorf("spawnPlan.gen = %d, want 2 (bumped from 1)", plan.gen)
	}

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Status != sqlcgen.SandboxStatusSpawning {
		t.Errorf("sandbox status = %s, want %s (force-respawn)", row.Status, sqlcgen.SandboxStatusSpawning)
	}
	if row.Gen != 2 {
		t.Errorf("sandbox gen = %d, want 2", row.Gen)
	}
}

// TestTryPlanSpawn_Suspect_NeverReachesTransitionGate proves the validation
// gate is a real, load-bearing check and not a no-op that always happens to
// succeed: EvaluateSpawnDecision's own Suspect branch always returns Skip
// (no staleness carve-out, unlike Spawning/Connecting/Booting/Ready above),
// so tryPlanSpawn returns (nil, nil) -- action.Kind != SpawnActionSpawn --
// before its own trigger-selection switch/sandbox.Transition call is ever
// reached at all, and the sandbox row is left completely untouched. This is
// exactly why no TriggerForceRespawn case exists for Suspect (see
// tryPlanSpawn's own default case, which would error loudly instead of
// silently writing if this assumption were ever violated by a future
// change) -- also proven illegal at the pure domain level directly
// (state_test.go's "suspect cannot force-respawn"/"suspect cannot spawn
// directly").
func TestTryPlanSpawn_Suspect_NeverReachesTransitionGate(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusSuspect,
	}); err != nil {
		t.Fatalf("move sandbox to suspect: %v", err)
	}

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-object-1"}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sessionRow, err := narvipg.NewSessionStore(pool).Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	sandboxRow, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}

	plan, err := planSpawnDirect(ctx, a, sessionRow, sandboxRow, true, time.Now())
	if err != nil {
		t.Fatalf("tryPlanSpawn (suspect): unexpected error %v (want nil, nil -- EvaluateSpawnDecision should Skip)", err)
	}
	if plan != nil {
		t.Fatal("tryPlanSpawn (suspect) returned a spawn plan, want nil (EvaluateSpawnDecision must Skip Suspect, never reaching the Transition gate)")
	}

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Status != sqlcgen.SandboxStatusSuspect {
		t.Errorf("sandbox status = %s, want unchanged %s", row.Status, sqlcgen.SandboxStatusSuspect)
	}
	if row.Gen != 1 {
		t.Errorf("sandbox gen = %d, want unchanged 1", row.Gen)
	}
}

// TestHandleEnsureDispatched_StuckSpawning_ForceRespawnsThroughRealPath
// proves this batch's own fix to planDispatch's branch (c) actually closes
// the gap two independent adversarial-review lenses proved: driving the
// REAL production entry point (a.Send(ctx, EnsureDispatched{}) ->
// handleEnsureDispatched -> planDispatch), never planSpawnDirect's own
// direct-call bypass, against a sandbox seeded Spawning/Connecting/Booting
// with created_at/last_seen_at well past platform.Timeouts.SpawnStuckTimeout
// and a pending turn now genuinely force-respawns it. Before this fix,
// branch (c) was an unconditional no-op and tryPlanSpawn/
// EvaluateSpawnDecision/TriggerForceRespawn were never reached from here at
// all -- this test mirrors exactly the scenario both review lenses used to
// prove the bug, but now proves the fix.
//
// The fake provider is configured to return a TRANSIENT ports.ProviderError
// deliberately: recordSpawnFailure's own transient branch leaves the
// sandbox exactly as tryPlanSpawn's own write already committed it (gen
// bumped, status spawning), with no further transition -- giving this test
// a durable, race-free postcondition to assert on directly via Postgres,
// rather than racing the real CreateSandbox call's own success path
// (Spawning->Connecting) which would otherwise need to be observed at
// exactly the right moment. provider.callCount() == 1 still proves the real
// network call genuinely happened, not just the state write.
func TestHandleEnsureDispatched_StuckSpawning_ForceRespawnsThroughRealPath(t *testing.T) {
	tests := []struct {
		name   string
		status sqlcgen.SandboxStatus
	}{
		{"spawning", sqlcgen.SandboxStatusSpawning},
		{"connecting", sqlcgen.SandboxStatusConnecting},
		{"booting", sqlcgen.SandboxStatusBooting},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)
			sessionID := createTestSession(ctx, t, pool)

			turnStore := narvipg.NewTurnStore(pool)
			createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

			sandboxStore := narvipg.NewSandboxStore(pool)
			if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
				t.Fatalf("create sandbox: %v", err)
			}
			if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
				SessionID: sessionID, Status: tc.status,
			}); err != nil {
				t.Fatalf("move sandbox to %s: %v", tc.status, err)
			}

			// Seed created_at/last_seen_at far enough in the past to clear
			// SpawnStuckTimeout (120s by default) -- a genuinely stuck spawn
			// with no sign of life, exactly like the scenario both review
			// lenses used to prove branch (c) never invoked tryPlanSpawn at
			// all.
			stuckSince := time.Now().Add(-2 * platform.DefaultTimeouts().SpawnStuckTimeout)
			if _, err := pool.Exec(ctx,
				`UPDATE sandboxes SET created_at = $2, last_seen_at = $2 WHERE session_id = $1`,
				sessionID, stuckSince,
			); err != nil {
				t.Fatalf("seed stuck timestamps: %v", err)
			}

			provider := &fakeSpawnProvider{nextErr: &ports.ProviderError{
				Transient: true, Code: "http_503", Op: ports.OpCreateSandbox, Err: errors.New("temporarily unavailable"),
			}}
			r := newDispatchTestRegistry(ctx, pool, provider, nil)
			t.Cleanup(func() { _ = r.Shutdown() })

			a, err := r.GetOrSpawn(ctx, sessionID)
			if err != nil {
				t.Fatalf("GetOrSpawn: %v", err)
			}

			// The real production entry point -- NOT planSpawnDirect's own
			// bypass -- so this proves planDispatch's branch (c) itself
			// (not just tryPlanSpawn in isolation) now reaches
			// EvaluateSpawnDecision/TriggerForceRespawn for a stuck
			// Spawning/Connecting/Booting sandbox.
			sendEnsureDispatched(ctx, t, a)

			waitUntil(t, 5*time.Second, func() bool {
				return provider.callCount() == 1
			})
			// Give executeSpawn's own second transact (recording the
			// transient failure) a moment to land.
			time.Sleep(300 * time.Millisecond)

			row, err := sandboxStore.Get(ctx, sessionID)
			if err != nil {
				t.Fatalf("get sandbox: %v", err)
			}
			if row.Gen != 2 {
				t.Errorf("sandbox gen = %d, want 2 (a genuine force-respawn bumped it from 1 -- branch (c) reached tryPlanSpawn/TriggerForceRespawn for real)", row.Gen)
			}
			if row.Status != sqlcgen.SandboxStatusSpawning {
				t.Errorf("sandbox status = %s, want %s (force-respawn writes spawning; the transient CreateSandbox failure leaves it there for a later retry)", row.Status, sqlcgen.SandboxStatusSpawning)
			}
		})
	}
}

// TestHandleEnsureDispatched_HealthySpawning_WithinStuckTimeout_NoChange
// proves the OTHER half of this fix's own safety requirement: a sandbox
// that is merely a few seconds into Spawning (well within
// platform.Timeouts.SpawnStuckTimeout, i.e. a perfectly healthy in-progress
// boot) driven through the SAME real production entry point
// (a.Send(ctx, EnsureDispatched{})) produces NO change at all --
// EvaluateSpawnDecision's own existing Skip guard ("already spawning")
// still correctly protects it, exactly as it already did for branch (a) 's
// own cooldown case before this fix. No CreateSandbox call, no gen bump, no
// status change.
func TestHandleEnsureDispatched_HealthySpawning_WithinStuckTimeout_NoChange(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := narvipg.NewSandboxStore(pool)
	created, err := sandboxStore.Create(ctx, sessionID)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusSpawning,
	}); err != nil {
		t.Fatalf("move sandbox to spawning: %v", err)
	}
	// created_at/last_seen_at are left at their real "just now" values from
	// Create above -- a genuinely healthy, just-started boot, nowhere near
	// SpawnStuckTimeout (120s by default).

	provider := &fakeSpawnProvider{nextRef: ports.SandboxRef{ProviderID: "provider-object-should-never-be-used"}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	// Mirrors TestHandleEnsureDispatched_CircuitBreakerOpen_DoesNotSpawn's
	// own pattern: there is no positive DB signal to poll FOR when
	// asserting something did NOT happen, so give the mailbox a moment to
	// process, then assert nothing changed.
	time.Sleep(300 * time.Millisecond)

	if got := provider.callCount(); got != 0 {
		t.Errorf("provider.CreateSandbox called %d times, want 0 (a healthy in-progress boot within SpawnStuckTimeout must not be disturbed)", got)
	}

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Status != sqlcgen.SandboxStatusSpawning {
		t.Errorf("sandbox status = %s, want unchanged %s", row.Status, sqlcgen.SandboxStatusSpawning)
	}
	if row.Gen != created.Gen {
		t.Errorf("sandbox gen = %d, want unchanged %d", row.Gen, created.Gen)
	}
}

// --- Step 23 ("resume"), §3.2/§8.7: executeResume's own decision-tree
// coverage, mirroring this file's own existing spawn/restore-path tests
// exactly (same rig helpers, same fakeSpawnProvider, same
// waitUntil/fixed-sleep conventions) for the Resume path
// EvaluateSpawnDecision's own "resume takes priority over restore" branch
// (already built, dispatch.go's own new caller) makes reachable for the
// first time.
//
// Stale is deliberately not exercised here either, for the exact same
// reason TestTryPlanSpawn_FromDeadStatus_UsesSpawnTrigger's own comment
// already gives for the parallel spawn-trigger tests: migrations/
// 000006_sandboxes.up.sql's own sandbox_status Postgres enum still has no
// 'stale' value at the Postgres level (confirmed by re-reading that
// migration file before writing these tests), so every scenario below is
// scoped to Stopped only -- StateStale's own legal TriggerResume edge is
// proven instead at the pure domain level (state_test.go).

// seedStoppedSandboxWithProviderID creates a sandbox row, moves it to
// Stopped, and records providerID on it (via UpdateProviderID, the SAME
// write recordProviderOutcome's own success path already uses) -- the
// (status, provider_id) precondition EvaluateSpawnDecision's Resume branch
// requires (Stopped/Stale + ProviderObjectID != ""). Returns the store so
// callers can re-Get afterward -- mirrors seedStoppedSandboxWithSnapshot's
// own shape exactly, just for the other eligibility column.
func seedStoppedSandboxWithProviderID(ctx context.Context, t *testing.T, pool *pgxpool.Pool, sessionID pgtype.UUID, providerID string) *narvipg.SandboxStore {
	t.Helper()
	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID, Status: sqlcgen.SandboxStatusStopped,
	}); err != nil {
		t.Fatalf("move sandbox to stopped: %v", err)
	}
	if _, err := sandboxStore.UpdateProviderID(ctx, sqlcgen.UpdateSandboxProviderIDParams{
		SessionID: sessionID, ProviderID: &providerID,
	}); err != nil {
		t.Fatalf("seed provider_id: %v", err)
	}
	return sandboxStore
}

// TestHandleEnsureDispatched_StoppedWithProviderID_ResumeCapable_Resumes
// proves the full real Resume path: a Stopped sandbox carrying a real
// provider_id (no snapshot needed) + a resume-capable fake provider + a
// pending turn -> EvaluateSpawnDecision's Resume branch fires -> a real
// ResumeSandbox call (never CreateSandbox, never RestoreFromSnapshot) with
// the sandbox's own existing ProviderObjectID -- gen bumps by exactly 1,
// and status ends up Connecting.
//
// Following this fix, the sandbox row IS genuinely, durably observable at
// Spawning in between (planResume's own interim-claim transact commits
// BEFORE ResumeSandbox is ever called -- this file's own top comment and
// planResume/executeResume's own doc comments explain why: that write is
// what closes the concurrency bug an adversarial review found). This
// particular test does not assert on that interim state directly (the
// fake provider here returns immediately, so the window is too narrow to
// observe deterministically without flaking) --
// TestResilience_ConcurrentResumeAcrossActors_ResumeSandboxCalledAtMostOnce
// (this file, below) is the test that holds ResumeSandbox deliberately
// blocked specifically to observe and exploit that interim state. TokenHash
// is also proven to go from NULL to non-NULL (a fresh token minted per
// §5.2 -- sandbox tokens are hashed at rest, one per gen, paraphrased not
// verbatim), and TimerConnectingDeadline ends up armed exactly once.
func TestHandleEnsureDispatched_StoppedWithProviderID_ResumeCapable_Resumes(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := seedStoppedSandboxWithProviderID(ctx, t, pool, sessionID, "resume-provider-object-1")

	provider := &fakeSpawnProvider{resumeSupported: true}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool {
		return provider.resumeCallCount() == 1
	})

	if got := provider.callCount(); got != 0 {
		t.Errorf("provider.CreateSandbox called %d times, want 0 (resume must call ResumeSandbox, never CreateSandbox)", got)
	}
	if got := provider.restoreCallCount(); got != 0 {
		t.Errorf("provider.RestoreFromSnapshot called %d times, want 0 (resume must call ResumeSandbox, never RestoreFromSnapshot)", got)
	}

	lastCall := provider.lastResumeCall()
	if lastCall.ProviderID != "resume-provider-object-1" {
		t.Errorf("ResumeSandbox ref.ProviderID = %q, want %q", lastCall.ProviderID, "resume-provider-object-1")
	}

	waitUntil(t, 5*time.Second, func() bool {
		row, err := sandboxStore.Get(ctx, sessionID)
		return err == nil && row.Status == sqlcgen.SandboxStatusConnecting
	})

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Status != sqlcgen.SandboxStatusConnecting {
		t.Errorf("sandbox status = %s, want %s (TriggerResumeAck lands the resume on connecting once ResumeSandbox acks)", row.Status, sqlcgen.SandboxStatusConnecting)
	}
	if row.Gen != 2 {
		t.Errorf("sandbox gen = %d, want 2 (bumped from the Stopped row's own gen 1)", row.Gen)
	}
	if row.ProviderID == nil || *row.ProviderID != "resume-provider-object-1" {
		t.Errorf("sandbox provider_id = %v, want unchanged %q (resume reuses the SAME provider object, never mints a new one)", row.ProviderID, "resume-provider-object-1")
	}
	if row.TokenHash == nil {
		t.Error("sandbox token_hash is nil, want set (a fresh token minted per §5.2 -- sandbox tokens are hashed at rest, one per gen)")
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_timers WHERE session_id = $1 AND name = $2`,
		sessionID, TimerConnectingDeadline,
	).Scan(&n); err != nil {
		t.Fatalf("count connecting_deadline timers: %v", err)
	}
	if n != 1 {
		t.Errorf("connecting_deadline timer count = %d, want 1", n)
	}
}

// TestHandleEnsureDispatched_ResumeAndRestoreBothEligible_ResumeWins proves
// spawndecision.go's own already-documented "resume takes priority over
// restore" priority ordering (EvaluateSpawnDecision's own comment: "reuses
// the SAME provider sandbox, so it's cheaper and preserves more state than
// a snapshot restore") for the FIRST time at the application-integration
// level, not just the pure domain level: a Stopped sandbox carrying BOTH a
// real provider_id (resume-eligible) AND a snapshot_id (restore-eligible),
// with a provider reporting BOTH capabilities, resumes -- RestoreFromSnapshot
// is never called.
func TestHandleEnsureDispatched_ResumeAndRestoreBothEligible_ResumeWins(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := seedStoppedSandboxWithSnapshot(ctx, t, pool, sessionID, "snap-and-resume-1")
	providerID := "resume-provider-object-2"
	if _, err := sandboxStore.UpdateProviderID(ctx, sqlcgen.UpdateSandboxProviderIDParams{
		SessionID: sessionID, ProviderID: &providerID,
	}); err != nil {
		t.Fatalf("seed provider_id: %v", err)
	}

	// Snapshots: true (via the embedded default) AND Resume: true -- both
	// capabilities the provider could act on; EvaluateSpawnDecision's own
	// priority ordering must pick Resume.
	provider := &fakeSpawnProvider{resumeSupported: true}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool {
		return provider.resumeCallCount() == 1
	})
	// Give a moment for anything else (e.g. an errant RestoreFromSnapshot
	// call) to have happened too, before asserting it did not.
	time.Sleep(300 * time.Millisecond)

	if got := provider.restoreCallCount(); got != 0 {
		t.Errorf("provider.RestoreFromSnapshot called %d times, want 0 (resume must win over restore when both are eligible)", got)
	}
	if got := provider.callCount(); got != 0 {
		t.Errorf("provider.CreateSandbox called %d times, want 0", got)
	}

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.SnapshotID == nil || *row.SnapshotID != "snap-and-resume-1" {
		t.Errorf("sandbox snapshot_id = %v, want unchanged %q (resume does not touch it)", row.SnapshotID, "snap-and-resume-1")
	}
}

// TestExecuteResume_PermanentProviderError_IncrementsCircuitBreakerAndSuspects
// proves resume's own permanent-failure behavior now matches spawn/
// restore's IDENTICALLY -- the direct consequence of this fix's own
// two-step shape: planResume's own interim Spawning write (gen bumped,
// status spawning) has ALREADY committed by the time ResumeSandbox is
// ever called, so a permanent failure has exactly the same live state to
// transition OUT of a permanent spawn/restore failure already does.
// recordResumeOutcome reuses recordSpawnFailure UNCHANGED (dispatch.go's
// own doc comment on recordResumeOutcome explains why this reuse is now
// correct, not merely convenient) -- so this test asserts EXACTLY what
// TestHandleEnsureDispatched_PermanentProviderError_IncrementsCircuitBreaker
// (the plain-spawn precedent for this same shape) already asserts: the
// circuit breaker increments, and the sandbox moves to Suspect (never
// straight to Failed -- §3.2's "a watchdog never writes failed directly"
// rule) via transitionSandboxToSuspect, with connecting_deadline deleted
// in favor of terminal_grace.
//
// This SUPERSEDES this test's own OLD assertion (before this fix) that
// gen/status were left completely UNCHANGED on a permanent resume
// failure -- that was only true because the OLD one-step TriggerResume
// wrote nothing before calling the provider; this fix's whole point is
// that it now writes the SAME interim claim spawn/restore already do, so
// a permanent failure now shows the SAME gen bump and Suspect transition
// theirs already does.
func TestExecuteResume_PermanentProviderError_IncrementsCircuitBreakerAndSuspects(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := seedStoppedSandboxWithProviderID(ctx, t, pool, sessionID, "resume-provider-object-3")

	provider := &fakeSpawnProvider{
		resumeSupported: true,
		nextResumeErr: &ports.ProviderError{
			Transient: false, Code: "RESUME_UNSUPPORTED_STATE", Op: ports.OpResumeSandbox, Err: errors.New("boom"),
		},
	}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool {
		row, err := sandboxStore.Get(ctx, sessionID)
		return err == nil && row.Status == sqlcgen.SandboxStatusSuspect
	})

	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.SpawnFailureCount != 1 {
		t.Errorf("spawn_failure_count = %d, want 1 (the SAME circuit-breaker columns a permanent spawn/restore failure uses)", row.SpawnFailureCount)
	}
	if !row.LastSpawnFailureAt.Valid {
		t.Error("last_spawn_failure_at not set")
	}
	if row.Status != sqlcgen.SandboxStatusSuspect {
		t.Errorf("sandbox status = %s, want %s (permanent resume failure now behaves identically to a permanent spawn/restore failure: Spawning -> Suspect via transitionSandboxToSuspect, never straight to Failed)",
			row.Status, sqlcgen.SandboxStatusSuspect)
	}
	if row.Gen != 2 {
		t.Errorf("sandbox gen = %d, want 2 (planResume's own interim claim already bumped it BEFORE the failed ResumeSandbox call -- this fix's whole point, unlike the OLD one-step shape's gen==1)", row.Gen)
	}
	if row.ProviderID == nil || *row.ProviderID != "resume-provider-object-3" {
		t.Errorf("sandbox provider_id = %v, want unchanged %q (a permanent resume failure must not touch it)", row.ProviderID, "resume-provider-object-3")
	}
	if got := provider.resumeCallCount(); got != 1 {
		t.Errorf("provider.ResumeSandbox called %d times, want 1", got)
	}

	var timerCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_timers WHERE session_id = $1 AND name = $2`,
		sessionID, TimerConnectingDeadline,
	).Scan(&timerCount); err != nil {
		t.Fatalf("count connecting_deadline timers: %v", err)
	}
	if timerCount != 0 {
		t.Errorf("connecting_deadline timer count = %d, want 0 (recordSpawnFailure deletes it in favor of terminal_grace, exactly like a permanent spawn/restore failure)", timerCount)
	}
}

// TestHandleEnsureDispatched_ResumeCircuitBreakerOpen_DoesNotResume mirrors
// TestHandleEnsureDispatched_RestoreCircuitBreakerOpen_DoesNotRestore
// exactly (same seeded-3-prior-failures-then-a-4th-is-blocked precedent),
// but on a Stopped+provider_id (resume-eligible) sandbox -- proving the
// circuit breaker gate in tryPlanSpawn runs BEFORE EvaluateSpawnDecision is
// ever consulted, so it blocks a would-be Resume exactly as completely as
// it blocks a would-be Spawn/Restore: ResumeSandbox is never called, and
// the sandbox row (status, gen, provider_id, circuit breaker columns) is
// left completely untouched by this 4th attempt.
func TestHandleEnsureDispatched_ResumeCircuitBreakerOpen_DoesNotResume(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := seedStoppedSandboxWithProviderID(ctx, t, pool, sessionID, "resume-provider-object-4")
	if _, err := sandboxStore.UpdateCircuitBreaker(ctx, sqlcgen.UpdateSandboxCircuitBreakerParams{
		SessionID:          sessionID,
		SpawnFailureCount:  3,
		LastSpawnFailureAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("seed circuit breaker: %v", err)
	}

	provider := &fakeSpawnProvider{resumeSupported: true}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	// Same fixed-sleep-then-assert-nothing-happened shape this file's own
	// circuit-breaker tests already use -- there is no positive DB signal
	// to poll FOR when asserting something did NOT happen.
	time.Sleep(300 * time.Millisecond)

	if got := provider.resumeCallCount(); got != 0 {
		t.Errorf("provider.ResumeSandbox called %d times, want 0 (circuit breaker should have blocked the 4th attempt)", got)
	}
	row, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if row.Status != sqlcgen.SandboxStatusStopped {
		t.Errorf("sandbox status = %s, want unchanged %s", row.Status, sqlcgen.SandboxStatusStopped)
	}
	if row.Gen != 1 {
		t.Errorf("sandbox gen = %d, want unchanged 1 (no resume attempt should have run at all)", row.Gen)
	}
	if row.SpawnFailureCount != 3 {
		t.Errorf("spawn_failure_count = %d, want unchanged 3", row.SpawnFailureCount)
	}
}

// TestHandleEnsureDispatched_ProviderWithoutResumeCapability_FallsBackToRestore
// proves EvaluateSpawnDecision's own supportsPersistentResume gate is
// correctly wired end to end from a real provider's Capabilities() through
// to the real write path (not just trusted at the pure domain-unit-test
// level): a provider that does NOT support resume (Capabilities().Resume
// == false, matching Modal's own real, permanent, documented choice --
// internal/adapters/outbound/modal's own doc.go) faced with a Stopped
// sandbox carrying BOTH a provider_id and a snapshot_id chooses Restore,
// never Resume -- ResumeSandbox is never called.
func TestHandleEnsureDispatched_ProviderWithoutResumeCapability_FallsBackToRestore(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	turnStore := narvipg.NewTurnStore(pool)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := seedStoppedSandboxWithSnapshot(ctx, t, pool, sessionID, "snap-no-resume-1")
	providerID := "provider-object-no-resume"
	if _, err := sandboxStore.UpdateProviderID(ctx, sqlcgen.UpdateSandboxProviderIDParams{
		SessionID: sessionID, ProviderID: &providerID,
	}); err != nil {
		t.Fatalf("seed provider_id: %v", err)
	}

	// resumeSupported defaults to false -- the SAME default fakeSpawnProvider
	// already had before this Step (Capabilities().Resume == false),
	// mirroring Modal's own real, permanent design choice.
	provider := &fakeSpawnProvider{nextRestoreRef: ports.SandboxRef{ProviderID: "restored-provider-object-no-resume"}}
	r := newDispatchTestRegistry(ctx, pool, provider, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}
	sendEnsureDispatched(ctx, t, a)

	waitUntil(t, 5*time.Second, func() bool {
		return provider.restoreCallCount() == 1
	})
	time.Sleep(300 * time.Millisecond)

	if got := provider.resumeCallCount(); got != 0 {
		t.Errorf("provider.ResumeSandbox called %d times, want 0 (a provider without Resume capability must never have it called)", got)
	}

	call := provider.lastRestoreCall()
	if call.snapshotID != "snap-no-resume-1" {
		t.Errorf("RestoreFromSnapshot snapshotID = %q, want %q", call.snapshotID, "snap-no-resume-1")
	}
}

// TestResilience_ConcurrentResumeAcrossActors_ResumeSandboxCalledAtMostOnce
// is a genuine, PERMANENT regression test for the concurrency bug an
// adversarial review found and empirically reproduced in the OLD
// one-step TriggerResume shape (see dispatch.go's own top comment, and
// state.go's own doc comments on TriggerResume/TriggerResumeAck, for the
// full account): the review killed actor A's advisory-lock connection
// mid-resume-call, hydrated actor B for the SAME session, and got a
// SECOND ResumeSandbox call for the identical provider object while actor
// A's own first call was still blocked -- because nothing marked the row
// as "a resume attempt is already in flight" the way Spawning already
// does for spawn/restore.
//
// This test reproduces the EXACT SAME shape, reusing resilience_killpod_
// integration_test.go's own newTestPoolPair/killAdvisoryLockHolder
// harness (two independent pools against one shared Postgres, "kill pod
// A" by terminating the real backend holding its advisory lock, never
// pool.Close() -- see that file's own doc comments for why each of those
// choices is deliberate), and proves the fix actually closes the race:
// once actor A's own interim Spawning write (planResume) has genuinely
// committed -- proven by waiting for the fake provider's own
// ResumeSandbox call to have STARTED, which cannot happen before that
// commit, since executeResume is only ever invoked with an already-
// committed plan (dispatch.go's own top comment) -- actor B's OWN
// EnsureDispatched, driven through the REAL production entry point
// (a.Send, never a direct planDispatch/tryPlanSpawn bypass), reads that
// same row back as Spawning (not Stopped) and EvaluateSpawnDecision's own
// existing guard (spawndecision.go, untouched by this fix) no-ops instead
// of returning SpawnActionResume a second time -- so ResumeSandbox is
// called AT MOST ONCE across both actors for this scenario. Before this
// fix, this same sequence would have produced a SECOND ResumeSandbox
// call from actor B (EvaluateSpawnDecision reading the still-Stopped row,
// since the old TriggerResume wrote nothing before calling the
// provider) -- see this test's own final section (temporarily revert the
// fix to confirm) for how this was verified to actually fail without it.
//
// Sequencing:
//  1. Seed a Stopped, resume-eligible sandbox row (real provider_id, no
//     snapshot) + a pending turn, directly via the stores -- exactly like
//     every other resume test in this file.
//  2. Hydrate actor A on poolA (registryA.GetOrSpawn) -- a genuine owner
//     holding the real Postgres advisory lock on a real poolA connection,
//     exactly like the killpod test's own step 2.
//  3. Drive actor A's own EnsureDispatched through the real mailbox
//     (a.Send). Its fake provider's ResumeSandbox call is configured to
//     BLOCK (resumeBlock) once called, so this call: (a) is proven to
//     have started (resumeCallCount()==1, polled), which can only happen
//     AFTER planResume's own interim-claim transact has already committed
//     (dispatch.go's own sequencing), and (b) stays "in flight" for the
//     rest of this test, exactly like the review's own reproduction.
//  4. "Kill pod A": terminate the exact Postgres backend holding the
//     advisory lock (killAdvisoryLockHolder), WITHOUT calling
//     registryA.Shutdown() first -- see resilience_killpod_integration_
//     test.go's own extensive doc comment on why this, not Pool.Close(),
//     is the faithful simulation, and why Shutdown() first would test the
//     wrong (already-proven) path.
//  5. Hydrate actor B on a genuinely FRESH pool (poolB, registryB) for the
//     SAME session -- this only succeeds because step 4 released the
//     advisory lock; BumpActorEpoch (hydrateAndAcquire) fences out actor
//     A's own eventual outcome-recording transact, exactly like
//     TestExecuteSpawn_StaleEpochOnRecord_PropagatesErrStaleEpoch's own
//     precedent, though that fencing is incidental to what this test
//     actually proves (the READ-DECIDE race, not the write-fencing one).
//  6. Drive actor B's own EnsureDispatched through the SAME real mailbox
//     entry point. Give it a moment to run (there is no positive DB
//     signal to poll FOR when asserting something did NOT happen, exactly
//     like this file's own circuit-breaker tests), then assert:
//     ResumeSandbox was called EXACTLY ONCE total (actor A's own call,
//     never a second one from actor B), and the sandbox row's gen is
//     STILL exactly what actor A's own interim write left it at (actor B
//     made no write of its own at all).
//  7. Release actor A's own blocked ResumeSandbox call (close the
//     channel) so its goroutine does not leak past this test -- mirroring
//     this file's own general hygiene, even though (like the killpod
//     test) this test never calls registryA.Shutdown() itself.
func TestResilience_ConcurrentResumeAcrossActors_ResumeSandboxCalledAtMostOnce(t *testing.T) {
	ctx := context.Background()
	poolA, poolB := newTestPoolPair(t)

	sessionID := createTestSession(ctx, t, poolA)

	turnStore := narvipg.NewTurnStore(poolA)
	createPendingTurn(ctx, t, turnStore, sessionID, "do the thing")

	sandboxStore := seedStoppedSandboxWithProviderID(ctx, t, poolA, sessionID, "concurrent-resume-provider-object")

	// --- Step 2: pod A hydrates and genuinely owns this session. ---
	providerA := &fakeSpawnProvider{resumeSupported: true, resumeBlock: make(chan struct{})}
	registryA := NewRegistry(ctx, poolA, platform.DefaultTimeouts(), nil, nil, providerA, "http://localhost:8080", nil, nil)
	actorA, err := registryA.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("registryA.GetOrSpawn: %v", err)
	}

	// --- Step 3: actor A's own EnsureDispatched starts a real
	// ResumeSandbox call, deliberately held in flight. ---
	sendEnsureDispatched(ctx, t, actorA)
	waitUntil(t, 5*time.Second, func() bool {
		return providerA.resumeCallCount() == 1
	})

	// By the time ResumeSandbox has been observed to have started, actor
	// A's own interim Spawning claim (planResume) has ALREADY committed --
	// dispatch.go's own sequencing guarantees the transact commits before
	// executeResume is ever invoked. Confirm this durably, from Postgres,
	// via a completely independent connection (poolB) -- not merely
	// trusted as an implication.
	rowAfterClaim, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox after actor A's resume claim: %v", err)
	}
	if rowAfterClaim.Status != sqlcgen.SandboxStatusSpawning {
		t.Fatalf("sandbox status = %s, want %s (actor A's own interim resume claim must have committed before ResumeSandbox was ever called)",
			rowAfterClaim.Status, sqlcgen.SandboxStatusSpawning)
	}
	if rowAfterClaim.Gen != 2 {
		t.Fatalf("sandbox gen = %d, want 2 (actor A's own interim claim bumps it from the Stopped row's own gen 1)", rowAfterClaim.Gen)
	}

	// --- Step 4: kill pod A (see this test's own doc comment, and
	// resilience_killpod_integration_test.go's own, for why this exact
	// mechanism and not Pool.Close()). ---
	killAdvisoryLockHolder(ctx, t, poolB)

	// --- Step 5: pod B, a genuinely fresh pool, hydrates its own actor
	// for the SAME session. ---
	providerB := &fakeSpawnProvider{resumeSupported: true}
	registryB := NewRegistry(ctx, poolB, platform.DefaultTimeouts(), nil, nil, providerB, "http://localhost:8080", nil, nil)
	t.Cleanup(poolB.Close)
	t.Cleanup(func() { _ = registryB.Shutdown() })

	actorB, err := registryB.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("registryB.GetOrSpawn: %v", err)
	}

	// --- Step 6: actor B's own EnsureDispatched, through the real
	// production entry point -- must NOT call ResumeSandbox a second
	// time. ---
	sendEnsureDispatched(ctx, t, actorB)
	// No positive DB signal exists to poll FOR when asserting something
	// did NOT happen (mirrors this file's own circuit-breaker tests) --
	// give the mailbox a generous moment to process.
	time.Sleep(500 * time.Millisecond)

	if got := providerB.resumeCallCount(); got != 0 {
		t.Errorf("actor B's own provider.ResumeSandbox called %d times, want 0 (EvaluateSpawnDecision must read the row as Spawning, not Stopped, and no-op)", got)
	}
	totalResumeCalls := providerA.resumeCallCount() + providerB.resumeCallCount()
	if totalResumeCalls != 1 {
		t.Errorf("total ResumeSandbox calls across both actors = %d, want 1 (at most once for this scenario -- this is the exact concurrency bug this fix closes: before it, this would be 2)", totalResumeCalls)
	}

	rowAfterActorB, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox after actor B's own EnsureDispatched: %v", err)
	}
	if rowAfterActorB.Gen != 2 {
		t.Errorf("sandbox gen = %d, want unchanged 2 (actor B must have made no write of its own at all -- EvaluateSpawnDecision's own Skip guard fired before any write path was ever reached)",
			rowAfterActorB.Gen)
	}
	if rowAfterActorB.Status != sqlcgen.SandboxStatusSpawning {
		t.Errorf("sandbox status = %s, want unchanged %s (actor B must not have touched it)", rowAfterActorB.Status, sqlcgen.SandboxStatusSpawning)
	}

	// --- Step 7: release actor A's own blocked ResumeSandbox call so its
	// goroutine does not leak past this test (poolA/registryA are
	// otherwise deliberately never gracefully shut down, exactly like the
	// killpod test's own precedent -- a real killed pod leaves nothing to
	// gracefully close either). ---
	close(providerA.resumeBlock)
}
