//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// fakeSpawnProvider is a test-only ports.SandboxProvider recording every
// CreateSandbox call it receives and returning a caller-configured
// (ref, err) pair -- this package's own EnsureDispatched decision-tree
// tests never talk to a real cloud provider.
type fakeSpawnProvider struct {
	mu      sync.Mutex
	calls   []ports.CreateSpec
	nextRef ports.SandboxRef
	nextErr error
}

var _ ports.SandboxProvider = (*fakeSpawnProvider)(nil)

func (f *fakeSpawnProvider) Capabilities() ports.Capabilities {
	return ports.Capabilities{Snapshots: false, Resume: false, ExplicitStop: false, ImageBuilds: false}
}

func (f *fakeSpawnProvider) CreateSandbox(_ context.Context, spec ports.CreateSpec) (ports.SandboxRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, spec)
	return f.nextRef, f.nextErr
}

func (f *fakeSpawnProvider) StopSandbox(context.Context, ports.SandboxRef) error { return nil }
func (f *fakeSpawnProvider) ResumeSandbox(context.Context, ports.SandboxRef) error {
	return errors.New("not implemented")
}
func (f *fakeSpawnProvider) TakeSnapshot(context.Context, ports.SandboxRef) (ports.SnapshotID, error) {
	return "", errors.New("not implemented")
}
func (f *fakeSpawnProvider) RestoreFromSnapshot(context.Context, ports.SnapshotID, ports.CreateSpec) (ports.SandboxRef, error) {
	return ports.SandboxRef{}, errors.New("not implemented")
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
