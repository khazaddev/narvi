//go:build integration

package sessionactor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// This file proves the wiring an independent verifier's review of this
// Step found missing: a real execution_complete completing a Processing
// turn actually sends a real push command (sendPushBestEffort), and a
// real push_complete actually calls ports.SourceControl.CreatePR and
// records the result as a "pr"-typed artifact (createPRBestEffort) --
// see pushpr.go's own top comment for the full design.

// testTokenEncryptionKey is a fixed, obviously-fake 32-byte AES-256-GCM
// key -- exactly the length platform.EncryptToken/DecryptToken require
// (see internal/platform/tokenencrypt.go), used only by this file's own
// tests to round-trip a fake GitHub access token through the real
// encrypt/decrypt path, never a real secret.
var testTokenEncryptionKey = []byte("0123456789abcdef0123456789abcdef")

// fakeSourceControl is a test-only ports.SourceControl recording every
// CreatePR call it receives and returning a caller-configured (ref, err)
// pair.
//
// Step 26 ("image builds") extends this same fake with an identical
// recording shape for ResolveBranchSHA (shaCalls/nextSHA/nextSHAErr, plus
// an optional per-repo shaFor map so a test can return DIFFERENT SHAs for
// different repos in one multi-repo session) rather than introducing a
// second, parallel fake -- this package's own imageresolve tests need the
// exact same CreatePR-call-recording precedent, just for the other
// SourceControl method dispatch.go's resolveAndSetImage now calls.
//
// Step 27 ("mocking + contract drift") extends this SAME fake again, the
// identical way, for ResolveContractsFingerprint (fingerprintCalls/
// nextFingerprint/nextFingerprintExists/nextFingerprintErr, plus an
// optional per-repo fingerprintFor map) -- this package's own
// contractdrift tests need the exact same recording precedent for the
// third SourceControl method checkContractDrift (contractdrift.go) calls.
type fakeSourceControl struct {
	mu      sync.Mutex
	calls   []ports.CreatePRSpec
	nextRef ports.PRRef
	nextErr error

	shaCalls   []ports.ResolveBranchSHASpec
	shaFor     map[string]string // keyed by repo name; falls back to nextSHA if absent
	nextSHA    string
	nextSHAErr error

	fingerprintCalls      []ports.ResolveContractsFingerprintSpec
	fingerprintFor        map[string]string // keyed by repo name; overrides nextFingerprint if present
	existsFor             map[string]bool   // keyed by repo name; overrides nextFingerprintExists if present
	nextFingerprint       string
	nextFingerprintExists bool
	nextFingerprintErr    error
}

var _ ports.SourceControl = (*fakeSourceControl)(nil)

func (f *fakeSourceControl) CreatePR(_ context.Context, spec ports.CreatePRSpec) (ports.PRRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, spec)
	return f.nextRef, f.nextErr
}

func (f *fakeSourceControl) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSourceControl) lastSpec() ports.CreatePRSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

func (f *fakeSourceControl) ResolveBranchSHA(_ context.Context, spec ports.ResolveBranchSHASpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shaCalls = append(f.shaCalls, spec)
	if f.nextSHAErr != nil {
		return "", f.nextSHAErr
	}
	if sha, ok := f.shaFor[spec.Repo]; ok {
		return sha, nil
	}
	return f.nextSHA, nil
}

func (f *fakeSourceControl) shaCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.shaCalls)
}

func (f *fakeSourceControl) ResolveContractsFingerprint(_ context.Context, spec ports.ResolveContractsFingerprintSpec) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fingerprintCalls = append(f.fingerprintCalls, spec)
	if f.nextFingerprintErr != nil {
		return "", false, f.nextFingerprintErr
	}
	fingerprint := f.nextFingerprint
	if fp, ok := f.fingerprintFor[spec.Repo]; ok {
		fingerprint = fp
	}
	exists := f.nextFingerprintExists
	if e, ok := f.existsFor[spec.Repo]; ok {
		exists = e
	}
	return fingerprint, exists, nil
}

func (f *fakeSourceControl) fingerprintCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.fingerprintCalls)
}

// reposJSONForTest builds the sessions.repos JSONB shape (design decision
// 1) for exactly one repo, name/url/branch as given.
func reposJSONForTest(t *testing.T, name, url, branch string) []byte {
	t.Helper()
	raw, err := json.Marshal([]map[string]any{
		{"name": name, "url": url, "branch": branch},
	})
	if err != nil {
		t.Fatalf("marshal test repos: %v", err)
	}
	return raw
}

