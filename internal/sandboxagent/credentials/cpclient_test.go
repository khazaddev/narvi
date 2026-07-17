package credentials_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
)

const testFetchTimeout = 5 * time.Second

func TestNewCPClient_SchemeSwap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{"wss to https", "wss://cp.example.com/sessions/sess-1/ws?type=sandbox"},
		// ws->http is only allowed for a loopback host (see
		// TestNewCPClient_PlaintextRejectedForNonLoopbackHost /
		// TestNewCPClient_PlaintextAllowedForLoopbackHost below) -- use one
		// here too, since this test is only exercising the scheme-swap
		// mechanism itself, not the loopback policy.
		{"ws to http", "ws://127.0.0.1:9999/sessions/sess-1/ws?type=sandbox"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client, err := credentials.NewCPClient(tc.in, testFetchTimeout)
			if err != nil {
				t.Fatalf("NewCPClient(%q) error = %v, want nil", tc.in, err)
			}
			_ = client // baseURL is unexported; exercised indirectly via Fetch below
		})
	}
}

// TestNewCPClient_PlaintextRejectedForNonLoopbackHost proves a plaintext
// ws:// URL is only accepted for a loopback host -- a real, non-loopback
// control plane must use wss://, since ws:// carries the sandbox bearer
// token and the minted credential in the clear.
func TestNewCPClient_PlaintextRejectedForNonLoopbackHost(t *testing.T) {
	t.Parallel()

	_, err := credentials.NewCPClient("ws://cp.example.com/sessions/sess-1/ws?type=sandbox", testFetchTimeout)
	if err == nil {
		t.Fatal("NewCPClient() error = nil, want an error for plaintext ws:// against a non-loopback host")
	}

	var urlErr *credentials.InvalidControlPlaneWsURLError
	if !errors.As(err, &urlErr) {
		t.Fatalf("NewCPClient() error = %v (%T), want *InvalidControlPlaneWsURLError", err, err)
	}
}

// TestNewCPClient_PlaintextAllowedForLoopbackHost proves the loopback
// exception itself works (not just that non-loopback is rejected) -- this
// is also what every other test in this file relies on via wsEquivalent's
// use of httptest.Server's own 127.0.0.1 address.
func TestNewCPClient_PlaintextAllowedForLoopbackHost(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"127.0.0.1:9999", "localhost:9999", "[::1]:9999"} {
		host := host
		t.Run(host, func(t *testing.T) {
			t.Parallel()
			_, err := credentials.NewCPClient("ws://"+host+"/sessions/sess-1/ws?type=sandbox", testFetchTimeout)
			if err != nil {
				t.Errorf("NewCPClient() error = %v, want nil for loopback host %q", err, host)
			}
		})
	}
}

func TestNewCPClient_InvalidScheme(t *testing.T) {
	t.Parallel()

	_, err := credentials.NewCPClient("https://cp.example.com/sessions/sess-1/ws", testFetchTimeout)
	if err == nil {
		t.Fatal("NewCPClient() error = nil, want an error for a non-ws(s) scheme")
	}

	var urlErr *credentials.InvalidControlPlaneWsURLError
	if !errors.As(err, &urlErr) {
		t.Fatalf("NewCPClient() error = %v (%T), want *InvalidControlPlaneWsURLError", err, err)
	}
}

func TestNewCPClient_UnparsableURL(t *testing.T) {
	t.Parallel()

	_, err := credentials.NewCPClient("://not a url", testFetchTimeout)
	if err == nil {
		t.Fatal("NewCPClient() error = nil, want an error for an unparsable URL")
	}
}

