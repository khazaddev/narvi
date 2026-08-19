package credentials_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// TestCPClient_FetchOpenCodeConfig_RequestShape proves the real request
// this method sends and that both documents decode correctly.
func TestCPClient_FetchOpenCodeConfig_RequestShape(t *testing.T) {
	t.Parallel()

	var gotMethod, gotAuth, gotPath, gotGen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotGen = r.Header.Get("X-Sandbox-Gen")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"global":{"model":"anthropic/claude"},"environment":{"agent":{"build":{}}}}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	got, err := client.FetchOpenCodeConfig(context.Background(), "sess-1", "sandbox-tok", 42)
	if err != nil {
		t.Fatalf("FetchOpenCodeConfig() error = %v, want nil", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotPath != "/sessions/sess-1/opencode-config" {
		t.Errorf("path = %q, want %q", gotPath, "/sessions/sess-1/opencode-config")
	}
	if gotAuth != "Bearer sandbox-tok" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer sandbox-tok")
	}
	if gotGen != "42" {
		t.Errorf("X-Sandbox-Gen header = %q, want %q", gotGen, "42")
	}
	if string(got.Global) != `{"model":"anthropic/claude"}` {
		t.Errorf("Global = %s, want %s", got.Global, `{"model":"anthropic/claude"}`)
	}
	if string(got.Environment) != `{"agent":{"build":{}}}` {
		t.Errorf("Environment = %s, want %s", got.Environment, `{"agent":{"build":{}}}`)
	}
}

// TestCPClient_FetchOpenCodeConfig_BothAbsentIsNotAnError proves the
// overwhelming common case (nothing configured at either scope for this
// session) is a plain, successful zero-value result -- never an error.
func TestCPClient_FetchOpenCodeConfig_BothAbsentIsNotAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	got, err := client.FetchOpenCodeConfig(context.Background(), "sess-1", "tok", 1)
	if err != nil {
		t.Fatalf("FetchOpenCodeConfig() error = %v, want nil", err)
	}
	if len(got.Global) != 0 {
		t.Errorf("Global = %s, want empty", got.Global)
	}
	if len(got.Environment) != 0 {
		t.Errorf("Environment = %s, want empty", got.Environment)
	}
}

// TestCPClient_FetchOpenCodeConfig_OnlyOnePresent proves each document is
// independently present/absent -- global configured, environment not.
func TestCPClient_FetchOpenCodeConfig_OnlyOnePresent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"global":{"autoupdate":true}}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	got, err := client.FetchOpenCodeConfig(context.Background(), "sess-1", "tok", 1)
	if err != nil {
		t.Fatalf("FetchOpenCodeConfig() error = %v, want nil", err)
	}
	if string(got.Global) != `{"autoupdate":true}` {
		t.Errorf("Global = %s, want %s", got.Global, `{"autoupdate":true}`)
	}
	if len(got.Environment) != 0 {
		t.Errorf("Environment = %s, want empty", got.Environment)
	}
}

func TestCPClient_FetchOpenCodeConfig_NonTwoXXIsAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"no usable opencode config for this session"}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	_, err = client.FetchOpenCodeConfig(context.Background(), "sess-1", "tok", 1)
	if err == nil {
		t.Fatal("FetchOpenCodeConfig() error = nil, want an error for a 403 response")
	}
}

func TestCPClient_FetchOpenCodeConfig_MalformedResponseBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	_, err = client.FetchOpenCodeConfig(context.Background(), "sess-1", "tok", 1)
	if err == nil {
		t.Fatal("FetchOpenCodeConfig() error = nil, want an error for a malformed 2xx response body")
	}
}
