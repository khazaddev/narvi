//go:build integration

// Integration tests for Step 53's own ("provider credential injection",
// §25.1/§25.3) CP-side sandbox-facing delivery endpoint
// (providercredentialsdelivery.go), against a real Postgres instance --
// sharing this package's own testRig (httpapi_integration_test.go) and
// scmcredentials_integration_test.go's own createSandboxWithToken/
// moveSandboxStatus/bumpSandboxGen helpers (same package, same auth/dead-
// sandbox/gen-fencing shape being proven here).
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

// providerCredsResponse mirrors internal/adapters/inbound/httpapi's own
// (unexported) providerCredentialsResponse for this test's own decode
// target -- same convention scmCredResponse already establishes in this
// package for the SCM case.
type providerCredsResponse struct {
	Credentials map[string]string `json:"credentials"`
}

// postProviderCredentials posts to the real delivery route with the given
// bearer/gen -- gen is omitted entirely (no X-Sandbox-Gen header at all)
// when gen == "", matching postScmCredentialsFull's own identical
// convention.
func postProviderCredentials(t *testing.T, r testRig, sessionID, bearer, gen string) (int, providerCredsResponse) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/sessions/"+sessionID+"/provider-credentials", strings.NewReader(``))
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

	var got providerCredsResponse
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

// createSessionWithReposAndEnvironment creates a session naming reposJSON
// (the SAME {name,url,branch} JSONB shape sessions.repos already uses)
// and, if environmentID is non-zero-value, a real environments row that
// session.environment_id references (sessions.environment_id carries a
// real FK to environments(id), so a real row must exist first).
func (r testRig) createSessionWithReposAndEnvironment(ctx context.Context, t *testing.T, reposJSON string, withEnvironment bool) sqlcgen.Session {
	t.Helper()

	params := sqlcgen.CreateSessionParams{
		SpawnSource: sqlcgen.SessionSpawnSourceWeb,
		Repos:       []byte(reposJSON),
	}
	if withEnvironment {
		env, err := r.environments.Create(ctx, sqlcgen.CreateEnvironmentParams{
			PathScope:      []byte(`[]`),
			MockConfigured: false,
		})
		if err != nil {
			t.Fatalf("create environment: %v", err)
		}
		params.EnvironmentID = env.ID
	}

	session, err := r.sessions.Create(ctx, params)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return session
}

const providerCredsRepos = `[{"name":"widgets","url":"https://github.com/acme/widgets","branch":null}]`

// --- Auth / dead-sandbox / gen-fencing (mirrors scmcredentials_integration_test.go) ---

func TestProviderCredentialsDelivery_MissingBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postProviderCredentials(t, rig, session.ID.String(), "", "1")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestProviderCredentialsDelivery_InvalidBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postProviderCredentials(t, rig, session.ID.String(), "totally-wrong-token", "1")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestProviderCredentialsDelivery_UnknownSession(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postProviderCredentials(t, rig, "11111111-1111-1111-1111-111111111111", "any-token", "1")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestProviderCredentialsDelivery_MalformedSessionID(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postProviderCredentials(t, rig, "not-a-uuid", "any-token", "1")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestProviderCredentialsDelivery_DeadSandboxStatus(t *testing.T) {
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
			session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
			createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
			moveSandboxStatus(ctx, t, rig, session.ID, tc.status)

			status, _ := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
			if status != http.StatusGone {
				t.Errorf("status = %d, want %d (dead sandbox status %s)", status, http.StatusGone, tc.status)
			}
		})
	}
}

func TestProviderCredentialsDelivery_SuspectSandbox_NotDead(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	moveSandboxStatus(ctx, t, rig, session.ID, sqlcgen.SandboxStatusSuspect)

	status, _ := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Errorf("status = %d, want %d (Suspect is not a dead status)", status, http.StatusOK)
	}
}

func TestProviderCredentialsDelivery_GenMismatch_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token") // gen 1

	status, _ := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "999")
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (gen mismatch)", status, http.StatusForbidden)
	}
}

func TestProviderCredentialsDelivery_MissingGen_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "" /* no X-Sandbox-Gen header at all */)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (missing X-Sandbox-Gen header)", status, http.StatusForbidden)
	}
}

func TestProviderCredentialsDelivery_CorrectCurrentGen_Succeeds(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token") // gen 1
	bumped := bumpSandboxGen(ctx, t, rig, session.ID, "respawned-bearer-token")

	if bumped.Gen != 2 {
		t.Fatalf("bumped.Gen = %d, want 2", bumped.Gen)
	}

	status, _ := postProviderCredentials(t, rig, session.ID.String(), "respawned-bearer-token", "2")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d (correct current gen)", status, http.StatusOK)
	}
}

// --- Resolution correctness (the actual point of this Step) ---

// TestProviderCredentialsDelivery_NoCredentialsConfigured_EmptyMap proves
// the overwhelming common case (zero rows configured anywhere) degrades
// to a plain empty map, never an error -- matching this endpoint's own
// doc comment.
func TestProviderCredentialsDelivery_NoCredentialsConfigured_EmptyMap(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, got := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Credentials) != 0 {
		t.Errorf("Credentials = %v, want empty", got.Credentials)
	}
}

// TestProviderCredentialsDelivery_GlobalOnly_Resolves proves a single
// global-scoped credential resolves when nothing more specific exists.
func TestProviderCredentialsDelivery_GlobalOnly_Resolves(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	if _, err := rig.providerCredentials.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil, sqlcgen.ProviderCredentialProviderAnthropic, encryptForTest(t, rig, "global-anthropic-key")); err != nil {
		t.Fatalf("create global credential: %v", err)
	}

	status, got := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Credentials["anthropic"] != "global-anthropic-key" {
		t.Errorf("Credentials[anthropic] = %q, want %q", got.Credentials["anthropic"], "global-anthropic-key")
	}
}