// createTestSessionWithRepos creates a session naming exactly one repo
// (design decision 1's own sessions.repos JSONB column), optionally owned
// by createdBy (a zero pgtype.UUID means no owner, matching
// createTestSession's own default).
func createTestSessionWithRepos(ctx context.Context, t *testing.T, pool *pgxpool.Pool, createdBy pgtype.UUID, name, url, branch string) pgtype.UUID {
	t.Helper()
	created, err := narvipg.NewSessionStore(pool).Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
		CreatedBy:   createdBy,
		Repos:       reposJSONForTest(t, name, url, branch),
	})
	if err != nil {
		t.Fatalf("create test session with repos: %v", err)
	}
	return created.ID
}

// createProcessingTurn directly seeds a turn already in StateProcessing --
// a surgical, direct DB seed (bypassing the real dispatch path), matching
// this package's own resilience test precedent exactly: these tests exist
// to prove completeProcessingTurn/sendPushBestEffort/createPRBestEffort's
// OWN behavior in reaction to an inbound SandboxEvent, not dispatch
// correctness (already covered by dispatch_integration_test.go).
func createProcessingTurn(ctx context.Context, t *testing.T, turns *narvipg.TurnStore, sessionID pgtype.UUID) sqlcgen.Turn {
	t.Helper()
	created, err := turns.Create(ctx, sqlcgen.CreateTurnParams{
		SessionID: sessionID,
		Status:    sqlcgen.TurnStatusProcessing,
	})
	if err != nil {
		t.Fatalf("create processing turn: %v", err)
	}
	return created
}

// sendSandboxEventForTest drives cmd through Actor.Send, exactly mirroring
// TestHandleSandboxEvent_FullRoundTrip's own local `send` helper (this
// file's own package-level copy, since Go test helpers are function-
// scoped there).
func sendSandboxEventForTest(ctx context.Context, t *testing.T, a *Actor, cmd SandboxEvent) SandboxEventOutcome {
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

// executionCompleteRaw marshals a real, schema-valid sandboxws.
// ExecutionComplete wire payload.
func executionCompleteRaw(t *testing.T, sessionID string, gen int, outcome sandboxws.ExecutionCompleteOutcome) json.RawMessage {
	t.Helper()
	messageID := uuid.NewString()
	evt := sandboxws.ExecutionComplete{
		Type:      "execution_complete",
		MessageId: messageID,
		SessionId: sessionID,
		Gen:       gen,
		AckId:     "execution_complete:" + messageID,
		Outcome:   outcome,
	}
	raw, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal execution_complete: %v", err)
	}
	return raw
}

// pushCompleteRaw marshals a real, schema-valid sandboxws.PushComplete
// wire payload naming exactly one pushed repo.
func pushCompleteRaw(t *testing.T, sessionID string, gen int, repoName, branch, sha string) json.RawMessage {
	t.Helper()
	messageID := uuid.NewString()
	evt := sandboxws.PushComplete{
		Type:      "push_complete",
		MessageId: messageID,
		SessionId: sessionID,
		Gen:       gen,
		AckId:     "push_complete:" + messageID,
		Repos: []sandboxws.PushCompleteReposElem{
			{Name: repoName, Branch: branch, Sha: sha},
		},
	}
	raw, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("marshal push_complete: %v", err)
	}
	return raw
}

