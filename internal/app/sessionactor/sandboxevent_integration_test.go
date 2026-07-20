//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// TestHandleSandboxEvent_FullRoundTrip drives handleSandboxEvent directly
// through Actor.Send against a real Postgres instance (bypassing
// internal/adapters/inbound/wshub entirely -- that package's own
// integration tests cover the full WS-to-ack round trip; this one isolates
// handleSandboxEvent's own transactional behavior), proving: a
// non-critical, non-transitioning event still persists and bumps
// last_seen_at with no ack; a critical event's outcome carries its ackId
// verbatim; "ready" while Connecting transitions to Booting; "heartbeat"
// with a nil lastBootPhase while Booting transitions to Ready; "ready"
// while already Ready is a silent no-op (persisted, liveness bumped,
// status unchanged, no error); and a stale (too-low) gen is rejected
// outright -- not persisted, last_seen_at untouched, no ack.
func TestHandleSandboxEvent_FullRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := narvipg.NewSandboxStore(pool)

	created, err := sandboxStore.Create(ctx, sessionID)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if created.Gen != 1 {
		t.Fatalf("sandbox gen = %d, want 1", created.Gen)
	}

	moveTo := func(status sqlcgen.SandboxStatus) {
		t.Helper()
		if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
			SessionID: sessionID,
			Status:    status,
		}); err != nil {
			t.Fatalf("move sandbox to %s: %v", status, err)
		}
	}

	countEvents := func(eventType string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM events WHERE session_id = $1 AND type = $2`,
			sessionID, eventType,
		).Scan(&n); err != nil {
			t.Fatalf("count %s events: %v", eventType, err)
		}
		return n
	}

	r := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", nil, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	send := func(t *testing.T, cmd SandboxEvent) SandboxEventOutcome {
		t.Helper()
		reply := make(chan SandboxEventOutcome, 1)
		cmd.Reply = reply
		if err := a.Send(ctx, cmd); err != nil {
			t.Fatalf("Send: %v", err)
		}
		select {
		case outcome := <-reply:
			return outcome
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for SandboxEventOutcome")
			return SandboxEventOutcome{}
		}
	}

	// --- (a) a non-critical, non-transitioning event still persists and
	// bumps last_seen_at, produces no ack. ---
	beforeToken, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}

	tokenRaw := json.RawMessage(`{"type":"token","messageId":"tok-1","sessionId":"s","gen":1}`)
	outcome := send(t, SandboxEvent{Type: "token", Gen: 1, MessageID: "tok-1", Raw: tokenRaw})
	if !outcome.Persisted {
		t.Error("token event: Persisted = false, want true")
	}
	if outcome.AckID != "" {
		t.Errorf("token event: AckID = %q, want empty (non-critical type)", outcome.AckID)
	}
	if got := countEvents("token"); got != 1 {
		t.Errorf("token event count = %d, want 1", got)
	}

	afterToken, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if !afterToken.LastSeenAt.Valid {
		t.Fatal("last_seen_at not set after token event")
	}
	if beforeToken.LastSeenAt.Valid && !afterToken.LastSeenAt.Time.After(beforeToken.LastSeenAt.Time) {
		t.Errorf("last_seen_at did not advance: before=%v after=%v", beforeToken.LastSeenAt.Time, afterToken.LastSeenAt.Time)
	}
	if afterToken.Status != sqlcgen.SandboxStatusPending {
		t.Errorf("token event changed status to %s, want unchanged (%s)", afterToken.Status, sqlcgen.SandboxStatusPending)
	}

	// --- (b) a critical event persists AND its outcome carries the ackId
	// verbatim. ---
	critRaw := json.RawMessage(`{"type":"execution_complete","messageId":"m1","sessionId":"s","gen":1,"ackId":"execution_complete:m1","outcome":"completed"}`)
	outcome = send(t, SandboxEvent{Type: "execution_complete", Gen: 1, MessageID: "m1", Raw: critRaw})
	if !outcome.Persisted {
		t.Error("execution_complete: Persisted = false, want true")
	}
	if outcome.AckID != "execution_complete:m1" {
		t.Errorf("execution_complete: AckID = %q, want %q", outcome.AckID, "execution_complete:m1")
	}
	if got := countEvents("execution_complete"); got != 1 {
		t.Errorf("execution_complete event count = %d, want 1", got)
	}

	// --- (c) "ready" while Connecting transitions to Booting. ---
	moveTo(sqlcgen.SandboxStatusConnecting)
	readyRaw := json.RawMessage(`{"type":"ready","messageId":"r1","sessionId":"s","gen":1}`)
	outcome = send(t, SandboxEvent{Type: "ready", Gen: 1, MessageID: "r1", Raw: readyRaw})
	if !outcome.Persisted {
		t.Error("ready: Persisted = false, want true")
	}
	got, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.Status != sqlcgen.SandboxStatusBooting {
		t.Errorf("status after ready-while-connecting = %s, want %s", got.Status, sqlcgen.SandboxStatusBooting)
	}

	// --- (d) "heartbeat" with nil lastBootPhase while Booting transitions
	// to Ready. ---
	hbRaw := json.RawMessage(`{"type":"heartbeat","messageId":"h1","sessionId":"s","gen":1,"conversationId":null,"lastBootPhase":null}`)
	outcome = send(t, SandboxEvent{Type: "heartbeat", Gen: 1, MessageID: "h1", Raw: hbRaw, LastBootPhase: nil})
	if !outcome.Persisted {
		t.Error("heartbeat: Persisted = false, want true")
	}
	got, err = sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if got.Status != sqlcgen.SandboxStatusReady {
		t.Errorf("status after heartbeat-while-booting = %s, want %s", got.Status, sqlcgen.SandboxStatusReady)
	}

	// --- (e) "ready" while ALREADY Ready is a silent no-op: event still
	// persisted, last_seen_at still bumped, status stays Ready, no error. ---
	beforeReady, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	time.Sleep(5 * time.Millisecond) // ensure a distinguishable later timestamp
	readyAgainRaw := json.RawMessage(`{"type":"ready","messageId":"r2","sessionId":"s","gen":1}`)
	outcome = send(t, SandboxEvent{Type: "ready", Gen: 1, MessageID: "r2", Raw: readyAgainRaw})
	if !outcome.Persisted {
		t.Error("second ready: Persisted = false, want true")
	}
	afterReady, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if afterReady.Status != sqlcgen.SandboxStatusReady {
		t.Errorf("status after redundant ready = %s, want unchanged %s", afterReady.Status, sqlcgen.SandboxStatusReady)
	}
	if !afterReady.LastSeenAt.Time.After(beforeReady.LastSeenAt.Time) {
		t.Errorf("last_seen_at did not advance on redundant ready: before=%v after=%v", beforeReady.LastSeenAt.Time, afterReady.LastSeenAt.Time)
	}
	if got := countEvents("ready"); got != 2 {
		t.Errorf("ready event count = %d, want 2 (both ready frames persisted)", got)
	}

	// --- (f) a stale (too-low) gen is NOT persisted, does NOT bump
	// last_seen_at, and produces no ack. ---
	beforeStale, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	staleRaw := json.RawMessage(`{"type":"heartbeat","messageId":"h-stale","sessionId":"s","gen":0}`)
	outcome = send(t, SandboxEvent{Type: "heartbeat", Gen: 0, MessageID: "h-stale", Raw: staleRaw})
	if outcome.Persisted {
		t.Error("stale-gen event: Persisted = true, want false")
	}
	if outcome.AckID != "" {
		t.Errorf("stale-gen event: AckID = %q, want empty", outcome.AckID)
	}
	afterStale, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if !afterStale.LastSeenAt.Time.Equal(beforeStale.LastSeenAt.Time) {
		t.Errorf("last_seen_at moved on a stale-gen event: before=%v after=%v", beforeStale.LastSeenAt.Time, afterStale.LastSeenAt.Time)
	}
	if got := countEvents("heartbeat"); got != 1 {
		t.Errorf("heartbeat event count = %d, want 1 (the stale-gen one must not have been persisted)", got)
	}
}

// TestHandleSandboxEvent_ArmsLivenessAndInactivityOnceOnBootingToReady
// proves the fix for the confirmed HIGH-severity audit finding
// (sessionactor-ports & docs-completeness-vs-plan lenses):
// liveness_check and inactivity are armed for the first time exactly at
// the real Booting->Ready transition -- driven through a real
// handleSandboxEvent via Actor.Send, exactly matching how
// sandboxTransitionTrigger documents "heartbeat" with a nil
// LastBootPhase as the Booting->Ready trigger -- and are NOT re-armed
// (their fires_at pushed forward) by a later heartbeat while already
// Ready, proving the once-only guard rather than a
// re-arm-every-heartbeat regression. Reads session_timers directly via
// raw SQL/TimerStore, never trusting handleSandboxEvent's own return
// value alone.
func TestHandleSandboxEvent_ArmsLivenessAndInactivityOnceOnBootingToReady(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	sessionID := createTestSession(ctx, t, pool)

	sandboxStore := narvipg.NewSandboxStore(pool)
	timerStore := narvipg.NewTimerStore(pool)

	created, err := sandboxStore.Create(ctx, sessionID)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	if _, err := sandboxStore.UpdateStatus(ctx, sqlcgen.UpdateSandboxStatusParams{
		SessionID: sessionID,
		Status:    sqlcgen.SandboxStatusBooting,
	}); err != nil {
		t.Fatalf("move sandbox to booting: %v", err)
	}

	timeouts := platform.DefaultTimeouts()
	r := NewRegistry(ctx, pool, timeouts, nil, nil, nil, "", nil, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	send := func(t *testing.T, cmd SandboxEvent) SandboxEventOutcome {
		t.Helper()
		reply := make(chan SandboxEventOutcome, 1)
		cmd.Reply = reply
		if err := a.Send(ctx, cmd); err != nil {
			t.Fatalf("Send: %v", err)
		}
		select {
		case outcome := <-reply:
			return outcome
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for SandboxEventOutcome")
			return SandboxEventOutcome{}
		}
	}

	getFiresAt := func(t *testing.T, name string) (time.Time, bool) {
		t.Helper()
		row, err := timerStore.Get(ctx, sqlcgen.GetSessionTimerParams{SessionID: sessionID, Name: name})
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, false
		}
		if err != nil {
			t.Fatalf("get %s timer: %v", name, err)
		}
		return row.FiresAt.Time, true
	}

	beforeHeartbeat := time.Now()

	// First heartbeat, nil lastBootPhase, while Booting -> transitions to
	// Ready (sandboxTransitionTrigger's own documented (b) mapping).
	hbRaw := json.RawMessage(`{"type":"heartbeat","messageId":"h1","sessionId":"s","gen":1,"conversationId":null,"lastBootPhase":null}`)
	outcome := send(t, SandboxEvent{Type: "heartbeat", Gen: int(created.Gen), MessageID: "h1", Raw: hbRaw, LastBootPhase: nil})
	if !outcome.Persisted {
		t.Fatal("first heartbeat: Persisted = false, want true")
	}

	gotSandbox, err := sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if gotSandbox.Status != sqlcgen.SandboxStatusReady {
		t.Fatalf("status after heartbeat-while-booting = %s, want %s", gotSandbox.Status, sqlcgen.SandboxStatusReady)
	}

	livenessFiresAt, ok := getFiresAt(t, TimerLivenessCheck)
	if !ok {
		t.Fatal("liveness_check timer was not armed by the Booting->Ready transition -- the confirmed gap this batch fixes")
	}
	inactivityFiresAt, ok := getFiresAt(t, TimerInactivity)
	if !ok {
		t.Fatal("inactivity timer was not armed by the Booting->Ready transition -- the confirmed gap this batch fixes")
	}

	const tolerance = 5 * time.Second
	if want := beforeHeartbeat.Add(timeouts.SteadyHeartbeatBudget); absDuration(livenessFiresAt.Sub(want)) > tolerance {
		t.Errorf("liveness_check fires_at = %v, want ~%v (now+SteadyHeartbeatBudget, +/-%v)", livenessFiresAt, want, tolerance)
	}
	if want := beforeHeartbeat.Add(timeouts.InactivityMinCheckInterval); absDuration(inactivityFiresAt.Sub(want)) > tolerance {
		t.Errorf("inactivity fires_at = %v, want ~%v (now+InactivityMinCheckInterval, +/-%v)", inactivityFiresAt, want, tolerance)
	}

	// Second heartbeat, still Ready (nil lastBootPhase again -- a real
	// sandbox reports it on every heartbeat regardless of phase):
	// sandboxTransitionTrigger's own (b) mapping requires status ==
	// Booting, which no longer holds, so this is NOT a transition -- just
	// a liveness-only bump. Neither timer's fires_at must move: proving
	// the once-only guard, not a re-arm-every-heartbeat regression that
	// would starve liveness_check of ever actually firing.
	hb2Raw := json.RawMessage(`{"type":"heartbeat","messageId":"h2","sessionId":"s","gen":1,"conversationId":null,"lastBootPhase":null}`)
	outcome = send(t, SandboxEvent{Type: "heartbeat", Gen: int(created.Gen), MessageID: "h2", Raw: hb2Raw, LastBootPhase: nil})
	if !outcome.Persisted {
		t.Fatal("second heartbeat: Persisted = false, want true")
	}

	gotSandbox, err = sandboxStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get sandbox: %v", err)
	}
	if gotSandbox.Status != sqlcgen.SandboxStatusReady {
		t.Fatalf("status after second heartbeat = %s, want unchanged %s", gotSandbox.Status, sqlcgen.SandboxStatusReady)
	}

	livenessFiresAt2, ok := getFiresAt(t, TimerLivenessCheck)
	if !ok {
		t.Fatal("liveness_check timer disappeared after second heartbeat")
	}
	inactivityFiresAt2, ok := getFiresAt(t, TimerInactivity)
	if !ok {
		t.Fatal("inactivity timer disappeared after second heartbeat")
	}
	if !livenessFiresAt2.Equal(livenessFiresAt) {
		t.Errorf("liveness_check fires_at changed on a second, already-Ready heartbeat: before=%v after=%v (must stay untouched)", livenessFiresAt, livenessFiresAt2)
	}
	if !inactivityFiresAt2.Equal(inactivityFiresAt) {
		t.Errorf("inactivity fires_at changed on a second, already-Ready heartbeat: before=%v after=%v (must stay untouched)", inactivityFiresAt, inactivityFiresAt2)
	}
}

// blockingCommander is a test-only ports.SandboxCommander whose
// SendCommand call closes started (the FIRST time it is ever called, only)
// and then blocks until release is closed -- a controllable synchronization
// point, not a sleep/timing guess, for proving a happens-before ordering
// between two goroutines-in-effect (here: the actor's own single
// mailbox-processing goroutine, at two different points in its own
// sequential execution of handleSandboxEvent).
type blockingCommander struct {
	started chan struct{}
	release chan struct{}
}

var _ ports.SandboxCommander = (*blockingCommander)(nil)

func newBlockingCommander() *blockingCommander {
	return &blockingCommander{started: make(chan struct{}), release: make(chan struct{})}
}

func (c *blockingCommander) SendCommand(string, json.RawMessage) error {
	close(c.started)
	<-c.release
	return nil
}

// TestHandleSandboxEvent_AckReplySentBeforeSlowPostCommitSideEffects proves
// Finding 1's own fix: cmd.Reply already carries an outcome BEFORE the
// slow best-effort post-commit side effect a real execution_complete
// triggers (sendPushBestEffort's own SandboxCommander.SendCommand call)
// is ever invoked -- not merely before it returns. The proof is a real
// happens-before relationship established via channels, not a sleep or
// timing guess (matching internal/adapters/outbound/opencode's own
// TestFinalize_LateSubtaskEmitIsDroppedNotRacedPastExecutionComplete
// precedent for this kind of controllable-blocking-point test):
// blockingCommander.SendCommand only closes `started` the instant it is
// entered, then blocks indefinitely on `release`. Since handleSandboxEvent
// runs entirely on the actor's own single command-processing goroutine,
// `started` being closed proves SendCommand has genuinely been reached --
// and, under the OLD (wrong) ordering this batch fixes, cmd.Reply would
// not have been sent yet at that exact instant (it ran AFTER
// sendPushBestEffort returned); under the fixed ordering, the reply was
// already placed on the buffered reply channel strictly before
// handleEnsureDispatched/sendPushBestEffort ever ran, so a non-blocking
// receive on reply must already succeed the moment `started` fires.
func TestHandleSandboxEvent_AckReplySentBeforeSlowPostCommitSideEffects(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{},
		"repo1", "https://github.com/acme/repo1.git", "feature-x")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)
	createProcessingTurn(ctx, t, turnStore, sessionID)

	commander := newBlockingCommander()
	r := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, commander, nil, "", nil, nil)
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	reply := make(chan SandboxEventOutcome, 1)
	cmd := SandboxEvent{
		Type:  "execution_complete",
		Gen:   1,
		Raw:   executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
		Reply: reply,
	}
	if err := a.Send(ctx, cmd); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Wait for SendCommand to have genuinely been entered -- proof that
	// handleSandboxEvent's own transact already committed (a "completed"
	// execution_complete only ever reaches sendPushBestEffort's
	// commander.SendCommand call AFTER its transact commits, pushpr.go),
	// and that handleEnsureDispatched has already run too (it runs first,
	// per the fix's own ordering).
	select {
	case <-commander.started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the blocking SandboxCommander.SendCommand to be invoked")
	}

	// At this EXACT instant, SendCommand is still blocked on release (never
	// closed yet below) -- so if cmd.Reply already has a value ready RIGHT
	// NOW, it was necessarily placed there strictly before SendCommand was
	// ever called, since both run sequentially on the same goroutine.
	select {
	case outcome := <-reply:
		if !outcome.Persisted {
			t.Error("outcome.Persisted = false, want true")
		}
		if outcome.AckID == "" {
			t.Error("outcome.AckID is empty, want a real ack for this critical event")
		}
	default:
		t.Fatal("cmd.Reply had no value ready while the slow post-commit side effect (SandboxCommander.SendCommand) was still blocked -- the ack is being delayed by a side effect, the exact regression this batch fixes")
	}

	// Release the blocked SendCommand so the actor can finish handling
	// this command and shut down cleanly.
	close(commander.release)
}

// absDuration returns d's absolute value.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
