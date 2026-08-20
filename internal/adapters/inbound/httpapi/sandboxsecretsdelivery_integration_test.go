//go:build integration

// Integration tests for §27.1's own ("sandbox secrets & opencode
// config", §27.1) CP-side sandbox-facing delivery endpoint
// (sandboxsecretsdelivery.go), against a real Postgres instance --
// sharing this package's own testRig (httpapi_integration_test.go) and
// scmcredentials_integration_test.go's own createSandboxWithToken/
// moveSandboxStatus/bumpSandboxGen helpers, mirroring
// providercredentialsdelivery_integration_test.go's own test shapes
// exactly (§27.1: "mirrors providercredentialsdelivery.go's own handshake
// verbatim").
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/platform"
)

type sandboxSecretsResp struct {
	Secrets map[string]string `json:"secrets"`
}

func postSandboxSecrets(t *testing.T, r testRig, sessionID, bearer, gen string) (int, sandboxSecretsResp) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/sessions/"+sessionID+"/sandbox-secrets", strings.NewReader(``))
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

	var got sandboxSecretsResp
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// encryptSecretForTest mirrors encryptForTest (providercredentialsdelivery_
// integration_test.go) exactly, named separately purely for this file's
// own readability at call sites that also touch provider credentials in
// the same test (none currently do, but mirrors that file's own
// precedent of a small, locally named helper).
func encryptSecretForTest(t *testing.T, rig testRig, plaintext string) []byte {
	t.Helper()
	encrypted, err := platform.EncryptToken(rig.tokenEncryptionKey, []byte(plaintext))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	return encrypted
}

const sandboxSecretsRepos = `[{"name":"widgets","url":"https://github.com/acme/widgets","branch":null}]`

// --- Auth / dead-sandbox / gen-fencing (mirrors providercredentialsdelivery_integration_test.go) ---

func TestSandboxSecretsDelivery_MissingBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postSandboxSecrets(t, rig, session.ID.String(), "", "1")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestSandboxSecretsDelivery_InvalidBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postSandboxSecrets(t, rig, session.ID.String(), "totally-wrong-token", "1")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestSandboxSecretsDelivery_UnknownSession(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postSandboxSecrets(t, rig, "11111111-1111-1111-1111-111111111111", "any-token", "1")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestSandboxSecretsDelivery_MalformedSessionID(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postSandboxSecrets(t, rig, "not-a-uuid", "any-token", "1")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestSandboxSecretsDelivery_DeadSandboxStatus(t *testing.T) {
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
			session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
			createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
			moveSandboxStatus(ctx, t, rig, session.ID, tc.status)

			status, _ := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
			if status != http.StatusGone {
				t.Errorf("status = %d, want %d (dead sandbox status %s)", status, http.StatusGone, tc.status)
			}
		})
	}
}

func TestSandboxSecretsDelivery_SuspectSandbox_NotDead(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	moveSandboxStatus(ctx, t, rig, session.ID, sqlcgen.SandboxStatusSuspect)

	status, _ := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d (Suspect is not a dead status)", status, http.StatusOK)
	}
}

func TestSandboxSecretsDelivery_GenMismatch_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token") // gen 1

	status, _ := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "999")
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (gen mismatch)", status, http.StatusForbidden)
	}
}

func TestSandboxSecretsDelivery_MissingGen_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "" /* no X-Sandbox-Gen header at all */)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (missing X-Sandbox-Gen header)", status, http.StatusForbidden)
	}
}

func TestSandboxSecretsDelivery_CorrectCurrentGen_Succeeds(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token") // gen 1
	bumped := bumpSandboxGen(ctx, t, rig, session.ID, "respawned-bearer-token")

	if bumped.Gen != 2 {
		t.Fatalf("bumped.Gen = %d, want 2", bumped.Gen)
	}

	status, _ := postSandboxSecrets(t, rig, session.ID.String(), "respawned-bearer-token", "2")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (correct current gen)", status, http.StatusOK)
	}
}

// --- Resolution correctness ---

func TestSandboxSecretsDelivery_NoSecretsConfigured_EmptyMap(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, got := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Secrets) != 0 {
		t.Errorf("Secrets = %v, want empty", got.Secrets)
	}
}

func TestSandboxSecretsDelivery_GlobalOnly_Resolves(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "MY_SECRET", encryptSecretForTest(t, rig, "global-value")); err != nil {
		t.Fatalf("create global secret: %v", err)
	}

	status, got := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Secrets["MY_SECRET"] != "global-value" {
		t.Errorf("Secrets[MY_SECRET] = %q, want %q", got.Secrets["MY_SECRET"], "global-value")
	}
}

func TestSandboxSecretsDelivery_RepoBeatsGlobal(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "MY_SECRET", encryptSecretForTest(t, rig, "global-value")); err != nil {
		t.Fatalf("create global secret: %v", err)
	}
	repoFullName := "acme/widgets"
	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeRepo, &repoFullName, "MY_SECRET", encryptSecretForTest(t, rig, "repo-value")); err != nil {
		t.Fatalf("create repo secret: %v", err)
	}

	status, got := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Secrets["MY_SECRET"] != "repo-value" {
		t.Errorf("Secrets[MY_SECRET] = %q, want %q (repo must beat global)", got.Secrets["MY_SECRET"], "repo-value")
	}
}