// TestHandleSandboxEvent_ExecutionCompleteCompleted_CompletesTurnAndSendsPush
// proves: a real execution_complete (outcome "completed") arriving for a
// session with a Processing turn transitions that turn to Completed,
// deletes turn_deadline, and -- as a best-effort side effect run AFTER the
// event's own transact commits -- sends a real sandboxws.Push command
// naming the session's own repo/branch.
func TestHandleSandboxEvent_ExecutionCompleteCompleted_CompletesTurnAndSendsPush(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{},
		"repo1", "https://github.com/acme/repo1.git", "feature-x")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)
	created := createProcessingTurn(ctx, t, turnStore, sessionID)

	// Arm turn_deadline exactly like a real dispatch would have, so this
	// test also proves it gets cleaned up on real completion.
	if _, err := narvipg.NewTimerStore(pool).Upsert(ctx, sqlcgen.UpsertSessionTimerParams{
		SessionID: sessionID, Name: TimerTurnDeadline,
		FiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("arm turn_deadline: %v", err)
	}

	commander := &fakeSendCommander{}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, commander, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	outcome := sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeCompleted),
	})
	if !outcome.Persisted {
		t.Error("outcome.Persisted = false, want true")
	}
	// AckID's exact deterministic-format assertion is already covered by
	// TestHandleSandboxEvent_FullRoundTrip; here we only care that SOME
	// non-empty ack was produced for this critical event.
	if outcome.AckID == "" {
		t.Error("outcome.AckID is empty, want a real ack for this critical event")
	}

	got, err := turnStore.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if got.Status != sqlcgen.TurnStatusCompleted {
		t.Errorf("turn status = %s, want %s", got.Status, sqlcgen.TurnStatusCompleted)
	}
	if !got.CompletedAt.Valid {
		t.Error("turn completed_at not set")
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM session_timers WHERE session_id = $1 AND name = $2`,
		sessionID, TimerTurnDeadline,
	).Scan(&n); err != nil {
		t.Fatalf("count turn_deadline timers: %v", err)
	}
	if n != 0 {
		t.Errorf("turn_deadline timer count = %d, want 0 (deleted on real completion)", n)
	}

	waitUntil(t, 5*time.Second, func() bool {
		return commander.callCount() == 1
	})

	var push sandboxws.Push
	if err := json.Unmarshal(commander.lastPayload(), &push); err != nil {
		t.Fatalf("unmarshal SendCommand payload as sandboxws.Push: %v", err)
	}
	if push.Type != "push" {
		t.Errorf("Push.Type = %q, want %q", push.Type, "push")
	}
	if push.SessionId != sessionID.String() {
		t.Errorf("Push.SessionId = %q, want %q", push.SessionId, sessionID.String())
	}
	if len(push.Repos) != 1 || push.Repos[0].Name != "repo1" || push.Repos[0].Branch != "feature-x" {
		t.Errorf("Push.Repos = %+v, want exactly one {Name: repo1, Branch: feature-x}", push.Repos)
	}
}

// TestHandleSandboxEvent_ExecutionCompleteFailed_NoPush proves: a real
// execution_complete reporting "failed" transitions the turn to Failed
// (session failure_reason "failed") and never sends a push command --
// only a genuine "completed" outcome has anything to push.
func TestHandleSandboxEvent_ExecutionCompleteFailed_NoPush(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{},
		"repo1", "https://github.com/acme/repo1.git", "feature-x")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	turnStore := narvipg.NewTurnStore(pool)
	created := createProcessingTurn(ctx, t, turnStore, sessionID)

	commander := &fakeSendCommander{}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, commander, nil, "", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "execution_complete",
		Gen:  1,
		Raw:  executionCompleteRaw(t, sessionID.String(), 1, sandboxws.ExecutionCompleteOutcomeFailed),
	})

	waitUntil(t, 5*time.Second, func() bool {
		got, err := turnStore.Get(ctx, created.ID)
		return err == nil && got.Status == sqlcgen.TurnStatusFailed
	})

	// Give any (should-be-nonexistent) push send a moment to happen before
	// asserting it did not -- there is no positive DB signal to poll FOR
	// when asserting something did NOT happen (same precedent as
	// dispatch_integration_test.go's own circuit-breaker test).
	time.Sleep(300 * time.Millisecond)
	if got := commander.callCount(); got != 0 {
		t.Errorf("commander.SendCommand called %d times, want 0 (a failed turn has nothing to push)", got)
	}

	sessionStore := narvipg.NewSessionStore(pool)
	sessionRow, err := sessionStore.Get(ctx, sessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sessionRow.FailureReason == nil || *sessionRow.FailureReason != sqlcgen.SessionFailureReasonFailed {
		t.Errorf("session.failure_reason = %v, want %q", sessionRow.FailureReason, sqlcgen.SessionFailureReasonFailed)
	}
}

// TestHandleSandboxEvent_PushComplete_CreatesPRArtifact proves: a real
// push_complete event, for a session whose creator has a real (encrypted)
// GitHub identity, calls SourceControl.CreatePR with the correct
// owner/repo (parsed from the session's own repo clone URL)/head/base/
// token, and records the result as a "pr"-typed artifact row.
func TestHandleSandboxEvent_PushComplete_CreatesPRArtifact(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	userStore := narvipg.NewUserStore(pool)
	identityStore := narvipg.NewIdentityStore(pool)

	user, err := userStore.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("pr-test-%d@example.com", time.Now().UnixNano()),
		DisplayName:  "PR Test User",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	const plaintextToken = "gh-fake-oauth-token"
	encrypted, err := platform.EncryptToken(testTokenEncryptionKey, []byte(plaintextToken))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	email := user.PrimaryEmail
	if _, err := identityStore.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:               user.ID,
		Provider:             sqlcgen.IdentityProviderGithub,
		ExternalID:           fmt.Sprintf("pr-test-external-%d", time.Now().UnixNano()),
		Email:                &email,
		EmailVerified:        true,
		LinkedVia:            sqlcgen.IdentityLinkedViaAdmin,
		AccessTokenEncrypted: encrypted,
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}

	sessionID := createTestSessionWithRepos(ctx, t, pool, user.ID,
		"repo1", "https://github.com/acme/repo1.git", "feature-x")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	sourceControl := &fakeSourceControl{nextRef: ports.PRRef{Number: 42, URL: "https://github.com/acme/repo1/pull/42"}}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete",
		Gen:  1,
		Raw:  pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-x", "abc123"),
	})

	waitUntil(t, 5*time.Second, func() bool {
		return sourceControl.callCount() == 1
	})

	spec := sourceControl.lastSpec()
	if spec.Owner != "acme" || spec.Repo != "repo1" {
		t.Errorf("CreatePRSpec.Owner/Repo = %q/%q, want acme/repo1", spec.Owner, spec.Repo)
	}
	if spec.Head != "feature-x" {
		t.Errorf("CreatePRSpec.Head = %q, want %q", spec.Head, "feature-x")
	}
	if spec.Base != placeholderPRBaseBranch {
		t.Errorf("CreatePRSpec.Base = %q, want %q", spec.Base, placeholderPRBaseBranch)
	}
	if spec.Token != plaintextToken {
		t.Errorf("CreatePRSpec.Token = %q, want the real decrypted token %q", spec.Token, plaintextToken)
	}

	artifactStore := narvipg.NewArtifactStore(pool)
	waitUntil(t, 5*time.Second, func() bool {
		rows, err := artifactStore.ListForSession(ctx, sessionID)
		return err == nil && len(rows) == 1
	})

	rows, err := artifactStore.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("artifact count = %d, want 1", len(rows))
	}
	if rows[0].Type != sqlcgen.ArtifactTypePr {
		t.Errorf("artifact type = %s, want %s", rows[0].Type, sqlcgen.ArtifactTypePr)
	}
	if rows[0].Url != "https://github.com/acme/repo1/pull/42" {
		t.Errorf("artifact url = %q, want %q", rows[0].Url, "https://github.com/acme/repo1/pull/42")
	}
}

// TestHandleSandboxEvent_PushComplete_NoCreatedBy_SkipsHonestly proves a
// session with no created_by user (§8.11's own "no bot fallback exists"
// gap) never calls CreatePR and never panics -- it simply has no PR
// artifact to show, logged, not silently swallowed as a bug.
func TestHandleSandboxEvent_PushComplete_NoCreatedBy_SkipsHonestly(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)

	sessionID := createTestSessionWithRepos(ctx, t, pool, pgtype.UUID{},
		"repo1", "https://github.com/acme/repo1.git", "feature-x")

	sandboxStore := narvipg.NewSandboxStore(pool)
	if _, err := sandboxStore.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	sourceControl := &fakeSourceControl{}
	r, err := NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "", sourceControl, testTokenEncryptionKey, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = r.Shutdown() })

	a, err := r.GetOrSpawn(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetOrSpawn: %v", err)
	}

	sendSandboxEventForTest(ctx, t, a, SandboxEvent{
		Type: "push_complete",
		Gen:  1,
		Raw:  pushCompleteRaw(t, sessionID.String(), 1, "repo1", "feature-x", "abc123"),
	})

	time.Sleep(300 * time.Millisecond)
	if got := sourceControl.callCount(); got != 0 {
		t.Errorf("CreatePR called %d times, want 0 (no created_by user)", got)
	}

	artifactStore := narvipg.NewArtifactStore(pool)
	rows, err := artifactStore.ListForSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("artifact count = %d, want 0", len(rows))
	}
}
