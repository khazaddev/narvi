package slackapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
	"github.com/khazaddev/narvi/internal/app/ports"
)

func TestDeliver_Success(t *testing.T) {
	t.Parallel()

	var gotPath, gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")

	payload, err := json.Marshal(slackapi.Payload{
		ChannelID: "C123",
		ThreadTS:  "1234.5678",
		Text:      "Turn completed successfully.",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := client.Deliver(context.Background(), ports.Notification{
		Kind:    ports.NotificationKindSlack,
		Payload: payload,
	}); err != nil {
		t.Fatalf("Deliver() error = %v, want nil", err)
	}

	if gotPath != "/chat.postMessage" {
		t.Errorf("request path = %q, want %q", gotPath, "/chat.postMessage")
	}
	if gotAuth != "Bearer xoxb-test-token" {
		t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer xoxb-test-token")
	}
	if gotBody["channel"] != "C123" {
		t.Errorf("channel = %v, want %q", gotBody["channel"], "C123")
	}
	if gotBody["thread_ts"] != "1234.5678" {
		t.Errorf("thread_ts = %v, want %q", gotBody["thread_ts"], "1234.5678")
	}
	if gotBody["text"] != "Turn completed successfully." {
		t.Errorf("text = %v, want %q", gotBody["text"], "Turn completed successfully.")
	}
}

func TestDeliver_SlackAPIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "channel_not_found"})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")

	payload, _ := json.Marshal(slackapi.Payload{ChannelID: "C123", ThreadTS: "1.1", Text: "hi"})

	err := client.Deliver(context.Background(), ports.Notification{Kind: ports.NotificationKindSlack, Payload: payload})
	if err == nil {
		t.Fatal("Deliver() error = nil, want non-nil")
	}
	var deliveryErr *slackapi.DeliveryError
	if !errors.As(err, &deliveryErr) {
		t.Fatalf("Deliver() error = %v, want *slackapi.DeliveryError", err)
	}
	if deliveryErr.SlackError != "channel_not_found" {
		t.Errorf("SlackError = %q, want %q", deliveryErr.SlackError, "channel_not_found")
	}
}

func TestDeliver_HTTPFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")
	payload, _ := json.Marshal(slackapi.Payload{ChannelID: "C123", ThreadTS: "1.1", Text: "hi"})

	err := client.Deliver(context.Background(), ports.Notification{Kind: ports.NotificationKindSlack, Payload: payload})
	if err == nil {
		t.Fatal("Deliver() error = nil, want non-nil (malformed/non-JSON body)")
	}
}

func TestDeliver_InvalidPayload(t *testing.T) {
	t.Parallel()

	client := slackapi.New(http.DefaultClient, "http://unused.invalid", "xoxb-test-token")

	err := client.Deliver(context.Background(), ports.Notification{
		Kind:    ports.NotificationKindSlack,
		Payload: []byte("not json"),
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want non-nil for invalid payload")
	}
}