// TestInvalidControlPlaneWsURLError_ErrorAndUnwrap exercises the error
// type's own Error()/Unwrap() methods directly -- both are real,
// user-facing behavior (errors.As/Is support, a non-empty message), not
// just plumbing.
func TestInvalidControlPlaneWsURLError_ErrorAndUnwrap(t *testing.T) {
	t.Parallel()

	inner := errors.New("inner cause")
	e := &credentials.InvalidControlPlaneWsURLError{Value: "bogus://x", Err: inner}

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

func TestCPClient_Fetch_RequestShape(t *testing.T) {
	t.Parallel()

	var gotHost, gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		var body struct {
			Host string `json:"host"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		gotHost = body.Host

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"username":  "x-token",
			"password":  "secret-pass",
			"expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339),
		})
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	cred, err := client.Fetch(context.Background(), "sess-1", "sandbox-tok", "example.com")
	if err != nil {
		t.Fatalf("Fetch() error = %v, want nil", err)
	}

	if gotHost != "example.com" {
		t.Errorf("request body host = %q, want %q", gotHost, "example.com")
	}
	if gotAuth != "Bearer sandbox-tok" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer sandbox-tok")
	}
	if gotPath != "/sessions/sess-1/scm-credentials" {
		t.Errorf("request path = %q, want %q", gotPath, "/sessions/sess-1/scm-credentials")
	}
	if cred.Username != "x-token" || cred.Password != "secret-pass" {
		t.Errorf("Fetch() = %+v, want username=x-token password=secret-pass", cred)
	}
}

func TestCPClient_Fetch_NonTwoXXIsAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	_, err = client.Fetch(context.Background(), "sess-1", "tok", "example.com")
	if err == nil {
		t.Fatal("Fetch() error = nil, want an error for a 500 response")
	}
}

// TestCPClient_Fetch_ErrorResponseBodyNeverLeaks constructs a fake 401
// response containing a secret-shaped payload and asserts that exact
// secret string never appears anywhere in Fetch's returned error message
// -- mirroring the Modal adapter's own classifyErrorResponse lesson
// (internal/adapters/outbound/modal/errors.go): a response body must never
// be embedded where it could leak a credential.
func TestCPClient_Fetch_ErrorResponseBodyNeverLeaks(t *testing.T) {
	t.Parallel()

	const secret = "leaked-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized","echo":{"password":"` + secret + `"}}`))
	}))
	defer server.Close()

	client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
	if err != nil {
		t.Fatalf("NewCPClient() error = %v", err)
	}

	_, err = client.Fetch(context.Background(), "sess-1", "tok", "example.com")
	if err == nil {
		t.Fatal("Fetch() error = nil, want an error for a 401 response")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("Fetch() error = %q, must never contain the raw response body/secret %q", err.Error(), secret)
	}
}

// TestCPClient_Fetch_RejectsNewlineInUsernameOrPassword proves Fetch
// rejects a response whose username/password contains a newline, rather
// than passing it through -- an adversarial review caught that RunGet
// writes these values verbatim into git's own newline-delimited
// credential-helper protocol, so a "\n" smuggled into either field could
// inject an extra key=value line git's parser would then honor (e.g. a
// second "username=evil" line overriding the intended one).
func TestCPClient_Fetch_RejectsNewlineInUsernameOrPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		username string
		password string
	}{
		{"newline in username", "realuser\nusername=evil", "realpass"},
		{"newline in password", "realuser", "realpass\npassword=evil"},
		{"carriage return in username", "realuser\rusername=evil", "realpass"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]string{
					"username":  tc.username,
					"password":  tc.password,
					"expiresAt": time.Now().Add(time.Hour).Format(time.RFC3339),
				})
			}))
			defer server.Close()

			client, err := credentials.NewCPClient(wsEquivalent(server.URL), testFetchTimeout)
			if err != nil {
				t.Fatalf("NewCPClient() error = %v", err)
			}

			_, err = client.Fetch(context.Background(), "sess-1", "tok", "example.com")
			if err == nil {
				t.Fatal("Fetch() error = nil, want an error for a username/password containing a newline")
			}
		})
	}
}

func TestCPClient_Fetch_MalformedResponseBody(t *testing.T) {
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

	_, err = client.Fetch(context.Background(), "sess-1", "tok", "example.com")
	if err == nil {
		t.Fatal("Fetch() error = nil, want an error for a malformed 2xx response body")
	}
}

// wsEquivalent turns an httptest.Server's http:// URL into a matching
// ws://.../sessions/x/ws?type=sandbox URL, so NewCPClient's own scheme-swap
// derivation (ws->http) reconstructs exactly the httptest server's real
// address.
func wsEquivalent(httpURL string) string {
	return strings.Replace(httpURL, "http://", "ws://", 1) + "/sessions/sess-1/ws?type=sandbox"
}
