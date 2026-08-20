package githubapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/githubapi"
	"github.com/khazaddev/narvi/internal/app/ports"
)

// encodeContentsResponse mirrors GitHub's real Contents API response shape
// (base64 content).
func encodeContentsResponse(content, sha string) map[string]any {
	return map[string]any{
		"content": base64.StdEncoding.EncodeToString([]byte(content)),
		"sha":     sha,
	}
}

func TestResolveCodeOwners_PrecedenceAndResolution(t *testing.T) {
	t.Parallel()

	codeowners := "" +
		"* @global-owner\n" +
		"/internal/app/scheduler/ @org/scheduler-team\n" +
		"/generated/ docs@example.com\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		// .github/CODEOWNERS is found FIRST -- the root CODEOWNERS and
		// docs/CODEOWNERS candidates must never even be requested.
		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/contents/.github/CODEOWNERS":
			_ = json.NewEncoder(w).Encode(encodeContentsResponse(codeowners, "file-sha"))

		case r.Method == http.MethodGet && r.URL.Path == "/repos/acme/widgets/contents/CODEOWNERS":
			t.Error("root CODEOWNERS should never be fetched once .github/CODEOWNERS was found")
			w.WriteHeader(http.StatusNotFound)

		case r.Method == http.MethodGet && r.URL.Path == "/users/global-owner":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 111, "login": "global-owner"})

		case r.Method == http.MethodGet && r.URL.Path == "/orgs/org/teams/scheduler-team/members":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 222, "login": "alice"},
				{"id": 333, "login": "bob"},
			})

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	owners, err := adapter.ResolveCodeOwners(context.Background(), ports.ResolveCodeOwnersSpec{
		Owner: "acme", Repo: "widgets", Ref: "main",
		Paths: []string{"internal/app/scheduler/backoff.go", "generated/schema.go", "README.md"},
		Token: "tok",
	})
	if err != nil {
		t.Fatalf("ResolveCodeOwners() error = %v, want nil", err)
	}

	want := []ports.Owner{
		{ExternalID: "222", Login: "alice", TeamSlug: "org/scheduler-team", Path: "internal/app/scheduler/backoff.go", Pattern: "/internal/app/scheduler/"},
		{ExternalID: "333", Login: "bob", TeamSlug: "org/scheduler-team", Path: "internal/app/scheduler/backoff.go", Pattern: "/internal/app/scheduler/"},
		{Email: "docs@example.com", Path: "generated/schema.go", Pattern: "/generated/"},
		{ExternalID: "111", Login: "global-owner", Path: "README.md", Pattern: "*"},
	}

	if len(owners) != len(want) {
		t.Fatalf("ResolveCodeOwners() returned %d owners, want %d: %+v", len(owners), len(want), owners)
	}
	for i, w := range want {
		if owners[i] != w {
			t.Errorf("owners[%d] = %+v, want %+v", i, owners[i], w)
		}
	}
}

func TestResolveCodeOwners_FallsBackThroughCandidateLocations(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/contents/.github/CODEOWNERS":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		case "/repos/acme/widgets/contents/CODEOWNERS":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		case "/repos/acme/widgets/contents/docs/CODEOWNERS":
			_ = json.NewEncoder(w).Encode(encodeContentsResponse("* @only-owner\n", "sha"))
		case "/users/only-owner":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 1, "login": "only-owner"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	owners, err := adapter.ResolveCodeOwners(context.Background(), ports.ResolveCodeOwnersSpec{
		Owner: "acme", Repo: "widgets", Ref: "main", Paths: []string{"x.go"}, Token: "tok",
	})
	if err != nil {
		t.Fatalf("ResolveCodeOwners() error = %v, want nil", err)
	}
	if len(owners) != 1 || owners[0].Login != "only-owner" {
		t.Errorf("owners = %+v, want exactly one Owner{Login: only-owner}", owners)
	}
}

