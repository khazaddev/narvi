package modal

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sessionconfig"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// testSessionConfig builds a realistic SESSION_CONFIG document (matching
// the shape /contracts/contractstest/sessionconfig_test.go round-trips)
// for use as CreateSpec.SessionConfig across this file's tests. Its Gen
// always matches the CreateSpec.Gen these tests pair it with (1, the
// common case) — see testSessionConfigWithGen for tests that exercise a
// different gen value and must keep both copies in agreement now that
// CreateSpec.Validate enforces it.
func testSessionConfig() sessionconfig.SessionConfig {
	return testSessionConfigWithGen(1)
}

// testSessionConfigWithGen is testSessionConfig with an explicit gen, for
// tests that pair it with a CreateSpec.Gen other than 1 — CreateSpec.Gen
// and SessionConfig.Gen must always agree (ports.CreateSpec.Validate).
func testSessionConfigWithGen(gen int) sessionconfig.SessionConfig {
	branch := "main"
	correlationID := "corr-abc123"
	return sessionconfig.SessionConfig{
		SessionId:         "sess-1",
		Gen:               gen,
		SandboxId:         "5b1c1e2e-6b1a-4b1a-9b1a-6b1a4b1a9b1a",
		SandboxToken:      "sandbox-token-plaintext",
		BootMode:          sessionconfig.SessionConfigBootModeFresh,
		ControlPlaneWsUrl: "wss://cp.narvi.dev/sessions/sess-1/ws?type=sandbox",
		Repos: []sessionconfig.SessionConfigReposElem{
			{Name: "narvi", Url: "https://github.com/khazaddev/narvi.git", Branch: &branch},
		},
		CorrelationId: &correlationID,
	}
}

func testConfig(baseURL string) Config {
	return Config{
		BaseURL:   baseURL,
		AuthToken: "test-auth-token",
		Timeouts:  platform.DefaultTimeouts(),
	}
}

// --- New() validation ---

func TestNew_Validation(t *testing.T) {
	valid := testConfig("http://127.0.0.1:9")

	t.Run("missing base URL", func(t *testing.T) {
		cfg := valid
		cfg.BaseURL = ""
		_, err := New(cfg)
		var target *MissingConfigError
		if !errors.As(err, &target) {
			t.Errorf("New() error = %v, want *MissingConfigError", err)
		}
	})

	t.Run("invalid base URL", func(t *testing.T) {
		cfg := valid
		cfg.BaseURL = "not-a-url"
		_, err := New(cfg)
		var target *InvalidBaseURLError
		if !errors.As(err, &target) {
			t.Errorf("New() error = %v, want *InvalidBaseURLError", err)
		}
	})

	t.Run("missing auth token", func(t *testing.T) {
		cfg := valid
		cfg.AuthToken = ""
		_, err := New(cfg)
		var target *MissingConfigError
		if !errors.As(err, &target) {
			t.Errorf("New() error = %v, want *MissingConfigError", err)
		}
	})

	t.Run("invalid egress proxy URL", func(t *testing.T) {
		cfg := valid
		cfg.EgressProxyURL = "://bad"
		_, err := New(cfg)
		var target *InvalidEgressProxyURLError
		if !errors.As(err, &target) {
			t.Errorf("New() error = %v, want *InvalidEgressProxyURLError", err)
		}
	})

	// A proxy URL conventionally carries basic-auth credentials
	// (http://user:pass@host:3128) — exactly what a malformed one must
	// never leak into an error message that ends up logged (§5.3).
	t.Run("invalid egress proxy URL with credentials never leaks the password", func(t *testing.T) {
		cfg := valid
		cfg.EgressProxyURL = "user:hunter2@proxy.internal" // missing scheme -> fails validation
		_, err := New(cfg)
		var target *InvalidEgressProxyURLError
		if !errors.As(err, &target) {
			t.Fatalf("New() error = %v, want *InvalidEgressProxyURLError", err)
		}
		if strings.Contains(target.Value, "hunter2") || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("InvalidEgressProxyURLError = %v, must not contain the raw password", err)
		}
	})

	t.Run("HTTP client timeout must exceed worst cold start", func(t *testing.T) {
		cfg := valid
		cfg.Timeouts.ProviderHTTPClientTimeout = 100 * time.Millisecond
		cfg.Timeouts.ProviderWorstColdStart = 200 * time.Millisecond
		_, err := New(cfg)
		var target *ColdStartTimeoutError
		if !errors.As(err, &target) {
			t.Errorf("New() error = %v, want *ColdStartTimeoutError", err)
		}
	})

	t.Run("valid config succeeds", func(t *testing.T) {
		p, err := New(valid)
		if err != nil {
			t.Fatalf("New() error = %v, want nil", err)
		}
		if p == nil {
			t.Fatal("New() returned nil Provider with nil error")
		}
	})
}

// --- HTTP client wiring ---

func TestNew_HTTPClientTimeoutWiredFromPlatformTimeouts(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:9")
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p.httpClient.Timeout != cfg.Timeouts.ProviderHTTPClientTimeout {
		t.Errorf("httpClient.Timeout = %v, want %v (platform.Timeouts.ProviderHTTPClientTimeout)",
			p.httpClient.Timeout, cfg.Timeouts.ProviderHTTPClientTimeout)
	}
}

// --- Capabilities ---

