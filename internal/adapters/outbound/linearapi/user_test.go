package linearapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
)

// TestGetUserEmail_Success proves GetUserEmail queries user(id) { email },
// authenticated with the given access token, and returns the real value.
func TestGetUserEmail_Success(t *testing.T) {
	t.Parallel()

	var gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"user": map[string]any{"email": "ada@example.com"},
			},
		})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)

	email, err := client.GetUserEmail(context.Background(), "test-access-token", "linear-user-1")
	if err != nil {
		t.Fatalf("GetUserEmail() error = %v, want nil", err)
	}
	if email != "ada@example.com" {
		t.Errorf("email = %q, want %q", email, "ada@example.com")
	}
	if gotAuth != "Bearer test-access-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-access-token")
	}

	variables, ok := gotBody["variables"].(map[string]any)
	if !ok {
		t.Fatalf("request body has no variables: %v", gotBody)
	}
	if variables["id"] != "linear-user-1" {
		t.Errorf("variables.id = %v, want %q", variables["id"], "linear-user-1")
	}
}

// TestGetUserEmail_GraphQLError proves a GraphQL-level error (Linear's own
// "not found") surfaces as a plain error, unclassified -- the caller
// (internal/app/identitylink.Resolve) is what decides retryable vs
// permanent.
func TestGetUserEmail_GraphQLError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "Entity not found"}},
		})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)

	_, err := client.GetUserEmail(context.Background(), "test-access-token", "unknown-user")
	if err == nil {
		t.Fatal("GetUserEmail() error = nil, want non-nil")
	}
}

// TestGetUserEmail_HTTPFailure proves a non-2xx HTTP response surfaces as
// a plain error.
func TestGetUserEmail_HTTPFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)

	_, err := client.GetUserEmail(context.Background(), "test-access-token", "linear-user-1")
	if err == nil {
		t.Fatal("GetUserEmail() error = nil, want non-nil")
	}
}