func TestResolveCodeOwners_NoFileAnywhereReturnsNilNotError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	owners, err := adapter.ResolveCodeOwners(context.Background(), ports.ResolveCodeOwnersSpec{
		Owner: "acme", Repo: "widgets", Ref: "main", Paths: []string{"x.go"}, Token: "tok",
	})
	if err != nil {
		t.Fatalf("ResolveCodeOwners() error = %v, want nil", err)
	}
	if owners != nil {
		t.Errorf("owners = %+v, want nil", owners)
	}
}

func TestResolveCodeOwners_UnresolvableOwnerSkippedNotFatal(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/contents/.github/CODEOWNERS":
			_ = json.NewEncoder(w).Encode(encodeContentsResponse("* @deleted-user @real-user\n", "sha"))
		case "/users/deleted-user":
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"message": "Not Found"})
		case "/users/real-user":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 42, "login": "real-user"})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	owners, err := adapter.ResolveCodeOwners(context.Background(), ports.ResolveCodeOwnersSpec{
		Owner: "acme", Repo: "widgets", Ref: "main", Paths: []string{"x.go"}, Token: "tok",
	})
	if err != nil {
		t.Fatalf("ResolveCodeOwners() error = %v, want nil", err)
	}
	if len(owners) != 1 || owners[0].Login != "real-user" {
		t.Errorf("owners = %+v, want exactly one Owner{Login: real-user} (deleted-user skipped, not fatal)", owners)
	}
}

// TestResolveCodeOwners_CapsDistinctOwnerResolutions is the B3 regression
// test named explicitly to close a real gap: "raising the cap
// passes everything" because no existing test in this file names more
// than a handful of distinct owners -- maxCodeOwnerRefsPerCall (50) was
// never actually exercised. A single catch-all pattern names 60 distinct
// "@login" entries (deliberately > the cap); this proves BOTH that the
// returned result is truncated to the cap AND -- the fact a length check
// alone cannot distinguish from "resolve everything, then truncate the
// slice afterward" -- that the cap stops NEW outbound resolutions
// entirely once reached, never merely hides the overflow.
func TestResolveCodeOwners_CapsDistinctOwnerResolutions(t *testing.T) {
	t.Parallel()

	const distinctOwners = 60 // deliberately > maxCodeOwnerRefsPerCall (50)
	const wantCapped = 50

	var codeowners strings.Builder
	codeowners.WriteString("*")
	for i := 0; i < distinctOwners; i++ {
		fmt.Fprintf(&codeowners, " @owner%d", i)
	}
	codeowners.WriteString("\n")

	userLookups := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/repos/acme/widgets/contents/.github/CODEOWNERS":
			_ = json.NewEncoder(w).Encode(encodeContentsResponse(codeowners.String(), "file-sha"))
		case strings.HasPrefix(r.URL.Path, "/users/owner"):
			userLookups++
			login := strings.TrimPrefix(r.URL.Path, "/users/")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": userLookups, "login": login})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	adapter := githubapi.New(server.Client(), server.URL)

	owners, err := adapter.ResolveCodeOwners(context.Background(), ports.ResolveCodeOwnersSpec{
		Owner: "acme", Repo: "widgets", Ref: "main", Paths: []string{"x.go"}, Token: "tok",
	})
	if err != nil {
		t.Fatalf("ResolveCodeOwners() error = %v, want nil", err)
	}
	if len(owners) != wantCapped {
		t.Errorf("ResolveCodeOwners() returned %d owners, want exactly %d (maxCodeOwnerRefsPerCall) even though the CODEOWNERS file names %d", len(owners), wantCapped, distinctOwners)
	}
	if userLookups != wantCapped {
		t.Errorf("made %d outbound /users/ lookups, want exactly %d (maxCodeOwnerRefsPerCall) -- the cap must stop NEW resolutions once reached, not merely truncate the result after resolving them all", userLookups, wantCapped)
	}
}