func TestProvider_Capabilities(t *testing.T) {
	p, err := New(testConfig("http://127.0.0.1:9"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	got := p.Capabilities()
	want := ports.Capabilities{
		Snapshots:       true,
		Resume:          false,
		ExplicitStop:    true,
		ImageBuilds:     true,
		DockerInSandbox: true,
		EgressPolicy:    true,
	}
	if got != want {
		t.Errorf("Capabilities() = %+v, want %+v", got, want)
	}
}

// --- ResumeSandbox: permanent, unsupported, no network call ---

func TestProvider_ResumeSandbox_Unsupported(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = p.ResumeSandbox(context.Background(), ports.SandboxRef{ProviderID: "sbx-1"})
	if err == nil {
		t.Fatal("ResumeSandbox() error = nil, want a permanent ProviderError")
	}
	if hit {
		t.Error("ResumeSandbox() made an HTTP call, want none (unsupported operation, no network round trip)")
	}

	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("ResumeSandbox() error = %v, want *ports.ProviderError", err)
	}
	if pe.Transient {
		t.Error("ResumeSandbox() error.Transient = true, want false (permanent)")
	}
	if pe.Op != ports.OpResumeSandbox {
		t.Errorf("ResumeSandbox() error.Op = %q, want %q", pe.Op, ports.OpResumeSandbox)
	}
	if ports.IsTransient(err) {
		t.Error("ports.IsTransient(err) = true, want false for a classified permanent error")
	}
}

// --- CreateSandbox: SESSION_CONFIG as one document, auth, correlation id ---

func TestProvider_CreateSandbox_SessionConfigTravelsAsOneDocument(t *testing.T) {
	spec := ports.CreateSpec{
		Gen:           3,
		Image:         "base:latest",
		SessionConfig: testSessionConfigWithGen(3),
	}

	var gotBody map[string]json.RawMessage
	var gotAuth, gotCorrelation, gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCorrelation = r.Header.Get(platform.CorrelationIDHeader)
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("server: decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxResponse{SandboxID: "sbx-created-1"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx := platform.WithCorrelationID(context.Background(), "corr-xyz")
	ref, err := p.CreateSandbox(ctx, spec)
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if ref.ProviderID != "sbx-created-1" {
		t.Errorf("CreateSandbox() ref.ProviderID = %q, want %q", ref.ProviderID, "sbx-created-1")
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want POST", gotMethod)
	}
	if gotPath != "/v1/sandboxes" {
		t.Errorf("request path = %q, want /v1/sandboxes", gotPath)
	}
	if gotAuth != "Bearer test-auth-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-auth-token")
	}
	if gotCorrelation != "corr-xyz" {
		t.Errorf("%s header = %q, want %q", platform.CorrelationIDHeader, gotCorrelation, "corr-xyz")
	}

	// The request body must carry SESSION_CONFIG as ONE nested document,
	// never spread across top-level fields: exactly {gen, image,
	// sessionConfig} at the top level, and none of SessionConfig's own
	// field names leaking to the top level.
	for _, forbidden := range []string{"sessionId", "bootMode", "sandboxToken", "controlPlaneWsUrl", "repos", "correlationId", "sandboxId"} {
		if _, ok := gotBody[forbidden]; ok {
			t.Errorf("request body has top-level key %q — SESSION_CONFIG must travel as one nested document, not spread fragments", forbidden)
		}
	}
	rawSessionConfig, ok := gotBody["sessionConfig"]
	if !ok {
		t.Fatal("request body missing top-level \"sessionConfig\" key")
	}

	var decodedSessionConfig sessionconfig.SessionConfig
	if err := json.Unmarshal(rawSessionConfig, &decodedSessionConfig); err != nil {
		t.Fatalf("decode sessionConfig: %v", err)
	}
	if !reflect.DeepEqual(decodedSessionConfig, spec.SessionConfig) {
		t.Errorf("round-tripped sessionConfig = %+v, want %+v", decodedSessionConfig, spec.SessionConfig)
	}

	var gotGen int
	if err := json.Unmarshal(gotBody["gen"], &gotGen); err != nil {
		t.Fatalf("decode gen: %v", err)
	}
	if gotGen != spec.Gen {
		t.Errorf("gen = %d, want %d", gotGen, spec.Gen)
	}
}

// TestProvider_CreateSandbox_RejectsGenMismatch proves CreateSandbox
// validates spec (ports.CreateSpec.Validate) before ever building a
// request: a Gen/SessionConfig.Gen divergence must be rejected as a
// permanent ProviderError with zero HTTP calls, not silently shipped to
// Modal with two disagreeing generations on the wire.
func TestProvider_CreateSandbox_RejectsGenMismatch(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	spec := ports.CreateSpec{Gen: 2, SessionConfig: testSessionConfigWithGen(1)}
	_, err = p.CreateSandbox(context.Background(), spec)
	if err == nil {
		t.Fatal("CreateSandbox() error = nil, want a ProviderError for a Gen/SessionConfig.Gen mismatch")
	}
	if hit {
		t.Error("CreateSandbox() made an HTTP call, want none for a spec that fails Validate")
	}

	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("CreateSandbox() error = %v, want *ports.ProviderError", err)
	}
	if pe.Transient {
		t.Error("CreateSandbox() error.Transient = true, want false (permanent: this spec can never succeed as-is)")
	}
	if pe.Op != ports.OpCreateSandbox {
		t.Errorf("CreateSandbox() error.Op = %q, want %q", pe.Op, ports.OpCreateSandbox)
	}
	var genErr *ports.GenMismatchError
	if !errors.As(err, &genErr) {
		t.Fatalf("CreateSandbox() error = %v, want it to wrap *ports.GenMismatchError", err)
	}
}

func TestProvider_CreateSandbox_NoCorrelationIDWhenAbsent(t *testing.T) {
	sawHeader := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, sawHeader = r.Header[platform.CorrelationIDHeader]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxResponse{SandboxID: "sbx-1"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	// No correlation id on the context at all.
	_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if sawHeader {
		t.Errorf("%s header present, want absent when no correlation id on context", platform.CorrelationIDHeader)
	}
}

// --- CreateSandbox: §27.5/§27.6 Docker/EgressPolicy substrate mapping (§27.5) ---

// captureRequestBody starts an httptest.Server that decodes every request
// body into a generic map (for top-level key presence/absence checks) and
// replies with sandboxResponse{SandboxID: "sbx-1"} -- shared by every test
// in this section.
func captureRequestBody(t *testing.T) (*httptest.Server, *map[string]json.RawMessage) {
	t.Helper()
	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("server: decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxResponse{SandboxID: "sbx-1"})
	}))
	return srv, &gotBody
}

// TestProvider_CreateSandbox_DockerRequiredMapsToVMRuntime proves
// spec.Docker=true maps onto Modal's own "vm" runtime wire value (§27.5's
// own "Modal concretely" mapping) -- read from spec's own top-level
// Docker field, not from spec.SessionConfig.Docker.
func TestProvider_CreateSandbox_DockerRequiredMapsToVMRuntime(t *testing.T) {
	srv, gotBody := captureRequestBody(t)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	cfg := testSessionConfig()
	cfg.Docker = true
	_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: cfg.Gen, SessionConfig: cfg, Docker: true})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	var gotRuntime string
	if err := json.Unmarshal((*gotBody)["runtime"], &gotRuntime); err != nil {
		t.Fatalf("decode runtime: %v (body=%v)", err, *gotBody)
	}
	if gotRuntime != "vm" {
		t.Errorf("runtime = %q, want %q", gotRuntime, "vm")
	}
}

