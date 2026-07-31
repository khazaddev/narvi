package githubapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/domain/reviewpost"
)

// TestCreateReview_Success proves CreateReview posts the right path/body
// to GitHub's own "create a review" endpoint.
func TestCreateReview_Success(t *testing.T) {
	t.Parallel()

	var gotPath, gotMethod string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 1})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	err := adapter.CreateReview(context.Background(), "acme", "widgets", 42, "gho_realtoken", reviewpost.FormalReviewEventRequestChanges, "### Code review verdict\n\nblocked.")
	if err != nil {
		t.Fatalf("CreateReview() error = %v, want nil", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodPost)
	}
	if gotPath != "/repos/acme/widgets/pulls/42/reviews" {
		t.Errorf("path = %q, want %q", gotPath, "/repos/acme/widgets/pulls/42/reviews")
	}
	if gotBody["event"] != string(reviewpost.FormalReviewEventRequestChanges) {
		t.Errorf("event = %v, want %q", gotBody["event"], reviewpost.FormalReviewEventRequestChanges)
	}
	if gotBody["body"] != "### Code review verdict\n\nblocked." {
		t.Errorf("body = %v, want the rendered verdict text", gotBody["body"])
	}
}

// TestCreateReview_NonOKStatus_ReturnsError proves a non-2xx response is
// surfaced as an error, never silently swallowed.
func TestCreateReview_NonOKStatus_ReturnsError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Review cannot be submitted empty"})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	err := adapter.CreateReview(context.Background(), "acme", "widgets", 42, "gho_realtoken", reviewpost.FormalReviewEventComment, "")
	if err == nil {
		t.Fatal("CreateReview() error = nil, want a non-nil error for a 422 response")
	}
}

// TestAddLabels_Success proves AddLabels posts the right path/body.
func TestAddLabels_Success(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	err := adapter.AddLabels(context.Background(), "acme", "widgets", 42, "gho_realtoken", []string{reviewpost.LabelMediumRisk})
	if err != nil {
		t.Fatalf("AddLabels() error = %v, want nil", err)
	}

	if gotPath != "/repos/acme/widgets/issues/42/labels" {
		t.Errorf("path = %q, want %q", gotPath, "/repos/acme/widgets/issues/42/labels")
	}
	labels, _ := gotBody["labels"].([]any)
	if len(labels) != 1 || labels[0] != reviewpost.LabelMediumRisk {
		t.Errorf("labels = %v, want [%q]", gotBody["labels"], reviewpost.LabelMediumRisk)
	}
}

// TestAddLabels_EmptyList_NoRequest proves AddLabels never issues an HTTP
// call at all for an empty label list -- a no-op plan should cost nothing.
func TestAddLabels_EmptyList_NoRequest(t *testing.T) {
	t.Parallel()

	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	if err := adapter.AddLabels(context.Background(), "acme", "widgets", 42, "gho_realtoken", nil); err != nil {
		t.Fatalf("AddLabels() error = %v, want nil", err)
	}
	if called {
		t.Error("AddLabels made an HTTP request for an empty label list, want none")
	}
}

// TestRemoveLabel_Success proves RemoveLabel issues a DELETE against the
// right, URL-escaped path.
func TestRemoveLabel_Success(t *testing.T) {
	t.Parallel()

	var gotPath, gotMethod string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	if err := adapter.RemoveLabel(context.Background(), "acme", "widgets", 42, "gho_realtoken", reviewpost.LabelLowRisk); err != nil {
		t.Fatalf("RemoveLabel() error = %v, want nil", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want %q", gotMethod, http.MethodDelete)
	}
	if gotPath != "/repos/acme/widgets/issues/42/labels/review:low-risk" {
		t.Errorf("path = %q, want %q", gotPath, "/repos/acme/widgets/issues/42/labels/review:low-risk")
	}
}

// TestRemoveLabel_404IsSuccess proves RemoveLabel treats a 404 (label
// already absent) as a successful no-op, never an error -- required for
// safe retry/concurrent-sync idempotence.
func TestRemoveLabel_404IsSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Label does not exist"})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	if err := adapter.RemoveLabel(context.Background(), "acme", "widgets", 42, "gho_realtoken", reviewpost.LabelLowRisk); err != nil {
		t.Errorf("RemoveLabel() error = %v, want nil (a 404 must be a successful no-op)", err)
	}
}

// TestRemoveLabel_OtherErrorStillFails proves a non-404 failure IS still
// surfaced as an error (the 404 tolerance above must not become a
// blanket "ignore every error" bug).
func TestRemoveLabel_OtherErrorStillFails(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	err := adapter.RemoveLabel(context.Background(), "acme", "widgets", 42, "gho_realtoken", reviewpost.LabelLowRisk)
	if err == nil {
		t.Fatal("RemoveLabel() error = nil, want a non-nil error for a 500 response")
	}
}

// TestListLabels_Success proves ListLabels decodes GitHub's own real
// label-list response shape into a plain []string of names.
func TestListLabels_Success(t *testing.T) {
	t.Parallel()

	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"name": reviewpost.LabelMediumRisk, "color": "fbca04"},
			{"name": "bug", "color": "d73a4a"},
		})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	labels, err := adapter.ListLabels(context.Background(), "acme", "widgets", 42, "gho_realtoken")
	if err != nil {
		t.Fatalf("ListLabels() error = %v, want nil", err)
	}

	if gotPath != "/repos/acme/widgets/issues/42/labels" {
		t.Errorf("path = %q, want %q", gotPath, "/repos/acme/widgets/issues/42/labels")
	}
	if len(labels) != 2 || labels[0] != reviewpost.LabelMediumRisk || labels[1] != "bug" {
		t.Errorf("labels = %v, want [%q, %q]", labels, reviewpost.LabelMediumRisk, "bug")
	}
}