// TestProviderCredentialsDelivery_RepoBeatsGlobal proves a repo-scoped
// credential for the session's own repo wins over a global one, for the
// SAME provider.
func TestProviderCredentialsDelivery_RepoBeatsGlobal(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	if _, err := rig.providerCredentials.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil, sqlcgen.ProviderCredentialProviderOpenai, encryptForTest(t, rig, "global-openai-key")); err != nil {
		t.Fatalf("create global credential: %v", err)
	}
	repoFullName := "acme/widgets"
	if _, err := rig.providerCredentials.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, &repoFullName, sqlcgen.ProviderCredentialProviderOpenai, encryptForTest(t, rig, "repo-openai-key")); err != nil {
		t.Fatalf("create repo credential: %v", err)
	}

	status, got := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Credentials["openai"] != "repo-openai-key" {
		t.Errorf("Credentials[openai] = %q, want %q (repo must beat global)", got.Credentials["openai"], "repo-openai-key")
	}
}

// TestProviderCredentialsDelivery_EnvironmentBeatsRepo proves an
// environment-scoped credential wins over a repo-scoped one for the SAME
// provider -- the doubly-confirmed, non-paraphrase resolution order this
// Step's own domain package (internal/domain/providercredential) settled
// on (see that package's own doc.go): environment is MORE specific than
// repo, not less.
func TestProviderCredentialsDelivery_EnvironmentBeatsRepo(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	repoFullName := "acme/widgets"
	if _, err := rig.providerCredentials.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, &repoFullName, sqlcgen.ProviderCredentialProviderGoogle, encryptForTest(t, rig, "repo-google-key")); err != nil {
		t.Fatalf("create repo credential: %v", err)
	}
	environmentID := session.EnvironmentID.String()
	if _, err := rig.providerCredentials.Create(ctx, sqlcgen.ProviderCredentialScopeEnvironment, &environmentID, sqlcgen.ProviderCredentialProviderGoogle, encryptForTest(t, rig, "env-google-key")); err != nil {
		t.Fatalf("create environment credential: %v", err)
	}

	status, got := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Credentials["google"] != "env-google-key" {
		t.Errorf("Credentials[google] = %q, want %q (environment must beat repo)", got.Credentials["google"], "env-google-key")
	}
}

// TestProviderCredentialsDelivery_MultipleProviders_EachResolvedIndependently
// proves 3 independent providers each resolve their own winner
// independently in one call -- google via environment, anthropic via
// repo, openai via global, all in the SAME response.
func TestProviderCredentialsDelivery_MultipleProviders_EachResolvedIndependently(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	environmentID := session.EnvironmentID.String()
	repoFullName := "acme/widgets"
	if _, err := rig.providerCredentials.Create(ctx, sqlcgen.ProviderCredentialScopeEnvironment, &environmentID, sqlcgen.ProviderCredentialProviderGoogle, encryptForTest(t, rig, "env-google-key")); err != nil {
		t.Fatalf("create environment google credential: %v", err)
	}
	if _, err := rig.providerCredentials.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, &repoFullName, sqlcgen.ProviderCredentialProviderAnthropic, encryptForTest(t, rig, "repo-anthropic-key")); err != nil {
		t.Fatalf("create repo anthropic credential: %v", err)
	}
	if _, err := rig.providerCredentials.Create(ctx, sqlcgen.ProviderCredentialScopeGlobal, nil, sqlcgen.ProviderCredentialProviderOpenai, encryptForTest(t, rig, "global-openai-key")); err != nil {
		t.Fatalf("create global openai credential: %v", err)
	}

	status, got := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if got.Credentials["google"] != "env-google-key" {
		t.Errorf("Credentials[google] = %q, want %q", got.Credentials["google"], "env-google-key")
	}
	if got.Credentials["anthropic"] != "repo-anthropic-key" {
		t.Errorf("Credentials[anthropic] = %q, want %q", got.Credentials["anthropic"], "repo-anthropic-key")
	}
	if got.Credentials["openai"] != "global-openai-key" {
		t.Errorf("Credentials[openai] = %q, want %q", got.Credentials["openai"], "global-openai-key")
	}
	if len(got.Credentials) != 3 {
		t.Errorf("len(Credentials) = %d, want 3", len(got.Credentials))
	}
}

// TestProviderCredentialsDelivery_OtherRepo_NotMatched proves a
// repo-scoped credential configured for a DIFFERENT repo than the
// session's own never leaks into this session's resolution.
func TestProviderCredentialsDelivery_OtherRepo_NotMatched(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, providerCredsRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	otherRepo := "acme/other-repo-entirely"
	if _, err := rig.providerCredentials.Create(ctx, sqlcgen.ProviderCredentialScopeRepo, &otherRepo, sqlcgen.ProviderCredentialProviderAnthropic, encryptForTest(t, rig, "other-repo-key")); err != nil {
		t.Fatalf("create other-repo credential: %v", err)
	}

	status, got := postProviderCredentials(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if _, present := got.Credentials["anthropic"]; present {
		t.Errorf("Credentials[anthropic] present = %v, want absent (that repo credential belongs to a different repo)", got.Credentials["anthropic"])
	}
}

// encryptForTest is a small local helper -- EncryptToken under rig's own
// tokenEncryptionKey, failing the test on error.
func encryptForTest(t *testing.T, rig testRig, plaintext string) []byte {
	t.Helper()
	encrypted, err := platform.EncryptToken(rig.tokenEncryptionKey, []byte(plaintext))
	if err != nil {
		t.Fatalf("EncryptToken: %v", err)
	}
	return encrypted
}