// TestProvider_CreateSandbox_DockerFalseOmitsRuntimeField proves a
// Docker-false request is byte-for-byte unaffected by this field's
// existence: no "runtime" key at all on the wire, exactly what this
// adapter sent before Runtime existed.
func TestProvider_CreateSandbox_DockerFalseOmitsRuntimeField(t *testing.T) {
	srv, gotBody := captureRequestBody(t)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	if _, ok := (*gotBody)["runtime"]; ok {
		t.Errorf("request body has top-level key \"runtime\", want absent for Docker=false")
	}
}

// TestProvider_CreateSandbox_EgressPolicyMapsToNetworkPolicy proves
// spec.EgressPolicy maps onto Modal's own networkPolicy wire field,
// carrying mode and allowlist verbatim -- read from spec's own top-level
// EgressPolicy field, not from spec.SessionConfig.EgressPolicy.
func TestProvider_CreateSandbox_EgressPolicyMapsToNetworkPolicy(t *testing.T) {
	srv, gotBody := captureRequestBody(t)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	policy := &sessionconfig.SessionConfigEgressPolicy{
		Mode:      sessionconfig.SessionConfigEgressPolicyModeAllowlist,
		Allowlist: []string{"github.com", "cp.narvi.dev"},
	}
	cfg := testSessionConfig()
	cfg.EgressPolicy = policy
	_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: cfg.Gen, SessionConfig: cfg, EgressPolicy: policy})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	rawPolicy, ok := (*gotBody)["networkPolicy"]
	if !ok {
		t.Fatal("request body missing top-level \"networkPolicy\" key")
	}
	var gotPolicy networkPolicyWire
	if err := json.Unmarshal(rawPolicy, &gotPolicy); err != nil {
		t.Fatalf("decode networkPolicy: %v", err)
	}
	if gotPolicy.Mode != "allowlist" {
		t.Errorf("networkPolicy.mode = %q, want %q", gotPolicy.Mode, "allowlist")
	}
	if !reflect.DeepEqual(gotPolicy.Allowlist, policy.Allowlist) {
		t.Errorf("networkPolicy.allowlist = %v, want %v", gotPolicy.Allowlist, policy.Allowlist)
	}
}

// TestProvider_CreateSandbox_NoEgressPolicyOmitsNetworkPolicyField proves
// a nil spec.EgressPolicy is byte-for-byte unaffected by this field's
// existence: no "networkPolicy" key at all on the wire.
func TestProvider_CreateSandbox_NoEgressPolicyOmitsNetworkPolicyField(t *testing.T) {
	srv, gotBody := captureRequestBody(t)
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	if _, ok := (*gotBody)["networkPolicy"]; ok {
		t.Errorf("request body has top-level key \"networkPolicy\", want absent when EgressPolicy is nil")
	}
}

// TestProvider_CreateSandbox_WireRequestNeverCarriesAPrivilegedField is a
// MUTATION-TESTABLE guard proving §27.5's own "privileged-mode DinD is
// rejected outright here... not a default, not an option, not behind a
// flag" holds structurally, not merely by doc-comment claim: it inspects
// the ACTUAL JSON this adapter sends to the fake Modal server for a
// docker-required, fully-populated request and asserts no key or string
// value anywhere in it contains "privileged" (case-insensitive) --
// neither this adapter's own createSandboxRequest struct, nor
// runtimeForSpec's own closed two-value vocabulary, has anywhere to put
// one. A future change that added e.g. `Privileged: true` to
// createSandboxRequest, or made runtimeForSpec select a third value, would
// fail this test immediately.
func TestProvider_CreateSandbox_WireRequestNeverCarriesAPrivilegedField(t *testing.T) {
	var rawBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		rawBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("server: read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxResponse{SandboxID: "sbx-1"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	cfg := testSessionConfig()
	cfg.Docker = true
	cfg.EgressPolicy = &sessionconfig.SessionConfigEgressPolicy{
		Mode:      sessionconfig.SessionConfigEgressPolicyModeAllowlist,
		Allowlist: []string{"github.com"},
	}
	_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{
		Gen: cfg.Gen, SessionConfig: cfg, Docker: true,
		EgressPolicy: cfg.EgressPolicy,
	})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	if strings.Contains(strings.ToLower(string(rawBody)), "privileged") {
		t.Errorf("request body contains \"privileged\" (case-insensitive) -- §27.5 rejects privileged-mode DinD outright, under every configuration: body=%s", rawBody)
	}

	// Belt-and-braces: also pin the STRUCT shape itself, not just this
	// one serialized instance -- a field that happens to be empty/zero on
	// this particular request would still pass the string-content check
	// above but must still be caught here.
	typ := reflect.TypeOf(createSandboxRequest{})
	for i := 0; i < typ.NumField(); i++ {
		name := strings.ToLower(typ.Field(i).Name)
		if strings.Contains(name, "privileged") {
			t.Errorf("createSandboxRequest has field %q -- §27.5 rejects privileged-mode DinD outright", typ.Field(i).Name)
		}
	}
}

// --- Error classification table ---

