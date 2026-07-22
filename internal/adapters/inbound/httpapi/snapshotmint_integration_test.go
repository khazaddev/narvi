//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// errNotImplemented is fakeSnapshotProvider's own stand-in error for
// every SandboxProvider method SnapshotMint never calls -- mirrors
// dispatch_integration_test.go's own fakeSpawnProvider precedent (its
// unused methods return errors.New("not implemented") too).
var errNotImplemented = errors.New("not implemented")

// fakeSnapshotProvider is a test-only ports.SandboxProvider recording
// every TakeSnapshot call it receives and returning a caller-configured
// (id, err) pair -- this package's own snapshot-mint integration tests
// never talk to a real cloud provider. Every OTHER SandboxProvider method
// is unused by SnapshotMint and returns a plain error if ever
// accidentally called, matching dispatch_integration_test.go's own
// fakeSpawnProvider precedent exactly.
type fakeSnapshotProvider struct {
	mu      sync.Mutex
	calls   []ports.SandboxRef
	nextID  ports.SnapshotID
	nextErr error
}

var _ ports.SandboxProvider = (*fakeSnapshotProvider)(nil)

func (f *fakeSnapshotProvider) Capabilities() ports.Capabilities {
	return ports.Capabilities{Snapshots: true}
}

func (f *fakeSnapshotProvider) CreateSandbox(context.Context, ports.CreateSpec) (ports.SandboxRef, error) {
	return ports.SandboxRef{}, errNotImplemented
}
func (f *fakeSnapshotProvider) StopSandbox(context.Context, ports.SandboxRef) error {
	return errNotImplemented
}
func (f *fakeSnapshotProvider) ResumeSandbox(context.Context, ports.SandboxRef) error {
	return errNotImplemented
}

func (f *fakeSnapshotProvider) TakeSnapshot(_ context.Context, ref ports.SandboxRef) (ports.SnapshotID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, ref)
	return f.nextID, f.nextErr
}

func (f *fakeSnapshotProvider) RestoreFromSnapshot(context.Context, ports.SnapshotID, ports.CreateSpec) (ports.SandboxRef, error) {
	return ports.SandboxRef{}, errNotImplemented
}
func (f *fakeSnapshotProvider) BuildImage(context.Context, ports.ImageSpec) (ports.BuildRef, error) {
	return "", errNotImplemented
}
func (f *fakeSnapshotProvider) DeleteImage(context.Context, ports.ImageRef) error {
	return errNotImplemented
}
func (f *fakeSnapshotProvider) List(context.Context) ([]ports.SandboxRef, error) { return nil, nil }

func (f *fakeSnapshotProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeSnapshotProvider) lastRef() ports.SandboxRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

// postSnapshotMint POSTs to /sessions/{sessionID}/snapshot with the given
// bearer token (omitted entirely if bearer == ""), and X-Sandbox-Gen "1"
// (matching a freshly created sandbox row's own default gen, §3.2, exactly
// the same default postScmCredentials' own sibling helper in
// scmcredentials_integration_test.go uses) -- this audit remediation's own
// new gen-fencing check must not silently change what every PRE-EXISTING
// test in this file was already proving. Use postSnapshotMintFull directly
// for a test that needs a non-default gen (or no X-Sandbox-Gen header at
// all). Returns the raw status and a best-effort decoded body.
func postSnapshotMint(t *testing.T, r testRig, sessionID string, bearer string) (int, map[string]string) {
	t.Helper()
	return postSnapshotMintFull(t, r, sessionID, bearer, "1")
}

// postSnapshotMintFull is postSnapshotMint's general-purpose form: gen is
// sent as the X-Sandbox-Gen header verbatim, or omitted entirely when
// gen == "" (matching a real caller that never sends the header at all,
// not merely an empty one) -- mirrors postScmCredentialsFull's own
// identical convention exactly.
func postSnapshotMintFull(t *testing.T, r testRig, sessionID, bearer, gen string) (int, map[string]string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/sessions/"+sessionID+"/snapshot", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if gen != "" {
		req.Header.Set("X-Sandbox-Gen", gen)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var got map[string]string
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// TestSnapshotMint_Success proves the full real flow: a valid sandbox
// bearer token + a sandbox row with a live provider_id -> a real
// TakeSnapshot call against the ref it names, and 200 with the exact
// snapshotId the fake provider returned.
func TestSnapshotMint_Success(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	if _, err := rig.pool.Exec(ctx, `UPDATE sandboxes SET provider_id = $2 WHERE session_id = $1`,
		session.ID, "modal-sandbox-object-1"); err != nil {
		t.Fatalf("set provider_id: %v", err)
	}

	rig.provider.nextID = "snap-real-123"

	status, got := postSnapshotMint(t, rig, session.ID.String(), "sandbox-bearer-token")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %+v)", status, http.StatusOK, got)
	}
	if got["snapshotId"] != "snap-real-123" {
		t.Errorf("snapshotId = %q, want %q", got["snapshotId"], "snap-real-123")
	}

	if callCount := rig.provider.callCount(); callCount != 1 {
		t.Fatalf("TakeSnapshot called %d times, want 1", callCount)
	}
	if got := rig.provider.lastRef(); got.ProviderID != "modal-sandbox-object-1" {
		t.Errorf("TakeSnapshot called with ProviderID = %q, want %q", got.ProviderID, "modal-sandbox-object-1")
	}
}

