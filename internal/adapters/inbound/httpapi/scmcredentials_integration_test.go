//go:build integration

package httpapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

// sha256Hex mirrors wshub.HashSandboxToken's own algorithm exactly (SHA-256,
// hex-encoded) -- duplicated here rather than imported since this is a
// small, dependency-free test helper and importing wshub purely for one
// hash call in a test would be a heavier dependency than necessary.
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// scmCredentialsResponse mirrors internal/adapters/inbound/httpapi's own
// (unexported) response shape for this test's own decode target.
type scmCredResponse struct {
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// createSandboxWithToken creates a sandbox row for sessionID and sets its
// token_hash to HashToken-equivalent of plaintextToken -- mirrors
// wshub.HashSandboxToken's own algorithm (SHA-256 hex) directly (this test
// package cannot import wshub's unexported internals, and importing the
// whole package just for this one hash in a test is unnecessary -- the
// algorithm is trivial and stable).
func createSandboxWithToken(ctx context.Context, t *testing.T, r testRig, sessionID pgtype.UUID, plaintextToken string) {
	t.Helper()
	if _, err := r.sandboxes.Create(ctx, sessionID); err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	hash := sha256Hex(plaintextToken)
	if _, err := r.pool.Exec(ctx, `UPDATE sandboxes SET token_hash = $2 WHERE session_id = $1`, sessionID, hash); err != nil {
		t.Fatalf("set token_hash: %v", err)
	}
}

// createSessionWithGitHubIdentity creates a session whose created_by user
// has a real, encrypted GitHub access token -- the SAME real
// EncryptToken flow Step 20's own OAuth callback uses, not a shortcut.
func createSessionWithGitHubIdentity(ctx context.Context, t *testing.T, r testRig, plaintextAccessToken string) sqlcgen.Session {
	t.Helper()

	externalID := fmt.Sprintf("scm-test-github-id-%d", time.Now().UnixNano())
	user, err := r.users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: externalID + "@example.com",
		DisplayName:  "SCM Test User",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	encrypted, err := platform.EncryptToken(r.tokenEncryptionKey, []byte(plaintextAccessToken))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}

	email := externalID + "@example.com"
	if _, err := r.identities.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID:               user.ID,
		Provider:             sqlcgen.IdentityProviderGithub,
		ExternalID:           externalID,
		Email:                &email,
		EmailVerified:        true,
		LinkedVia:            sqlcgen.IdentityLinkedViaAdmin,
		AccessTokenEncrypted: encrypted,
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}

	session, err := r.sessions.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
		CreatedBy:   user.ID,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return session
}