func TestProvider_ErrorClassification(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantTransient bool
	}{
		{"500 internal server error", http.StatusInternalServerError, `{"error":{"code":"INTERNAL"}}`, true},
		{"502 bad gateway", http.StatusBadGateway, `{"error":{"code":"BAD_GATEWAY"}}`, true},
		{"429 too many requests", http.StatusTooManyRequests, `{"error":{"code":"RATE_LIMITED"}}`, true},
		{"400 bad request", http.StatusBadRequest, `{"error":{"code":"INVALID_ARGUMENT"}}`, false},
		{"401 unauthorized", http.StatusUnauthorized, `{"error":{"code":"UNAUTHENTICATED"}}`, false},
		{"403 forbidden", http.StatusForbidden, `{"error":{"code":"PERMISSION_DENIED"}}`, false},
		{"404 not found", http.StatusNotFound, `{"error":{"code":"NOT_FOUND"}}`, false},
		{"409 conflict", http.StatusConflict, `{"error":{"code":"ALREADY_EXISTS"}}`, false},
		{"422 unprocessable entity", http.StatusUnprocessableEntity, `{"error":{"code":"VALIDATION_FAILED"}}`, false},
		{"418 unknown/unclassifiable code defaults to transient", http.StatusTeapot, `{"error":{"code":"WEIRD_NEW_CODE"}}`, true},
		{"402 unknown/unclassifiable code defaults to transient", http.StatusPaymentRequired, `{}`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			p, err := New(testConfig(srv.URL))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()})
			if err == nil {
				t.Fatal("CreateSandbox() error = nil, want a ProviderError")
			}

			var pe *ports.ProviderError
			if !errors.As(err, &pe) {
				t.Fatalf("CreateSandbox() error = %v, want *ports.ProviderError", err)
			}
			if pe.Transient != tt.wantTransient {
				t.Errorf("status %d: Transient = %v, want %v", tt.status, pe.Transient, tt.wantTransient)
			}
			if ports.IsTransient(err) != tt.wantTransient {
				t.Errorf("status %d: ports.IsTransient(err) = %v, want %v", tt.status, ports.IsTransient(err), tt.wantTransient)
			}
		})
	}
}

func TestProvider_NetworkErrorIsTransient(t *testing.T) {
	// Bind then immediately close a listener to get a port nothing is
	// listening on: connecting to it fails fast with "connection
	// refused" rather than hanging.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("ln.Close: %v", err)
	}

	p, err := New(testConfig("http://" + addr))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()})
	if err == nil {
		t.Fatal("CreateSandbox() error = nil, want a network-level ProviderError")
	}
	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("CreateSandbox() error = %v, want *ports.ProviderError", err)
	}
	if !pe.Transient {
		t.Error("connection-refused error: Transient = false, want true")
	}
}

func TestProvider_ClientTimeoutIsTransient(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-block // never respond within the test's short client timeout
	}))
	defer func() {
		close(block)
		srv.Close()
	}()

	cfg := testConfig(srv.URL)
	cfg.Timeouts.ProviderHTTPClientTimeout = 50 * time.Millisecond
	cfg.Timeouts.ProviderWorstColdStart = 10 * time.Millisecond

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()})
	if err == nil {
		t.Fatal("CreateSandbox() error = nil, want a client-timeout ProviderError")
	}
	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("CreateSandbox() error = %v, want *ports.ProviderError", err)
	}
	if !pe.Transient {
		t.Error("client-timeout error: Transient = false, want true")
	}
	if pe.Code != "NETWORK_TIMEOUT" {
		t.Errorf("client-timeout error: Code = %q, want %q", pe.Code, "NETWORK_TIMEOUT")
	}
}

// --- Egress proxy ---

func TestProvider_NoEgressProxy_ConnectsDirectly(t *testing.T) {
	hit := false
	modalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxResponse{SandboxID: "sbx-direct"})
	}))
	defer modalSrv.Close()

	cfg := testConfig(modalSrv.URL)
	// EgressProxyURL deliberately left empty.
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ref, err := p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}
	if !hit {
		t.Error("modal server was not hit, want a direct connection when no egress proxy is configured")
	}
	if ref.ProviderID != "sbx-direct" {
		t.Errorf("ref.ProviderID = %q, want %q", ref.ProviderID, "sbx-direct")
	}
}

func TestProvider_EgressProxy_Honored(t *testing.T) {
	decoyHits := 0
	decoySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		decoyHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxResponse{SandboxID: "sbx-DECOY-DIRECT-HIT"})
	}))
	defer decoySrv.Close()

	proxyHits := 0
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxResponse{SandboxID: "sbx-PROXIED-OK"})
	}))
	defer proxySrv.Close()

	cfg := testConfig(decoySrv.URL)
	cfg.EgressProxyURL = proxySrv.URL

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ref, err := p.CreateSandbox(context.Background(), ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v", err)
	}

	if ref.ProviderID != "sbx-PROXIED-OK" {
		t.Errorf("ref.ProviderID = %q, want %q (response must come from the proxy)", ref.ProviderID, "sbx-PROXIED-OK")
	}
	if proxyHits != 1 {
		t.Errorf("proxy server hits = %d, want 1", proxyHits)
	}
	if decoyHits != 0 {
		t.Errorf("decoy (direct target) server hits = %d, want 0 — traffic must route through the configured egress proxy", decoyHits)
	}
}

// --- TakeSnapshot / RestoreFromSnapshot / BuildImage / DeleteImage / List ---

func TestProvider_TakeSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/sbx-1/snapshot" {
			t.Errorf("request = %s %s, want POST /v1/sandboxes/sbx-1/snapshot", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshotResponse{SnapshotID: "snap-1"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	id, err := p.TakeSnapshot(context.Background(), ports.SandboxRef{ProviderID: "sbx-1"})
	if err != nil {
		t.Fatalf("TakeSnapshot() error = %v", err)
	}
	if id != "snap-1" {
		t.Errorf("TakeSnapshot() = %q, want %q", id, "snap-1")
	}
}

func TestProvider_TakeSnapshot_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := p.TakeSnapshot(context.Background(), ports.SandboxRef{ProviderID: "missing"}); err == nil {
		t.Fatal("TakeSnapshot() error = nil, want a ProviderError for a 404 response")
	}
}

