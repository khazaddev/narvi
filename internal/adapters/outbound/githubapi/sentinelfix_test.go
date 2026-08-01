package githubapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// Step 48 ("sentinels + suggestions", §12.2 item 2, §17.2/§17.6): GetFileContent/
// UpdateFileContent (apply-suggestion) and RegisterPRStack (sentinel-auto-fix
// stack registration) against a fake httptest server, mirroring this
// package's own established real-request/fake-server test convention
// (adapter_test.go).

func TestGetFileContent_DecodesBase64Content(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": base64.StdEncoding.EncodeToString([]byte("package foo\n")),
			"sha":     "abc123",
		})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	content, sha, exists, err := adapter.GetFileContent(context.Background(), ports.GetFileContentSpec{
		Owner: "acme", Repo: "widgets", Path: "internal/foo/bar.go", Ref: "main", Token: "tok",
	})
	if err != nil {
		t.Fatalf("GetFileContent() error = %v", err)
	}
	if !exists {
		t.Fatalf("GetFileContent() exists = false, want true")
	}
	if content != "package foo\n" {
		t.Errorf("GetFileContent() content = %q, want %q", content, "package foo\n")
	}
	if sha != "abc123" {
		t.Errorf("GetFileContent() sha = %q, want %q", sha, "abc123")
	}
}

func TestGetFileContent_404MeansNotExistsNoError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "Not Found"})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	_, _, exists, err := adapter.GetFileContent(context.Background(), ports.GetFileContentSpec{
		Owner: "acme", Repo: "widgets", Path: "does/not/exist.go", Ref: "main", Token: "tok",
	})
	if err != nil {
		t.Fatalf("GetFileContent() error = %v, want nil (404 is a legitimate 'does not exist' answer)", err)
	}
	if exists {
		t.Fatalf("GetFileContent() exists = true, want false")
	}
}

func TestGetFileContent_ServerErrorIsAGenuineError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	_, _, exists, err := adapter.GetFileContent(context.Background(), ports.GetFileContentSpec{
		Owner: "acme", Repo: "widgets", Path: "x.go", Ref: "main", Token: "tok",
	})
	if err == nil {
		t.Fatal("GetFileContent() error = nil, want a genuine error for a 500 response")
	}
	if exists {
		t.Fatalf("GetFileContent() exists = true, want false on a genuine failure")
	}
}

func TestUpdateFileContent_EncodesContentAndReturnsCommitSHA(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %s, want PUT", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"commit": map[string]any{"sha": "newcommitsha"},
		})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	commitSHA, err := adapter.UpdateFileContent(context.Background(), ports.UpdateFileContentSpec{
		Owner: "acme", Repo: "widgets", Path: "internal/foo/bar.go",
		Content: "package foo // fixed\n", SHA: "abc123", Branch: "pr-branch",
		Message: "Apply suggested fix", Token: "tok",
	})
	if err != nil {
		t.Fatalf("UpdateFileContent() error = %v", err)
	}
	if commitSHA != "newcommitsha" {
		t.Errorf("UpdateFileContent() commitSHA = %q, want %q", commitSHA, "newcommitsha")
	}

	wantContent := base64.StdEncoding.EncodeToString([]byte("package foo // fixed\n"))
	if gotBody["content"] != wantContent {
		t.Errorf("PUT body content = %v, want base64 %q", gotBody["content"], wantContent)
	}
	if gotBody["sha"] != "abc123" {
		t.Errorf("PUT body sha = %v, want %q", gotBody["sha"], "abc123")
	}
	if gotBody["branch"] != "pr-branch" {
		t.Errorf("PUT body branch = %v, want %q", gotBody["branch"], "pr-branch")
	}
}

func TestUpdateFileContent_StaleSHARejectedByServerSurfacesAsError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "sha does not match"})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	_, err := adapter.UpdateFileContent(context.Background(), ports.UpdateFileContentSpec{
		Owner: "acme", Repo: "widgets", Path: "x.go", Content: "y", SHA: "stale", Branch: "b", Message: "m", Token: "tok",
	})
	if err == nil {
		t.Fatal("UpdateFileContent() error = nil, want a genuine error on a stale-SHA conflict")
	}
}

func TestRegisterPRStack_PostsBothPRNumbersBottomToTop(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	err := adapter.RegisterPRStack(context.Background(), ports.RegisterPRStackSpec{
		Owner: "acme", Repo: "widgets", PRNumbers: []int{10, 11}, Token: "tok",
	})
	if err != nil {
		t.Fatalf("RegisterPRStack() error = %v", err)
	}

	prs, ok := gotBody["pull_requests"].([]any)
	if !ok || len(prs) != 2 {
		t.Fatalf("POST body pull_requests = %v, want a 2-element array", gotBody["pull_requests"])
	}
	if prs[0] != float64(10) || prs[1] != float64(11) {
		t.Errorf("POST body pull_requests = %v, want [10, 11] (bottom to top)", prs)
	}
}

// TestRegisterPRStack_FailureIsReportedNeverSwallowed proves this method
// itself always reports a real failure -- §17.2's own "logged and
// otherwise ignored" policy is the CALLER's decision (pushpr.go), never
// something this port implementation silently absorbs on the caller's
// behalf.
func TestRegisterPRStack_FailureIsReportedNeverSwallowed(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)
	err := adapter.RegisterPRStack(context.Background(), ports.RegisterPRStackSpec{
		Owner: "acme", Repo: "widgets", PRNumbers: []int{1, 2}, Token: "tok",
	})
	if err == nil {
		t.Fatal("RegisterPRStack() error = nil, want a genuine error surfaced on a 404")
	}
}
