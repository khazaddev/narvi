package githubapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/app/ports"
)

func TestMergePR_Success(t *testing.T) {
	t.Parallel()

	var gotPath, gotMethod, gotAuth string
	var gotBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "merged-sha-123", "merged": true, "message": "Pull Request successfully merged"})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	sha, err := adapter.MergePR(context.Background(), ports.MergePRSpec{
		Owner: "acme", Repo: "widgets", Number: 1204,
		HeadSHA: "abc123", MergeMethod: "squash", Token: "gho_realtoken",
	})
	if err != nil {
		t.Fatalf("MergePR() error = %v, want nil", err)
	}
	if sha != "merged-sha-123" {
		t.Errorf("MergePR() sha = %q, want %q", sha, "merged-sha-123")
	}
	if gotMethod != http.MethodPut {
		t.Errorf("request method = %q, want PUT", gotMethod)
	}
	if gotPath != "/repos/acme/widgets/pulls/1204/merge" {
		t.Errorf("request path = %q, want %q", gotPath, "/repos/acme/widgets/pulls/1204/merge")
	}
	if gotAuth != "Bearer gho_realtoken" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer gho_realtoken")
	}
	if gotBody["sha"] != "abc123" || gotBody["merge_method"] != "squash" {
		t.Errorf("request body = %+v, missing expected fields", gotBody)
	}
}

func TestMergePR_NotMergeable405(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Pull Request is not mergeable"})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	_, err := adapter.MergePR(context.Background(), ports.MergePRSpec{
		Owner: "acme", Repo: "widgets", Number: 1204, HeadSHA: "abc123", Token: "tok",
	})
	if err == nil {
		t.Fatal("MergePR() error = nil, want a 405 MergePRError")
	}
	var mergeErr *ports.MergePRError
	if !errors.As(err, &mergeErr) {
		t.Fatalf("MergePR() error = %v (%T), want *ports.MergePRError", err, err)
	}
	if mergeErr.Status != http.StatusMethodNotAllowed {
		t.Errorf("MergePRError.Status = %d, want 405", mergeErr.Status)
	}
}

func TestMergePR_StaleSHA409(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Head branch was modified. Review and try the merge again."})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	_, err := adapter.MergePR(context.Background(), ports.MergePRSpec{
		Owner: "acme", Repo: "widgets", Number: 1204, HeadSHA: "stale-sha", Token: "tok",
	})
	var mergeErr *ports.MergePRError
	if !errors.As(err, &mergeErr) {
		t.Fatalf("MergePR() error = %v (%T), want *ports.MergePRError", err, err)
	}
	if mergeErr.Status != http.StatusConflict {
		t.Errorf("MergePRError.Status = %d, want 409", mergeErr.Status)
	}
}

func TestMergePR_ReportedFalseIsAnError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"sha": "", "merged": false, "message": "should not happen"})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	_, err := adapter.MergePR(context.Background(), ports.MergePRSpec{
		Owner: "acme", Repo: "widgets", Number: 1204, HeadSHA: "abc123", Token: "tok",
	})
	if err == nil {
		t.Fatal("MergePR() error = nil, want an error when merged=false despite HTTP 200")
	}
}