func TestProvider_RestoreFromSnapshot(t *testing.T) {
	spec := ports.CreateSpec{Gen: 4, Image: "img:v2", SessionConfig: testSessionConfigWithGen(4)}
	var got restoreSandboxRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/restore" {
			t.Errorf("request = %s %s, want POST /v1/sandboxes/restore", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxResponse{SandboxID: "sbx-restored"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ref, err := p.RestoreFromSnapshot(context.Background(), ports.SnapshotID("snap-9"), spec)
	if err != nil {
		t.Fatalf("RestoreFromSnapshot() error = %v", err)
	}
	if ref.ProviderID != "sbx-restored" {
		t.Errorf("ref.ProviderID = %q, want %q", ref.ProviderID, "sbx-restored")
	}
	if got.SnapshotID != "snap-9" || got.Gen != 4 {
		t.Errorf("request body = %+v, want SnapshotID=snap-9 Gen=4", got)
	}
	if !reflect.DeepEqual(got.SessionConfig, spec.SessionConfig) {
		t.Errorf("request SessionConfig = %+v, want %+v", got.SessionConfig, spec.SessionConfig)
	}
}

// TestProvider_RestoreFromSnapshot_DockerAndEgressPolicyMapToWire proves
// RestoreFromSnapshot maps spec.Docker/spec.EgressPolicy onto the SAME
// Runtime/NetworkPolicy wire fields CreateSandbox does -- a restore is
// still asking for a real, live sandbox instance, so it needs the
// identical substrate mapping (wire.go's own restoreSandboxRequest doc
// comment).
func TestProvider_RestoreFromSnapshot_DockerAndEgressPolicyMapToWire(t *testing.T) {
	cfg := testSessionConfigWithGen(4)
	cfg.Docker = true
	policy := &sessionconfig.SessionConfigEgressPolicy{
		Mode:      sessionconfig.SessionConfigEgressPolicyModeAllowlist,
		Allowlist: []string{"github.com"},
	}
	cfg.EgressPolicy = policy
	spec := ports.CreateSpec{Gen: 4, Image: "img:v2", SessionConfig: cfg, Docker: true, EgressPolicy: policy}

	var got restoreSandboxRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(sandboxResponse{SandboxID: "sbx-restored"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := p.RestoreFromSnapshot(context.Background(), ports.SnapshotID("snap-9"), spec); err != nil {
		t.Fatalf("RestoreFromSnapshot() error = %v", err)
	}

	if got.Runtime != "vm" {
		t.Errorf("Runtime = %q, want %q", got.Runtime, "vm")
	}
	if got.NetworkPolicy == nil {
		t.Fatal("NetworkPolicy = nil, want a non-nil allowlist policy")
	}
	if got.NetworkPolicy.Mode != "allowlist" || !reflect.DeepEqual(got.NetworkPolicy.Allowlist, policy.Allowlist) {
		t.Errorf("NetworkPolicy = %+v, want mode=allowlist allowlist=%v", got.NetworkPolicy, policy.Allowlist)
	}
}

func TestProvider_RestoreFromSnapshot_RejectsGenMismatch(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	spec := ports.CreateSpec{Gen: 2, SessionConfig: testSessionConfigWithGen(1)}
	_, err = p.RestoreFromSnapshot(context.Background(), ports.SnapshotID("snap-1"), spec)
	if err == nil {
		t.Fatal("RestoreFromSnapshot() error = nil, want a ProviderError for a Gen/SessionConfig.Gen mismatch")
	}
	if hit {
		t.Error("RestoreFromSnapshot() made an HTTP call, want none for a spec that fails Validate")
	}

	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("RestoreFromSnapshot() error = %v, want *ports.ProviderError", err)
	}
	if pe.Op != ports.OpRestoreFromSnapshot {
		t.Errorf("RestoreFromSnapshot() error.Op = %q, want %q", pe.Op, ports.OpRestoreFromSnapshot)
	}
}

func TestProvider_RestoreFromSnapshot_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	spec := ports.CreateSpec{Gen: 1, SessionConfig: testSessionConfig()}
	if _, err := p.RestoreFromSnapshot(context.Background(), ports.SnapshotID("snap-1"), spec); err == nil {
		t.Fatal("RestoreFromSnapshot() error = nil, want a ProviderError for a 409 response")
	}
}

func TestProvider_BuildImage(t *testing.T) {
	spec := ports.ImageSpec{
		Base:           "base:v1",
		Repos:          map[string]ports.RepoRef{"narvi": {URL: "https://github.com/acme/narvi.git", SHA: "abc123"}},
		RuntimeVersion: "go1.26",
	}
	var got imageBuildRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images" {
			t.Errorf("request = %s %s, want POST /v1/images", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{BuildID: "build-1"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	outcome, err := p.BuildImage(context.Background(), spec)
	if err != nil {
		t.Fatalf("BuildImage() error = %v", err)
	}
	if outcome.Ref != "build-1" {
		t.Errorf("BuildImage() = %q, want %q", outcome.Ref, "build-1")
	}
	if outcome.PublishedCacheVersion != "" {
		t.Errorf("BuildImage() PublishedCacheVersion = %q, want empty (no CacheMount was requested)", outcome.PublishedCacheVersion)
	}
	wantRepos := map[string]imageBuildRequestRepo{"narvi": {URL: "https://github.com/acme/narvi.git", SHA: "abc123"}}
	if !reflect.DeepEqual(got, imageBuildRequest{Base: spec.Base, Repos: wantRepos, RuntimeVersion: spec.RuntimeVersion}) {
		t.Errorf("request body = %+v, want fields matching spec %+v", got, spec)
	}
}

// --- BuildImage: CacheMount wiring + pure-accelerator fallback (§19.1's
// closing paragraph(c)) ---

// TestProvider_BuildImage_CacheMount_SentOnWire proves a spec carrying
// CacheMount produces a request whose cacheVolume field mirrors
// ports.CacheMount{Key, MountVersion, PublishVersion, Paths} exactly, and
// that a successful build echoes PublishVersion back as
// BuildOutcome.PublishedCacheVersion (§19.1's closing paragraph, third
// iteration: immutable versioned cache snapshots).
func TestProvider_BuildImage_CacheMount_SentOnWire(t *testing.T) {
	spec := ports.ImageSpec{
		Base:           "base:v1",
		RuntimeVersion: "go1.26",
		CacheMount: &ports.CacheMount{
			Key:            "cachekey-abc123",
			MountVersion:   "41",
			PublishVersion: "42",
			Paths:          []string{"/root/.npm/_cacache", "/root/.cache/pip"},
		},
	}
	var got imageBuildRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{BuildID: "build-cached-1"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	outcome, err := p.BuildImage(context.Background(), spec)
	if err != nil {
		t.Fatalf("BuildImage() error = %v", err)
	}
	if outcome.Ref != "build-cached-1" {
		t.Errorf("BuildImage() = %q, want %q", outcome.Ref, "build-cached-1")
	}
	if outcome.PublishedCacheVersion != "42" {
		t.Errorf("BuildImage() PublishedCacheVersion = %q, want %q (spec.CacheMount.PublishVersion, echoed back on a successful cache-mount-bearing request)", outcome.PublishedCacheVersion, "42")
	}
	if got.CacheVolume == nil {
		t.Fatal("request body cacheVolume = nil, want it populated")
	}
	if got.CacheVolume.Key != "cachekey-abc123" {
		t.Errorf("cacheVolume.Key = %q, want %q", got.CacheVolume.Key, "cachekey-abc123")
	}
	if got.CacheVolume.MountVersion != "41" {
		t.Errorf("cacheVolume.MountVersion = %q, want %q", got.CacheVolume.MountVersion, "41")
	}
	if got.CacheVolume.PublishVersion != "42" {
		t.Errorf("cacheVolume.PublishVersion = %q, want %q", got.CacheVolume.PublishVersion, "42")
	}
	wantPaths := []string{"/root/.npm/_cacache", "/root/.cache/pip"}
	if !reflect.DeepEqual(got.CacheVolume.Paths, wantPaths) {
		t.Errorf("cacheVolume.Paths = %v, want %v", got.CacheVolume.Paths, wantPaths)
	}
}

// TestProvider_BuildImage_CacheMount_FirstBuildHasNoMountVersion proves a
// spec with no MountVersion yet (this cache key's very first build) still
// sends a well-formed request — MountVersion omitted from the wire
// (omitempty), PublishVersion always present — and still confirms
// publication on success.
func TestProvider_BuildImage_CacheMount_FirstBuildHasNoMountVersion(t *testing.T) {
	spec := ports.ImageSpec{
		Base:           "base:v1",
		RuntimeVersion: "go1.26",
		CacheMount: &ports.CacheMount{
			Key:            "cachekey-first",
			MountVersion:   "",
			PublishVersion: "1",
			Paths:          []string{"/root/.npm/_cacache"},
		},
	}
	var gotRaw map[string]json.RawMessage
	var got imageBuildRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &gotRaw); err != nil {
			t.Errorf("decode request (raw): %v", err)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{BuildID: "build-first-1"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	outcome, err := p.BuildImage(context.Background(), spec)
	if err != nil {
		t.Fatalf("BuildImage() error = %v", err)
	}
	if outcome.PublishedCacheVersion != "1" {
		t.Errorf("BuildImage() PublishedCacheVersion = %q, want %q", outcome.PublishedCacheVersion, "1")
	}
	var cacheVolumeRaw map[string]json.RawMessage
	if err := json.Unmarshal(gotRaw["cacheVolume"], &cacheVolumeRaw); err != nil {
		t.Fatalf("decode request cacheVolume: %v", err)
	}
	if _, ok := cacheVolumeRaw["mountVersion"]; ok {
		t.Error(`request body cacheVolume has a "mountVersion" key, want it omitted when MountVersion is empty (this key's first build)`)
	}
	if got.CacheVolume.PublishVersion != "1" {
		t.Errorf("cacheVolume.PublishVersion = %q, want %q", got.CacheVolume.PublishVersion, "1")
	}
}

// TestProvider_BuildImage_NoCacheMount_OmitsCacheVolumeField proves a spec
// with CacheMount left nil (every ImageSpec literal that predates this
// field, and every caller that never opts in) produces a request with NO
// cacheVolume key at all on the wire — not merely a null value — so this
// change is invisible to a fake (or real) server that has no idea the
// field exists.
func TestProvider_BuildImage_NoCacheMount_OmitsCacheVolumeField(t *testing.T) {
	var gotRaw map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotRaw); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{BuildID: "build-nocache-1"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	spec := ports.ImageSpec{Base: "base:v1", RuntimeVersion: "go1.26"}
	if _, err := p.BuildImage(context.Background(), spec); err != nil {
		t.Fatalf("BuildImage() error = %v", err)
	}
	if _, ok := gotRaw["cacheVolume"]; ok {
		t.Error(`request body has a "cacheVolume" key, want it entirely absent when spec.CacheMount is nil`)
	}
}

// cacheMountTroubleServer builds an httptest.Server standing in for a
// build service that cannot honor a cache mount at all: the FIRST request
// (the one carrying a non-nil cacheVolume) is rejected with troubleCode;
// any SECOND request succeeds — modeling Provider.BuildImage's own
// decline-and-retry-cold fallback. Returns the server and a pointer to the
// observed request count.
func cacheMountTroubleServer(t *testing.T, status int, troubleCode string) (*httptest.Server, *int) {
	t.Helper()
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req imageBuildRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.CacheVolume != nil {
			// First attempt: this fake build service cannot honor the
			// requested cache mount.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": troubleCode, "message": "cache mount trouble"},
			})
			return
		}
		// Second attempt (or any request with no cache mount at all): an
		// ordinary successful cold build.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{BuildID: "build-cold-fallback"})
	}))
	return srv, &requestCount
}

// TestProvider_BuildImage_CacheMountTrouble_FallsBackToColdBuild is the
// pure-accelerator rule's own MANDATORY test (task requirement: "an
// unavailable, declined, hung, or pruned cache produces a successful cold
// build, never a failure"): for each structured cache-trouble code this
// adapter's own invented protocol recognizes — including a build-service-
// reported internal timeout (DECLINED because the cache subsystem itself
// HUNG, reported honestly and fast rather than via a client-side timeout —
// see isCacheMountTrouble's own doc comment for why) and a not-found/
// PRUNED version — BuildImage must still return a successful BuildOutcome,
// with PublishedCacheVersion empty (the fallback dropped the mount, so
// nothing was published), proving a cache problem can never surface as a
// caller-visible BuildImage failure.
//
// This test MUST fail if cacheMountTroubleCodes/isCacheMountTrouble's own
// structured-code recognition is ever removed or narrowed to miss one of
// these: deleting a case's own membership from cacheMountTroubleCodes
// turns its sub-test's "want nil (pure accelerator...)" into an actual
// returned error and its own requestCount assertion from 2 to 1.
func TestProvider_BuildImage_CacheMountTrouble_FallsBackToColdBuild(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		troubleCode string
	}{
		{name: "corrupted cache version", status: http.StatusConflict, troubleCode: "CACHE_MOUNT_CORRUPTED"},
		{name: "unavailable cache subsystem (declined)", status: http.StatusServiceUnavailable, troubleCode: "CACHE_MOUNT_UNAVAILABLE"},
		{name: "build service's own internal cache-mount timeout (hung, reported fast and honestly)", status: http.StatusGatewayTimeout, troubleCode: "CACHE_MOUNT_TIMEOUT"},
		{name: "pinned MountVersion not found (already pruned)", status: http.StatusNotFound, troubleCode: "CACHE_VERSION_NOT_FOUND"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, requestCount := cacheMountTroubleServer(t, tt.status, tt.troubleCode)
			defer srv.Close()

			p, err := New(testConfig(srv.URL))
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}

			spec := ports.ImageSpec{
				Base:           "base:v1",
				RuntimeVersion: "go1.26",
				CacheMount:     &ports.CacheMount{Key: "trouble-key", MountVersion: "7", PublishVersion: "8", Paths: []string{"/root/.npm/_cacache"}},
			}
			outcome, err := p.BuildImage(context.Background(), spec)
			if err != nil {
				t.Fatalf("BuildImage() error = %v, want nil (pure accelerator: %s must degrade to a successful cold build)", err, tt.troubleCode)
			}
			if outcome.Ref != "build-cold-fallback" {
				t.Errorf("BuildImage() = %q, want %q (the cold-build fallback response)", outcome.Ref, "build-cold-fallback")
			}
			if outcome.PublishedCacheVersion != "" {
				t.Errorf("BuildImage() PublishedCacheVersion = %q, want empty (the cold retry dropped the cache mount entirely, so nothing was published)", outcome.PublishedCacheVersion)
			}
			if *requestCount != 2 {
				t.Errorf("server observed %d requests, want 2 (one with the cache mount, one cold retry)", *requestCount)
			}
		})
	}
}