// TestSnapshotMint_MissingBearer proves a missing Authorization header is
// 401.
func TestSnapshotMint_MissingBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postSnapshotMint(t, rig, session.ID.String(), "")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
	if got := rig.provider.callCount(); got != 0 {
		t.Errorf("TakeSnapshot called %d times, want 0 (auth must be checked first)", got)
	}
}

// TestSnapshotMint_InvalidBearer proves a wrong (but present) bearer
// token is 401.
func TestSnapshotMint_InvalidBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postSnapshotMint(t, rig, session.ID.String(), "totally-wrong-token")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// TestSnapshotMint_UnknownSession proves a well-formed but nonexistent
// session id is 404.
func TestSnapshotMint_UnknownSession(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postSnapshotMint(t, rig, "11111111-1111-1111-1111-111111111111", "any-token")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestSnapshotMint_MalformedSessionID proves a malformed session id is
// ALSO 404 -- mirrors scmcredentials.go's own "malformed and nonexistent
// both mean no such session" precedent (this caller is sandbox-agent
// code, never a browser).
func TestSnapshotMint_MalformedSessionID(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postSnapshotMint(t, rig, "not-a-uuid", "any-token")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestSnapshotMint_NoLiveProviderID proves a sandbox row with no
// provider_id (nil -- e.g. never actually spawned against a real
// provider object) -> 409, and TakeSnapshot is never called.
func TestSnapshotMint_NoLiveProviderID(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	// provider_id left NULL (createSandboxWithToken's own Create call
	// never sets it).

	status, _ := postSnapshotMint(t, rig, session.ID.String(), "sandbox-bearer-token")
	if status != http.StatusConflict {
		t.Errorf("status = %d, want %d", status, http.StatusConflict)
	}
	if got := rig.provider.callCount(); got != 0 {
		t.Errorf("TakeSnapshot called %d times, want 0", got)
	}
}

// TestSnapshotMint_ProviderError proves a real *ports.ProviderError
// returned by TakeSnapshot -> 502, never a 500 (see snapshotmint.go's own
// doc comment for why 502 was chosen).
func TestSnapshotMint_ProviderError(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	if _, err := rig.pool.Exec(ctx, `UPDATE sandboxes SET provider_id = $2 WHERE session_id = $1`,
		session.ID, "modal-sandbox-object-1"); err != nil {
		t.Fatalf("set provider_id: %v", err)
	}

	rig.provider.nextErr = &ports.ProviderError{
		Transient: false, Code: "PROVIDER_DOWN", Op: ports.OpTakeSnapshot, Err: errNotImplemented,
	}

	status, _ := postSnapshotMint(t, rig, session.ID.String(), "sandbox-bearer-token")
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", status, http.StatusBadGateway)
	}
}

// TestSnapshotMint_NilTokenHash_Rejected proves this endpoint's own
// bearer check does NOT inherit wshub's own WS-handshake nil-token_hash
// bypass -- mirrors TestScmCredentials_NilTokenHash_Rejected exactly
// (both call the SAME verifySandboxBearerToken).
func TestSnapshotMint_NilTokenHash_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	if _, err := rig.sandboxes.Create(ctx, session.ID); err != nil {
		t.Fatalf("create sandbox (token_hash left NULL): %v", err)
	}

	status, _ := postSnapshotMint(t, rig, session.ID.String(), "any-non-empty-token")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d (a nil token_hash must reject, never bypass, on this endpoint)", status, http.StatusUnauthorized)
	}
}

// --- Dead-sandbox check (audit remediation) ---

// TestSnapshotMint_DeadSandboxStatus proves a sandbox in a dead status is
// rejected with 410, even with an otherwise-valid bearer token and gen --
// mirrors TestScmCredentials_DeadSandboxStatus exactly (same statuses, same
// status code, same reasoning: this endpoint's own IsDeadSandboxStatus
// guard was entirely missing before this audit remediation, letting a
// terminalized sandbox's last-known-live token keep minting real provider
// snapshots indefinitely). TakeSnapshot must never be called.
func TestSnapshotMint_DeadSandboxStatus(t *testing.T) {
	tests := []struct {
		name   string
		status sqlcgen.SandboxStatus
	}{
		{"stopped", sqlcgen.SandboxStatusStopped},
		{"failed", sqlcgen.SandboxStatusFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newTestRig(t)
			ctx := context.Background()

			session := rig.createSession(ctx, t)
			createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
			if _, err := rig.pool.Exec(ctx, `UPDATE sandboxes SET provider_id = $2 WHERE session_id = $1`,
				session.ID, "modal-sandbox-object-1"); err != nil {
				t.Fatalf("set provider_id: %v", err)
			}
			moveSandboxStatus(ctx, t, rig, session.ID, tc.status)

			status, _ := postSnapshotMint(t, rig, session.ID.String(), "sandbox-bearer-token")
			if status != http.StatusGone {
				t.Errorf("status = %d, want %d (dead sandbox status %s)", status, http.StatusGone, tc.status)
			}
			if got := rig.provider.callCount(); got != 0 {
				t.Errorf("TakeSnapshot called %d times, want 0 (dead sandbox must be rejected before ever reaching the provider)", got)
			}
		})
	}
}