func TestSandboxSecretsDelivery_EnvironmentBeatsRepo(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	repoFullName := "acme/widgets"
	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeRepo, &repoFullName, "MY_SECRET", encryptSecretForTest(t, rig, "repo-value")); err != nil {
		t.Fatalf("create repo secret: %v", err)
	}
	envID := session.EnvironmentID.String()
	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeEnvironment, &envID, "MY_SECRET", encryptSecretForTest(t, rig, "env-value")); err != nil {
		t.Fatalf("create environment secret: %v", err)
	}

	status, got := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Secrets["MY_SECRET"] != "env-value" {
		t.Errorf("Secrets[MY_SECRET] = %q, want %q (environment must beat repo)", got.Secrets["MY_SECRET"], "env-value")
	}
}

// TestSandboxSecretsDelivery_OtherRepo_NotMatched proves a repo-scoped
// secret for a repo NOT named by this session never resolves for it.
func TestSandboxSecretsDelivery_OtherRepo_NotMatched(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	otherRepo := "acme/other"
	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeRepo, &otherRepo, "MY_SECRET", encryptSecretForTest(t, rig, "other-repo-value")); err != nil {
		t.Fatalf("create other repo secret: %v", err)
	}

	status, got := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if _, present := got.Secrets["MY_SECRET"]; present {
		t.Errorf("Secrets[MY_SECRET] present = %v, want absent (this session never names acme/other)", got.Secrets["MY_SECRET"])
	}
}

// --- Decrypt-only-winners (§25.1's discipline, reused unchanged) ---

// TestSandboxSecretsDelivery_CorruptedLoser_NeverBlocksWinner is the
// direct behavioral proof of "decrypt-only-the-winner": a GLOBAL row's
// ciphertext is deliberately corrupted (not valid AES-GCM output at all)
// but is SHADOWED by a valid repo-scoped row for the SAME name -- if this
// endpoint ever decrypted every candidate rather than only the winner, the
// corrupted global row's decrypt failure would either surface as a 500 or
// at minimum get logged; the winning repo value must resolve correctly
// regardless, proving the loser's ciphertext is never touched.
func TestSandboxSecretsDelivery_CorruptedLoser_NeverBlocksWinner(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	// Deliberately corrupt, not merely absent-key ciphertext -- garbage
	// bytes that DecryptToken's own AES-GCM authentication tag check will
	// reject outright.
	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "MY_SECRET", []byte("not-valid-aes-gcm-ciphertext")); err != nil {
		t.Fatalf("create corrupted global secret: %v", err)
	}
	repoFullName := "acme/widgets"
	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeRepo, &repoFullName, "MY_SECRET", encryptSecretForTest(t, rig, "valid-repo-value")); err != nil {
		t.Fatalf("create valid repo secret: %v", err)
	}

	status, got := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (a corrupted LOSING candidate must never fail the whole request)", status, http.StatusOK)
	}
	if got.Secrets["MY_SECRET"] != "valid-repo-value" {
		t.Errorf("Secrets[MY_SECRET] = %q, want %q (winner must resolve regardless of the corrupted loser)", got.Secrets["MY_SECRET"], "valid-repo-value")
	}
}

// TestSandboxSecretsDelivery_CorruptedWinner_OmittedNotFatal proves the
// OTHER half: when the WINNING candidate itself is corrupted (here, the
// only candidate at all), the whole request still succeeds (200), simply
// omitting that one name from the response -- never a 500 for the whole
// request.
func TestSandboxSecretsDelivery_CorruptedWinner_OmittedNotFatal(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, sandboxSecretsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "CORRUPTED_SECRET", []byte("not-valid-aes-gcm-ciphertext")); err != nil {
		t.Fatalf("create corrupted global secret: %v", err)
	}
	// A second, independently valid secret proves isolation: the
	// corrupted row must not deny every OTHER secret either.
	if _, err := rig.sandboxSecrets.Create(ctx, sqlcgen.SandboxSecretScopeGlobal, nil, "HEALTHY_SECRET", encryptSecretForTest(t, rig, "healthy-value")); err != nil {
		t.Fatalf("create healthy global secret: %v", err)
	}

	status, got := postSandboxSecrets(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (a corrupted WINNING candidate must still be a 200, never a 500)", status, http.StatusOK)
	}
	if _, present := got.Secrets["CORRUPTED_SECRET"]; present {
		t.Errorf("Secrets[CORRUPTED_SECRET] present, want omitted (decrypt failed)")
	}
	if got.Secrets["HEALTHY_SECRET"] != "healthy-value" {
		t.Errorf("Secrets[HEALTHY_SECRET] = %q, want %q (must resolve independently of the corrupted sibling)", got.Secrets["HEALTHY_SECRET"], "healthy-value")
	}
}