// TestProvider_BuildImage_ClientSideTimeout_NeverRetriedCold is the
// dedicated regression test for the harmful NETWORK_TIMEOUT broadening
// this iteration removes: "a build that legitimately exceeds
// ProviderHTTPClientTimeout now retries cold — strictly slower than the
// attempt that just timed out — so it times out again, doubling wall
// clock and guaranteeing failure where one slow-but-viable attempt
// existed before." The first (and, this test proves, ONLY) request —
// carrying the cache mount — blocks past a short client timeout,
// modeling a build that is simply slow (for a reason unrelated to the
// cache) and would have succeeded given enough time. BuildImage must
// return the ORIGINAL timeout error, unmodified, and must NEVER attempt a
// second, cold request — retrying here would only re-run the identical
// budget against STRICTLY MORE work (a full cold download), doubling wall
// clock for no chance of success.
//
// This test MUST fail if isCacheMountTrouble is ever broadened back to
// treat NETWORK_TIMEOUT as cache trouble: requestCount would become 2 and
// err would become nil.
func TestProvider_BuildImage_ClientSideTimeout_NeverRetriedCold(t *testing.T) {
	block := make(chan struct{})

	// requestCount is an atomic.Int32: the FIRST request's own handler
	// goroutine here never returns before the test itself observes
	// BuildImage's own result (it blocks on <-block, released only by this
	// test's own deferred cleanup) — a plain `int` would be a genuine data
	// race under -race (§11).
	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		var req imageBuildRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.CacheVolume != nil {
			// Never respond within the test's short client timeout —
			// modeling a build that is simply slow, for a reason unrelated
			// to the cache mount (e.g. a large dependency set, or
			// non-package-manager setup work, §19.4).
			<-block
			return
		}
		// A cold retry must never reach here — if it does, requestCount
		// alone already proves the regression; this response exists only
		// so the test would fail loudly rather than hang if it somehow did.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{BuildID: "build-should-never-be-reached"})
	}))
	// Deferred in this order deliberately: httptest.Server.Close() blocks
	// until every outstanding handler goroutine returns, including the
	// still-blocked request's handler above — so block must be closed
	// (unblocking that goroutine) BEFORE srv.Close() is called, or this
	// test would deadlock on its own cleanup. defer runs LIFO, so
	// declaring srv.Close() first and close(block) second is what makes
	// close(block) run FIRST.
	defer srv.Close()
	defer close(block)

	cfg := testConfig(srv.URL)
	cfg.Timeouts.ProviderHTTPClientTimeout = 50 * time.Millisecond
	cfg.Timeouts.ProviderWorstColdStart = 10 * time.Millisecond

	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	spec := ports.ImageSpec{
		Base:           "base:v1",
		RuntimeVersion: "go1.26",
		CacheMount:     &ports.CacheMount{Key: "slow-key", MountVersion: "3", PublishVersion: "4", Paths: []string{"/root/.npm/_cacache"}},
	}
	_, err = p.BuildImage(context.Background(), spec)
	if err == nil {
		t.Fatal("BuildImage() error = nil, want the original client-side timeout error — a bare timeout is not evidence of cache trouble and must never trigger a cold retry")
	}
	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("BuildImage() error = %v, want *ports.ProviderError", err)
	}
	if pe.Code != networkTimeoutCode {
		t.Errorf("BuildImage() error.Code = %q, want %q (the original, un-retried timeout)", pe.Code, networkTimeoutCode)
	}
	if got := requestCount.Load(); got != 1 {
		t.Errorf("server observed %d requests, want exactly 1 (a client-side timeout must never trigger a cold-build retry)", got)
	}
}

