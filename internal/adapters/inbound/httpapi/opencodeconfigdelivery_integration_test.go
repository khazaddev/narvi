//go:build integration

// Integration tests for §27.1's own ("sandbox secrets & opencode
// config", §27.2) CP-side sandbox-facing delivery endpoint
// (opencodeconfigdelivery.go), against a real Postgres instance --
// sharing this package's own testRig (httpapi_integration_test.go) and
// scmcredentials_integration_test.go's own createSandboxWithToken/
// moveSandboxStatus/bumpSandboxGen helpers, mirroring
// sandboxsecretsdelivery_integration_test.go's own handshake test shapes
// (§27.2: "delivered at boot over a sibling sandbox-facing endpoint, same
// handshake").
package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

type openCodeConfigDeliveryResp struct {
	Global      json.RawMessage `json:"global,omitempty"`
	Environment json.RawMessage `json:"environment,omitempty"`
}

func postOpenCodeConfig(t *testing.T, r testRig, sessionID, bearer, gen string) (int, openCodeConfigDeliveryResp) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, r.server.URL+"/sessions/"+sessionID+"/opencode-config", strings.NewReader(``))
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

	var got openCodeConfigDeliveryResp
	_ = json.NewDecoder(resp.Body).Decode(&got)
	return resp.StatusCode, got
}

const openCodeConfigDeliveryRepos = `[{"name":"widgets","url":"https://github.com/acme/widgets","branch":null}]`

// --- Auth / dead-sandbox / gen-fencing ---

func TestOpenCodeConfigDelivery_MissingBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, openCodeConfigDeliveryRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postOpenCodeConfig(t, rig, session.ID.String(), "", "1")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestOpenCodeConfigDelivery_UnknownSession(t *testing.T) {
	rig := newTestRig(t)

	status, _ := postOpenCodeConfig(t, rig, "11111111-1111-1111-1111-111111111111", "any-token", "1")
	if status != http.StatusNotFound {
		t.Errorf("status = %d, want %d", status, http.StatusNotFound)
	}
}

func TestOpenCodeConfigDelivery_DeadSandboxStatus(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, openCodeConfigDeliveryRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")
	moveSandboxStatus(ctx, t, rig, session.ID, sqlcgen.SandboxStatusStopped)

	status, _ := postOpenCodeConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusGone {
		t.Errorf("status = %d, want %d (dead sandbox status)", status, http.StatusGone)
	}
}

func TestOpenCodeConfigDelivery_GenMismatch_Rejected(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, openCodeConfigDeliveryRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token") // gen 1

	status, _ := postOpenCodeConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "999")
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want %d (gen mismatch)", status, http.StatusForbidden)
	}
}

func TestOpenCodeConfigDelivery_InvalidBearer(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, openCodeConfigDeliveryRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, _ := postOpenCodeConfig(t, rig, session.ID.String(), "totally-wrong-token", "1")
	if status != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

// --- Resolution correctness: both scopes at once, never a single winner ---

func TestOpenCodeConfigDelivery_NothingConfigured_BothAbsent(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, openCodeConfigDeliveryRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	status, got := postOpenCodeConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Global) != 0 || len(got.Environment) != 0 {
		t.Errorf("got = %+v, want both absent", got)
	}
}

func TestOpenCodeConfigDelivery_GlobalOnly(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, openCodeConfigDeliveryRepos, false)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	if _, err := rig.openCodeConfigs.UpsertGlobal(ctx, []byte(`{"autoupdate":true}`)); err != nil {
		t.Fatalf("UpsertGlobal: %v", err)
	}

	status, got := postOpenCodeConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Global) == 0 {
		t.Fatal("Global is absent, want the configured document")
	}
	var doc map[string]any
	if err := json.Unmarshal(got.Global, &doc); err != nil {
		t.Fatalf("unmarshal global: %v", err)
	}
	if doc["autoupdate"] != true {
		t.Errorf("Global doc = %+v, want autoupdate=true", doc)
	}
	if len(got.Environment) != 0 {
		t.Errorf("Environment = %s, want absent (no environment attached to this session)", got.Environment)
	}
}

// TestOpenCodeConfigDelivery_BothScopesAtOnce is the key behavioral proof
// of §27.2's own "delivered at boot... both scopes at once" design: with
// BOTH a global AND this session's own environment document configured,
// the response carries BOTH, simultaneously -- never narrowed to one
// winner the way sandbox_secrets/provider_credentials resolve.
func TestOpenCodeConfigDelivery_BothScopesAtOnce(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, openCodeConfigDeliveryRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	if _, err := rig.openCodeConfigs.UpsertGlobal(ctx, []byte(`{"autoupdate":true}`)); err != nil {
		t.Fatalf("UpsertGlobal: %v", err)
	}
	environmentID := session.EnvironmentID.String()
	if _, err := rig.openCodeConfigs.UpsertEnvironment(ctx, environmentID, []byte(`{"model":"anthropic/claude"}`)); err != nil {
		t.Fatalf("UpsertEnvironment: %v", err)
	}

	status, got := postOpenCodeConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Global) == 0 {
		t.Error("Global is absent, want the configured document (both scopes must be delivered together)")
	}
	if len(got.Environment) == 0 {
		t.Error("Environment is absent, want the configured document (both scopes must be delivered together)")
	}
}

// TestOpenCodeConfigDelivery_OtherEnvironment_NotMatched proves an
// environment-scoped document configured for a DIFFERENT Environment
// never leaks into this session's own response.
func TestOpenCodeConfigDelivery_OtherEnvironment_NotMatched(t *testing.T) {
	rig := newTestRig(t)
	ctx := context.Background()
	session := rig.createSessionWithReposAndEnvironment(ctx, t, openCodeConfigDeliveryRepos, true)
	createSandboxWithToken(ctx, t, rig, session.ID, "sandbox-bearer-token")

	otherEnv, err := rig.environments.Create(ctx, sqlcgen.CreateEnvironmentParams{PathScope: []byte(`[]`)})
	if err != nil {
		t.Fatalf("create other environment: %v", err)
	}
	if _, err := rig.openCodeConfigs.UpsertEnvironment(ctx, otherEnv.ID.String(), []byte(`{"model":"should-not-be-delivered"}`)); err != nil {
		t.Fatalf("UpsertEnvironment (other): %v", err)
	}

	status, got := postOpenCodeConfig(t, rig, session.ID.String(), "sandbox-bearer-token", "1")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Environment) != 0 {
		t.Errorf("Environment = %s, want absent (this session's own environment has nothing configured)", got.Environment)
	}
}
