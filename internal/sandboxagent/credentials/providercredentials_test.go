package credentials_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

// TestCPClient_FetchProviderCredentials_RequestShape proves the real
// request this method sends: no body at all (unlike Fetch's own {"host":
// ...} body -- there is no host concept for a provider API key), the SAME
// two headers Fetch already sends, and the correct path.
func TestCPClient_FetchProviderCredentials_RequestShape(t *testing.T) {
	t.Parallel()

	var gotMethod, gotAuth, gotPath, gotGen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotGen = r.Header.Get("X-Sandbox-Gen")

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"credentials": map[string]string{"anthropic": "sk-real-value"},
		})
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	got, err := client.FetchProviderCredentials(context.Background(), "sess-1", "sandbox-tok", 42)
	if err != nil {
		t.Fatalf("FetchProviderCredentials() error = %v, want nil", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotPath != "/sessions/sess-1/provider-credentials" {
		t.Errorf("path = %q, want %q", gotPath, "/sessions/sess-1/provider-credentials")
	}
	if gotAuth != "Bearer sandbox-tok" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer sandbox-tok")
	}
	if gotGen != "42" {
		t.Errorf("X-Sandbox-Gen header = %q, want %q", gotGen, "42")
	}
	if got["anthropic"] != "sk-real-value" {
		t.Errorf("Credentials[anthropic] = %q, want %q", got["anthropic"], "sk-real-value")
	}
}

// TestCPClient_FetchProviderCredentials_EmptyMapIsNotAnError proves the
// overwhelming common case (nothing configured for this session) is a
// plain, successful empty map -- never an error.
func TestCPClient_FetchProviderCredentials_EmptyMapIsNotAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"credentials": map[string]string{}})
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	got, err := client.FetchProviderCredentials(context.Background(), "sess-1", "tok", 1)
	if err != nil {
		t.Fatalf("FetchProviderCredentials() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("Credentials = %v, want empty", got)
	}
}

func TestCPClient_FetchProviderCredentials_NonTwoXXIsAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"no usable provider credential for this session"}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	_, err = client.FetchProviderCredentials(context.Background(), "sess-1", "tok", 1)
	if err == nil {
		t.Fatal("FetchProviderCredentials() error = nil, want an error for a 403 response")
	}
}

// TestCPClient_FetchProviderCredentials_ErrorResponseBodyNeverLeaks
// mirrors TestCPClient_Fetch_ErrorResponseBodyNeverLeaks exactly, for this
// method -- a real credential value embedded in an error response body
// must never surface in the returned error's own message.
func TestCPClient_FetchProviderCredentials_ErrorResponseBodyNeverLeaks(t *testing.T) {
	t.Parallel()

	const secret = "leaked-secret-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"internal error","echo":{"anthropic":"` + secret + `"}}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	_, err = client.FetchProviderCredentials(context.Background(), "sess-1", "tok", 1)
	if err == nil {
		t.Fatal("FetchProviderCredentials() error = nil, want an error for a 500 response")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("FetchProviderCredentials() error = %q, must never contain the raw response body/secret %q", err.Error(), secret)
	}
}

func TestCPClient_FetchProviderCredentials_MalformedResponseBody(t *testing.T) {
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

	_, err = client.FetchProviderCredentials(context.Background(), "sess-1", "tok", 1)
	if err == nil {
		t.Fatal("FetchProviderCredentials() error = nil, want an error for a malformed 2xx response body")
	}
}