// TestProvider_BuildImage_CacheMountUnparseableResponse_FallsBackToColdBuild
// is the pure-accelerator rule's own dedicated test for an UNPARSEABLE
// error response — the audit-remediation finding's own second example:
// "Same for a 503 with a non-JSON body." The first request (carrying the
// cache mount) gets a 503 with a plain-text, non-JSON body; BuildImage
// must still return a successful BuildRef from the cold retry.
func TestProvider_BuildImage_CacheMountUnparseableResponse_FallsBackToColdBuild(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		var req imageBuildRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.CacheVolume != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("upstream volume subsystem is degraded\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildResponse{BuildID: "build-cold-after-unparseable"})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	spec := ports.ImageSpec{
		Base:           "base:v1",
		RuntimeVersion: "go1.26",
		CacheMount:     &ports.CacheMount{Key: "unparseable-key", MountVersion: "9", PublishVersion: "10", Paths: []string{"/root/.npm/_cacache"}},
	}
	outcome, err := p.BuildImage(context.Background(), spec)
	if err != nil {
		t.Fatalf("BuildImage() error = %v, want nil (pure accelerator: a 503 with a non-JSON body on a cache-mount request must degrade to a successful cold build)", err)
	}
	if outcome.Ref != "build-cold-after-unparseable" {
		t.Errorf("BuildImage() = %q, want %q", outcome.Ref, "build-cold-after-unparseable")
	}
	if outcome.PublishedCacheVersion != "" {
		t.Errorf("BuildImage() PublishedCacheVersion = %q, want empty (the cold retry dropped the cache mount)", outcome.PublishedCacheVersion)
	}
	if requestCount != 2 {
		t.Errorf("server observed %d requests, want 2 (one 503/non-JSON with the cache mount, one cold retry)", requestCount)
	}
}

