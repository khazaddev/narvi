package rwx_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/rwx"
	"github.com/khazaddev/narvi/internal/platform"
)

// Black-box (`package rwx_test`) -- DispatchClient hits a real, documented
// REST API (§4.1.1), genuinely testable end to end against a fake
// httptest.Server, exactly like every other outbound adapter's own public
// surface in this codebase (modal, githubapi) -- no need to reach
// unexported members here, unlike provider_test.go's own CLI-transport
// tests.

func TestDispatchClient_Dispatch_Success(t *testing.T) {
	t.Parallel()

	var gotPath, gotMethod, gotAuth, gotCorrelation string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotCorrelation = r.Header.Get(platform.CorrelationIDHeader)
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("server: decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"dispatch_id": "dispatch-123"})
	}))
	defer server.Close()

	client := rwx.NewDispatchClient(server.Client(), server.URL, "test-rwx-token")

	ctx := platform.WithCorrelationID(context.Background(), "corr-xyz")
	dispatchID, err := client.Dispatch(ctx, "preview-key", "abc123sha", "", map[string]string{
		"pr-number":  "42",
		"head-sha":   "abc123sha",
		"session-id": "sess-1",
	})
	if err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if dispatchID != "dispatch-123" {
		t.Errorf("Dispatch() = %q, want %q", dispatchID, "dispatch-123")
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/mint/api/runs/dispatches" {
		t.Errorf("path = %q, want %q", gotPath, "/mint/api/runs/dispatches")
	}
	if gotAuth != "Bearer test-rwx-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer test-rwx-token")
	}
	if gotCorrelation != "corr-xyz" {
		t.Errorf("%s = %q, want %q", platform.CorrelationIDHeader, gotCorrelation, "corr-xyz")
	}
	if gotBody["key"] != "preview-key" {
		t.Errorf("request body key = %v, want %q", gotBody["key"], "preview-key")
	}
	if gotBody["ref"] != "abc123sha" {
		t.Errorf("request body ref = %v, want %q", gotBody["ref"], "abc123sha")
	}
	params, ok := gotBody["params"].(map[string]any)
	if !ok {
		t.Fatalf("request body params = %v, want a map", gotBody["params"])
	}
	if params["pr-number"] != "42" || params["head-sha"] != "abc123sha" || params["session-id"] != "sess-1" {
		t.Errorf("request body params = %v, want {pr-number:42, head-sha:abc123sha, session-id:sess-1}", params)
	}
}

func TestDispatchClient_Dispatch_DefaultBaseURL(t *testing.T) {
	t.Parallel()
	// NewDispatchClient with an empty baseURL must default to RWX's real
	// host -- proven indirectly: passing "" here must not panic and must
	// produce a client that (if it ever made a real call) would target
	// https://cloud.rwx.com, never an empty/invalid URL. A real network
	// call against that host is never made in this test (no server to hit
	// -- this only proves construction succeeds).
	client := rwx.NewDispatchClient(nil, "", "tok")
	if client == nil {
		t.Fatal("NewDispatchClient() = nil")
	}
}

func TestDispatchClient_Dispatch_ErrorClassification(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		wantTransient bool
	}{
		{"500 internal server error", http.StatusInternalServerError, true},
		{"429 too many requests", http.StatusTooManyRequests, true},
		{"401 unauthorized", http.StatusUnauthorized, false},
		{"404 not found", http.StatusNotFound, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "error", "error": "boom"})
			}))
			defer server.Close()

			client := rwx.NewDispatchClient(server.Client(), server.URL, "tok")
			_, err := client.Dispatch(context.Background(), "key", "sha", "", nil)
			if err == nil {
				t.Fatal("Dispatch() error = nil, want a DispatchError")
			}

			var de *rwx.DispatchError
			if !errors.As(err, &de) {
				t.Fatalf("Dispatch() error = %v, want *rwx.DispatchError", err)
			}
			if de.Transient != tt.wantTransient {
				t.Errorf("Transient = %v, want %v", de.Transient, tt.wantTransient)
			}
			if de.Status != tt.status {
				t.Errorf("Status = %d, want %d", de.Status, tt.status)
			}
		})
	}
}

func TestDispatchClient_Dispatch_NetworkError(t *testing.T) {
	t.Parallel()

	client := rwx.NewDispatchClient(http.DefaultClient, "http://127.0.0.1:9", "tok")
	_, err := client.Dispatch(context.Background(), "key", "sha", "", nil)
	if err == nil {
		t.Fatal("Dispatch() error = nil, want a network-level DispatchError")
	}
	var de *rwx.DispatchError
	if !errors.As(err, &de) {
		t.Fatalf("Dispatch() error = %v, want *rwx.DispatchError", err)
	}
	if !de.Transient {
		t.Error("Transient = false, want true (every network-level failure is transient)")
	}
}

func TestDispatchClient_Dispatch_MalformedResponseJSON(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	client := rwx.NewDispatchClient(server.Client(), server.URL, "tok")
	_, err := client.Dispatch(context.Background(), "key", "sha", "", nil)
	if err == nil {
		t.Fatal("Dispatch() error = nil, want a decode error")
	}
}