func postScmCredentials(t *testing.T, r testRig, sessionID string, bearer string) (int, scmCredResponse) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/sessions/"+sessionID+"/scm-credentials",
		strings.NewReader(`{"host":"github.com"}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var got scmCredResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// TestScmCredentials_Success proves the full real flow: a valid sandbox
// bearer token + a session whose user has a real, encrypted GitHub access
// token -> 200 with a credential whose password decrypts to the SAME
// plaintext a real Step-20 OAuth flow would have encrypted.
func TestScmCredentials_Success(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	const plaintextAccessToken = "gho_realGitHubAccessToken"
	session := createSessionWithGitHubIdentity(ctx, t, rig, plaintextAccessToken)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	before := time.Now()
	status, got := postScmCredentials(t, rig, session.ID.String(), "sandbox-bearer-token")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Username != "x-access-token" {
		t.Errorf("Username = %q, want %q", got.Username, "x-access-token")
	}
	if got.Password != plaintextAccessToken {
		t.Errorf("Password = %q, want %q (must decrypt to the SAME plaintext originally encrypted)", got.Password, plaintextAccessToken)
	}
	wantExpiry := before.Add(platform.DefaultTimeouts().ScmCredentialTTL)
	if got.ExpiresAt.Sub(wantExpiry).Abs() > time.Minute {
		t.Errorf("ExpiresAt = %v, want close to %v", got.ExpiresAt, wantExpiry)
	}
}

// TestScmCredentials_MissingBearer proves a missing Authorization header
// is 401.
func TestScmCredentials_MissingBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := createSessionWithGitHubIdentity(ctx, t, rig, "gho_x")
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postScmCredentials(t, rig, session.ID.String(), "")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// TestScmCredentials_InvalidBearer proves a wrong (but present) bearer
// token is 401.
func TestScmCredentials_InvalidBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := createSessionWithGitHubIdentity(ctx, t, rig, "gho_x")
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postScmCredentials(t, rig, session.ID.String(), "totally-wrong-token")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// TestScmCredentials_UnknownSession proves a well-formed but nonexistent
// session id is 404.
func TestScmCredentials_UnknownSession(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postScmCredentials(t, rig, "11111111-1111-1111-1111-111111111111", "any-token")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestScmCredentials_MalformedSessionID proves a malformed session id is
// ALSO 404 -- mirrors wshub/sandbox.go's own "malformed and nonexistent
// both mean no such session" precedent (this caller is sandbox-agent
// code, never a browser).
func TestScmCredentials_MalformedSessionID(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postScmCredentials(t, rig, "not-a-uuid", "any-token")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

// TestScmCredentials_NoCreatedBy proves a session with no created_by user
// (created_by NULL) -> 403, the honest "no bot fallback" gap.
func TestScmCredentials_NoCreatedBy(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postScmCredentials(t, rig, session.ID.String(), "sandbox-bearer-token")
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestScmCredentials_NoGitHubIdentity proves a session whose user has no
// linked GitHub identity at all -> 403.
func TestScmCredentials_NoGitHubIdentity(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	user, err := rig.users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "no-github@example.com", DisplayName: "No GitHub", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	session, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb, CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postScmCredentials(t, rig, session.ID.String(), "sandbox-bearer-token")
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}

// TestScmCredentials_NilTokenHash_Rejected proves this endpoint's own
// bearer check (verifySandboxBearerToken) does NOT inherit wshub's own
// WS-handshake nil-token_hash bypass ("accept any non-empty presented
// token while token_hash is NULL") -- a sandbox row with no token_hash
// ever set (created directly via sandboxes.Create, mirroring a legacy or
// not-yet-minted row) must reject EVERY presented bearer token with 401,
// never fall through to a real, decrypted credential. Confirmed
// unreachable via any real spawn path this Step's own code takes (which
// always sets token_hash), but this test locks in the deliberately
// stricter behavior regardless -- see verifySandboxBearerToken's own doc
// comment for why this endpoint cannot afford to inherit that bypass.
func TestScmCredentials_NilTokenHash_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := createSessionWithGitHubIdentity(ctx, t, rig, "gho_realGitHubAccessToken")
	if _, err := rig.sandboxes.Create(ctx, session.ID); err != nil {
		t.Fatalf("create sandbox (token_hash left NULL): %v", err)
	}

	status, _ := postScmCredentials(t, rig, session.ID.String(), "any-non-empty-token")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d (a nil token_hash must reject, never bypass, on this endpoint)", status, http.StatusUnauthorized)
	}
}

// TestScmCredentials_TamperedCiphertext proves a real, tampered/corrupted
// access_token_encrypted value (AES-GCM's own authentication tag catching
// the tamper at platform.DecryptToken time, not a mocked failure) hits the
// SAME 403 "no usable credential" branch as the other sub-cases in this
// outcome class -- this sub-case was previously confirmed only by code
// inspection, never independently exercised by a real corrupted ciphertext
// flowing through the real DecryptToken call.
func TestScmCredentials_TamperedCiphertext(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	session := createSessionWithGitHubIdentity(ctx, t, rig, "gho_realGitHubAccessToken")
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	// Corrupt the stored ciphertext directly, in place -- a real tampered
	// value, not a mock -- so DecryptToken's own AES-GCM authentication
	// check must be what catches this, exactly like this file's own
	// tokenencrypt.go doc comment promises ("Returns an error... if
	// ciphertext was tampered with in any way").
	if _, err := rig.pool.Exec(ctx,
		`UPDATE identities SET access_token_encrypted = access_token_encrypted || '\xff'::bytea WHERE user_id = $1`,
		session.CreatedBy,
	); err != nil {
		t.Fatalf("corrupt access_token_encrypted: %v", err)
	}

	status, _ := postScmCredentials(t, rig, session.ID.String(), "sandbox-bearer-token")
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (tampered ciphertext must hit the same no-usable-credential branch as every other sub-case)", status, http.StatusForbidden)
	}
}

// TestScmCredentials_NoStoredToken proves an identity with no
// access_token_encrypted at all -> 403.
func TestScmCredentials_NoStoredToken(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()

	user, err := rig.users.Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: "no-token@example.com", DisplayName: "No Token", Role: sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	email := "no-token@example.com"
	if _, err := rig.identities.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID: user.ID, Provider: sqlcgen.IdentityProviderGithub, ExternalID: "ext-no-token",
		Email: &email, EmailVerified: true, LinkedVia: sqlcgen.IdentityLinkedViaAdmin,
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}
	session, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb, CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postScmCredentials(t, rig, session.ID.String(), "sandbox-bearer-token")
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d", status, http.StatusForbidden)
	}
}
