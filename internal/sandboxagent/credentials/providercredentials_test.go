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
		// Step 59 (§29.6): the per-provider value is now a discriminated
		// union, not a bare plaintext string.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"credentials": map[string]any{"anthropic": map[string]string{"type": "api", "key": "sk-real-value"}},
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
	entry := got["anthropic"]
	if entry.Type != "api" || entry.Key == nil || *entry.Key != "sk-real-value" {
		t.Errorf("Credentials[anthropic] = %+v, want type=api key=%q", entry, "sk-real-value")
	}
}

// TestCPClient_FetchProviderCredentials_OAuthEntry proves the "oauth"
// Auth-union member (§29.6) decodes correctly, and -- critically
// -- that AuthValue has no field a "refresh" key in the response COULD
// populate even if a buggy/malicious CP response tried to send one.
func TestCPClient_FetchProviderCredentials_OAuthEntry(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A "refresh" key here (which must never legitimately appear, per
		// providercredentialsdelivery.go's own credentialAuthValue) is
		// deliberately included to prove AuthValue has nowhere to put it.
		_, _ = w.Write([]byte(`{"credentials":{"openai":{"type":"oauth","access":"access-abc","expires":1234567890123,"accountId":"acct-xyz","refresh":"SHOULD-BE-IGNORED"}}}`))
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
	entry := got["openai"]
	if entry.Type != "oauth" {
		t.Errorf("Type = %q, want %q", entry.Type, "oauth")
	}
	if entry.Access == nil || *entry.Access != "access-abc" {
		t.Errorf("Access = %v, want %q", entry.Access, "access-abc")
	}
	if entry.Expires == nil || *entry.Expires != 1234567890123 {
		t.Errorf("Expires = %v, want %d", entry.Expires, int64(1234567890123))
	}
	if entry.AccountID == nil || *entry.AccountID != "acct-xyz" {
		t.Errorf("AccountID = %v, want %q", entry.AccountID, "acct-xyz")
	}
	if entry.Key != nil {
		t.Errorf("Key = %v, want nil for an oauth entry", entry.Key)
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
