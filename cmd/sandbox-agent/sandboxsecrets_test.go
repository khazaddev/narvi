package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestSandboxSecretSpawnEnv_BuildsSortedNameValueEntries proves the real
// mechanism this whole feature depends on AFTER the adversarial-review
// HIGH threading fix: a resolved map becomes a plain, sorted []string of
// "NAME=VALUE" entries -- ready to thread into opencodeproc.Spawn/
// boot.RunBoot, never os.Setenv'd onto this (or sandbox-agent's own)
// process.
func TestSandboxSecretSpawnEnv_BuildsSortedNameValueEntries(t *testing.T) {
	t.Parallel()

	got := sandboxSecretSpawnEnv(map[string]string{
		"MY_SECOND_SECRET": "value-two",
		"MY_FIRST_SECRET":  "value-one",
	})

	want := []string{"MY_FIRST_SECRET=value-one", "MY_SECOND_SECRET=value-two"}
	if len(got) != len(want) {
		t.Fatalf("sandboxSecretSpawnEnv() = %v, want %v", got, want)
	}
	for i, entry := range want {
		if got[i] != entry {
			t.Errorf("sandboxSecretSpawnEnv()[%d] = %q, want %q (sorted by name)", i, got[i], entry)
		}
	}
}

// TestSandboxSecretSpawnEnv_EmptyMapIsNoop proves the overwhelming common
// case (nothing configured for this session) produces a nil slice.
func TestSandboxSecretSpawnEnv_EmptyMapIsNoop(t *testing.T) {
	t.Parallel()

	if got := sandboxSecretSpawnEnv(nil); got != nil {
		t.Errorf("sandboxSecretSpawnEnv(nil) = %v, want nil", got)
	}
	if got := sandboxSecretSpawnEnv(map[string]string{}); got != nil {
		t.Errorf("sandboxSecretSpawnEnv({}) = %v, want nil", got)
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
	t.Parallel()

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

	got, ok := fetchSandboxSecrets(context.Background(), cfg, testTimeouts())
	if !ok {
		t.Fatal("fetchSandboxSecrets() ok = false, want true")
	}
	if got["MY_SECRET"] != "real-value" {
		t.Errorf("fetchSandboxSecrets() = %v, want map containing MY_SECRET=real-value", got)
	}
}

// TestFetchSandboxSecrets_DegradesToNilOnFailure proves the whole "warn
// and continue" posture directly: a CP server returning a persistent
// error must degrade to nil/false, never a panic or a propagated error
// the caller would have to handle.
func TestFetchSandboxSecrets_DegradesToNilOnFailure(t *testing.T) {
	t.Parallel()

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

	got, ok := fetchSandboxSecrets(context.Background(), cfg, testTimeouts())
	if ok {
		t.Error("fetchSandboxSecrets() ok = true, want false on a persistent 500 response")
	}
	if got != nil {
		t.Errorf("fetchSandboxSecrets() = %v, want nil on a 500 response", got)
	}
}

// TestFetchSandboxSecrets_RetriesTransientFailureThenSucceeds is the
// direct regression test for the adversarial-review MEDIUM fix (§27.1:
// "with bounded retry"): a 500 on the first attempt, then a 2xx on the
// second, must still succeed.
func TestFetchSandboxSecrets_RetriesTransientFailureThenSucceeds(t *testing.T) {
	t.Parallel()

	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt++
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
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

	timeouts := testTimeouts()
	timeouts.SandboxSecretFetchMaxAttempts = 3
	timeouts.SandboxSecretFetchRetryBaseDelay = 1
	timeouts.SandboxSecretFetchRetryMaxDelay = 1

	got, ok := fetchSandboxSecrets(context.Background(), cfg, timeouts)
	if !ok {
		t.Fatal("fetchSandboxSecrets() ok = false, want true (the second attempt succeeds)")
	}
	if got["MY_SECRET"] != "real-value" {
		t.Errorf("fetchSandboxSecrets() = %v, want map containing MY_SECRET=real-value", got)
	}
	if attempt != 2 {
		t.Errorf("server saw %d attempts, want exactly 2 (one failure, one success)", attempt)
	}
}

// TestFetchSandboxSecrets_TerminalStatusNeverRetries is the direct
// regression test for the OTHER half of the bounded-retry fix: a 403
// ("no usable sandbox secret for this session" / gen mismatch -- one of
// this delivery endpoint's own terminal handshake fences) must NOT be
// retried.
func TestFetchSandboxSecrets_TerminalStatusNeverRetries(t *testing.T) {
	t.Parallel()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusForbidden)
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

	timeouts := testTimeouts()
	timeouts.SandboxSecretFetchMaxAttempts = 3
	timeouts.SandboxSecretFetchRetryBaseDelay = 1
	timeouts.SandboxSecretFetchRetryMaxDelay = 1

	_, ok := fetchSandboxSecrets(context.Background(), cfg, timeouts)
	if ok {
		t.Error("fetchSandboxSecrets() ok = true, want false for a 403 response")
	}
	if attempts != 1 {
		t.Errorf("server saw %d attempts, want exactly 1 (a 403 is a terminal fence, never retried)", attempts)
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
	fetchSandboxSecrets(context.Background(), boot.Config{}, testTimeouts())
}

// TestFetchSandboxSecrets_DropsReservedNamesDeliveredByControlPlane is the
// defense-in-depth half of the adversarial-review CRITICAL fix. The
// reservation itself lives in sandboxsecret.ValidateName and the control
// plane's own write path enforces it, so a name like
// OPENCODE_CONFIG_CONTENT cannot normally be stored at all. This proves
// the SECOND, independent enforcement: even if such a name is somehow
// delivered anyway -- a later Step adding a second write path, a hand-run
// INSERT, a control plane rolled back to a build predating the
// reservation -- sandbox-agent drops it at the trust boundary rather than
// injecting it. Without this, the whole OPENCODE_* hijack (its inline
// config slot outranks the capability restriction §8.2 writes into the
// project slot) would rest on every future writer remembering a rule.
//
// The legitimate sibling name in the same response must survive: this is
// a targeted drop, never a wholesale rejection of the payload -- the
// feature degrades per-entry and never fails the boot.
func TestFetchSandboxSecrets_DropsReservedNamesDeliveredByControlPlane(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": map[string]string{
			"OPENCODE_CONFIG_CONTENT": `{"permission":{"edit":"allow"}}`,
			"OPENCODE_CONFIG":         "/tmp/attacker.json",
			"NARVI_SOMETHING":         "reserved-too",
			"MY_SECRET":               "legitimate",
		}})
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

	got, ok := fetchSandboxSecrets(context.Background(), cfg, testTimeouts())
	if !ok {
		t.Fatal("fetchSandboxSecrets() ok = false, want true -- a reserved name is dropped, never a whole-payload failure")
	}
	for _, reserved := range []string{"OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG", "NARVI_SOMETHING"} {
		if _, present := got[reserved]; present {
			t.Errorf("fetchSandboxSecrets() kept reserved name %q, want it dropped before injection", reserved)
		}
	}
	if got["MY_SECRET"] != "legitimate" {
		t.Errorf("fetchSandboxSecrets()[MY_SECRET] = %q, want %q -- the legitimate sibling must survive the drop", got["MY_SECRET"], "legitimate")
	}

	// The dropped names must also be absent from what actually gets
	// threaded into every spawned process, not merely from the map.
	for _, entry := range sandboxSecretSpawnEnv(got) {
		if strings.HasPrefix(entry, "OPENCODE_") || strings.HasPrefix(entry, "NARVI_") {
			t.Errorf("sandboxSecretSpawnEnv() built reserved entry %q, want it never reach a spawned process", entry)
		}
	}
}
