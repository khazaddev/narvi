package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
)

const testSandboxSecretsFetchTimeout = 5 * time.Second

// wsEquivalentForTest converts an httptest.Server's own http:// URL into
// the ws:// shape boot.Config.SessionConfig.ControlPlaneWsUrl carries in
// production (credentials.NewCPClient itself derives the reverse) --
// mirrors internal/sandboxagent/credentials' own identical test helper
// (unexported there, so re-declared here rather than exported solely for
// this package's own tests to import).
func wsEquivalentForTest(httpURL string) string {
	return "ws" + strings.TrimPrefix(httpURL, "http") + "/sessions/sess-1/ws?type=sandbox"
}

// TestApplySandboxSecretEnv_SetsEachNameValuePair proves the real
// mechanism this whole feature depends on: os.Setenv onto sandbox-agent's
// own process environment, so a LATER supervisor.EnvWithout call picks it
// up automatically. Not t.Parallel() -- mutates real process environment
// variables.
func TestApplySandboxSecretEnv_SetsEachNameValuePair(t *testing.T) {
	t.Setenv("MY_FIRST_SECRET", "") // pre-register with t.Setenv so cleanup is automatic
	t.Setenv("MY_SECOND_SECRET", "")

	got := applySandboxSecretEnv(map[string]string{
		"MY_FIRST_SECRET":  "value-one",
		"MY_SECOND_SECRET": "value-two",
	})

	wantNames := []string{"MY_FIRST_SECRET", "MY_SECOND_SECRET"}
	if len(got) != len(wantNames) {
		t.Fatalf("applySandboxSecretEnv() returned names = %v, want %v", got, wantNames)
	}
	for i, name := range wantNames {
		if got[i] != name {
			t.Errorf("applySandboxSecretEnv() returned names[%d] = %q, want %q (sorted)", i, got[i], name)
		}
	}

	if v := os.Getenv("MY_FIRST_SECRET"); v != "value-one" {
		t.Errorf("os.Getenv(MY_FIRST_SECRET) = %q, want %q", v, "value-one")
	}
	if v := os.Getenv("MY_SECOND_SECRET"); v != "value-two" {
		t.Errorf("os.Getenv(MY_SECOND_SECRET) = %q, want %q", v, "value-two")
	}
}

// TestApplySandboxSecretEnv_EmptyMapIsNoop proves the overwhelming common
// case (nothing configured for this session) touches nothing.
func TestApplySandboxSecretEnv_EmptyMapIsNoop(t *testing.T) {
	got := applySandboxSecretEnv(nil)
	if got != nil {
		t.Errorf("applySandboxSecretEnv(nil) = %v, want nil", got)
	}
	got = applySandboxSecretEnv(map[string]string{})
	if got != nil {
		t.Errorf("applySandboxSecretEnv({}) = %v, want nil", got)
	}
}

// TestFetchSandboxSecrets_RealRoundTrip proves fetchSandboxSecrets' own
// thin wrapper actually reaches a real (test) CP server and returns its
// resolved map -- the underlying CPClient.FetchSandboxSecrets method's
// own wire-shape correctness is covered exhaustively in
// internal/sandboxagent/credentials' own test suite; this test covers
// only this package's own additional wrapping (building the client from
// boot.Config, logging, degrade-on-failure).
func TestFetchSandboxSecrets_RealRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": map[string]string{"MY_SECRET": "real-value"}})
	}))
	defer server.Close()

	cfg := boot.Config{
		SessionConfig: &sessionconfig.SessionConfig{
			ControlPlaneWsUrl: wsEquivalentForTest(server.URL),
			SessionId:         "sess-1",
			SandboxToken:      "tok",
			Gen:               1,
		},
	}

	got := fetchSandboxSecrets(context.Background(), cfg, testSandboxSecretsFetchTimeout)
	if got["MY_SECRET"] != "real-value" {
		t.Errorf("fetchSandboxSecrets() = %v, want map containing MY_SECRET=real-value", got)
	}
}

// TestFetchSandboxSecrets_DegradesToNilOnFailure proves the whole "warn
// and continue" posture directly: a CP server returning an error must
// degrade to nil, never a panic or a propagated error the caller would
// have to handle.
func TestFetchSandboxSecrets_DegradesToNilOnFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := boot.Config{
		SessionConfig: &sessionconfig.SessionConfig{
			ControlPlaneWsUrl: wsEquivalentForTest(server.URL),
			SessionId:         "sess-1",
			SandboxToken:      "tok",
			Gen:               1,
		},
	}

	got := fetchSandboxSecrets(context.Background(), cfg, testSandboxSecretsFetchTimeout)
	if got != nil {
		t.Errorf("fetchSandboxSecrets() = %v, want nil on a 500 response", got)
	}
}

// TestFetchSandboxSecrets_NilSessionConfigPanics is a defensive canary
// for the "never reaches BuildImage" property's OTHER half (the type-
// shape half is internal/app/ports/createspec_test.go's own
// TestImageSpec_HasNoSecretCarryingField): a real image build's own
// boot.Config always has SessionConfig == nil (loadSessionConfig,
// internal/sandboxagent/boot/config.go, returns nil when
// NARVI_SESSION_CONFIG is simply never set -- exactly BuildImage's own
// request shape, which carries no SessionConfig field for any provider
// to construct one from), and run()'s own call site
// (cmd/sandbox-agent/main.go) only ever calls fetchSandboxSecrets inside
// its `if cfg.SessionConfig != nil` branch. This test proves that IF a
// future refactor ever moved that call outside the guard, the mistake
// would surface immediately and loudly (a nil-pointer panic on the very
// first field access, cfg.SessionConfig.ControlPlaneWsUrl) rather than
// silently attempting a request with a zero-value/malformed URL -- a
// crash-on-misuse property, not merely an untested assumption.
func TestFetchSandboxSecrets_NilSessionConfigPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("fetchSandboxSecrets(cfg with nil SessionConfig) did not panic, want a nil-pointer panic (see this test's own doc comment)")
		}
	}()
	fetchSandboxSecrets(context.Background(), boot.Config{}, testSandboxSecretsFetchTimeout)
}
