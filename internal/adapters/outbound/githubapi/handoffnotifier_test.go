package githubapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/handoff"
)

// TestHandoffNotifier_Deliver_PostsCommentAndAddsLabel proves ONE Deliver
// call does both halves of a handoff-sentinel run: posts the already-
// rendered summary as a plain issue comment, THEN adds the fixed
// "handoff" label.
func TestHandoffNotifier_Deliver_PostsCommentAndAddsLabel(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var commentBody map[string]any
	var addLabelsBody map[string]any
	var order []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/42/comments":
			order = append(order, "comment")
			_ = json.NewDecoder(r.Body).Decode(&commentBody)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/acme/widgets/issues/42/labels":
			order = append(order, "label")
			_ = json.NewDecoder(r.Body).Decode(&addLabelsBody)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	notifier := githubapi.NewHandoffNotifier(adapter, "baked-in-bot-token")

	payload, err := json.Marshal(githubapi.HandoffPayload{
		Owner:    "acme",
		Repo:     "widgets",
		PRNumber: 42,
		Body:     "### Handoff readiness\n\nsomething to report.",
		Label:    handoff.Label,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	if err := notifier.Deliver(context.Background(), ports.Notification{
		Kind:    ports.NotificationKindHandoffSentinel,
		Payload: payload,
	}); err != nil {
		t.Fatalf("Deliver() error = %v, want nil", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if commentBody["body"] != "### Handoff readiness\n\nsomething to report." {
		t.Errorf("comment body = %v, want the rendered summary", commentBody["body"])
	}

	addedLabels, _ := addLabelsBody["labels"].([]any)
	if len(addedLabels) != 1 || addedLabels[0] != handoff.Label {
		t.Errorf("added labels = %v, want [%q]", addLabelsBody["labels"], handoff.Label)
	}

	if len(order) != 2 || order[0] != "comment" || order[1] != "label" {
		t.Errorf("call order = %v, want [comment, label] (comment posted before the label sync)", order)
	}
}

// TestHandoffNotifier_Deliver_CommentFailure_NeverSyncsLabel proves the
// ordering contract: if posting the comment itself fails, the label is
// never added at all -- no partial "labeled but no comment posted" state.
func TestHandoffNotifier_Deliver_CommentFailure_NeverSyncsLabel(t *testing.T) {
	t.Parallel()

	var labelCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/widgets/issues/42/comments" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "boom"})
			return
		}
		labelCalls++
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	notifier := githubapi.NewHandoffNotifier(adapter, "baked-in-bot-token")

	payload, err := json.Marshal(githubapi.HandoffPayload{
		Owner: "acme", Repo: "widgets", PRNumber: 42, Body: "text", Label: handoff.Label,
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	err = notifier.Deliver(context.Background(), ports.Notification{Kind: ports.NotificationKindHandoffSentinel, Payload: payload})
	if err == nil {
		t.Fatal("Deliver() error = nil, want a non-nil error when posting the comment itself fails")
	}
	if labelCalls != 0 {
		t.Errorf("labelCalls = %d, want 0 (label sync must never run after a failed comment post)", labelCalls)
	}
}

// TestHandoffNotifier_Deliver_InvalidPayload proves a malformed outbox
// payload is a decode error, never a panic.
func TestHandoffNotifier_Deliver_InvalidPayload(t *testing.T) {
	t.Parallel()

	adapter := githubapi.New(http.DefaultClient, "http://unused.invalid")
	notifier := githubapi.NewHandoffNotifier(adapter, "bot-token")

	err := notifier.Deliver(context.Background(), ports.Notification{
		Kind:    ports.NotificationKindHandoffSentinel,
		Payload: []byte(`not json`),
	})
	if err == nil {
		t.Fatal("Deliver() error = nil, want a non-nil decode error")
	}
}
