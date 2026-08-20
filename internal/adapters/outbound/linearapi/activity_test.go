package linearapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/linearapi"
)

// TestCreateResponseActivity_Success proves CreateResponseActivity posts a
// "response"-typed AgentActivity via a real agentActivityCreate GraphQL
// mutation, authenticated with the given access token.
func TestCreateResponseActivity_Success(t *testing.T) {
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
				"agentActivityCreate": map[string]any{"success": true},
			},
		})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)

	err := client.CreateResponseActivity(context.Background(), "test-access-token", "agent-session-1", "Turn completed successfully.")
	if err != nil {
		t.Fatalf("CreateResponseActivity() error = %v, want nil", err)
	}

	if gotAuth != "Bearer test-access-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-access-token")
	}

	variables, ok := gotBody["variables"].(map[string]any)
	if !ok {
		t.Fatalf("request body has no variables: %v", gotBody)
	}
	input, ok := variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("variables has no input: %v", variables)
	}
	if input["agentSessionId"] != "agent-session-1" {
		t.Errorf("agentSessionId = %v, want %q", input["agentSessionId"], "agent-session-1")
	}
	content, ok := input["content"].(map[string]any)
	if !ok {
		t.Fatalf("input has no content: %v", input)
	}
	if content["type"] != "response" {
		t.Errorf("content.type = %v, want %q", content["type"], "response")
	}
	if content["body"] != "Turn completed successfully." {
		t.Errorf("content.body = %v, want %q", content["body"], "Turn completed successfully.")
	}
}

// TestCreateErrorActivity_Success mirrors TestCreateResponseActivity_Success
// but for the "error"-typed content, proving the two outcome types are
// distinguished correctly.
func TestCreateErrorActivity_Success(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"agentActivityCreate": map[string]any{"success": true},
			},
		})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)

	err := client.CreateErrorActivity(context.Background(), "test-access-token", "agent-session-1", "Turn failed: sandbox crashed.")
	if err != nil {
		t.Fatalf("CreateErrorActivity() error = %v, want nil", err)
	}

	variables := gotBody["variables"].(map[string]any)
	input := variables["input"].(map[string]any)
	content := input["content"].(map[string]any)
	if content["type"] != "error" {
		t.Errorf("content.type = %v, want %q", content["type"], "error")
	}
	if content["body"] != "Turn failed: sandbox crashed." {
		t.Errorf("content.body = %v, want %q", content["body"], "Turn failed: sandbox crashed.")
	}
}

// TestCreateResponseActivity_GraphQLError proves a top-level GraphQL
// "errors" array on an HTTP 200 response is surfaced as a real error, not
// silently swallowed.
func TestCreateResponseActivity_GraphQLError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "not authorized"}},
		})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)

	err := client.CreateResponseActivity(context.Background(), "bad-token", "agent-session-1", "hi")
	if err == nil {
		t.Fatal("CreateResponseActivity() error = nil, want non-nil")
	}
}

// TestCreateThoughtActivity_Success proves CreateThoughtActivity posts a
// "thought"-typed AgentActivity via a real agentActivityCreate GraphQL
// mutation, authenticated with the given access token -- the synchronous
// 10-second acknowledgment §8.10's webhook handler sends on a `created`
// AgentSessionEvent, and the outbox worker's async mid-turn progress
// notification.
func TestCreateThoughtActivity_Success(t *testing.T) {
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
				"agentActivityCreate": map[string]any{"success": true},
			},
		})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)

	err := client.CreateThoughtActivity(context.Background(), "test-access-token", "agent-session-1", "Looking into this now.")
	if err != nil {
		t.Fatalf("CreateThoughtActivity() error = %v, want nil", err)
	}

	if gotAuth != "Bearer test-access-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer test-access-token")
	}

	variables, ok := gotBody["variables"].(map[string]any)
	if !ok {
		t.Fatalf("request body has no variables: %v", gotBody)
	}
	input, ok := variables["input"].(map[string]any)
	if !ok {
		t.Fatalf("variables has no input: %v", variables)
	}
	if input["agentSessionId"] != "agent-session-1" {
		t.Errorf("agentSessionId = %v, want %q", input["agentSessionId"], "agent-session-1")
	}
	content, ok := input["content"].(map[string]any)
	if !ok {
		t.Fatalf("input has no content: %v", input)
	}
	if content["type"] != "thought" {
		t.Errorf("content.type = %v, want %q", content["type"], "thought")
	}
	if content["body"] != "Looking into this now." {
		t.Errorf("content.body = %v, want %q", content["body"], "Looking into this now.")
	}
}

// TestCreateThoughtActivity_GraphQLError mirrors
// TestCreateResponseActivity_GraphQLError for the thought path: a top-level
// GraphQL "errors" array on an HTTP 200 response is surfaced as a real
// error, not silently swallowed.
func TestCreateThoughtActivity_GraphQLError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"errors": []map[string]any{{"message": "not authorized"}},
		})
	}))
	defer server.Close()

	client := linearapi.New(server.Client(), server.URL)

	err := client.CreateThoughtActivity(context.Background(), "bad-token", "agent-session-1", "hi")
	if err == nil {
		t.Fatal("CreateThoughtActivity() error = nil, want non-nil")
	}
}
