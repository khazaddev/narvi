package credentials_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/narvidev/narvi/internal/sandboxagent/credentials"
)

// TestCPClient_FetchSandboxSecrets_RequestShape proves the real request
// this method sends: no body at all, the SAME two headers every other
// delivery call in this package sends, and the correct path.
func TestCPClient_FetchSandboxSecrets_RequestShape(t *testing.T) {
	t.Parallel()

	var gotMethod, gotAuth, gotPath, gotGen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotGen = r.Header.Get("X-Sandbox-Gen")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"secrets": map[string]string{"MY_SECRET": "sk-real-value"},
		})
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	got, err := client.FetchSandboxSecrets(context.Background(), "sess-1", "sandbox-tok", 42)
	if err != nil {
		t.Fatalf("FetchSandboxSecrets() error = %v, want nil", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotPath != "/sessions/sess-1/sandbox-secrets" {
		t.Errorf("path = %q, want %q", gotPath, "/sessions/sess-1/sandbox-secrets")
	}
	if gotAuth != "Bearer sandbox-tok" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer sandbox-tok")
	}
	if gotGen != "42" {
		t.Errorf("X-Sandbox-Gen header = %q, want %q", gotGen, "42")
	}
	if got["MY_SECRET"] != "sk-real-value" {
		t.Errorf("Secrets[MY_SECRET] = %q, want %q", got["MY_SECRET"], "sk-real-value")
	}
}

// TestCPClient_FetchSandboxSecrets_EmptyMapIsNotAnError proves the
// overwhelming common case (nothing configured for this session) is a
// plain, successful empty map -- never an error.
func TestCPClient_FetchSandboxSecrets_EmptyMapIsNotAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": map[string]string{}})
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	got, err := client.FetchSandboxSecrets(context.Background(), "sess-1", "tok", 1)
	if err != nil {
		t.Fatalf("FetchSandboxSecrets() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Secrets = %v, want empty", got)
	}
}

func TestCPClient_FetchSandboxSecrets_NonTwoXXIsAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"no usable sandbox secret for this session"}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	_, err = client.FetchSandboxSecrets(context.Background(), "sess-1", "tok", 1)
	if err == nil {
		t.Fatal("FetchSandboxSecrets() error = nil, want an error for a 403 response")
	}
}

// TestCPClient_FetchSandboxSecrets_ErrorResponseBodyNeverLeaks mirrors
// TestCPClient_FetchProviderCredentials_ErrorResponseBodyNeverLeaks
// exactly, for this method -- a real secret value embedded in an error
// response body must never surface in the returned error's own message.
func TestCPClient_FetchSandboxSecrets_ErrorResponseBodyNeverLeaks(t *testing.T) {
	t.Parallel()

	const secret = "leaked-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error","echo":{"MY_SECRET":"` + secret + `"}}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	_, err = client.FetchSandboxSecrets(context.Background(), "sess-1", "tok", 1)
	if err == nil {
		t.Fatal("FetchSandboxSecrets() error = nil, want an error for a 500 response")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("FetchSandboxSecrets() error = %q, must never contain the raw response body/secret %q", err.Error(), secret)
	}
}

func TestCPClient_FetchSandboxSecrets_MalformedResponseBody(t *testing.T) {
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

	_, err = client.FetchSandboxSecrets(context.Background(), "sess-1", "tok", 1)
	if err == nil {
		t.Fatal("FetchSandboxSecrets() error = nil, want an error for a malformed 2xx response body")
	}
}
