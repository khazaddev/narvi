package snapshotclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/snapshotclient"
)

const testMintTimeout = 5 * time.Second

func TestNew_SchemeSwap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{"wss to https", "wss://cp.example.com/sessions/sess-1/ws?type=sandbox"},
		// ws->http is only allowed for a loopback host (see
		// TestNew_PlaintextRejectedForNonLoopbackHost /
		// TestNew_PlaintextAllowedForLoopbackHost below) -- use one here
		// too, since this test only exercises the scheme-swap mechanism
		// itself, not the loopback policy.
		{"ws to http", "ws://127.0.0.1:9999/sessions/sess-1/ws?type=sandbox"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := snapshotclient.New(tc.in, testMintTimeout)
			if err != nil {
				t.Fatalf("New(%q) error = %v, want nil", tc.in, err)
			}
			_ = client // baseURL is unexported; exercised indirectly via Mint below
		})
	}
}

// TestNew_PlaintextRejectedForNonLoopbackHost proves a plaintext ws:// URL
// is only accepted for a loopback host -- a real, non-loopback control
// plane must use wss://, since ws:// carries the sandbox bearer token and
// the minted snapshot id in the clear.
func TestNew_PlaintextRejectedForNonLoopbackHost(t *testing.T) {
	t.Parallel()

	_, err := snapshotclient.New("ws://cp.example.com/sessions/sess-1/ws?type=sandbox", testMintTimeout)
	if err == nil {
		t.Fatal("New() error = nil, want an error for plaintext ws:// against a non-loopback host")
	}

	var urlErr *snapshotclient.InvalidControlPlaneWsURLError
	if !errors.As(err, &urlErr) {
		t.Fatalf("New() error = %v (%T), want *InvalidControlPlaneWsURLError", err, err)
	}
}

// TestNew_PlaintextAllowedForLoopbackHost proves the loopback exception
// itself works (not just that non-loopback is rejected) -- this is also
// what every other test in this file relies on via wsEquivalent's use of
// httptest.Server's own 127.0.0.1 address.
func TestNew_PlaintextAllowedForLoopbackHost(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"127.0.0.1:9999", "localhost:9999", "[::1]:9999"} {
		host := host
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			_, err := snapshotclient.New("ws://"+host+"/sessions/sess-1/ws?type=sandbox", testMintTimeout)
			if err != nil {
				t.Errorf("New() error = %v, want nil for loopback host %q", err, host)
			}
		})
	}
}

func TestNew_InvalidScheme(t *testing.T) {
	t.Parallel()

	_, err := snapshotclient.New("https://cp.example.com/sessions/sess-1/ws", testMintTimeout)
	if err == nil {
		t.Fatal("New() error = nil, want an error for a non-ws(s) scheme")
	}

	var urlErr *snapshotclient.InvalidControlPlaneWsURLError
	if !errors.As(err, &urlErr) {
		t.Fatalf("New() error = %v (%T), want *InvalidControlPlaneWsURLError", err, err)
	}
}

func TestNew_UnparsableURL(t *testing.T) {
	t.Parallel()

	_, err := snapshotclient.New("://not a url", testMintTimeout)
	if err == nil {
		t.Fatal("New() error = nil, want an error for an unparsable URL")
	}
}

// TestInvalidControlPlaneWsURLError_ErrorAndUnwrap exercises the error
// type's own Error()/Unwrap() methods directly.
func TestInvalidControlPlaneWsURLError_ErrorAndUnwrap(t *testing.T) {
	t.Parallel()

	inner := errors.New("inner cause")
	e := &snapshotclient.InvalidControlPlaneWsURLError{Value: "bogus://x", Err: inner}

	if e.Error() == "" {
		t.Error("Error() = \"\", want a non-empty message")
	}
	if !strings.Contains(e.Error(), "bogus://x") {
		t.Errorf("Error() = %q, want it to mention the invalid value", e.Error())
	}
	if e.Unwrap() != inner {
		t.Errorf("Unwrap() = %v, want the wrapped inner error %v", e.Unwrap(), inner)
	}
}

func TestClient_Mint_RequestShape(t *testing.T) {
	t.Parallel()

	var gotPath, gotAuth, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"snapshotId": "snap-abc-123"})
	}))
	defer server.Close()

	client, err := snapshotclient.New(wsEquivalent(server.URL), testMintTimeout)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	id, err := client.Mint(context.Background(), "sess-1", "sandbox-tok")
	if err != nil {
		t.Fatalf("Mint() error = %v, want nil", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("request method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotAuth != "Bearer sandbox-tok" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer sandbox-tok")
	}
	if gotPath != "/sessions/sess-1/snapshot" {
		t.Errorf("request path = %q, want %q", gotPath, "/sessions/sess-1/snapshot")
	}
	if id != "snap-abc-123" {
		t.Errorf("Mint() = %q, want %q", id, "snap-abc-123")
	}
}

func TestClient_Mint_NonTwoXXIsAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	client, err := snapshotclient.New(wsEquivalent(server.URL), testMintTimeout)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = client.Mint(context.Background(), "sess-1", "tok")
	if err == nil {
		t.Fatal("Mint() error = nil, want an error for a 502 response")
	}
}

// TestClient_Mint_ErrorResponseBodyNeverLeaks constructs a fake error
// response containing a secret-shaped payload and asserts that exact
// string never appears anywhere in Mint's returned error message --
// mirroring credentials.CPClient.Fetch's own identical test.
func TestClient_Mint_ErrorResponseBodyNeverLeaks(t *testing.T) {
	t.Parallel()

	const secret = "leaked-secret-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized","echo":{"token":"` + secret + `"}}`))
	}))
	defer server.Close()

	client, err := snapshotclient.New(wsEquivalent(server.URL), testMintTimeout)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = client.Mint(context.Background(), "sess-1", "tok")
	if err == nil {
		t.Fatal("Mint() error = nil, want an error for a 401 response")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Mint() error = %q, must never contain the raw response body/secret %q", err.Error(), secret)
	}
}

func TestClient_Mint_MalformedResponseBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer server.Close()

	client, err := snapshotclient.New(wsEquivalent(server.URL), testMintTimeout)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = client.Mint(context.Background(), "sess-1", "tok")
	if err == nil {
		t.Fatal("Mint() error = nil, want an error for a malformed 2xx response body")
	}
}

func TestClient_Mint_MissingSnapshotIDIsAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{})
	}))
	defer server.Close()

	client, err := snapshotclient.New(wsEquivalent(server.URL), testMintTimeout)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = client.Mint(context.Background(), "sess-1", "tok")
	if err == nil {
		t.Fatal("Mint() error = nil, want an error for a response with an empty/missing snapshotId")
	}
}

// wsEquivalent turns an httptest.Server's http:// URL into a matching
// ws://.../sessions/x/ws?type=sandbox URL, so New's own scheme-swap
// derivation (ws->http) reconstructs exactly the httptest server's real
// address.
func wsEquivalent(httpURL string) string {
	return strings.Replace(httpURL, "http://", "ws://", 1) + "/sessions/sess-1/ws?type=sandbox"
}
