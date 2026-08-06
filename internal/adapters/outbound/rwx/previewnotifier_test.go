package rwx_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/rwx"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// TestPreviewNotifier_Deliver proves Deliver decodes a
// rwx.PreviewDispatchPayload and dispatches it via the real Dispatches API
// shape (§4.1.2 point 2), satisfying ports.Notifier -- mirrors
// githubapi_test's own TestBotNotifier_Deliver precedent exactly.
func TestPreviewNotifier_Deliver(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"dispatch_id": "d-1"})
	}))
	defer server.Close()

	client := rwx.NewDispatchClient(server.Client(), server.URL, "baked-in-rwx-token")
	notifier := rwx.NewPreviewNotifier(client)

	payload, err := json.Marshal(rwx.PreviewDispatchPayload{
		DispatchKey: "preview-build",
		Ref:         "deadbeef",
		PRNumber:    7,
		HeadSHA:     "deadbeef",
		SessionID:   "sess-42",
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	err = notifier.Deliver(context.Background(), ports.Notification{
		Kind:    ports.NotificationKindRWXPreviewDispatch,
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v, want nil", err)
	}

	if gotBody["key"] != "preview-build" {
		t.Errorf("dispatch key = %v, want %q", gotBody["key"], "preview-build")
	}
	if gotBody["ref"] != "deadbeef" {
		t.Errorf("dispatch ref = %v, want %q", gotBody["ref"], "deadbeef")
	}
	params, ok := gotBody["params"].(map[string]any)
	if !ok {
		t.Fatalf("dispatch params = %v, want a map", gotBody["params"])
	}
	if params["pr-number"] != "7" {
		t.Errorf("params[pr-number] = %v, want %q", params["pr-number"], "7")
	}
	if params["head-sha"] != "deadbeef" {
		t.Errorf("params[head-sha] = %v, want %q", params["head-sha"], "deadbeef")
	}
	if params["session-id"] != "sess-42" {
		t.Errorf("params[session-id] = %v, want %q", params["session-id"], "sess-42")
	}
}

// TestPreviewNotifier_Deliver_MalformedPayload proves a malformed payload
// is a real, returned error, never a panic or silent no-op.
func TestPreviewNotifier_Deliver_MalformedPayload(t *testing.T) {
	t.Parallel()

	client := rwx.NewDispatchClient(nil, "https://example.invalid", "tok")
	notifier := rwx.NewPreviewNotifier(client)

	err := notifier.Deliver(context.Background(), ports.Notification{
		Kind:    ports.NotificationKindRWXPreviewDispatch,
		Payload: []byte("not json"),
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want an error for a malformed payload")
	}
}

// TestPreviewNotifier_Deliver_DispatchFailurePropagates proves a Dispatches
// API failure surfaces as a real Deliver error -- outboxworker's own
// domain/outbox.EvaluateBackoff decides retry/dead-letter purely from
// this non-nil error, never from inspecting its shape (ports.Notifier's
// own doc comment).
func TestPreviewNotifier_Deliver_DispatchFailurePropagates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := rwx.NewDispatchClient(server.Client(), server.URL, "tok")
	notifier := rwx.NewPreviewNotifier(client)

	payload, _ := json.Marshal(rwx.PreviewDispatchPayload{DispatchKey: "k", Ref: "sha"})
	err := notifier.Deliver(context.Background(), ports.Notification{
		Kind:    ports.NotificationKindRWXPreviewDispatch,
		Payload: payload,
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want the Dispatches API's own 500 to propagate")
	}
}
