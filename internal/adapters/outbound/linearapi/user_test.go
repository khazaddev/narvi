package linearapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/narvidev/narvi/internal/adapters/outbound/linearapi"
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

// TestGetUserEmail_EntityNotFound proves Linear's own real "Entity not
// found" GraphQL error (a user id that no longer resolves -- deactivated/
// removed from the workspace) surfaces as ErrLinearUserNotFound
// specifically, so internal/adapters/inbound/linear/identity.go's own
// fetch closure can classify it as platform.Permanent -- mirroring
// slackapi.ErrSlackUserNotFound's identical role for Slack's own
// "user_not_found" response.
func TestGetUserEmail_EntityNotFound(t *testing.T) {
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
	if !errors.Is(err, linearapi.ErrLinearUserNotFound) {
		t.Fatalf("GetUserEmail() error = %v, want errors.Is(err, ErrLinearUserNotFound)", err)
	}
}

// TestGetUserEmail_OtherGraphQLError proves a GraphQL-level error OTHER
// than "Entity not found" (e.g. a permission-denied field error) still
// surfaces as a plain, unclassified error -- the caller (internal/
// adapters/inbound/linear/identity.go's own fetch closure) treats it as
// retryable, never as platform.Permanent, distinguishing it from the
// definitive-not-found case above.
func TestGetUserEmail_OtherGraphQLError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "You do not have permission to access this resource"}},
		})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)

	_, err := client.GetUserEmail(context.Background(), "test-access-token", "some-user")
	if err == nil {
		t.Fatal("GetUserEmail() error = nil, want non-nil")
	}
	if errors.Is(err, linearapi.ErrLinearUserNotFound) {
		t.Errorf("GetUserEmail() error = %v, want NOT errors.Is(err, ErrLinearUserNotFound)", err)
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