// TestSnapshotMint_SuspectSandbox_NotDead proves a Suspect sandbox --
// deliberately NOT in the dead-status deny-list -- still succeeds, mirroring
// TestScmCredentials_SuspectSandbox_NotDead exactly: a sandbox merely
// suspected of having missed a heartbeat must still be able to mint a
// snapshot.
func TestSnapshotMint_SuspectSandbox_NotDead(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	if _, err := rig.pool.Exec(ctx, `UPDATE sandboxes SET provider_id = $2 WHERE session_id = $1`,
		session.ID, "modal-sandbox-object-1"); err != nil {
		t.Fatalf("set provider_id: %v", err)
	}
	moveSandboxStatus(ctx, t, rig, session.ID, sqlcgen.SandboxStatusSuspect)

	rig.provider.nextID = "snap-suspect-ok"

	status, got := postSnapshotMint(t, rig, session.ID.String(), "sandbox-bearer-token")
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d (Suspect is not a dead status)", status, http.StatusOK)
	}
	if got["snapshotId"] != "snap-suspect-ok" {
		t.Errorf("snapshotId = %q, want %q", got["snapshotId"], "snap-suspect-ok")
	}
}

// --- Gen fencing (audit remediation) ---

// TestSnapshotMint_GenMismatch_Rejected proves a stale/wrong X-Sandbox-Gen
// -> 403, mirroring TestScmCredentials_GenMismatch_Rejected exactly.
// TakeSnapshot must never be called.
func TestSnapshotMint_GenMismatch_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token") // gen 1
	if _, err := rig.pool.Exec(ctx, `UPDATE sandboxes SET provider_id = $2 WHERE session_id = $1`,
		session.ID, "modal-sandbox-object-1"); err != nil {
		t.Fatalf("set provider_id: %v", err)
	}

	status, _ := postSnapshotMintFull(t, rig, session.ID.String(), "sandbox-bearer-token", "999")
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (gen mismatch)", status, http.StatusForbidden)
	}
	if got := rig.provider.callCount(); got != 0 {
		t.Errorf("TakeSnapshot called %d times, want 0", got)
	}
}

// TestSnapshotMint_MissingGen_Rejected proves an ABSENT X-Sandbox-Gen
// header -- not merely a mismatched one -- is rejected the SAME way (403),
// mirroring TestScmCredentials_MissingGen_Rejected exactly.
func TestSnapshotMint_MissingGen_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	if _, err := rig.pool.Exec(ctx, `UPDATE sandboxes SET provider_id = $2 WHERE session_id = $1`,
		session.ID, "modal-sandbox-object-1"); err != nil {
		t.Fatalf("set provider_id: %v", err)
	}

	status, _ := postSnapshotMintFull(t, rig, session.ID.String(), "sandbox-bearer-token", "" /* no X-Sandbox-Gen header at all */)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (missing X-Sandbox-Gen header)", status, http.StatusForbidden)
	}
}

// TestSnapshotMint_CorrectCurrentGen_Succeeds proves the gen check compares
// against the sandbox row's REAL current gen, not a hardcoded "1" --
// mirrors TestScmCredentials_CorrectCurrentGen_Succeeds exactly: bumps the
// sandbox to gen 2 via a real UpsertForSpawn respawn, then proves
// presenting gen "2" succeeds.
func TestSnapshotMint_CorrectCurrentGen_Succeeds(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := rig.createSession(ctx, t)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token") // gen 1
	bumped := bumpSandboxGen(ctx, t, rig, session.ID, "respawned-bearer-token")
	if bumped.Gen != 2 {
		t.Fatalf("bumped.Gen = %d, want 2 (test setup assumption: UpsertForSpawn bumps an existing gen-1 row to gen 2)", bumped.Gen)
	}
	if _, err := rig.pool.Exec(ctx, `UPDATE sandboxes SET provider_id = $2 WHERE session_id = $1`,
		session.ID, "modal-sandbox-object-1"); err != nil {
		t.Fatalf("set provider_id: %v", err)
	}

	rig.provider.nextID = "snap-regen-ok"

	status, got := postSnapshotMintFull(t, rig, session.ID.String(), "respawned-bearer-token", fmt.Sprintf("%d", bumped.Gen))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (correct current gen)", status, http.StatusOK)
	}
	if got["snapshotId"] != "snap-regen-ok" {
		t.Errorf("snapshotId = %q, want %q", got["snapshotId"], "snap-regen-ok")
	}
}