// TestProvider_BuildImage_UnparseableResponse_PermanentStatus_NotRetried
// guards the broadened signal against masking a genuine, non-retryable
// rejection: an unparseable body on a PERMANENT status (422, not one of
// the transient statuses isCacheMountTrouble's own third signal is scoped
// to) must surface as an ordinary failure, never trigger the cold-build
// retry — even though a cache mount was requested.
func TestProvider_BuildImage_UnparseableResponse_PermanentStatus_NotRetried(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte("not json, and not a cache-mount problem either"))
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	spec := ports.ImageSpec{
		Base:           "base:v1",
		RuntimeVersion: "go1.26",
		CacheMount:     &ports.CacheMount{Key: "some-key", Paths: []string{"/root/.npm/_cacache"}},
	}
	_, err = p.BuildImage(context.Background(), spec)
	if err == nil {
		t.Fatal("BuildImage() error = nil, want a ProviderError for a genuine, permanent, non-cache rejection")
	}
	if requestCount != 1 {
		t.Errorf("server observed %d requests, want exactly 1 (an unparseable body on a PERMANENT status must not trigger the cache fallback retry)", requestCount)
	}
}

// TestProvider_BuildImage_CacheMountTrouble_NoRetryWhenNoCacheRequested
// proves the fallback is scoped to a request that itself carried a cache
// mount: a plain BuildImage failure — including one that happens to use
// the SAME error codes cacheMountTroubleCodes recognizes, purely by
// coincidence — must never trigger a second request when spec.CacheMount
// was nil to begin with. Guards against a broader "retry on this code,
// unconditionally" implementation that would silently double every
// ordinary failed build for callers who never asked for a cache at all.
func TestProvider_BuildImage_CacheMountTrouble_NoRetryWhenNoCacheRequested(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "CACHE_MOUNT_CORRUPTED", "message": "coincidental code, no cache was ever requested"},
		})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	spec := ports.ImageSpec{Base: "base:v1", RuntimeVersion: "go1.26"} // CacheMount left nil
	if _, err := p.BuildImage(context.Background(), spec); err == nil {
		t.Fatal("BuildImage() error = nil, want a ProviderError (no cache was requested, so this is an ordinary build failure)")
	}
	if requestCount != 1 {
		t.Errorf("server observed %d requests, want exactly 1 (no cold-build retry when spec.CacheMount was nil)", requestCount)
	}
}

// TestProvider_BuildImage_OrdinaryFailureWithCacheMount_NotRetried proves
// the fallback does NOT fire for an ordinary build failure that happens to
// occur on a request that also carried a cache mount — only a code in
// cacheMountTroubleCodes triggers the retry; every other failure (a
// genuine setup.sh/build defect) must surface exactly once, unmasked.
func TestProvider_BuildImage_OrdinaryFailureWithCacheMount_NotRetried(t *testing.T) {
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "SETUP_SCRIPT_FAILED", "message": "setup.sh exited 1"},
		})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	spec := ports.ImageSpec{
		Base:           "base:v1",
		RuntimeVersion: "go1.26",
		CacheMount:     &ports.CacheMount{Key: "some-key", Paths: []string{"/root/.npm/_cacache"}},
	}
	_, err = p.BuildImage(context.Background(), spec)
	if err == nil {
		t.Fatal("BuildImage() error = nil, want a ProviderError for a genuine (non-cache) build failure")
	}
	var pe *ports.ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("BuildImage() error = %v, want *ports.ProviderError", err)
	}
	if pe.Code != "SETUP_SCRIPT_FAILED" {
		t.Errorf("BuildImage() error.Code = %q, want %q (the real underlying failure, not masked)", pe.Code, "SETUP_SCRIPT_FAILED")
	}
	if requestCount != 1 {
		t.Errorf("server observed %d requests, want exactly 1 (an ordinary build failure must not trigger the cache fallback retry)", requestCount)
	}
}

func TestProvider_BuildImage_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := p.BuildImage(context.Background(), ports.ImageSpec{Base: "base:v1"}); err == nil {
		t.Fatal("BuildImage() error = nil, want a ProviderError for a 422 response")
	}
}

func TestProvider_DeleteImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/images/img-1" {
			t.Errorf("request = %s %s, want DELETE /v1/images/img-1", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := p.DeleteImage(context.Background(), ports.ImageRef("img-1")); err != nil {
		t.Errorf("DeleteImage() error = %v", err)
	}
}

func TestProvider_List(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sandboxes" {
			t.Errorf("request = %s %s, want GET /v1/sandboxes", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(listResponse{Sandboxes: []sandboxResponse{{SandboxID: "sbx-1"}, {SandboxID: "sbx-2"}}})
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	refs, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := []ports.SandboxRef{{ProviderID: "sbx-1"}, {ProviderID: "sbx-2"}}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("List() = %+v, want %+v", refs, want)
	}
}

func TestProvider_List_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := p.List(context.Background()); err == nil {
		t.Fatal("List() error = nil, want a ProviderError for a 500 response")
	}
}

func TestProvider_StopSandbox(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sandboxes/sbx-1/stop" {
			t.Errorf("request = %s %s, want POST /v1/sandboxes/sbx-1/stop", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if err := p.StopSandbox(context.Background(), ports.SandboxRef{ProviderID: "sbx-1"}); err != nil {
		t.Errorf("StopSandbox() error = %v", err)
	}
}
