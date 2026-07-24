package slackapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
)

func TestGetUserEmail_Success(t *testing.T) {
	t.Parallel()

	var gotPath, gotAuth, gotUser string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUser = r.URL.Query().Get("user")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"user": map[string]any{
				"profile": map[string]any{"email": "ada@example.com"},
			},
		})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")

	email, ok, err := client.GetUserEmail(context.Background(), "U123")
	if err != nil {
		t.Fatalf("GetUserEmail() error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("GetUserEmail() ok = false, want true")
	}
	if email != "ada@example.com" {
		t.Errorf("email = %q, want %q", email, "ada@example.com")
	}
	if gotPath != "/users.info" {
		t.Errorf("request path = %q, want %q", gotPath, "/users.info")
	}
	if gotAuth != "Bearer xoxb-test-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer xoxb-test-token")
	}
	if gotUser != "U123" {
		t.Errorf("user query param = %q, want %q", gotUser, "U123")
	}
}

func TestGetUserEmail_NoEmailVisible(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"user": map[string]any{"profile": map[string]any{}},
		})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")

	email, ok, err := client.GetUserEmail(context.Background(), "U123")
	if err != nil {
		t.Fatalf("GetUserEmail() error = %v, want nil", err)
	}
	if ok {
		t.Fatal("GetUserEmail() ok = true, want false (no email visible)")
	}
	if email != "" {
		t.Errorf("email = %q, want empty", email)
	}
}

func TestGetUserEmail_UserNotFoundIsPermanent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "user_not_found"})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")

	_, ok, err := client.GetUserEmail(context.Background(), "U123")
	if ok {
		t.Fatal("GetUserEmail() ok = true, want false")
	}
	if !errors.Is(err, slackapi.ErrSlackUserNotFound) {
		t.Fatalf("GetUserEmail() error = %v, want ErrSlackUserNotFound", err)
	}
}

func TestGetUserEmail_OtherAPIErrorIsRetryable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "ratelimited"})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")

	_, ok, err := client.GetUserEmail(context.Background(), "U123")
	if ok {
		t.Fatal("GetUserEmail() ok = true, want false")
	}
	if err == nil {
		t.Fatal("GetUserEmail() error = nil, want non-nil")
	}
	if errors.Is(err, slackapi.ErrSlackUserNotFound) {
		t.Error("GetUserEmail() error should NOT be ErrSlackUserNotFound for a non-user_not_found API error")
	}
}

func TestGetUserEmail_HTTPFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")

	_, ok, err := client.GetUserEmail(context.Background(), "U123")
	if ok {
		t.Fatal("GetUserEmail() ok = true, want false")
	}
	if err == nil {
		t.Fatal("GetUserEmail() error = nil, want non-nil (malformed/non-JSON body)")
	}
}
