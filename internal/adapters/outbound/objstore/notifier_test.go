package objstore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/app/ports"
)

func TestBlobDeleteNotifier_Deliver_Success(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	store, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	n := NewBlobDeleteNotifier(store)

	payload, err := json.Marshal(BlobDeletePayload{Key: "sessions/abc/uploads/def"})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	if err := n.Deliver(context.Background(), ports.Notification{Payload: payload}); err != nil {
		t.Fatalf("Deliver() error = %v, want nil", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("request method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/test-bucket/sessions/abc/uploads/def" {
		t.Errorf("request path = %q, want %q", gotPath, "/test-bucket/sessions/abc/uploads/def")
	}
}

func TestBlobDeleteNotifier_Deliver_PropagatesStoreError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	n := NewBlobDeleteNotifier(store)

	payload, _ := json.Marshal(BlobDeletePayload{Key: "k"})
	if err := n.Deliver(context.Background(), ports.Notification{Payload: payload}); err == nil {
		t.Fatal("Deliver() error = nil, want a propagated error for a 500 response")
	}
}

// TestBlobDeleteNotifier_Deliver_IdempotentOnAlreadyDeleted proves
// redelivery of a "blob_delete" outbox entry for a key that was already
// successfully deleted (a crash between Store.Delete succeeding and the
// outbox mark-delivered write, or a retried attempt after a transient
// failure) is safe: the underlying Store.Delete swallows not-found, so
// Deliver returns nil rather than an error that would otherwise keep
// retrying forever.
func TestBlobDeleteNotifier_Deliver_IdempotentOnAlreadyDeleted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	store, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	n := NewBlobDeleteNotifier(store)

	payload, _ := json.Marshal(BlobDeletePayload{Key: "already-gone"})
	if err := n.Deliver(context.Background(), ports.Notification{Payload: payload}); err != nil {
		t.Errorf("Deliver() error = %v, want nil (redelivery against an already-absent key is idempotent)", err)
	}
}

func TestBlobDeleteNotifier_Deliver_MalformedPayload(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	store, err := New(testConfig(srv.URL))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	n := NewBlobDeleteNotifier(store)

	if err := n.Deliver(context.Background(), ports.Notification{Payload: []byte("not json")}); err == nil {
		t.Fatal("Deliver() error = nil, want a decode error for a malformed payload")
	}
	if hit {
		t.Error("Deliver() made an HTTP call, want none for a payload that fails to decode")
	}
}
